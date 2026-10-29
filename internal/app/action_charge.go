package app

import (
	"fmt"
	"strings"
)

// actionCharge tracks what happened to the tasks started by one
// rate-limited message, so the charge can be refunded when every task it
// tried to queue was turned away for capacity reasons (queue full, resource
// pressure). Updates for one chat are handled one at a time by the update
// dispatcher, so the charge registered for a chat always belongs to the
// message currently being handled in it.
type actionCharge struct {
	userID   int64
	accepted int
	rejected int
}

func (b *TelegramBot) beginActionCharge(chatID, userID int64) {
	b.actionChargeMu.Lock()
	defer b.actionChargeMu.Unlock()
	if b.actionCharges == nil {
		b.actionCharges = make(map[int64]*actionCharge)
	}
	b.actionCharges[chatID] = &actionCharge{userID: userID}
}

// noteActionQueued records whether a task for chatID was queued (accepted)
// or turned away for capacity reasons.
func (b *TelegramBot) noteActionQueued(chatID int64, accepted bool) {
	b.actionChargeMu.Lock()
	defer b.actionChargeMu.Unlock()
	charge := b.actionCharges[chatID]
	if charge == nil {
		return
	}
	if accepted {
		charge.accepted++
	} else {
		charge.rejected++
	}
}

// finishActionCharge closes the charge for chatID and refunds it when the
// message queued nothing and at least one task was rejected for capacity.
// Messages that only opened a selection panel or hit invalid input stay
// charged, so panels cannot be used to bypass the limiter.
func (b *TelegramBot) finishActionCharge(chatID int64) {
	b.actionChargeMu.Lock()
	charge := b.actionCharges[chatID]
	delete(b.actionCharges, chatID)
	b.actionChargeMu.Unlock()
	if charge != nil && charge.accepted == 0 && charge.rejected > 0 {
		b.userLimiter.Refund(charge.userID)
	}
}

// formatQuotaText renders /quota: how many rate-limited actions the user can
// start now and how the daily quota stands.
func (b *TelegramBot) formatQuotaText(chatID, userID int64) string {
	en := b.getChatLanguage(chatID) == telegramLanguageEn
	if b.isAdminUser(userID) {
		if en {
			return "You are an admin: downloads are not rate limited."
		}
		return "你是管理员：下载不受限流限制。"
	}
	status := b.userLimiter.Status(userID)
	if !status.Enabled {
		if en {
			return "Rate limiting is disabled: there is no download quota."
		}
		return "限流未开启：下载没有次数限制。"
	}
	var lines []string
	if en {
		lines = append(lines, "📊 My download allowance",
			fmt.Sprintf("Available now: %d/%d (one refills every %s)", status.Available, status.Burst, formatRetryAfter(status.Interval)))
		if status.NextIn > 0 {
			lines = append(lines, fmt.Sprintf("Next refill in: %s", formatRetryAfter(status.NextIn)))
		}
		if status.DailyQuota > 0 {
			lines = append(lines, fmt.Sprintf("Used in the last 24h: %d/%d", status.DailyUsed, status.DailyQuota))
			if status.DailyUsed > 0 {
				lines = append(lines, fmt.Sprintf("Daily quota resets in: %s", formatRetryAfter(status.DailyResetIn)))
			}
		} else {
			lines = append(lines, "Daily quota: unlimited")
		}
		lines = append(lines, "Tasks turned away because the queue was full do not count.")
	} else {
		lines = append(lines, "📊 我的下载额度",
			fmt.Sprintf("当前可用：%d/%d（每 %s 恢复 1 次）", status.Available, status.Burst, formatRetryAfter(status.Interval)))
		if status.NextIn > 0 {
			lines = append(lines, fmt.Sprintf("下次恢复：%s 后", formatRetryAfter(status.NextIn)))
		}
		if status.DailyQuota > 0 {
			lines = append(lines, fmt.Sprintf("24 小时内已用：%d/%d", status.DailyUsed, status.DailyQuota))
			if status.DailyUsed > 0 {
				lines = append(lines, fmt.Sprintf("每日额度重置：%s 后", formatRetryAfter(status.DailyResetIn)))
			}
		} else {
			lines = append(lines, "每日额度：不限")
		}
		lines = append(lines, "因队列已满被拒绝的任务不计入次数。")
	}
	return strings.Join(lines, "\n")
}
