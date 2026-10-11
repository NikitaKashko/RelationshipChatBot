// Package notifications delivers a durable outbox outside database transactions.
// One worker belongs to one bot process. Unknown delivery outcomes are never retried.
package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

var ErrPermanent = errors.New("permanent delivery failure")

type RetryError struct{ After time.Duration }

func (e *RetryError) Error() string { return "delivery rejected temporarily" }

type Message struct {
	ID, Pair, Recipient int64
	Kind                string
	Payload             json.RawMessage
	Attempts            int
}
type Sender func(context.Context, Message) (int64, error)
type Worker struct {
	Store *store.Store
	Send  Sender
}

// Recover is run once before polling/worker startup, never while another worker
// is active. A previous process may have sent a 'sending' row before crashing.
func (w *Worker) Recover(ctx context.Context) error {
	return w.Store.Write(ctx, func(q store.Query) error {
		_, err := q.ExecContext(ctx, "UPDATE notifications SET status='uncertain',error='interrupted delivery' WHERE status='sending'")
		return err
	})
}

// Permitted advances actual minutes (rather than constructing an ambiguous local
// clock) so overnight windows also behave correctly through DST transitions.
func Permitted(now time.Time, zone, start, end string) (time.Time, error) {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return time.Time{}, err
	}
	a, err := time.Parse("15:04", start)
	if err != nil {
		return time.Time{}, err
	}
	b, err := time.Parse("15:04", end)
	if err != nil {
		return time.Time{}, err
	}
	from, to := a.Hour()*60+a.Minute(), b.Hour()*60+b.Minute()
	if from == to {
		return now, nil
	}
	for i := 0; i < 48*60; i++ {
		local := now.In(loc)
		m := local.Hour()*60 + local.Minute()
		quiet := m >= from && m < to
		if from > to {
			quiet = m >= from || m < to
		}
		if !quiet {
			return now, nil
		}
		now = now.Truncate(time.Minute).Add(time.Minute)
	}
	return time.Time{}, errors.New("invalid quiet window")
}

