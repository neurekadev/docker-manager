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
// being woken; DispatchBatch bounds the messages of one channel sent at
// once (a digest lists DigestMaxLines of them).
const (
	DispatchInterval = time.Minute
	DispatchBatch    = 100
)

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
				// Try again later, not at once.
				if later := s.clk.Now().Add(RetryMin); next.Before(later) {
					next = later
				}
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

// Dispatch sends every due channel's messages once and returns when the
// next pending message is due (zero: none pending). It reads only the due
// channels (by due time, indexed) and at most DispatchBatch messages of
// each; it sends nothing while the manager moves.
func (s *Service) Dispatch(ctx context.Context) (time.Time, error) {
	if s.locked() {
		return time.Time{}, nil
	}
	now := s.now()
	channels, err := store.DueAlertChannels(ctx, s.db, now)
	if err != nil {
		return time.Time{}, err
	}
	var errs []error
	if len(channels) > 0 {
		instance := ""
		if set, err := store.GetInstanceSettings(ctx, s.db); err == nil {
			instance = set.Name
		}
		sem := make(chan struct{}, DispatchParallel)
		var wg sync.WaitGroup
		var mu sync.Mutex
		for _, channelID := range channels {
			sem <- struct{}{}
			wg.Go(func() {
				defer func() { <-sem }()
				if err := s.sendChannel(ctx, instance, channelID, now); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
		wg.Wait()
	}
	if ctx.Err() != nil {
		return time.Time{}, ctx.Err()
	}
	// More than a batch was due, or new messages became due meanwhile: go
	// on at once.
	after := s.now()
	if due, err := store.DueAlertChannels(ctx, s.db, after); err != nil {
		errs = append(errs, err)
	} else if len(due) > 0 {
		return after, errors.Join(errs...)
	}
	next, _, err := store.NextAlertDelivery(ctx, s.db, after)
	if err != nil {
		errs = append(errs, err)
	}
	return next, errors.Join(errs...)
}

// sendChannel sends a due channel's oldest pending messages (in order,
// one message or a digest) from their snapshots and records the outcome.
// A failure defers every pending message of the channel.
func (s *Service) sendChannel(ctx context.Context, instance, channelID string, now time.Time) error {
	batch, err := store.ChannelAlertDeliveries(ctx, s.db, channelID, DispatchBatch)
	if err != nil || len(batch) == 0 || batch[0].NextAttemptAt.After(now) {
		return err
	}
	ch, err := store.GetNotificationChannel(ctx, s.db, channelID)
	gone := errors.Is(err, domain.ErrNotificationChannelNotFound)
	if err != nil && !gone {
		return err
	}
	var send, done []domain.AlertDelivery
	for _, d := range batch {
		d.UpdatedAt = now
		if gone || !ch.Enabled || !ch.Wants(d.Kind, d.EnvironmentID) || (d.Event == domain.AlertEventResolved && !ch.SendResolved) {
			d.State = domain.DeliveryDropped
			done = append(done, d)
			continue
		}
		send = append(send, d)
	}
	if len(send) == 0 {
		return store.SetAlertDeliveries(ctx, s.db, done)
	}
	msg := buildMessage(instance, s.opts.PublicURL, send)
	ok, class := false, errClassInternal
	if s.opts.Sender != nil {
		res, err := s.opts.Sender.Send(ctx, channelID, msg)
		switch {
		case ctx.Err() != nil:
			return nil // shutting down: the messages stay pending
		case errors.Is(err, domain.ErrNotificationChannelNotFound):
			for i := range send {
				send[i].State = domain.DeliveryDropped
			}
			return store.SetAlertDeliveries(ctx, s.db, append(done, send...))
		case err != nil:
			s.log.Warn("could not send an alert message", "notification_channel_id", channelID, "error", err)
		default:
			ok, class = res.OK, res.ErrorClass
		}
	}
	// The whole channel waits for the oldest message's backoff: its order
	// is kept.
	retry := now.Add(backoff(send[0].Attempts + 1))
	waiting := false
	for i := range send {
		d := &send[i]
		d.Attempts++
		switch {
		case ok:
			d.State, d.SentAt, d.LastError = domain.DeliverySent, &now, ""
		case now.Sub(d.CreatedAt) >= GiveUpAfter:
			d.State, d.LastError = domain.DeliveryFailed, class
		default:
			d.LastError, d.NextAttemptAt, waiting = class, retry, true
		}
	}
	if !ok {
		s.log.Warn("an alert message could not be sent; it will be retried", "notification_channel_id", channelID,
			"error_class", class, "messages", len(send))
	}
	if err := store.SetAlertDeliveries(ctx, s.db, append(done, send...)); err != nil {
		return err
	}
	if waiting {
		return store.DeferChannelAlertDeliveries(ctx, s.db, channelID, retry, now)
	}
	return nil
}
