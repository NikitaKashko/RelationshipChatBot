// Package backup creates verified SQLite snapshots without copying a live WAL file.
package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type Service struct {
	Store *store.Store
	Dir   string
	Space func(string) (uint64, error)
	mu    sync.Mutex
}

// Verify checks the snapshot read-only, including foreign keys. Restore is offline.
func Verify(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err = db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil || result != "ok" {
		return errors.New("backup integrity check failed")
	}
	rows, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("backup foreign key check failed")
	}
	if err = rows.Err(); err != nil {
		return err
	}
	var deleted int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM deletion_ledger").Scan(&deleted); err != nil {
		return err
	}
	if deleted != 0 {
		return errors.New("restoration requires independent deletion replay; not supported by this prototype")
	}
	return nil
}

func (s *Service) Run(ctx context.Context) (path string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Store.Now().UTC()
	result, err := s.Store.DB.ExecContext(ctx, "INSERT INTO backup_runs(started_at,status) VALUES(?,'running')", now.Unix())
	if err != nil {
		return "", err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return "", err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		status := "success"
		var message any
		if err != nil {
			status = "failed"
			message = "Backup failed; previous verified copies retained."
		}
		_, e := s.Store.DB.ExecContext(clean, "UPDATE backup_runs SET finished_at=?,status=?,path=?,error=? WHERE id=?", s.Store.Now().Unix(), status, path, message, id)
		if err == nil && e != nil {
			err = errors.New("cannot record backup result")
		}
	}()
	if err = os.MkdirAll(s.Dir, 0770); err != nil {
		return "", err
	}
	var pages, size uint64
	if err = s.Store.DB.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
		return "", err
	}
	if err = s.Store.DB.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
		return "", err
	}
	space := s.Space
	if space == nil {
		space = FreeSpace
	}
	free, e := space(s.Dir)
	if e != nil {
		return "", e
	}
	if free < pages*size*3+16*1024*1024 {
		return "", errors.New("insufficient disk space")
	}
	f, err := os.CreateTemp(s.Dir, "snapshot-*.tmp")
	if err != nil {
		return "", err
	}
	tmp := f.Name()
	f.Close()
	os.Remove(tmp)
	defer os.Remove(tmp)
	if _, err = s.Store.DB.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return "", errors.New("snapshot creation failed")
	}
	if err = Verify(ctx, tmp); err != nil {
		return "", err
	}
	if err = os.Chmod(tmp, 0660); err != nil {
		return "", err
	}
	suffix := filepath.Base(tmp)
	suffix = strings.TrimSuffix(strings.TrimPrefix(suffix, "snapshot-"), ".tmp")
	stem := now.Format("20060102-150405") + "-" + suffix + ".sqlite"
	path = filepath.Join(s.Dir, "daily-"+stem)
	if err = os.Rename(tmp, path); err != nil {
		return "", err
	}
	os.Chtimes(path, now, now)
	// A weekly snapshot is the most recent completed snapshot of its ISO week.
	weekly := filepath.Join(s.Dir, "weekly-"+stem)
	if err = copySnapshot(path, weekly); err != nil {
		return path, err
	}
	os.Chtimes(weekly, now, now)
	if err = s.Rotate(now); err != nil {
		return path, err
	}
	return path, nil
}

func copySnapshot(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(target), "weekly-*.tmp")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	closeErr := out.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0660); err != nil {
		return err
	}
	return os.Rename(tmp, target)
}

func (s *Service) Rotate(now time.Time) error {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	type snapshot struct {
		name string
		at   time.Time
	}
	for _, kind := range []string{"daily", "weekly"} {
		files := []snapshot{}
		for _, entry := range entries {
			name := entry.Name()
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasPrefix(name, kind+"-") || !strings.HasSuffix(name, ".sqlite") {
				continue
			}
			parts := strings.Split(strings.TrimSuffix(name, ".sqlite"), "-")
			if len(parts) != 4 {
				continue
			}
			at, e := time.Parse("20060102-150405", parts[1]+"-"+parts[2])
			if e != nil {
				continue
			}
			files = append(files, snapshot{name, at})
		}
		sort.Slice(files, func(i, j int) bool { return files[i].at.After(files[j].at) })
		seen := map[string]bool{}
		limit := 7
		if kind == "weekly" {
			limit = 4
		}
		for _, file := range files {
			key := file.at.Format("2006-01-02")
			if kind == "weekly" {
				y, w := file.at.ISOWeek()
				key = fmt.Sprintf("%d-%d", y, w)
			}
			keep := !seen[key] && len(seen) < limit && !file.at.Before(now.AddDate(0, 0, -28))
			if keep {
				seen[key] = true
				continue
			}
			// Only recognized regular snapshot filenames in the configured directory.
			if err = os.Remove(filepath.Join(s.Dir, file.name)); err != nil {
				return err
			}
		}
	}
	return nil
}
