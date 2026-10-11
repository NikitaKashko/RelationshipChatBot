package backup

import (
	"context"
	"fmt"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotRestoreAndDiskFailure(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	db, e := store.Open(ctx, filepath.Join(root, "live.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.DB.Close()
	_, e = db.DB.ExecContext(ctx, "INSERT INTO users(id,name,code,created_at) VALUES(1,'Private fixture','code',1)")
	if e != nil {
		t.Fatal(e)
	}
	s := Service{Store: db, Dir: filepath.Join(root, "backups")}
	path, e := s.Run(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = Verify(ctx, path); e != nil {
		t.Fatal(e)
	}
	restored, e := store.Open(ctx, path)
	if e != nil {
		t.Fatal(e)
	}
	var n int
	e = restored.DB.QueryRowContext(ctx, "SELECT count(*) FROM users").Scan(&n)
	restored.DB.Close()
	if e != nil || n != 1 {
		t.Fatal("snapshot lost data", e, n)
	}
	s.Space = func(string) (uint64, error) { return 0, nil }
	if _, e = s.Run(ctx); e == nil {
		t.Fatal("disk failure reported success")
	}
	if _, e = os.Stat(path); e != nil {
		t.Fatal("prior backup lost", e)
	}
	var successes, failures int
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM backup_runs WHERE status='success'").Scan(&successes)
	db.DB.QueryRowContext(ctx, "SELECT count(*) FROM backup_runs WHERE status='failed'").Scan(&failures)
	if successes != 1 || failures != 1 {
		t.Fatal(successes, failures)
	}
}
func TestCalendarRotation(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for d := 0; d < 40; d++ {
		for _, kind := range []string{"daily", "weekly"} {
			for _, hour := range []int{1, 2} {
				name := fmt.Sprintf("%s-%s-%06d-test.sqlite", kind, now.AddDate(0, 0, -d).Format("20060102"), hour*10000)
				if e := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	os.WriteFile(filepath.Join(dir, "important.txt"), []byte("keep"), 0600)
	s := Service{Dir: dir}
	if e := s.Rotate(now); e != nil {
		t.Fatal(e)
	}
	files, e := os.ReadDir(dir)
	if e != nil {
		t.Fatal(e)
	}
	if len(files) != 12 {
		t.Fatal("wanted 7 daily + 4 weekly + unrelated file", len(files))
	}
}
