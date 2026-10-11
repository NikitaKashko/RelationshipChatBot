// Package actions separates model-prepared drafts from confirmed writes.
package actions

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/NikitaKashko/RelationshipChatBot/internal/affection"
	"io"
	"strings"

	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/feedback"
	"github.com/NikitaKashko/RelationshipChatBot/internal/identity"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

type Service struct{ Store *store.Store }

func decode(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return errors.New("неверные аргументы действия")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("лишние данные действия")
	}
	return nil
}

func validate(kind string, raw []byte) error {
	if strings.HasPrefix(kind, "ai:") {
		kind = strings.TrimPrefix(kind, "ai:")
		switch kind {
		case "search_save", "place_create", "place_edit", "place_archive", "place_interest", "candidate", "plan_save", "plan_cancel", "plan_not_went", "visit_record", "review", "freeze", "preferences":
		default:
			return store.ErrDenied
		}
	}
	switch kind {
	case "search_save":
		var p collection.SearchSave
		if err := decode(raw, &p); err != nil {
			return err
		}
		return collection.Validate(p.Card)
	case "preferences":
		return decode(raw, new(store.Preferences))
	case "notification_names":
		return decode(raw, new(affection.Names))
	case "visit_record":
		return decode(raw, new(feedback.Visit))
	case "visit_correct":
		return decode(raw, new(feedback.Correction))
	case "review":
		return decode(raw, new(feedback.Review))
	case "freeze":
		return decode(raw, new(feedback.Freeze))
	case "plan_not_went":
		return decode(raw, new(planning.Cancel))
	case "location_proposal":
		return decode(raw, new(identity.Location))
	case "location_confirm":
		return decode(raw, new(identity.LocationConfirmation))
	case "place_create":
		var p collection.Input
		if err := decode(raw, &p); err != nil {
			return err
		}
		return collection.Validate(p)
	case "place_edit":
		var p collection.Edit
		if err := decode(raw, &p); err != nil {
			return err
		}
		return collection.Validate(p.Card)
	case "place_archive":
		return decode(raw, new(collection.Archive))
	case "place_interest":
		return decode(raw, new(collection.Interest))
	case "candidate":
		return decode(raw, new(planning.Candidate))
	case "plan_save":
		var p planning.Input
		if err := decode(raw, &p); err != nil {
			return err
		}
		return planning.Validate(p)
	case "plan_cancel":
		return decode(raw, new(planning.Cancel))
	case "profile":
		return decode(raw, new(identity.Profile))
	case "pair_accept":
		return decode(raw, new(struct {
			Hash string `json:"hash"`
		}))
	case "consent":
		return decode(raw, new(struct {
			Kind    string `json:"kind"`
			Enabled bool   `json:"enabled"`
		}))
	default:
		return errors.New("неизвестное действие")
	}
}

