package actions_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/feedback"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func visitID(t *testing.T, raw json.RawMessage) int64 {
	t.Helper()
	var p struct{ Visit int64 }
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	return p.Visit
}
func TestPartialVisitExplicitFactAndIndependentReviews(t *testing.T) {
	d := fixture(t)
	a, b := d.place(t, 1, "Кафе"), d.place(t, 2, "Парк")
	raw := d.apply(t, 1, "plan_save", planning.Input{Title: "Вечер", Start: "2026-10-12 18:00", End: "2026-10-12 20:00", Timezone: "Europe/Moscow", Places: []int64{a, b}})
	var plan struct{ Plan int64 }
	json.Unmarshal(raw, &plan)
	d.now = time.Date(2026, 10, 13, 10, 0, 0, 0, time.UTC)
	if d.count(t, "SELECT count(*) FROM visits") != 0 {
		t.Fatal("elapsed plan became visit")
	}
	p := feedback.Visit{Plan: plan.Plan, Version: 1, Places: []int64{a}, Date: "2026-10-12", Precision: "day"}
	draft := d.prepare(t, 1, "visit_record", p)
	if d.count(t, "SELECT count(*) FROM visits") != 0 {
		t.Fatal("unconfirmed visit written")
	}
	raw, err := d.act.Apply(d.ctx, 1, draft)
	if err != nil {
		t.Fatal(err)
	}
	id := visitID(t, raw)
	if _, err = d.act.Apply(d.ctx, 1, draft); err != nil {
		t.Fatal(err)
	}
	if d.count(t, "SELECT count(*) FROM visits") != 1 {
		t.Fatal("replay duplicated visit")
	}
	cards := collection.Service{Store: d.db}
	ca, _ := cards.Get(d.ctx, d.db.DB, 1, a)
	cb, _ := cards.Get(d.ctx, d.db.DB, 1, b)
	if !ca.Visited || ca.FreezeUntil <= float64(d.now.Unix()) || cb.Visited || cb.FreezeUntil != 0 {
		t.Fatal("partial visit affected unvisited card")
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind='feedback' AND status='pending'") != 2 {
		t.Fatal("second partner lost impressions prompt")
	}
	d.apply(t, 1, "review", feedback.Review{Visit: id, Place: a, Rating: "liked", Again: "again", Comment: "мой отзыв"})
	d.apply(t, 2, "review", feedback.Review{Visit: id, Place: a, Rating: "disliked", Again: "never", Comment: "второй отзыв"})
	ca, _ = cards.Get(d.ctx, d.db.DB, 1, a)
	if !ca.Refused {
		t.Fatal("partner refusal absent")
	}
	d.apply(t, 1, "freeze", feedback.Freeze{Place: a})
	ca, _ = cards.Get(d.ctx, d.db.DB, 1, a)
	if collection.AutomaticEligible(ca, float64(d.now.Unix())) {
		t.Fatal("freeze removal erased refusal")
	}
	d.apply(t, 1, "review", feedback.Review{Visit: id, Place: a, Rating: "neutral", Again: "again"})
	if d.count(t, "SELECT count(*) FROM reviews WHERE user_id=2 AND comment='второй отзыв' AND again='never'") != 1 {
		t.Fatal("author overwrote partner review")
	}
	bad := d.prepare(t, 3, "review", feedback.Review{Visit: id, Place: a, Rating: "liked", Again: "again"})
	if _, err = d.act.Apply(d.ctx, 3, bad); !errors.Is(err, store.ErrDenied) {
		t.Fatal("foreign visit allowed", err)
	}
	rawArgs := json.RawMessage(`{"visit":1,"place":1,"rating":"liked","again":"again","user_id":2}`)
	if _, err = d.act.Prepare(d.ctx, 1, "review", rawArgs); err == nil {
		t.Fatal("trusted author argument accepted")
	}
}