func (w *Worker) next(ctx context.Context) (*Message, error) {
	var selected *Message
	err := w.Store.Write(ctx, func(q store.Query) error {
		now := w.Store.Now()
		// Close invalid scopes and expired rows, including messages delayed by quiet hours.
		if _, err := q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND (expires_at<=? OR NOT EXISTS(SELECT 1 FROM users u WHERE u.id=recipient_id AND u.status='ready') OR (pair_id IS NOT NULL AND NOT EXISTS(SELECT 1 FROM pairs p JOIN pair_members m ON m.pair_id=p.id WHERE p.id=notifications.pair_id AND p.status='active' AND m.active=1 AND m.user_id=recipient_id)))`, now.Unix()); err != nil {
			return err
		}
		// Keep the newest saved summary for each recipient/plan; old scheduled jobs
		// are checked against the current version below, rather than merged.
		if _, err := q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND kind='plan_saved' AND EXISTS(SELECT 1 FROM notifications n WHERE n.status='pending' AND n.kind='plan_saved' AND n.pair_id=notifications.pair_id AND n.recipient_id=notifications.recipient_id AND json_extract(n.payload,'$.plan_id')=json_extract(notifications.payload,'$.plan_id') AND n.id>notifications.id)`); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, "SELECT id,pair_id,recipient_id,kind,payload,attempts,expires_at FROM notifications WHERE status='pending' AND available_at<=? ORDER BY available_at,id LIMIT 100", now.Unix())
		if err != nil {
			return err
		}
		type entry struct {
			m      Message
			expiry float64
		}
		var entries []entry
		for rows.Next() {
			var e entry
			var pair sql.NullInt64
			var raw string
			if err = rows.Scan(&e.m.ID, &pair, &e.m.Recipient, &e.m.Kind, &raw, &e.m.Attempts, &e.expiry); err != nil {
				break
			}
			e.m.Pair = pair.Int64
			e.m.Payload = json.RawMessage(raw)
			entries = append(entries, e)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			m := e.m
			var payload struct {
				Plan    int64 `json:"plan_id"`
				Version int64 `json:"version"`
			}
			if err = json.Unmarshal(m.Payload, &payload); err != nil {
				return err
			}
			relevant := true
			if m.Kind == "reminder" || m.Kind == "feedback" || m.Kind == "plan_saved" {
				var status string
				var version int64
				var start float64
				err = q.QueryRowContext(ctx, "SELECT status,version,start_at FROM plans WHERE id=? AND pair_id=?", payload.Plan, m.Pair).Scan(&status, &version, &start)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return err
				}
				relevant = err == nil && version == payload.Version
				if m.Kind == "reminder" {
					var enabled bool
					if err = q.QueryRowContext(ctx, "SELECT reminder_enabled FROM users WHERE id=?", m.Recipient).Scan(&enabled); err != nil {
						return err
					}
					relevant = relevant && enabled && status == "planned" && start > float64(now.Unix())
				}
				if m.Kind == "feedback" {
					relevant = relevant && (status == "planned" || status == "visited")
				}
				if m.Kind == "plan_saved" {
					relevant = relevant && (status == "planned" || status == "visited")
				}
			}
			if !relevant {
				if _, err = q.ExecContext(ctx, "UPDATE notifications SET status='cancelled' WHERE id=?", m.ID); err != nil {
					return err
				}
				continue
			}
			var zone, from, to string
			if err = q.QueryRowContext(ctx, "SELECT timezone,quiet_start,quiet_end FROM users WHERE id=?", m.Recipient).Scan(&zone, &from, &to); err != nil {
				return err
			}
			allowed, err := Permitted(now, zone, from, to)
			if err != nil {
				if _, err = q.ExecContext(ctx, "UPDATE notifications SET status='failed',error='invalid quiet settings' WHERE id=?", m.ID); err != nil {
					return err
				}
				continue
			}
			if allowed.After(now) {
				state := "pending"
				if float64(allowed.Unix()) >= e.expiry {
					state = "cancelled"
				}
				if _, err = q.ExecContext(ctx, "UPDATE notifications SET available_at=?,status=? WHERE id=?", allowed.Unix(), state, m.ID); err != nil {
					return err
				}
				continue
			}
			if _, err = q.ExecContext(ctx, "UPDATE notifications SET status='sending',attempts=attempts+1 WHERE id=?", m.ID); err != nil {
				return err
			}
			m.Attempts++
			selected = &m
			return nil
		}
		return nil
	})
	return selected, err
}

func (w *Worker) Step(ctx context.Context) (bool, error) {
	if w.Send == nil {
		return false, errors.New("missing notification sender")
	}
	m, err := w.next(ctx)
	if err != nil || m == nil {
		return false, err
	}
	sendCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	messageID, sendErr := w.Send(sendCtx, *m)
	cancel()
	// Persist the outcome even when shutdown cancelled the send context.
	saveCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	err = w.Store.Write(saveCtx, func(q store.Query) error {
		status, reason := "sent", ""
		var retry *RetryError
		var id any = messageID
		if sendErr != nil {
			id = nil
			status = "uncertain"
			reason = "delivery outcome unknown"
			if errors.Is(sendErr, ErrPermanent) {
				status = "failed"
				reason = "permanent rejection"
			}
			if errors.As(sendErr, &retry) {
				status = "failed"
				reason = "retry attempts exhausted"
				if m.Attempts < 3 {
					status = "pending"
					reason = "temporary rejection"
				}
			}
		}
		if status == "pending" {
			delay := retry.After
			if delay < time.Second {
				delay = time.Second
			}
			if delay > 48*time.Hour {
				delay = 48 * time.Hour
			}
			_, e := q.ExecContext(saveCtx, "UPDATE notifications SET status=?,error=?,available_at=? WHERE id=? AND status='sending'", status, reason, w.Store.Now().Add(delay).Unix(), m.ID)
			return e
		}
		_, e := q.ExecContext(saveCtx, "UPDATE notifications SET status=?,error=?,telegram_message_id=? WHERE id=? AND status='sending'", status, reason, id, m.ID)
		return e
	})
	return true, err
}

func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		more, err := w.Step(ctx)
		if err != nil {
			return fmt.Errorf("notification worker stopped")
		}
		if more {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
