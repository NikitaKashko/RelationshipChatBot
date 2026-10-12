package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

type Location struct {
	City     string `json:"city"`
	Timezone string `json:"timezone"`
}
type LocationConfirmation struct {
	Version int64 `json:"version"`
}

func (s *Service) ProposeLocation(ctx context.Context, q store.Query, actor int64, p Location) (int64, error) {
	if strings.TrimSpace(p.City) == "" || p.Timezone == "" || p.Timezone == "Local" {
		return 0, errors.New(texts.T("internal.identity.location.001"))
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return 0, err
	}
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return 0, err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	if pair == 0 {
		return 0, store.ErrDenied
	}
	var version, partner int64
	if err = q.QueryRowContext(ctx, "SELECT location_version+1 FROM pairs WHERE id=?", pair).Scan(&version); err != nil {
		return 0, err
	}
	if _, err = q.ExecContext(ctx, "UPDATE pairs SET proposed_city=?,proposed_timezone=?,proposed_by=?,location_version=? WHERE id=?", strings.TrimSpace(p.City), p.Timezone, actor, version, pair); err != nil {
		return 0, err
	}
	if err = q.QueryRowContext(ctx, "SELECT user_id FROM pair_members WHERE pair_id=? AND active=1 AND user_id<>?", pair, actor).Scan(&partner); err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"city": strings.TrimSpace(p.City), "timezone": p.Timezone, "version": version})
	_, err = q.ExecContext(ctx, `INSERT INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,?,'location_proposal',?,?,?,?)`, fmt.Sprintf("pair:%d:location:%d", pair, version), pair, partner, string(raw), s.Store.Now().Unix(), s.Store.Now().Unix()+48*3600, s.Store.Now().Unix())
	return version, err
}
func (s *Service) ConfirmLocation(ctx context.Context, q store.Query, actor int64, p LocationConfirmation) error {
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return err
	}
	if pair == 0 {
		return store.ErrDenied
	}
	var by, version sql.NullInt64
	if err = q.QueryRowContext(ctx, "SELECT proposed_by,location_version FROM pairs WHERE id=?", pair).Scan(&by, &version); err != nil {
		return err
	}
	if !by.Valid || by.Int64 == actor {
		return store.ErrDenied
	}
	if version.Int64 != p.Version {
		return store.ErrConflict
	}
	_, err = q.ExecContext(ctx, "UPDATE pairs SET city=proposed_city,timezone=proposed_timezone,proposed_city=NULL,proposed_timezone=NULL,proposed_by=NULL WHERE id=?", pair)
	if err != nil {
		return err
	}
	var zone string
	if err = q.QueryRowContext(ctx, "SELECT timezone FROM pairs WHERE id=?", pair).Scan(&zone); err != nil {
		return err
	}
	plans := planning.Service{Store: s.Store}
	return plans.RefreshFeedback(ctx, q, pair, zone)
}
