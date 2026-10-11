package ai

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestEstimateCacheAndUnknowns(t *testing.T) {
	u := &Usage{Input: 1000000, Cached: 500000, Output: 1000000}
	d, r := Estimate("deepseek-flash", u, 100)
	if d == nil || r == nil || math.Abs(*d-1.353) > 1e-9 || math.Abs(*r-135.3) > 1e-9 {
		t.Fatal("cache accounting")
	}
	d, r = Estimate("deepseek-v4-pro", u, 0)
	if d == nil || r != nil || math.Abs(*d-4.642) > 1e-9 {
		t.Fatal("pro or unknown exchange rate")
	}
	for _, v := range []*Usage{nil, {Input: 1, Cached: 2}, {Input: -1}, {Output: -1}} {
		if d, r = Estimate("deepseek-flash", v, 100); d != nil || r != nil {
			t.Fatal("invalid usage billed as known")
		}
	}
	if d, _ = Estimate("future-model", u, 100); d != nil {
		t.Fatal("unknown tariff")
	}
}

func TestBudgetNoticeMonthlyDedupAndNoCutoff(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "billing.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	db.Now = func() time.Time { return now }
	_, err = db.DB.ExecContext(ctx, "INSERT INTO users(id,name,code,role,status,city,timezone,created_at) VALUES(1,'Owner','own','owner','ready','Moscow','Europe/Moscow',0),(2,'Member','mem','member','ready','Moscow','Europe/Moscow',0)")
	if err != nil {
		t.Fatal(err)
	}
	s := Service{Store: db, Model: "deepseek-flash", Billing: Billing{USDRUB: 100, TargetRUB: 500}}
	add := func(u *Usage, success bool) {
		t.Helper()
		r, e := db.DB.ExecContext(ctx, "INSERT INTO api_usage(user_id,provider,model,uncertain,success,created_at) VALUES(2,'deepseek','deepseek-flash',1,0,?)", now.Unix())
		if e != nil {
			t.Fatal(e)
		}
		id, _ := r.LastInsertId()
		if e = s.account(ctx, id, u, success); e != nil {
			t.Fatal(e)
		}
	}
	add(nil, false)
	add(&Usage{Output: 5000000}, true)
	add(&Usage{Output: 5000000}, true) // Remains usable after threshold.
	var count int
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM notifications WHERE kind='budget_target' AND recipient_id=1 AND pair_id IS NULL").Scan(&count); err != nil || count != 1 {
		t.Fatal("notice not unique to owner", err, count)
	}
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM api_usage WHERE estimated_usd IS NULL AND uncertain=1").Scan(&count); err != nil || count != 1 {
		t.Fatal("lost unknown usage")
	}
	now = now.AddDate(0, 1, 0)
	add(&Usage{Output: 5000000}, true)
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM notifications WHERE kind='budget_target'").Scan(&count); err != nil || count != 2 {
		t.Fatal("new month notice", err, count)
	}
}
