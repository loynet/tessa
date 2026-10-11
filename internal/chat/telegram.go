package chat

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"tessa/internal/telegram"
)

const telegramMaxBackoff = 30 * time.Second

// Only read calls use this loop. Retrying delivery would need a policy for
// ambiguous success and duplicate replies.
func (c *Assistant) readTelegram(ctx context.Context, method string, read func() error) error {
	backoff := time.Second
	var outageStarted, lastLog time.Time
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := read()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			c.metrics.ObserveTelegramRead(method, "success")
			if !outageStarted.IsZero() {
				c.logger.Info("telegram read recovered", "method", method, "outage_duration", time.Since(outageStarted))
			}
			return nil
		}
		c.metrics.SetTelegramReady(false)
		reason, retryAfter, retry := telegram.ClassifyError(err)
		c.metrics.ObserveTelegramRead(method, reason)
		if !retry {
			switch reason {
			case "credentials":
				return fmt.Errorf("telegram %s failed: credentials rejected; check the bot token and access", method)
			case "conflict":
				return fmt.Errorf("telegram %s failed: polling conflict; run only one instance per bot token and disable any webhook", method)
			default:
				return fmt.Errorf("telegram %s failed: %s error; check Bot API configuration and protocol", method, reason)
			}
		}
		// Equal jitter keeps a nonzero floor and caps the exponential delay.
		// Telegram's flood-control delay is a minimum, even above our cap.
		delay := max(backoff/2+time.Duration(rand.Int64N(int64(backoff/2))), retryAfter)
		now := time.Now()
		if outageStarted.IsZero() {
			outageStarted = now
		}
		if lastLog.IsZero() || now.Sub(lastLog) >= time.Minute {
			c.logger.Warn("telegram read unavailable; retrying", "method", method, "reason", reason, "retry_delay", delay, "outage_duration", now.Sub(outageStarted))
			lastLog = now
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		backoff = min(backoff*2, telegramMaxBackoff)
	}
}
