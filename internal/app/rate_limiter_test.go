package app

import (
	"testing"
	"time"
)

func TestUserRateLimiterBurstAndRefill(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newUserRateLimiter(true, 5, 2, 0)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _, _ := l.Allow(1); !ok {
			t.Fatalf("action %d within burst should be allowed", i)
		}
	}
	ok, retryAfter, _ := l.Allow(1)
	if ok {
		t.Fatalf("action beyond burst should be denied")
	}
	if retryAfter <= 0 || retryAfter > 5*time.Second {
		t.Fatalf("unexpected retryAfter %s", retryAfter)
	}
	now = now.Add(5 * time.Second)
	if ok, _, _ := l.Allow(1); !ok {
		t.Fatalf("action after one interval should be allowed")
	}
}

func TestUserRateLimiterDailyQuota(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newUserRateLimiter(true, 1, 10, 2)
	l.now = func() time.Time { return now }

	for i := 0; i < 2; i++ {
		if ok, _, _ := l.Allow(1); !ok {
			t.Fatalf("action %d within quota should be allowed", i)
		}
	}
	if ok, _, _ := l.Allow(1); ok {
		t.Fatalf("action beyond daily quota should be denied")
	}
	now = now.Add(24 * time.Hour)
	if ok, _, _ := l.Allow(1); !ok {
		t.Fatalf("quota should reset after 24h")
	}
}

func TestUserRateLimiterSweepsIdleBucketsAfterUse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := newUserRateLimiter(true, 5, 3, 0)
	l.now = func() time.Time { return now }

	// Consume a token so the stored token count stays below burst; the sweep
	// must still treat the bucket as idle once it would have refilled.
	if ok, _, _ := l.Allow(1); !ok {
		t.Fatalf("first action should be allowed")
	}
	now = now.Add(25 * time.Hour)
	if ok, _, _ := l.Allow(2); !ok {
		t.Fatalf("other user should be allowed")
	}
	l.mu.Lock()
	_, stillThere := l.buckets[1]
	l.mu.Unlock()
	if stillThere {
		t.Fatalf("idle bucket for user 1 should have been swept")
	}
}
