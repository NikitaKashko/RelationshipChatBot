// Package store owns SQLite transactions and authorization boundaries.
// Network operations must never run inside Write.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

var ErrDenied = errors.New(texts.T("internal.store.store.001"))
var ErrConflict = errors.New(texts.T("internal.store.store.002"))

type Query interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type Store struct {
	DB  *sql.DB
	Now func() time.Time
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection keeps pragmas and transactions consistent. Short writes only.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{DB: db, Now: time.Now}
	for _, stmt := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=10000", "PRAGMA journal_mode=WAL"} {
		if _, err = db.ExecContext(ctx, stmt); err != nil {
			db.Close()
			return nil, err
		}
	}
	if err = s.migrate(ctx, path); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Write(ctx context.Context, fn func(Query) error) (err error) {
	c, err := s.DB.Conn(ctx)
	if err != nil {
		return err
	}
	defer c.Close()
	if _, err = c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	defer func() { // Rollback must work even if the caller's context was cancelled.
		clean, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = c.ExecContext(clean, "ROLLBACK")
	}()
	if err = fn(c); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return err
}

func (s *Store) migrate(ctx context.Context, path string) error {
	var version, count int
	if err := s.DB.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version == 2 {
		return nil
	}
	if version == 1 {
		return s.Write(ctx, func(q Query) error {
			var current int
			if err := q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
				return err
			}
			if current == 2 {
				return nil
			}
			if current != 1 {
				return errors.New("schema changed during migration")
			}
			_, err := q.ExecContext(ctx, "ALTER TABLE plans ADD COLUMN is_draft INTEGER NOT NULL DEFAULT 0;\nALTER TABLE plans ADD COLUMN date_only INTEGER NOT NULL DEFAULT 0;\nALTER TABLE plans ADD COLUMN description TEXT NOT NULL DEFAULT '';\nALTER TABLE plans ADD COLUMN has_end INTEGER NOT NULL DEFAULT 1;\nINSERT INTO plans(pair_id,author_id,title,start_at,end_at,timezone,created_at,is_draft)\nSELECT pair_id,min(added_by),'Идеи из прежних кандидатов',0,1,coalesce((SELECT timezone FROM pairs WHERE id=pair_id),'Europe/Moscow'),0,1 FROM candidates GROUP BY pair_id;\nINSERT INTO plan_items(plan_id,place_id,position,snapshot)\nSELECT p.id,c.place_id,c.position,json_object('id',c.place_id,'title',x.title,'address',x.address,'city',x.city,'map_url',x.map_url,'description',x.description,'categories',json('[]')) FROM candidates c JOIN plans p ON p.pair_id=c.pair_id AND p.is_draft=1 JOIN places x ON x.id=c.place_id;\nDELETE FROM candidates;\nPRAGMA user_version=2;")
			return err
		})
	}
	if version != 0 {
		return fmt.Errorf("unsupported schema version %d", version)
	}
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&count); err != nil {
		return err
	}
	legacy := count > 0
	if legacy {
		var columns int
		if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM pragma_table_info('users') WHERE name='partner'").Scan(&columns); err != nil {
			return err
		}
		if columns != 1 {
			return errors.New("unknown database schema; refusing replacement")
		}
		backup := path + ".pre-v1.sqlite"
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			// VACUUM INTO makes a consistent snapshot including committed WAL data.
			if _, err = s.DB.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	err := s.Write(ctx, func(q Query) error {
		if legacy {
			for _, table := range []string{"users", "requests", "places", "likes", "cooldowns"} {
				var exists int
				if err := q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil {
					return err
				}
				if exists > 0 {
					if _, err := q.ExecContext(ctx, "ALTER TABLE "+table+" RENAME TO legacy_"+table); err != nil {
						return err
					}
				}
			}
		}
		if _, err := q.ExecContext(ctx, schema); err != nil {
			return err
		}
		if legacy {
			if err := s.importLegacy(ctx, q); err != nil {
				return err
			}
		}
		_, err := q.ExecContext(ctx, "PRAGMA user_version=1")
		return err
	})
	if err != nil {
		return err
	}
	return s.migrate(ctx, path)
}
func (s *Store) importLegacy(ctx context.Context, q Query) error {
	now := s.Now().Unix()
	if _, err := q.ExecContext(ctx, "INSERT INTO users(id,name,code,notification_name,created_at) SELECT id,name,code,name,? FROM legacy_users", now); err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, "SELECT u.id,v.id FROM legacy_users u JOIN legacy_users v ON u.partner=v.id AND v.partner=u.id WHERE u.id<v.id")
	if err != nil {
		return err
	}
	var pairs [][2]int64
	for rows.Next() {
		var p [2]int64
		if err = rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		r, err := q.ExecContext(ctx, "INSERT INTO pairs(created_at) VALUES(?)", now)
		if err != nil {
			return err
		}
		id, _ := r.LastInsertId()
		for _, u := range p {
			if _, err = q.ExecContext(ctx, "INSERT INTO pair_members(pair_id,user_id) VALUES(?,?)", id, u); err != nil {
				return err
			}
		}
	}
	var hasPlaces int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='legacy_places'").Scan(&hasPlaces); err != nil {
		return err
	}
	if hasPlaces == 0 {
		return nil
	}
	statements := []string{`INSERT INTO places(id,author_id,personal_user_id,pair_id,title,description,created_at)
   SELECT p.id,p.owner,CASE WHEN a.pair_id=b.pair_id THEN NULL ELSE p.owner END,
   CASE WHEN a.pair_id=b.pair_id THEN a.pair_id ELSE NULL END,p.title,p.note,?
   FROM legacy_places p LEFT JOIN pair_members a ON a.user_id=p.owner AND a.active=1
   LEFT JOIN pair_members b ON b.user_id=p.partner AND b.active=1`, `INSERT OR IGNORE INTO categories(scope,title) SELECT CASE WHEN p.pair_id IS NULL THEN 'user:'||p.personal_user_id ELSE 'pair:'||p.pair_id END,l.category FROM places p JOIN legacy_places l ON l.id=p.id`, `INSERT INTO place_categories SELECT p.id,c.id FROM places p JOIN legacy_places l ON l.id=p.id JOIN categories c ON c.title=l.category AND c.scope=CASE WHEN p.pair_id IS NULL THEN 'user:'||p.personal_user_id ELSE 'pair:'||p.pair_id END`}
	for i, stmt := range statements {
		var args []any
		if i == 0 {
			args = []any{now}
		}
		if _, err = q.ExecContext(ctx, stmt, args...); err != nil {
			return err
		}
	}
	var hasLikes int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE name='legacy_likes'").Scan(&hasLikes); err != nil {
		return err
	}
	if hasLikes > 0 {
		_, err = q.ExecContext(ctx, `INSERT INTO place_preferences(place_id,user_id,interest) SELECT l.place,l.user,'want' FROM legacy_likes l JOIN places p ON p.id=l.place WHERE p.personal_user_id=l.user OR EXISTS(SELECT 1 FROM pair_members m WHERE m.user_id=l.user AND m.pair_id=p.pair_id AND m.active=1)`)
		return err
	}
	return nil
}

