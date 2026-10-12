package actions_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/affection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestAffectionNicknameCooldownAndNoPhraseRepeat(t *testing.T) {
	d := fixture(t)
	s := affection.Service{Store: d.db}
	d.db.DB.ExecContext(d.ctx, "UPDATE users SET name='Никита',notification_name='НЕ ИСПОЛЬЗОВАТЬ' WHERE id=1")
	d.apply(t, 2, "notification_names", affection.Names{Partner: "зайка"})
	previous := ""
	for i := 0; i < 15; i++ {
		if err := s.Click(d.ctx, 1, "thinking"); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := d.db.DB.QueryRowContext(d.ctx, "SELECT payload FROM notifications ORDER BY id DESC LIMIT 1").Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var p struct{ Text string }
		json.Unmarshal([]byte(raw), &p)
		if !strings.Contains(p.Text, "зайка") || strings.Contains(p.Text, "Никита") || p.Text == previous {
			t.Fatal("nickname precedence or immediate repeat", p.Text)
		}
		previous = p.Text
		if err := s.Click(d.ctx, 1, "hug"); !errors.Is(err, affection.ErrCooldown) {
			t.Fatal("cross-button cooldown absent", err)
		}
		d.now = d.now.Add(time.Minute)
	}
	if d.count(t, "SELECT count(*) FROM api_usage") != 0 {
		t.Fatal("attention called paid API")
	}
	if d.count(t, "SELECT count(*) FROM notifications WHERE expires_at-created_at=43200") != 15 {
		t.Fatal("attention expiry not twelve hours")
	}
	d.apply(t, 2, "notification_names", affection.Names{})
	if err := s.Click(d.ctx, 1, "attention"); err != nil {
		t.Fatal(err)
	}
	var raw string
	d.db.DB.QueryRowContext(d.ctx, "SELECT payload FROM notifications ORDER BY id DESC LIMIT 1").Scan(&raw)
	if !strings.Contains(raw, "Никита") {
		t.Fatal("sender name fallback absent")
	}
	if err := s.Click(d.ctx, 3, "attention"); !errors.Is(err, store.ErrDenied) {
		t.Fatal("solo click enqueued", err)
	}
}

func TestAffectionConcurrentClicksOnlyOneAccepted(t *testing.T) {
	d := fixture(t)
	s := affection.Service{Store: d.db}
	var wg sync.WaitGroup
	errs := make(chan error, 3)
	for _, kind := range []string{"attention", "thinking", "hug"} {
		wg.Add(1)
		go func(kind string) { defer wg.Done(); errs <- s.Click(d.ctx, 1, kind) }(kind)
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, affection.ErrCooldown) {
			t.Fatal(err)
		}
	}
	if accepted != 1 || d.count(t, "SELECT count(*) FROM notifications") != 1 {
		t.Fatal("concurrent cooldown bypass")
	}
}
