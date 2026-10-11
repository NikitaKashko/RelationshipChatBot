// Package feedback records confirmed facts; elapsed plans never create visits.
// Mutations run inside the caller's transaction and derive ownership from actor.
package feedback

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
)

type Service struct{ Store *store.Store }
type Visit struct {
	Plan      int64   `json:"plan,omitempty"`
	Version   int64   `json:"version,omitempty"`
	Places    []int64 `json:"places"`
	Date      string  `json:"date"`
	Precision string  `json:"precision"`
	Freeze    bool    `json:"freeze"`
}
type Correction struct {
	ID int64 `json:"id"`
}
type Review struct {
	Visit   int64  `json:"visit"`
	Place   int64  `json:"place"`
	Rating  string `json:"rating"`
	Again   string `json:"again"`
	Comment string `json:"comment"`
}
type Freeze struct {
	Place int64  `json:"place"`
	Until string `json:"until"`
}

func date(p Visit, zone string, now time.Time) (any, error) {
	layouts := map[string]string{"day": "2006-01-02", "month": "2006-01", "year": "2006"}
	layout, ok := layouts[p.Precision]
	if !ok {
		return nil, errors.New("укажи точность даты: day, month или year")
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	t, err := time.ParseInLocation(layout, p.Date, loc)
	if err != nil || t.Format(layout) != p.Date || t.After(now) {
		return nil, errors.New("нужна действительная дата прошлого посещения")
	}
	if p.Precision != "day" {
		if p.Freeze {
			return nil, errors.New("для автоматического фриза нужна точная дата; примерную дату можно сохранить без него")
		}
		return nil, nil
	}
	return t.Unix(), nil
}

func (s *Service) notify(ctx context.Context, q store.Query, actor, pair int64, kind string, fields any) error {
	if pair == 0 {
		return nil
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	// The durable id is also the semantic key. Confirmed drafts ensure replay
	// cannot insert another event; direct domain writes are separate events.
	var next int64
	if err = q.QueryRowContext(ctx, "SELECT coalesce(max(id),0)+1 FROM notifications").Scan(&next); err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) SELECT ?,?,user_id,?,?,?,?,? FROM pair_members WHERE pair_id=? AND active=1 AND user_id<>?`, fmt.Sprintf("feedback:%d", next), pair, kind, string(raw), s.Store.Now().Unix(), s.Store.Now().Add(48*time.Hour).Unix(), s.Store.Now().Unix(), pair, actor)
	return err
}

func (s *Service) Record(ctx context.Context, q store.Query, actor int64, p Visit) (int64, error) {
	u, err := store.UserFor(ctx, q, actor, false)
	if err != nil {
		return 0, err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	zone := u.Timezone
	if pair != 0 {
		var z sql.NullString
		if err = q.QueryRowContext(ctx, "SELECT timezone FROM pairs WHERE id=?", pair).Scan(&z); err != nil {
			return 0, err
		}
		if z.Valid {
			zone = z.String
		}
	}
	at, err := date(p, zone, s.Store.Now())
	if err != nil {
		return 0, err
	}
	if len(p.Places) == 0 {
		return 0, errors.New("выбери посещённые места")
	}
	if p.Plan != 0 {
		plans := planning.Service{Store: s.Store}
		plan, err := plans.Get(ctx, q, actor, p.Plan)
		if err != nil {
			return 0, err
		}
		if plan.Version != p.Version || plan.Status != "planned" {
			return 0, store.ErrConflict
		}
		if p.Precision != "day" || plan.Start.After(s.Store.Now()) {
			return 0, errors.New("запланированный поход ещё не состоялся или дата неточная")
		}
		allowed := map[int64]bool{}
		for _, c := range plan.Items {
			allowed[c.ID] = true
		}
		for _, id := range p.Places {
			if !allowed[id] {
				return 0, store.ErrDenied
			}
		}
		// Scheduled visits always freeze; manual history requires a deliberate choice.
		p.Freeze = true
	}
	seen := map[int64]bool{}
	created := float64(s.Store.Now().UnixNano()) / 1e9
	for _, id := range p.Places {
		if seen[id] {
			return 0, errors.New("место повторяется")
		}
		seen[id] = true
		if err = store.PlaceFor(ctx, q, actor, id); err != nil {
			return 0, err
		}
		var placePair sql.NullInt64
		if err = q.QueryRowContext(ctx, "SELECT pair_id FROM places WHERE id=?", id).Scan(&placePair); err != nil {
			return 0, err
		}
		if placePair.Int64 != pair {
			return 0, store.ErrDenied
		}
		// Distinguish a new confirmed visit from an earlier removal even when both
		// actions share the same second. Corrections never modify this watermark.
		var changed sql.NullFloat64
		if err = q.QueryRowContext(ctx, "SELECT max(changed_at) FROM freeze_overrides WHERE place_id=? AND until_at IS NULL", id).Scan(&changed); err != nil {
			return 0, err
		}
		if changed.Valid && created <= changed.Float64 {
			created = changed.Float64 + 0.000001
		}
	}
	var pairID, personal, planID any
	if pair != 0 {
		pairID = pair
	} else {
		personal = actor
	}
	if p.Plan != 0 {
		planID = p.Plan
	}
	res, err := q.ExecContext(ctx, `INSERT INTO visits(pair_id,personal_user_id,plan_id,marked_by,visit_date,date_precision,visited_at,automatic_freeze,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, pairID, personal, planID, actor, p.Date, p.Precision, at, p.Freeze, created)
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, place := range p.Places {
		if _, err = q.ExecContext(ctx, "INSERT INTO visit_items(visit_id,place_id) VALUES(?,?)", id, place); err != nil {
			return 0, err
		}
	}
	if p.Plan != 0 {
		if _, err = q.ExecContext(ctx, "UPDATE plans SET status='visited' WHERE id=?", p.Plan); err != nil {
			return 0, err
		}
		// Feedback remains for each partner's impressions; reminders no longer matter.
		if _, err = q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND kind='reminder' AND json_extract(payload,'$.plan_id')=?`, p.Plan); err != nil {
			return 0, err
		}
	}
	// No retrospective prompt for manual or spontaneous history.
	return id, nil
}

func (s *Service) VisitFor(ctx context.Context, q store.Query, actor, id int64, valid bool) (int64, error) {
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return 0, err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	var scope sql.NullInt64
	var ok bool
	err = q.QueryRowContext(ctx, "SELECT pair_id,valid FROM visits WHERE id=? AND (pair_id=? OR personal_user_id=?)", id, pair, actor).Scan(&scope, &ok)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrDenied
	}
	if err != nil {
		return 0, err
	}
	if valid && !ok {
		return 0, store.ErrConflict
	}
	return scope.Int64, nil
}
func (s *Service) Correct(ctx context.Context, q store.Query, actor int64, p Correction) error {
	pair, err := s.VisitFor(ctx, q, actor, p.ID, true)
	if err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, "UPDATE visits SET valid=0 WHERE id=?", p.ID); err != nil {
		return err
	}
	// Plan remains historical. Invalidate old questionnaire without inventing
	// another visit or silently changing the separately stored manual override.
	if _, err = q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND kind='feedback' AND json_extract(payload,'$.plan_id')=(SELECT plan_id FROM visits WHERE id=?)`, p.ID); err != nil {
		return err
	}
	return s.notify(ctx, q, actor, pair, "visit_corrected", map[string]int64{"visit_id": p.ID})
}
func (s *Service) NotWent(ctx context.Context, q store.Query, actor int64, p planning.Cancel) error {
	plans := planning.Service{Store: s.Store}
	old, err := plans.Get(ctx, q, actor, p.ID)
	if err != nil {
		return err
	}
	if old.Version != p.Version || old.Status != "planned" {
		return store.ErrConflict
	}
	if _, err = q.ExecContext(ctx, "UPDATE plans SET status='not_went',version=version+1 WHERE id=?", p.ID); err != nil {
		return err
	}
	if _, err = q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND kind IN ('reminder','feedback') AND json_extract(payload,'$.plan_id')=?`, p.ID); err != nil {
		return err
	}
	return s.notify(ctx, q, actor, old.Pair, "plan_not_went", map[string]int64{"plan_id": p.ID})
}
func (s *Service) Review(ctx context.Context, q store.Query, actor int64, p Review) error {
	if _, err := s.VisitFor(ctx, q, actor, p.Visit, true); err != nil {
		return err
	}
	if err := store.PlaceFor(ctx, q, actor, p.Place); err != nil {
		return err
	}
	if p.Rating != "liked" && p.Rating != "neutral" && p.Rating != "disliked" {
		return errors.New("выбери оценку")
	}
	if p.Again != "again" && p.Again != "not_soon" && p.Again != "never" {
		return errors.New("выбери желание вернуться")
	}
	if len([]rune(p.Comment)) > 2000 {
		return errors.New("комментарий слишком длинный")
	}
	var count int
	if err := q.QueryRowContext(ctx, "SELECT count(*) FROM visit_items WHERE visit_id=? AND place_id=?", p.Visit, p.Place).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return store.ErrDenied
	}
	_, err := q.ExecContext(ctx, `INSERT INTO reviews(visit_id,place_id,user_id,rating,again,comment) VALUES(?,?,?,?,?,?) ON CONFLICT(visit_id,place_id,user_id) DO UPDATE SET rating=excluded.rating,again=excluded.again,comment=excluded.comment`, p.Visit, p.Place, actor, p.Rating, p.Again, strings.TrimSpace(p.Comment))
	if err != nil {
		return err
	}
	// Changing this author's response never edits the other partner's refusal.
	_, err = q.ExecContext(ctx, `INSERT INTO place_preferences(place_id,user_id,refused) VALUES(?,?,?) ON CONFLICT(place_id,user_id) DO UPDATE SET refused=excluded.refused`, p.Place, actor, p.Again == "never")
	return err
}
func (s *Service) Freeze(ctx context.Context, q store.Query, actor int64, p Freeze) error {
	if err := store.PlaceFor(ctx, q, actor, p.Place); err != nil {
		return err
	}
	var until any
	if p.Until != "" {
		t, err := time.Parse(time.RFC3339, p.Until)
		if err != nil || !t.After(s.Store.Now()) {
			return errors.New("укажи будущую дату фриза с UTC-смещением")
		}
		until = t.Unix()
	}
	_, err := q.ExecContext(ctx, `INSERT INTO freeze_overrides(place_id,until_at,changed_by,changed_at) VALUES(?,?,?,?) ON CONFLICT(place_id) DO UPDATE SET until_at=excluded.until_at,changed_by=excluded.changed_by,changed_at=excluded.changed_at`, p.Place, until, actor, float64(s.Store.Now().UnixNano())/1e9)
	if err != nil {
		return err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return err
	}
	return s.notify(ctx, q, actor, pair, "freeze_changed", map[string]any{"place_id": p.Place, "until": p.Until})
}