func TestRepeatCorrectionAndManualFreezePriority(t *testing.T) {
	d := fixture(t)
	place := d.place(t, 1, "Музей")
	cards := collection.Service{Store: d.db}
	first := visitID(t, d.apply(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-01", Precision: "day", Freeze: true}))
	initial, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	d.apply(t, 2, "freeze", feedback.Freeze{Place: place})
	removed, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	if removed.FreezeUntil != 0 {
		t.Fatal("manual removal ineffective")
	}
	second := visitID(t, d.apply(t, 2, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-11", Precision: "day", Freeze: true}))
	repeated, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	if repeated.FreezeUntil <= initial.FreezeUntil {
		t.Fatal("same-second new visit did not restart freeze")
	}
	d.apply(t, 1, "visit_correct", feedback.Correction{ID: second})
	corrected, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	if corrected.FreezeUntil != 0 {
		t.Fatal("correction lost prior manual removal")
	}
	third := visitID(t, d.apply(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-10", Precision: "day", Freeze: true}))
	until := d.now.Add(60 * 24 * time.Hour)
	d.apply(t, 2, "freeze", feedback.Freeze{Place: place, Until: until.Format(time.RFC3339)})
	d.apply(t, 1, "visit_correct", feedback.Correction{ID: third})
	d.apply(t, 2, "visit_correct", feedback.Correction{ID: first})
	corrected, _ = cards.Get(d.ctx, d.db.DB, 1, place)
	if corrected.FreezeUntil != float64(until.Unix()) {
		t.Fatal("correction erased future override")
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind='visit_corrected'") != 3 {
		t.Fatal("correction notification missing")
	}
}

func TestHistoricalPrecisionAndNotWent(t *testing.T) {
	d := fixture(t)
	place := d.place(t, 1, "Озеро")
	id := visitID(t, d.apply(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2025-06", Precision: "month"}))
	var precision string
	var at any
	if err := d.db.DB.QueryRowContext(d.ctx, "SELECT date_precision,visited_at FROM visits WHERE id=?", id).Scan(&precision, &at); err != nil {
		t.Fatal(err)
	}
	if precision != "month" || at != nil || d.count(t, "SELECT count(*) FROM notifications") != 0 {
		t.Fatal("historical visit invented day or retrospective prompt")
	}
	for _, p := range []feedback.Visit{{Places: []int64{place}, Date: "2026-11-01", Precision: "day"}, {Places: []int64{place}, Date: "2025-06", Precision: "month", Freeze: true}, {Places: []int64{place, place}, Date: "2026-10-10", Precision: "day"}} {
		draft := d.prepare(t, 1, "visit_record", p)
		if _, err := d.act.Apply(d.ctx, 1, draft); err == nil {
			t.Fatal("invalid visit accepted", p)
		}
	}
	raw := d.apply(t, 1, "plan_save", planning.Input{Title: "Не вышло", Start: "2026-10-12 18:00", End: "2026-10-12 20:00", Timezone: "Europe/Moscow", Places: []int64{place}})
	var plan struct{ Plan int64 }
	json.Unmarshal(raw, &plan)
	d.apply(t, 2, "plan_not_went", planning.Cancel{ID: plan.Plan, Version: 1})
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind IN ('feedback','reminder') AND status='pending'") != 0 {
		t.Fatal("not went retained jobs")
	}
	cards := collection.Service{Store: d.db}
	c, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	if c.FreezeUntil != 0 || !collection.AutomaticEligible(c, float64(d.now.Unix())) {
		t.Fatal("not went froze idea")
	}
}

func TestCorrectionFallsBackAndVisitFailureRollsBack(t *testing.T) {
	d := fixture(t)
	place := d.place(t, 1, "Театр")
	d.apply(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-01", Precision: "day", Freeze: true})
	cards := collection.Service{Store: d.db}
	old, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	newest := visitID(t, d.apply(t, 2, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-10", Precision: "day", Freeze: true}))
	d.apply(t, 1, "visit_correct", feedback.Correction{ID: newest})
	corrected, _ := cards.Get(d.ctx, d.db.DB, 1, place)
	if corrected.FreezeUntil != old.FreezeUntil {
		t.Fatal("correction did not use previous valid visit")
	}
	before := d.count(t, "SELECT count(*) FROM visits")
	if _, err := d.db.DB.ExecContext(d.ctx, `CREATE TRIGGER fail_visit BEFORE INSERT ON visit_items BEGIN SELECT RAISE(ABORT,'simulated failure'); END`); err != nil {
		t.Fatal(err)
	}
	draft := d.prepare(t, 1, "visit_record", feedback.Visit{Places: []int64{place}, Date: "2026-10-11", Precision: "day", Freeze: true})
	if _, err := d.act.Apply(d.ctx, 1, draft); err == nil {
		t.Fatal("failure ignored")
	}
	if d.count(t, "SELECT count(*) FROM visits") != before || d.count(t, "SELECT count(*) FROM drafts WHERE applied_at IS NOT NULL AND kind='visit_record'") != 2 {
		t.Fatal("failed visit partially committed")
	}
}