type User struct {
	ID                                                         int64
	Name, Code, Role, Status, City, Timezone, NotificationName string
}

func UserFor(ctx context.Context, q Query, actor int64, onboarding bool) (User, error) {
	var u User
	err := q.QueryRowContext(ctx, `SELECT id,name,code,role,status,coalesce(city,''),coalesce(timezone,''),coalesce(notification_name,name) FROM users WHERE id=?`, actor).Scan(&u.ID, &u.Name, &u.Code, &u.Role, &u.Status, &u.City, &u.Timezone, &u.NotificationName)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrDenied
	}
	if err != nil {
		return u, err
	}
	if u.Status == "deleted" || (!onboarding && u.Status != "ready") {
		return u, ErrDenied
	}
	return u, nil
}
func PairFor(ctx context.Context, q Query, actor int64) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, `SELECT p.id FROM pairs p JOIN pair_members m ON m.pair_id=p.id WHERE m.user_id=? AND m.active=1 AND p.status='active'`, actor).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}
func PlaceFor(ctx context.Context, q Query, actor, id int64) error {
	if _, err := UserFor(ctx, q, actor, false); err != nil {
		return err
	}
	pair, err := PairFor(ctx, q, actor)
	if err != nil {
		return err
	}
	var count int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM places WHERE id=? AND (personal_user_id=? OR pair_id=?)", id, actor, pair).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrDenied
	}
	return nil
}
func PlanFor(ctx context.Context, q Query, actor, id int64) error {
	if _, err := UserFor(ctx, q, actor, false); err != nil {
		return err
	}
	pair, err := PairFor(ctx, q, actor)
	if err != nil {
		return err
	}
	var count int
	if err = q.QueryRowContext(ctx, "SELECT count(*) FROM plans WHERE id=? AND pair_id=?", id, pair).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return ErrDenied
	}
	return nil
}
