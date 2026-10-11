package ai

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/feedback"
	"github.com/NikitaKashko/RelationshipChatBot/internal/planning"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
)

func (s *Service) readContext(ctx context.Context, actor int64) (any, error) {
	pair, err := store.AIFor(ctx, s.Store.DB, actor)
	if err != nil {
		return nil, err
	}
	user, err := store.UserFor(ctx, s.Store.DB, actor, false)
	if err != nil {
		return nil, err
	}
	city, zone := user.City, user.Timezone
	if pair != 0 {
		var c, z sql.NullString
		if err = s.Store.DB.QueryRowContext(ctx, "SELECT city,timezone FROM pairs WHERE id=?", pair).Scan(&c, &z); err != nil {
			return nil, err
		}
		city = c.String
		zone = z.String
	}
	now := s.Store.Now().UTC().Format(time.RFC3339)
	result := map[string]any{"now_utc": now, "city": city, "timezone": zone, "plans": []any{}, "visits": []any{}}
	rows, err := s.Store.DB.QueryContext(ctx, "SELECT id,title,version,start_at,end_at,timezone,status FROM plans WHERE pair_id=? ORDER BY start_at DESC", pair)
	if err != nil {
		return nil, err
	}
	var plans []map[string]any
	for rows.Next() {
		var id, v int64
		var title, tz, status string
		var a, b float64
		if err = rows.Scan(&id, &title, &v, &a, &b, &tz, &status); err != nil {
			break
		}
		plans = append(plans, map[string]any{"id": id, "title": title, "version": v, "start": time.Unix(int64(a), 0).UTC().Format(time.RFC3339), "end": time.Unix(int64(b), 0).UTC().Format(time.RFC3339), "timezone": tz, "status": status})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		rows, err := s.Store.DB.QueryContext(ctx, "SELECT place_id FROM plan_items WHERE plan_id=? ORDER BY position", p["id"])
		if err != nil {
			return nil, err
		}
		var places []int64
		for rows.Next() {
			var id int64
			if err = rows.Scan(&id); err != nil {
				break
			}
			places = append(places, id)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return nil, err
		}
		p["places"] = places
	}
	result["plans"] = plans
	// Only the caller's unsaved plan drafts, with a closed DTO. This allows
	// "замени кафе" before saving without exposing hidden card fields.
	rows, err = s.Store.DB.QueryContext(ctx, "SELECT payload FROM drafts WHERE user_id=? AND coalesce(pair_id,0)=? AND kind='ai:plan_save' AND applied_at IS NULL AND expires_at>? ORDER BY created_at", actor, pair, s.Store.Now().Unix())
	if err != nil {
		return nil, err
	}
	var drafts []planning.Input
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var p planning.Input
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		drafts = append(drafts, p)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result["unsaved_plan_drafts"] = drafts
	rows, err = s.Store.DB.QueryContext(ctx, "SELECT v.id,vi.place_id,v.visit_date,v.date_precision FROM visits v JOIN visit_items vi ON vi.visit_id=v.id WHERE v.valid=1 AND (v.pair_id=? OR v.personal_user_id=?) ORDER BY v.id DESC", pair, actor)
	if err != nil {
		return nil, err
	}
	var visits []map[string]any
	for rows.Next() {
		var id, place int64
		var date, precision string
		if err = rows.Scan(&id, &place, &date, &precision); err != nil {
			break
		}
		visits = append(visits, map[string]any{"visit": id, "place": place, "date": date, "precision": precision})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result["visits"] = visits
	rows, err = s.Store.DB.QueryContext(ctx, `SELECT r.visit_id,r.place_id,r.user_id=?,r.rating,r.again FROM reviews r JOIN visits v ON v.id=r.visit_id WHERE v.valid=1 AND (v.pair_id=? OR (v.personal_user_id=? AND r.user_id=?)) AND (r.user_id=? OR EXISTS(SELECT 1 FROM pair_members m WHERE m.user_id=r.user_id AND m.pair_id=? AND m.active=1))`, actor, pair, actor, actor, actor, pair)
	if err != nil {
		return nil, err
	}
	var reviews []map[string]any
	for rows.Next() {
		var visit, place int64
		var mine bool
		var rating, again sql.NullString
		if err = rows.Scan(&visit, &place, &mine, &rating, &again); err != nil {
			break
		}
		reviews = append(reviews, map[string]any{"visit": visit, "place": place, "mine": mine, "rating": rating.String, "again": again.String})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result["ratings"] = reviews
	// Explicit field allowlist; identity, notification names and private texts are
	// never selected. Personal preference content is structured/confirmed separately.
	rows, err = s.Store.DB.QueryContext(ctx, `SELECT u.id=?,u.preferences FROM users u WHERE u.id=? OR EXISTS(SELECT 1 FROM pair_members m WHERE m.user_id=u.id AND m.pair_id=? AND m.active=1)`, actor, actor, pair)
	if err != nil {
		return nil, err
	}
	var prefs []map[string]any
	for rows.Next() {
		var me bool
		var raw string
		if err = rows.Scan(&me, &raw); err != nil {
			break
		}
		var p store.Preferences
		if err = json.Unmarshal([]byte(raw), &p); err != nil {
			break
		}
		prefs = append(prefs, map[string]any{"mine": me, "preferences": p})
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result["preferences"] = prefs
	return result, nil
}

func (s *Service) preview(ctx context.Context, actor int64, kind string, raw json.RawMessage) (string, error) {
	if _, err := store.AIFor(ctx, s.Store.DB, actor); err != nil {
		return "", err
	}
	cards := collection.Service{Store: s.Store}
	plans := planning.Service{Store: s.Store}
	facts := feedback.Service{Store: s.Store}
	switch kind {
	case "place_create":
		var p collection.Input
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		if err := collection.Validate(p); err != nil {
			return "", err
		}
		if len(p.Photos) > 0 || len(p.Sources) > 0 {
			return "", store.ErrDenied
		}
		return "Добавить идею?\n" + collection.Render(collection.Card{Input: p}), nil
	case "place_edit":
		var p collection.Edit
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.ID)
		if err != nil {
			return "", err
		}
		if c.Version != p.Version {
			return "", store.ErrConflict
		}
		if err = collection.Validate(p.Card); err != nil {
			return "", err
		}
		return "Изменить карточку?\n" + collection.Render(collection.Card{Input: p.Card}), nil
	case "place_archive":
		var p collection.Archive
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.ID)
		if err != nil {
			return "", err
		}
		if c.Version != p.Version {
			return "", store.ErrConflict
		}
		return fmt.Sprintf("Изменить архив «%s»: %t?", c.Title, p.Archived), nil
	case "candidate":
		var p planning.Candidate
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.ID)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Изменить кандидатов: «%s», убрать: %t?", c.Title, p.Remove), nil
	case "plan_save":
		var p planning.Input
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		if p.ID != 0 {
			old, err := plans.Get(ctx, s.Store.DB, actor, p.ID)
			if err != nil {
				return "", err
			}
			if old.Version != p.Version {
				return "", store.ErrConflict
			}
		}
		return plans.Preview(ctx, s.Store.DB, actor, p)
	case "plan_cancel", "plan_not_went":
		var p planning.Cancel
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		old, err := plans.Get(ctx, s.Store.DB, actor, p.ID)
		if err != nil {
			return "", err
		}
		if old.Version != p.Version {
			return "", store.ErrConflict
		}
		return fmt.Sprintf("%s: «%s»?", kind, old.Title), nil
	case "review":
		var p feedback.Review
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		if _, err := facts.VisitFor(ctx, s.Store.DB, actor, p.Visit, true); err != nil {
			return "", err
		}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.Place)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Только твой отзыв о «%s»:\nОценка: %s\nВернуться: %s\n%s\nСохранить? Фриз сам не меняется.", c.Title, p.Rating, p.Again, p.Comment), nil
	case "visit_record":
		var p feedback.Visit
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		for _, id := range p.Places {
			if err := store.PlaceFor(ctx, s.Store.DB, actor, id); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("Записать посещение: %s (%s), места %v, фриз: %t?", p.Date, p.Precision, p.Places, p.Freeze || p.Plan != 0), nil
	case "freeze":
		var p feedback.Freeze
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		c, err := cards.Get(ctx, s.Store.DB, actor, p.Place)
		if err != nil {
			return "", err
		}
		if p.Until == "" {
			return "Снять общий фриз «" + c.Title + "»? Личные отказы остаются.", nil
		}
		return "Установить фриз «" + c.Title + "» до " + p.Until + "?", nil
	case "preferences":
		var p store.Preferences
		if err := rawDecode(raw, &p); err != nil {
			return "", err
		}
		pretty, _ := json.MarshalIndent(p, "", "  ")
		return "Сохранить только твои постоянные предпочтения?\n" + string(pretty), nil
	default:
		return "", store.ErrDenied
	}
}
