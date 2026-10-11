package actions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
)

func queued(t *testing.T, d *domain, kind string, at, expires time.Time) int64 {
	t.Helper()
	r, err := d.db.DB.ExecContext(d.ctx, `INSERT INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("test:%s:%d", kind, d.count(t, "SELECT count(*) FROM notifications")), d.pair, 2, kind, `{"text":"test"}`, at.Unix(), expires.Unix(), d.now.Unix())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	return id
}
func state(t *testing.T, d *domain, id int64) string {
	t.Helper()
	var s string
	if err := d.db.DB.QueryRowContext(d.ctx, "SELECT status FROM notifications WHERE id=?", id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOutboxRecoveryAndUnknownOutcomeNeverRepeat(t *testing.T) {
	d := fixture(t)
	id := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	calls := 0
	w := notifications.Worker{Store: d.db, Send: func(context.Context, notifications.Message) (int64, error) { calls++; return 123, nil }}
	if more, err := w.Step(d.ctx); err != nil || !more {
		t.Fatal(more, err)
	}
	if state(t, d, id) != "sent" {
		t.Fatal("success not recorded")
	}
	// A restart keeps sent receipts; a crash after claim has unknown outcome.
	second := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	d.db.DB.ExecContext(d.ctx, "UPDATE notifications SET status='sending' WHERE id=?", second)
	if err := w.Recover(d.ctx); err != nil {
		t.Fatal(err)
	}
	if state(t, d, second) != "uncertain" {
		t.Fatal("interrupted send incorrectly retried")
	}
	if more, err := w.Step(d.ctx); err != nil || more || calls != 1 {
		t.Fatal("restart duplicate", more, err, calls)
	}
	third := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	w.Send = func(context.Context, notifications.Message) (int64, error) {
		calls++
		return 0, errors.New("network response lost")
	}
	if _, err := w.Step(d.ctx); err != nil {
		t.Fatal(err)
	}
	if state(t, d, third) != "uncertain" {
		t.Fatal("unknown network outcome retried")
	}
	w.Step(d.ctx)
	if calls != 2 {
		t.Fatal("uncertain delivery repeated")
	}
}

func TestOutboxQuietHoursExpiryAndBoundedRejections(t *testing.T) {
	d := fixture(t)
	d.now = time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC) // Midnight Moscow.
	id := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	calls := 0
	w := notifications.Worker{Store: d.db, Send: func(context.Context, notifications.Message) (int64, error) { calls++; return 10, nil }}
	if more, err := w.Step(d.ctx); err != nil || more {
		t.Fatal("quiet delivery", more, err)
	}
	if calls != 0 || state(t, d, id) != "pending" {
		t.Fatal("quiet hours bypassed")
	}
	d.now = time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)
	if more, err := w.Step(d.ctx); err != nil || !more {
		t.Fatal(more, err)
	}
	if calls != 1 {
		t.Fatal("notification did not resume")
	}
	expired := queued(t, d, "affection", d.now.Add(-24*time.Hour), d.now.Add(-12*time.Hour))
	w.Step(d.ctx)
	if state(t, d, expired) != "cancelled" || calls != 1 {
		t.Fatal("outdated affection delivered")
	}
	retry := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	w.Send = func(context.Context, notifications.Message) (int64, error) {
		calls++
		return 0, &notifications.RetryError{After: time.Second}
	}
	for i := 0; i < 3; i++ {
		if _, err := w.Step(d.ctx); err != nil {
			t.Fatal(err)
		}
		d.now = d.now.Add(time.Second)
	}
	if state(t, d, retry) != "failed" {
		t.Fatal("temporary rejections unbounded")
	}
	w.Step(d.ctx)
	if calls != 4 {
		t.Fatal("failed row repeated")
	}
	permanent := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	w.Send = func(context.Context, notifications.Message) (int64, error) {
		calls++
		return 0, notifications.ErrPermanent
	}
	w.Step(d.ctx)
	w.Step(d.ctx)
	if state(t, d, permanent) != "failed" || calls != 5 {
		t.Fatal("blocked bot retried")
	}
	ended := queued(t, d, "affection", d.now, d.now.Add(12*time.Hour))
	d.db.DB.ExecContext(d.ctx, "UPDATE pairs SET status='ended' WHERE id=?", d.pair)
	w.Step(d.ctx)
	if state(t, d, ended) != "cancelled" || calls != 5 {
		t.Fatal("ended pair delivered")
	}
}

func TestOutboxCoalescingCurrentVersionAndFeedbackExpiry(t *testing.T) {
	d := fixture(t)
	place := d.place(t, 1, "Кафе")
	p := planning.Input{Title: "План", Start: "2026-10-12 18:00", End: "2026-10-12 20:00", Timezone: "Europe/Moscow", Places: []int64{place}}
	raw := d.apply(t, 1, "plan_save", p)
	var id struct{ Plan int64 }
	json.Unmarshal(raw, &id)
	p.ID = id.Plan
	p.Version = 1
	p.Title = "Обновлённый"
	d.apply(t, 2, "plan_save", p)
	p.Version = 2
	p.Title = "Последняя версия"
	d.apply(t, 2, "plan_save", p)
	var delivered []notifications.Message
	w := notifications.Worker{Store: d.db, Send: func(_ context.Context, m notifications.Message) (int64, error) {
		delivered = append(delivered, m)
		return 9, nil
	}}
	for i := 0; i < 5; i++ {
		more, err := w.Step(d.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if len(delivered) != 1 {
		t.Fatal("stale plan summary not coalesced", len(delivered))
	}
	var payload struct{ Version int64 }
	json.Unmarshal(delivered[0].Payload, &payload)
	if payload.Version != 3 {
		t.Fatal("old version sent")
	}
	d.now = time.Date(2026, 10, 12, 16, 0, 0, 0, time.UTC) // 19:00 Moscow, after start.
	w.Step(d.ctx)
	if len(delivered) != 1 {
		t.Fatal("reminder sent after start")
	}
	d.now = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	w.Step(d.ctx)
	if len(delivered) != 1 || d.count(t, "SELECT count(*) FROM notifications WHERE status='pending'") != 0 {
		t.Fatal("long outage delivered expired feedback")
	}
}

func TestQuietWindowsPersonalTimezoneAndDST(t *testing.T) {
	now := time.Date(2026, 10, 11, 20, 30, 0, 0, time.UTC)
	moscow, err := notifications.Permitted(now, "Europe/Moscow", "23:00", "09:00")
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2026, 10, 12, 6, 0, 0, 0, time.UTC)
	if !moscow.Equal(expected) {
		t.Fatal(moscow)
	}
	utc, err := notifications.Permitted(now, "UTC", "23:00", "09:00")
	if err != nil || !utc.Equal(now) {
		t.Fatal("personal timezone ignored")
	}
	dst := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	out, err := notifications.Permitted(dst, "Europe/Berlin", "01:00", "03:00")
	if err != nil || !out.Equal(time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC)) {
		t.Fatal("DST quiet end", out, err)
	}
}
