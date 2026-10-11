package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

// Retry claims a private single-use button. Failures offer a fresh button; the
// same callback cannot concurrently send another paid request.
func (s *Service) Retry(ctx context.Context, actor int64, id string) (Result, error) {
	if s.Provider == nil {
		return Result{}, errors.New("AI not configured")
	}
	var text string
	err := s.Store.Write(ctx, func(q store.Query) error {
		pair, err := store.AIConversationFor(ctx, q, actor)
		if err != nil {
			return err
		}
		var raw string
		var scope sql.NullInt64
		var applied sql.NullFloat64
		var expiry float64
		err = q.QueryRowContext(ctx, "SELECT payload,pair_id,applied_at,expires_at FROM drafts WHERE id=? AND user_id=? AND kind='ai_retry'", id, actor).Scan(&raw, &scope, &applied, &expiry)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrDenied
		}
		if err != nil {
			return err
		}
		if scope.Int64 != pair {
			return store.ErrDenied
		}
		if applied.Valid || expiry <= float64(s.Store.Now().Unix()) {
			return store.ErrConflict
		}
		var p struct {
			Conversation string `json:"conversation"`
			Text         string `json:"text"`
		}
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			return err
		}
		var latest string
		err = q.QueryRowContext(ctx, "SELECT id FROM conversations WHERE user_id=? AND id LIKE ? ORDER BY created_at DESC,id DESC LIMIT 1", actor, prefix(pair)+"%").Scan(&latest)
		if err != nil || latest != p.Conversation {
			return store.ErrConflict
		}
		text = p.Text
		_, err = q.ExecContext(ctx, "UPDATE drafts SET applied_at=? WHERE id=?", s.Store.Now().Unix(), id)
		return err
	})
	if err != nil {
		return Result{}, err
	}
	return s.Converse(ctx, actor, text)
}
