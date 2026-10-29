package app

import (
	"fmt"
	"sync"
	"time"
)

// userRateLimiter provides per-user flood protection and a rolling daily quota
// for expensive (download / asset-export) actions. It is safe for concurrent
// use. Admins are expected to be filtered out by the caller before Allow is
// invoked.
//
// Two independent gates are enforced:
//  1. A token bucket (capacity = burst, one token refilled every interval)
//     that smooths bursts of requests from a single user.
//  2. A rolling 24h quota that caps the total number of accepted actions per
//     user per day (0 disables the quota).
type userRateLimiter struct {
	enabled    bool
	interval   time.Duration // sustained spacing between actions
	burst      float64       // token bucket capacity
	dailyQuota int           // max accepted actions per rolling 24h (0 = unlimited)

	now func() time.Time // injectable clock for tests

	mu      sync.Mutex
	buckets map[int64]*userBucket

	// lastSweep guards periodic pruning of idle buckets so the map does not
	// grow without bound for one-off users.
	lastSweep time.Time
}

type userBucket struct {
	tokens     float64
	lastRefill time.Time
	dayStart   time.Time
	dayCount   int
}

func newUserRateLimiter(enabled bool, intervalSec, burst, dailyQuota int) *userRateLimiter {
	interval := time.Duration(intervalSec) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if burst <= 0 {
		burst = 3
	}
	if dailyQuota < 0 {
		dailyQuota = 0
	}
	return &userRateLimiter{
		enabled:    enabled,
		interval:   interval,
		burst:      float64(burst),
		dailyQuota: dailyQuota,
		now:        time.Now,
		buckets:    make(map[int64]*userBucket),
	}
}

func (l *userRateLimiter) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// Allow reports whether an action from userID may proceed. When denied it
// returns a retryAfter hint and a short human-readable reason.
func (l *userRateLimiter) Allow(userID int64) (ok bool, retryAfter time.Duration, reason string) {
	if l == nil || !l.enabled {
		return true, 0, ""
	}
	// userID <= 0 means the caller could not identify a user (e.g. channel
	// posts). Do not rate-limit what we cannot attribute.
	if userID <= 0 {
		return true, 0, ""
	}

	now := l.clock()
	l.mu.Lock()
	defer l.mu.Unlock()

	l.sweepLocked(now)

	bucket := l.buckets[userID]
	if bucket == nil {
		bucket = &userBucket{tokens: l.burst, lastRefill: now, dayStart: now}
		l.buckets[userID] = bucket
	}

	// Refill the token bucket based on elapsed time.
	if elapsed := now.Sub(bucket.lastRefill); elapsed > 0 && l.interval > 0 {
		bucket.tokens += float64(elapsed) / float64(l.interval)
		if bucket.tokens > l.burst {
			bucket.tokens = l.burst
		}
		bucket.lastRefill = now
	}

	// Roll the daily window.
	if now.Sub(bucket.dayStart) >= 24*time.Hour {
		bucket.dayStart = now
		bucket.dayCount = 0
	}

	if l.dailyQuota > 0 && bucket.dayCount >= l.dailyQuota {
		wait := 24*time.Hour - now.Sub(bucket.dayStart)
		if wait < 0 {
			wait = 0
		}
		return false, wait, fmt.Sprintf("daily limit reached (%d/day)", l.dailyQuota)
	}

	if bucket.tokens < 1 {
		needed := 1 - bucket.tokens
		wait := time.Duration(needed * float64(l.interval))
		if wait < 0 {
			wait = 0
		}
		return false, wait, "too many requests, please slow down"
	}

	bucket.tokens--
	bucket.dayCount++
	return true, 0, ""
}

// sweepLocked drops idle buckets to bound memory. Caller must hold l.mu.
func (l *userRateLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < time.Hour {
		return
	}
	l.lastSweep = now
	for id, bucket := range l.buckets {
		// A bucket is idle if it would be fully refilled by now and its daily
		// window has expired; such an entry carries no state worth keeping.
		// Tokens are only refilled lazily in Allow, so project the refill here
		// instead of reading the stale stored value.
		if l.projectedTokens(bucket, now) >= l.burst && now.Sub(bucket.dayStart) >= 24*time.Hour {
			delete(l.buckets, id)
		}
	}
}

func (l *userRateLimiter) projectedTokens(bucket *userBucket, now time.Time) float64 {
	tokens := bucket.tokens
	if elapsed := now.Sub(bucket.lastRefill); elapsed > 0 && l.interval > 0 {
		tokens += float64(elapsed) / float64(l.interval)
	}
	return tokens
}

// Refund gives back one previously accepted action, e.g. when the task it
// started was rejected because the queue was full. It never raises the
// bucket above its capacity or the daily count below zero.
func (l *userRateLimiter) Refund(userID int64) {
	if l == nil || !l.enabled || userID <= 0 {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[userID]
	if bucket == nil {
		return
	}
	bucket.tokens++
	if bucket.tokens > l.burst {
		bucket.tokens = l.burst
	}
	if bucket.dayCount > 0 {
		bucket.dayCount--
	}
}

// userRateLimitStatus is a read-only view of a user's limiter state.
type userRateLimitStatus struct {
	Enabled      bool
	Burst        int
	Available    int           // actions that may be fired right now
	NextIn       time.Duration // time until the next action refills (0 when full)
	Interval     time.Duration
	DailyQuota   int // 0 = unlimited
	DailyUsed    int
	DailyResetIn time.Duration
}

// Status reports a user's current allowance without consuming anything.
func (l *userRateLimiter) Status(userID int64) userRateLimitStatus {
	if l == nil || !l.enabled {
		return userRateLimitStatus{}
	}
	now := l.clock()
	status := userRateLimitStatus{
		Enabled:    true,
		Burst:      int(l.burst),
		Interval:   l.interval,
		DailyQuota: l.dailyQuota,
		Available:  int(l.burst),
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	bucket := l.buckets[userID]
	if bucket == nil {
		return status
	}
	tokens := l.projectedTokens(bucket, now)
	if tokens > l.burst {
		tokens = l.burst
	}
	status.Available = int(tokens)
	if tokens < l.burst {
		frac := tokens - float64(int(tokens))
		status.NextIn = time.Duration((1 - frac) * float64(l.interval))
	}
	if now.Sub(bucket.dayStart) < 24*time.Hour {
		status.DailyUsed = bucket.dayCount
		status.DailyResetIn = 24*time.Hour - now.Sub(bucket.dayStart)
	}
	if l.dailyQuota > 0 && status.DailyUsed >= l.dailyQuota {
		status.Available = 0
	}
	return status
}
