package ai

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func prefix(pair int64) string { return fmt.Sprintf("p%d-", pair) }

func (s *Service) current(ctx context.Context, actor, pair int64, id string) error {
	current, err := store.AIFor(ctx, s.Store.DB, actor)
	if err != nil {
		return err
	}
	if current != pair {
		return store.ErrDenied
	}
	var latest string
	err = s.Store.DB.QueryRowContext(ctx, "SELECT id FROM conversations WHERE user_id=? AND id LIKE ? ORDER BY created_at DESC,id DESC LIMIT 1", actor, prefix(pair)+"%").Scan(&latest)
	if err != nil || latest != id {
		return store.ErrConflict
	}
	return nil
}
func (s *Service) Prune(ctx context.Context) error {
	return s.Store.Write(ctx, func(q store.Query) error {
		if _, err := q.ExecContext(ctx, "DELETE FROM messages WHERE created_at<=?", s.Store.Now().Add(-30*24*time.Hour).Unix()); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "DELETE FROM conversations WHERE created_at<=? AND NOT EXISTS(SELECT 1 FROM messages WHERE conversation_id=conversations.id)", s.Store.Now().Add(-30*24*time.Hour).Unix())
		return err
	})
}
func (s *Service) New(ctx context.Context, actor int64) (string, error) {
	var id string
	err := s.Store.Write(ctx, func(q store.Query) error {
		pair, err := store.AIFor(ctx, q, actor)
		if err != nil {
			return err
		}
		id, err = s.newIn(ctx, q, actor, pair)
		return err
	})
	return id, err
}
func (s *Service) newIn(ctx context.Context, q store.Query, actor, pair int64) (string, error) {
	token, err := identity.RandomToken()
	if err != nil {
		return "", err
	}
	id := prefix(pair) + token
	var latest float64
	if err = q.QueryRowContext(ctx, "SELECT coalesce(max(created_at),0) FROM conversations WHERE user_id=?", actor).Scan(&latest); err != nil {
		return "", err
	}
	now := float64(s.Store.Now().UnixNano()) / 1e9
	if now <= latest {
		now = latest + 0.000001
	}
	_, err = q.ExecContext(ctx, "INSERT INTO conversations(id,user_id,created_at) VALUES(?,?,?)", id, actor, now)
	return id, err
}
func (s *Service) Clear(ctx context.Context, actor int64) error {
	return s.Store.Write(ctx, func(q store.Query) error {
		if _, err := store.UserFor(ctx, q, actor, false); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, "DELETE FROM conversations WHERE user_id=?", actor); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, "DELETE FROM drafts WHERE user_id=? AND kind='ai_retry'", actor)
		return err
	})
}
func (s *Service) begin(ctx context.Context, actor int64, text string) (id string, pair int64, messages []Message, err error) {
	if err = s.Prune(ctx); err != nil {
		return
	}
	err = s.Store.Write(ctx, func(q store.Query) error {
		var e error
		pair, e = store.AIFor(ctx, q, actor)
		if e != nil {
			return e
		}
		e = q.QueryRowContext(ctx, "SELECT id FROM conversations WHERE user_id=? AND id LIKE ? ORDER BY created_at DESC,id DESC LIMIT 1", actor, prefix(pair)+"%").Scan(&id)
		if errors.Is(e, sql.ErrNoRows) {
			id, e = s.newIn(ctx, q, actor, pair)
		}
		if e != nil {
			return e
		}
		if _, e = q.ExecContext(ctx, "INSERT INTO messages(conversation_id,role,text,created_at) VALUES(?,'user',?,?)", id, text, s.Store.Now().Unix()); e != nil {
			return e
		}
		rows, e := q.QueryContext(ctx, "SELECT role,text FROM messages WHERE conversation_id=? ORDER BY id", id)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var m Message
			if e = rows.Scan(&m.Role, &m.Content); e != nil {
				return e
			}
			messages = append(messages, m)
		}
		return rows.Err()
	})
	return
}
