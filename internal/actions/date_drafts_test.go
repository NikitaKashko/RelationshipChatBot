package actions_test

import (
	"encoding/json"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"testing"
	"time"
)

func TestSharedDraftApprovalWithoutPlacesOrTimes(t *testing.T) {
	d := fixture(t)
	p := planning.Input{Draft: true, Title: "Вечер дома", Description: "Настольные игры", Timezone: "Europe/Moscow"}
	result := d.apply(t, 1, "plan_save", p)
	var out struct{ Plan int64 }
	json.Unmarshal(result, &out)
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind IN ('reminder','feedback')") != 0 {
		t.Fatal("draft scheduled notifications")
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE kind='plan_saved' AND recipient_id=2") != 1 {
		t.Fatal("partner not notified")
	}
	svc := planning.Service{Store: d.db}
	saved, err := svc.Get(d.ctx, d.db.DB, 2, out.Plan)
	if err != nil || !saved.Draft || len(saved.Items) != 0 {
		t.Fatal("draft unavailable", err, saved)
	}
	p.ID = out.Plan
	p.Version = saved.Version
	p.Draft = false
	p.Date = "2026-10-12"
	d.apply(t, 2, "plan_save", p)
	saved, err = svc.Get(d.ctx, d.db.DB, 1, out.Plan)
	if err != nil || saved.Draft || !saved.DateOnly || saved.HasEnd {
		t.Fatal("approval flags", err, saved)
	}
	var at float64
	if err = d.db.DB.QueryRowContext(d.ctx, "SELECT available_at FROM notifications WHERE kind='reminder' AND status='pending' LIMIT 1").Scan(&at); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Moscow")
	if time.Unix(int64(at), 0).In(loc).Format("2006-01-02 15:04") != "2026-10-12 10:00" {
		t.Fatal("wrong date-only reminder", at)
	}
	if err = d.db.DB.QueryRowContext(d.ctx, "SELECT available_at FROM notifications WHERE kind='feedback' AND status='pending' LIMIT 1").Scan(&at); err != nil {
		t.Fatal(err)
	}
	if time.Unix(int64(at), 0).In(loc).Format("2006-01-02 15:04") != "2026-10-13 12:00" {
		t.Fatal("wrong feedback", at)
	}
	p.Version = 1
	raw, _ := json.Marshal(p)
	id, err := d.act.Prepare(d.ctx, 1, "plan_save", raw)
	if err == nil {
		if _, err = d.act.Apply(d.ctx, 1, id); err == nil {
			t.Fatal("stale edit overwritten")
		}
	}
}

func TestDateWithOptionalEnd(t *testing.T) {
	p := planning.Input{Title: "Прогулка", Timezone: "Europe/Moscow", Start: "2026-10-12 20:00"}
	if err := planning.Validate(p); err != nil {
		t.Fatal(err)
	}
	p.Date = "2026-02-30"
	p.Start = ""
	if err := planning.Validate(p); err == nil {
		t.Fatal("invalid calendar date accepted")
	}
}
