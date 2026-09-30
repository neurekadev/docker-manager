package alerts

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/neurekadev/docker-manager/internal/clock"
	"github.com/neurekadev/docker-manager/internal/domain"
	"github.com/neurekadev/docker-manager/internal/manager/store"
)

// Delivery (#159): the dispatcher sends the outbox (alert_deliveries)
// through notify.Service.Send, channel by channel in the order the
// messages were written, DispatchParallel channels at once. A channel is
// due when its oldest pending message is; everything pending for it then
// goes out together (one message, or a digest for a burst). A failed send
// is retried with backoff (RetryMin doubling up to RetryMax) and given up
// after GiveUpAfter; messages of a deleted or disabled channel, or of an
// alert the channel is no longer subscribed to, are dropped. Nothing is
// sent while the manager moves. Delivery is at least once: a send that
// succeeded but could not be recorded is sent again.

// DispatchInterval is how often the dispatcher looks at the outbox without
// being woken.
const DispatchInterval = time.Minute

// errClassInternal records a send that failed before reaching the service.
const errClassInternal = "internal"

func (s *Service) runDispatch(ctx context.Context) {
	ticker := s.clk.NewTicker(DispatchInterval)
	defer ticker.Stop()
	for {
		var next time.Time
		if !s.locked() {
			var err error
			if next, err = s.Dispatch(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("could not send alert messages", "error", err)
			}
		}
		var timer clock.Timer
		var due <-chan time.Time
		if !next.IsZero() {
			timer = s.clk.NewTimer(max(next.Sub(s.clk.Now()), 0))
			due = timer.C()
		}
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ticker.C():
		case <-s.dispatch:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// backoff is the wait after the attempts-th failed send.
func backoff(attempts int) time.Duration {
	d := RetryMin
	for i := 1; i < attempts && d < RetryMax; i++ {
		d *= 2
	}
	return min(d, RetryMax)
}

// Dispatch sends every channel's due messages once and returns when the
// next pending message is due (zero: none pending). It sends nothing
// while the manager moves.
func (s *Service) Dispatch(ctx context.Context) (time.Time, error) {
	if s.locked() {
		return time.Time{}, nil
	}
	pending, err := store.PendingAlertDeliveries(ctx, s.db)
	if err != nil {
		return time.Time{}, err
	}
	now := s.now()
	var next time.Time
	var batches [][]domain.AlertDelivery
	for i := 0; i < len(pending); {
		j := i
		for j < len(pending) && pending[j].ChannelID == pending[i].ChannelID {
			j++
		}
		head := pending[i]
		if head.NextAttemptAt.After(now) {
			if next.IsZero() || head.NextAttemptAt.Before(next) {
				next = head.NextAttemptAt
			}
		} else {
			batches = append(batches, pending[i:j])
		}
		i = j
	}
	if len(batches) == 0 {
		return next, nil
	}
	instance := ""
	if set, err := store.GetInstanceSettings(ctx, s.db); err == nil {
		instance = set.Name
	}
	sem := make(chan struct{}, DispatchParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for _, b := range batches {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			retry, err := s.sendBatch(ctx, instance, b, now)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
			}
			if !retry.IsZero() && (next.IsZero() || retry.Before(next)) {
				next = retry
			}
		})
	}
	wg.Wait()
	return next, errors.Join(errs...)
}

// sendBatch sends one channel's pending messages (in order) and records
// the outcome. It returns when the batch is due again after a failure.
func (s *Service) sendBatch(ctx context.Context, instance string, batch []domain.AlertDelivery, now time.Time) (time.Time, error) {
	channelID := batch[0].ChannelID
	ch, err := store.GetNotificationChannel(ctx, s.db, channelID)
	gone := errors.Is(err, domain.ErrNotificationChannelNotFound)
	if err != nil && !gone {
		return time.Time{}, err
	}
	var items []item
	var send, done []domain.AlertDelivery
	for _, d := range batch {
		d.UpdatedAt = now
		if gone || !ch.Enabled {
			d.State = domain.DeliveryDropped
			done = append(done, d)
			continue
		}
		a, err := store.GetAlert(ctx, s.db, d.AlertID)
		if errors.Is(err, domain.ErrAlertNotFound) {
			d.State = domain.DeliveryDropped
			done = append(done, d)
			continue
		}
		if err != nil {
			return time.Time{}, err
		}
		if !ch.Wants(a.Kind, a.EnvironmentID) || (d.Event == domain.AlertEventResolved && !ch.SendResolved) {
			d.State = domain.DeliveryDropped
			done = append(done, d)
			continue
		}
		items = append(items, item{alert: a, event: d.Event})
		send = append(send, d)
	}
	var retry time.Time
	if len(send) > 0 {
		msg := buildMessage(instance, s.opts.PublicURL, items)
		ok, class := false, errClassInternal
		if s.opts.Sender != nil {
			res, err := s.opts.Sender.Send(ctx, channelID, msg)
			switch {
			case ctx.Err() != nil:
				return time.Time{}, nil // shutting down: the messages stay pending
			case errors.Is(err, domain.ErrNotificationChannelNotFound):
				for i := range send {
					send[i].State = domain.DeliveryDropped
				}
				return time.Time{}, store.SetAlertDeliveries(ctx, s.db, append(done, send...))
			case err != nil:
				s.log.Warn("could not send an alert message", "notification_channel_id", channelID, "error", err)
			default:
				ok, class = res.OK, res.ErrorClass
			}
		}
		attempts := send[0].Attempts + 1
		for i := range send {
			d := &send[i]
			d.Attempts++
			switch {
			case ok:
				d.State, d.SentAt, d.LastError = domain.DeliverySent, &now, ""
			case now.Sub(d.CreatedAt) >= GiveUpAfter:
				d.State, d.LastError = domain.DeliveryFailed, class
			default:
				d.LastError = class
				// The whole batch waits for the oldest message's backoff:
				// the channel's order is kept.
				d.NextAttemptAt = now.Add(backoff(attempts))
				retry = d.NextAttemptAt
			}
		}
		if !ok {
			s.log.Warn("an alert message could not be sent; it will be retried", "notification_channel_id", channelID,
				"error_class", class, "messages", len(send))
		}
	}
	return retry, store.SetAlertDeliveries(ctx, s.db, append(done, send...))
}
