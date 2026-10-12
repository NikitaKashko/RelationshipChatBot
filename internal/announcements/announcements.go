// Package announcements queues concise runtime release notes for ready users.
package announcements

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)

type Release struct {
	Version int    `json:"version"`
	Enabled bool   `json:"enabled"`
	ID      string `json:"id"`
	Text    string `json:"text"`
}

func Read(path string) (Release, error) {
	var release Release
	raw, err := os.ReadFile(path)
	if err != nil {
		return release, fmt.Errorf("read release announcement %q: %w", path, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&release); err != nil {
		return release, fmt.Errorf("parse release announcement %q: %w", path, err)
	}
	if release.Version != 1 {
		return release, fmt.Errorf("release announcement %q: unsupported version %d", path, release.Version)
	}
	release.ID = strings.TrimSpace(release.ID)
	release.Text = strings.TrimSpace(release.Text)
	if !release.Enabled {
		return release, nil
	}
	if !validID.MatchString(release.ID) {
		return release, fmt.Errorf("release announcement %q: invalid id", path)
	}
	if release.Text == "" || len([]rune(release.Text)) > 1000 {
		return release, fmt.Errorf("release announcement %q: text must contain 1..1000 characters", path)
	}
	return release, nil
}

func Current() (Release, error) {
	if err := texts.Validate(); err != nil {
		return Release{}, err
	}
	return Read(filepath.Join(filepath.Dir(texts.Path()), "announcement.json"))
}

// EnqueueCurrent inserts at most one durable message for each currently ready
// user. The unique semantic key makes repeated service starts idempotent.
func EnqueueCurrent(ctx context.Context, db *store.Store) (int64, error) {
	release, err := Current()
	if err != nil || !release.Enabled {
		return 0, err
	}
	return Enqueue(ctx, db, release)
}

func Enqueue(ctx context.Context, db *store.Store, release Release) (inserted int64, err error) {
	if !release.Enabled {
		return 0, nil
	}
	if !validID.MatchString(release.ID) || strings.TrimSpace(release.Text) == "" || len([]rune(release.Text)) > 1000 {
		return 0, fmt.Errorf("invalid release announcement")
	}
	payload, err := json.Marshal(map[string]string{"text": strings.TrimSpace(release.Text)})
	if err != nil {
		return 0, err
	}
	err = db.Write(ctx, func(q store.Query) error {
		rows, err := q.QueryContext(ctx, "SELECT id FROM users WHERE status='ready' ORDER BY id")
		if err != nil {
			return err
		}
		var recipients []int64
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			recipients = append(recipients, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		now := db.Now()
		for _, recipient := range recipients {
			result, err := q.ExecContext(ctx, `INSERT INTO notifications(semantic_key,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,'release_announcement',?,?,?,?) ON CONFLICT(semantic_key) DO NOTHING`, fmt.Sprintf("release:%s:%d", release.ID, recipient), recipient, string(payload), now.Unix(), now.Add(7*24*time.Hour).Unix(), now.Unix())
			if err != nil {
				return err
			}
			if affected, e := result.RowsAffected(); e == nil {
				inserted += affected
			}
		}
		return nil
	})
	return inserted, err
}
