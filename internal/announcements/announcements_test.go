package announcements

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/notifications"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func TestAnnouncementIsIdempotentAndRespectsQuietHours(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	now := time.Date(2026, 10, 13, 20, 30, 0, 0, time.UTC) // 23:30 in Moscow.
	db.Now = func() time.Time { return now }
	for _, id := range []int64{1, 2} {
		if _, err = db.DB.ExecContext(ctx, "INSERT INTO users(id,name,code,status,timezone,quiet_start,quiet_end,created_at) VALUES(?,?,?,'ready','Europe/Moscow','23:00','09:00',?)", id, "User", fmt.Sprintf("X%d", id), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	release := Release{Version: 1, Enabled: true, ID: "release-one", Text: "Короткое обновление"}
	if inserted, err := Enqueue(ctx, db, release); err != nil || inserted != 2 {
		t.Fatal(inserted, err)
	}
	if inserted, err := Enqueue(ctx, db, release); err != nil || inserted != 0 {
		t.Fatal("restart duplicated announcement", inserted, err)
	}
	var count int
	if err = db.DB.QueryRowContext(ctx, "SELECT count(*) FROM notifications WHERE kind='release_announcement'").Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	calls := 0
	worker := notifications.Worker{Store: db, Send: func(context.Context, notifications.Message) (int64, error) {
		calls++
		return 1, nil
	}}
	if sent, err := worker.Step(ctx); err != nil || sent || calls != 0 {
		t.Fatal("announcement ignored quiet hours", sent, err, calls)
	}
	var available float64
	var payload string
	if err = db.DB.QueryRowContext(ctx, "SELECT available_at,payload FROM notifications ORDER BY id LIMIT 1").Scan(&available, &payload); err != nil {
		t.Fatal(err)
	}
	if time.Unix(int64(available), 0).UTC() != time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC) {
		t.Fatal("wrong quiet-hours release", time.Unix(int64(available), 0))
	}
	var decoded map[string]string
	if json.Unmarshal([]byte(payload), &decoded) != nil || decoded["text"] != release.Text {
		t.Fatal("announcement payload changed", payload)
	}
}