func (s *Service) Prepare(ctx context.Context, actor int64, kind string, raw []byte) (id string, err error) {
	if err = validate(kind, raw); err != nil {
		return
	}
	err = s.Store.Write(ctx, func(q store.Query) error {
		if strings.HasPrefix(kind, "ai:") {
			if _, e := store.AIFor(ctx, q, actor); e != nil {
				return e
			}
		}
		if _, e := store.UserFor(ctx, q, actor, kind == "profile"); e != nil {
			return e
		}
		pair, e := store.PairFor(ctx, q, actor)
		if e != nil {
			return e
		}
		var scope any
		if pair != 0 {
			scope = pair
		}
		if strings.HasPrefix(kind, "ai:") {
			// Repeated tool calls/explicit provider retries can reuse an identical
			// pending proposal. They never create a second committed action.
			var existing string
			e = q.QueryRowContext(ctx, "SELECT id FROM drafts WHERE user_id=? AND coalesce(pair_id,0)=? AND kind=? AND payload=? AND applied_at IS NULL AND expires_at>? ORDER BY created_at DESC LIMIT 1", actor, pair, kind, string(raw), s.Store.Now().Unix()).Scan(&existing)
			if e == nil {
				id = existing
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		}
		id, e = identity.RandomToken()
		if e != nil {
			return e
		}
		if strings.TrimPrefix(kind, "ai:") == "plan_save" {
			var p planning.Input
			_ = decode(raw, &p)
			plans := planning.Service{Store: s.Store}
			if _, e = plans.Preview(ctx, q, actor, p); e != nil {
				return e
			}
		}
		_, e = q.ExecContext(ctx, "INSERT INTO drafts(id,user_id,pair_id,kind,payload,created_at,expires_at) VALUES(?,?,?,?,?,?,?)", id, actor, scope, kind, string(raw), s.Store.Now().Unix(), s.Store.Now().Unix()+86400)
		return e
	})
	return
}

func (s *Service) Apply(ctx context.Context, actor int64, id string) (result json.RawMessage, err error) {
	err = s.Store.Write(ctx, func(q store.Query) error {
		var kind, raw string
		var pair sql.NullInt64
		var expiry float64
		var applied sql.NullFloat64
		var previous sql.NullString
		e := q.QueryRowContext(ctx, "SELECT kind,payload,pair_id,expires_at,applied_at,result FROM drafts WHERE id=? AND user_id=?", id, actor).Scan(&kind, &raw, &pair, &expiry, &applied, &previous)
		if errors.Is(e, sql.ErrNoRows) {
			return store.ErrDenied
		}
		if e != nil {
			return e
		}
		if _, e = store.UserFor(ctx, q, actor, kind == "profile"); e != nil {
			return e
		}
		if strings.HasPrefix(kind, "ai:") {
			if _, e = store.AIFor(ctx, q, actor); e != nil {
				return e
			}
			kind = strings.TrimPrefix(kind, "ai:")
		}
		// Completed callbacks are receipts, never new writes. Pair acceptance itself
		// changes scope, so replay checks its resulting pair rather than old scope.
		current, e := store.PairFor(ctx, q, actor)
		if e != nil {
			return e
		}
		if applied.Valid {
			expected := pair.Int64
			if kind == "pair_accept" {
				var r struct {
					Pair int64 `json:"pair"`
				}
				if e = json.Unmarshal([]byte(previous.String), &r); e != nil {
					return e
				}
				expected = r.Pair
			}
			if current != expected {
				return store.ErrConflict
			}
			result = json.RawMessage(previous.String)
			return nil
		}
		if expiry <= float64(s.Store.Now().Unix()) || current != pair.Int64 {
			return store.ErrConflict
		}
		if e = validate(kind, []byte(raw)); e != nil {
			return e
		}
		ids := identity.Service{Store: s.Store}
		cards := collection.Service{Store: s.Store}
		plans := planning.Service{Store: s.Store}
		facts := feedback.Service{Store: s.Store}
		switch kind {
		case "search_save":
			var p collection.SearchSave
			_ = decode([]byte(raw), &p)
			n, e := cards.Create(ctx, q, actor, p.Card)
			if e != nil {
				return e
			}
			if p.Candidate {
				if e = plans.Candidate(ctx, q, actor, planning.Candidate{ID: n}); e != nil {
					return e
				}
			}
			result, _ = json.Marshal(map[string]int64{"place": n})
		case "preferences":
			var p store.Preferences
			_ = decode([]byte(raw), &p)
			value, _ := json.Marshal(p)
			if _, e = q.ExecContext(ctx, "UPDATE users SET preferences=? WHERE id=?", string(value), actor); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "notification_names":
			var p affection.Names
			_ = decode([]byte(raw), &p)
			service := affection.Service{Store: s.Store}
			if e = service.Names(ctx, q, actor, p); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "visit_record":
			var p feedback.Visit
			_ = decode([]byte(raw), &p)
			n, e := facts.Record(ctx, q, actor, p)
			if e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"visit": n})
		case "visit_correct":
			var p feedback.Correction
			_ = decode([]byte(raw), &p)
			if e = facts.Correct(ctx, q, actor, p); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "review":
			var p feedback.Review
			_ = decode([]byte(raw), &p)
			if e = facts.Review(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": p.Place})
		case "freeze":
			var p feedback.Freeze
			_ = decode([]byte(raw), &p)
			if e = facts.Freeze(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": p.Place})
		case "plan_not_went":
			var p planning.Cancel
			_ = decode([]byte(raw), &p)
			if e = facts.NotWent(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"plan": p.ID})
		case "location_proposal":
			var p identity.Location
			_ = decode([]byte(raw), &p)
			v, e := ids.ProposeLocation(ctx, q, actor, p)
			if e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"location_version": v})
		case "location_confirm":
			var p identity.LocationConfirmation
			_ = decode([]byte(raw), &p)
			if e = ids.ConfirmLocation(ctx, q, actor, p); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "place_create":
			var p collection.Input
			_ = decode([]byte(raw), &p)
			n, e := cards.Create(ctx, q, actor, p)
			if e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": n})
		case "place_edit":
			var p collection.Edit
			_ = decode([]byte(raw), &p)
			if e = cards.Edit(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": p.ID})
		case "place_archive":
			var p collection.Archive
			_ = decode([]byte(raw), &p)
			if e = cards.Archive(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": p.ID})
		case "place_interest":
			var p collection.Interest
			_ = decode([]byte(raw), &p)
			if e = cards.Interest(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"place": p.ID})
		case "candidate":
			var p planning.Candidate
			_ = decode([]byte(raw), &p)
			if e = plans.Candidate(ctx, q, actor, p); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "plan_save":
			var p planning.Input
			_ = decode([]byte(raw), &p)
			n, e := plans.Save(ctx, q, actor, p)
			if e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"plan": n})
		case "plan_cancel":
			var p planning.Cancel
			_ = decode([]byte(raw), &p)
			if e = plans.Cancel(ctx, q, actor, p); e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"plan": p.ID})
		case "profile":
			var p identity.Profile
			_ = decode([]byte(raw), &p)
			if e = ids.FinishProfile(ctx, q, actor, p); e != nil {
				return e
			}
			result = json.RawMessage(`{"saved":true}`)
		case "pair_accept":
			var p struct {
				Hash string `json:"hash"`
			}
			_ = decode([]byte(raw), &p)
			n, e := ids.AcceptIn(ctx, q, actor, p.Hash)
			if e != nil {
				return e
			}
			result, _ = json.Marshal(map[string]int64{"pair": n})
		case "consent":
			var p struct {
				Kind    string `json:"kind"`
				Enabled bool   `json:"enabled"`
			}
			_ = decode([]byte(raw), &p)
			if p.Kind != "ai" && p.Kind != "notes" && p.Kind != "reviews" && p.Kind != "external_audio" {
				return errors.New("неизвестное согласие")
			}
			if _, e = q.ExecContext(ctx, `INSERT INTO consents(user_id,kind,enabled,updated_at) VALUES(?,?,?,?) ON CONFLICT(user_id,kind) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`, actor, p.Kind, p.Enabled, s.Store.Now().Unix()); e != nil {
				return e
			}
			if p.Kind == "ai" && !p.Enabled {
				if _, e = q.ExecContext(ctx, `UPDATE drafts SET expires_at=? WHERE applied_at IS NULL AND kind LIKE 'ai:%' AND (user_id=? OR pair_id=?)`, s.Store.Now().Unix(), actor, current); e != nil {
					return e
				}
			}
			result = json.RawMessage(`{"saved":true}`)
		}
		_, e = q.ExecContext(ctx, "UPDATE drafts SET applied_at=?,result=? WHERE id=?", s.Store.Now().Unix(), string(result), id)
		return e
	})
	return
}
