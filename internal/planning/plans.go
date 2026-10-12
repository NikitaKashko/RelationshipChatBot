// Package planning saves dated plans and durable notifications atomically.
package planning

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/NikitaKashko/RelationshipChatBot/internal/collection"
	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

type Service struct{ Store *store.Store }
type Stale struct{ ID int64 }

var ErrPairTimezone = errors.New(texts.T("internal.planning.plans.017"))

func (e *Stale) Error() string {
	return store.ErrConflict.Error()
}
func (e *Stale) Unwrap() error {
	return store.ErrConflict
}

type Input struct {
	Draft       bool    `json:"draft,omitempty"`
	Date        string  `json:"date,omitempty"`
	Description string  `json:"description,omitempty"`
	ID          int64   `json:"id,omitempty"`
	Version     int64   `json:"version,omitempty"`
	Title       string  `json:"title"`
	Start       string  `json:"start"`
	End         string  `json:"end"`
	Timezone    string  `json:"timezone"`
	Places      []int64 `json:"places"`
}
type Candidate struct {
	ID     int64 `json:"id"`
	Remove bool  `json:"remove,omitempty"`
}
type Cancel struct {
	ID      int64 `json:"id"`
	Version int64 `json:"version"`
}
type Plan struct {
	HasEnd                  bool
	Draft, DateOnly         bool
	Description             string
	ID, Version, Pair       int64
	Title, Timezone, Status string
	Start, End              time.Time
	Items                   []collection.Card
}

func parse(raw string, loc *time.Location) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	const layout = "2006-01-02 15:04"
	t, err := time.ParseInLocation(layout, raw, loc)
	if err != nil || t.In(loc).Format(layout) != raw {
		return time.Time{}, errors.New(texts.T("internal.planning.plans.001"))
	}
	// Reject ambiguous wall clocks during a timezone fall-back. Offset-bearing
	// RFC3339 remains available to choose the intended actual instant explicitly.
	for _, d := range []time.Duration{-2 * time.Hour, -time.Hour, -30 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		if t.Add(d).In(loc).Format(layout) == raw {
			return time.Time{}, errors.New(texts.T("internal.planning.plans.002"))
		}
	}
	return t, nil
}
func Dates(p Input) (time.Time, time.Time, error) {
	if p.Draft && p.Date == "" && p.Start == "" {
		return time.Unix(0, 0), time.Unix(1, 0), nil
	}
	if p.Timezone == "" || p.Timezone == "Local" {
		return time.Time{}, time.Time{}, errors.New(texts.T("internal.planning.plans.003"))
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if p.Start == "" && p.Date != "" {
		day, e := time.ParseInLocation("2006-01-02", p.Date, loc)
		if e != nil || day.Format("2006-01-02") != p.Date {
			return time.Time{}, time.Time{}, errors.New(texts.T("internal.planning.plans.004"))
		}
		return day.Add(10 * time.Hour), day.Add(23*time.Hour + 59*time.Minute), nil
	}
	start, err := parse(p.Start, loc)
	if err != nil {
		return start, time.Time{}, err
	}
	if p.End == "" {
		local := start.In(loc)
		end := time.Date(local.Year(), local.Month(), local.Day(), 23, 59, 59, 0, loc)
		if !end.After(start) {
			end = start.Add(time.Second)
		}
		return start, end, nil
	}
	end, err := parse(p.End, loc)
	if err != nil {
		return start, end, err
	}
	if !end.After(start) {
		return start, end, errors.New(texts.T("internal.planning.plans.005"))
	}
	return start, end, nil
}
func Validate(p Input) error {
	if strings.TrimSpace(p.Title) == "" || len([]rune(p.Title)) > 200 || len([]rune(p.Description)) > 3000 {
		return errors.New(texts.T("internal.planning.plans.006"))
	}
	if p.ID != 0 && p.Version < 1 {
		return errors.New(texts.T("internal.planning.plans.007"))
	}
	_, _, err := Dates(p)
	return err
}
func (s *Service) Candidate(ctx context.Context, q store.Query, actor int64, p Candidate) error {
	if err := store.PlaceFor(ctx, q, actor, p.ID); err != nil {
		return err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return err
	}
	if pair == 0 {
		return store.ErrDenied
	}
	var archived bool
	var placePair sql.NullInt64
	if err = q.QueryRowContext(ctx, "SELECT archived,pair_id FROM places WHERE id=?", p.ID).Scan(&archived, &placePair); err != nil {
		return err
	}
	if archived || placePair.Int64 != pair {
		return store.ErrDenied
	}
	if p.Remove {
		_, err = q.ExecContext(ctx, "DELETE FROM candidates WHERE pair_id=? AND place_id=?", pair, p.ID)
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT OR IGNORE INTO candidates(pair_id,place_id,added_by,position) SELECT ?,?,?,coalesce(max(position)+1,0) FROM candidates WHERE pair_id=?`, pair, p.ID, actor, pair)
	return err
}
func (s *Service) Get(ctx context.Context, q store.Query, actor, id int64) (Plan, error) {
	var p Plan
	if err := store.PlanFor(ctx, q, actor, id); err != nil {
		return p, err
	}
	var start, end float64
	err := q.QueryRowContext(ctx, "SELECT id,pair_id,version,title,start_at,end_at,timezone,status,is_draft,date_only,description,has_end FROM plans WHERE id=?", id).Scan(&p.ID, &p.Pair, &p.Version, &p.Title, &start, &end, &p.Timezone, &p.Status, &p.Draft, &p.DateOnly, &p.Description, &p.HasEnd)
	if err != nil {
		return p, err
	}
	p.Start = time.Unix(int64(start), 0)
	p.End = time.Unix(int64(end), 0)
	rows, err := q.QueryContext(ctx, "SELECT snapshot FROM plan_items WHERE plan_id=? ORDER BY position", id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var card collection.Card
		if err = rows.Scan(&raw); err != nil {
			return p, err
		}
		if err = json.Unmarshal([]byte(raw), &card); err != nil {
			return p, err
		}
		p.Items = append(p.Items, card)
	}
	return p, rows.Err()
}
func (s *Service) Preview(ctx context.Context, q store.Query, actor int64, p Input) (string, error) {
	if err := Validate(p); err != nil {
		return "", err
	}
	if _, err := store.UserFor(ctx, q, actor, false); err != nil {
		return "", err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return "", err
	}
	if pair == 0 {
		return "", store.ErrDenied
	}
	start, end, _ := Dates(p)
	loc, _ := time.LoadLocation(p.Timezone)
	if loc == nil {
		loc = time.UTC
	}
	text := p.Title + texts.T("internal.planning.plans.008") + start.In(loc).Format("02.01.2006 15:04") + texts.T("internal.planning.plans.009") + end.In(loc).Format("02.01.2006 15:04") + "\n" + p.Timezone
	if p.Draft {
		text = texts.T("internal.planning.plans.010") + p.Title
		if start.Unix() != 0 {
			text += texts.T("internal.planning.plans.011") + start.In(loc).Format("02.01.2006")
		}
	} else if p.Start == "" {
		text = p.Title + texts.T("internal.planning.plans.012") + p.Date + texts.T("internal.planning.plans.013")
	} else if p.End == "" {
		text = p.Title + texts.T("internal.planning.plans.008") + start.In(loc).Format("02.01.2006 15:04") + texts.T("internal.planning.plans.014")
	}
	if p.Description != "" {
		text += "\n" + p.Description
	}
	c := collection.Service{Store: s.Store}
	for i, id := range p.Places {
		card, err := c.Get(ctx, q, actor, id)
		if err != nil {
			return "", err
		}
		if card.Archived {
			return "", store.ErrDenied
		}
		text += fmt.Sprintf("\n%d. %s", i+1, card.Title)
		if card.Refused {
			text += texts.T("internal.planning.plans.015")
		}
		if card.FreezeUntil > float64(s.Store.Now().Unix()) {
			text += texts.T("internal.planning.plans.016")
		}
	}
	return text, nil
}
func (s *Service) Save(ctx context.Context, q store.Query, actor int64, p Input) (int64, error) {
	if p.Draft && p.Timezone == "" {
		u, err := store.UserFor(ctx, q, actor, false)
		if err != nil {
			return 0, err
		}
		p.Timezone = u.Timezone
	}
	if _, err := s.Preview(ctx, q, actor, p); err != nil {
		return 0, err
	}
	pair, err := store.PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	start, end, _ := Dates(p)
	var pairTimezone sql.NullString
	if err = q.QueryRowContext(ctx, "SELECT timezone FROM pairs WHERE id=?", pair).Scan(&pairTimezone); err != nil {
		return 0, err
	}
	if !pairTimezone.Valid && !p.Draft {
		return 0, ErrPairTimezone
	}
	changed := []string{texts.T("internal.planning.plans.018")}
	version := int64(1)
	id := p.ID
	if id != 0 {
		old, err := s.Get(ctx, q, actor, id)
		if err != nil {
			return 0, err
		}
		if old.Version != p.Version || old.Status != "planned" {
			return 0, &Stale{ID: id}
		}
		version = old.Version + 1
		changed = nil
		if old.Title != p.Title {
			changed = append(changed, texts.T("internal.planning.plans.019"))
		}
		if !old.Start.Equal(start) {
			changed = append(changed, texts.T("internal.planning.plans.020"))
		}
		if !old.End.Equal(end) {
			changed = append(changed, texts.T("internal.planning.plans.021"))
		}
		if old.Timezone != p.Timezone {
			changed = append(changed, texts.T("internal.planning.plans.022"))
		}
		same := len(old.Items) == len(p.Places)
		if same {
			for i, v := range p.Places {
				if old.Items[i].ID != v {
					same = false
					break
				}
			}
		}
		if !same {
			changed = append(changed, texts.T("internal.planning.plans.023"))
		}
		if len(changed) == 0 {
			changed = append(changed, texts.T("internal.planning.plans.024"))
		}
		if _, err = q.ExecContext(ctx, "UPDATE plans SET title=?,start_at=?,end_at=?,timezone=?,version=? WHERE id=? AND version=?", p.Title, start.Unix(), end.Unix(), p.Timezone, version, id, p.Version); err != nil {
			return 0, err
		}
		if _, err = q.ExecContext(ctx, "DELETE FROM plan_items WHERE plan_id=?", id); err != nil {
			return 0, err
		}
	} else {
		r, err := q.ExecContext(ctx, "INSERT INTO plans(pair_id,author_id,title,start_at,end_at,timezone,created_at) VALUES(?,?,?,?,?,?,?)", pair, actor, p.Title, start.Unix(), end.Unix(), p.Timezone, s.Store.Now().Unix())
		if err != nil {
			return 0, err
		}
		id, err = r.LastInsertId()
		if err != nil {
			return 0, err
		}
	}
	if _, err = q.ExecContext(ctx, "UPDATE plans SET is_draft=?,date_only=?,description=?,has_end=? WHERE id=?", p.Draft, p.Start == "" && p.Date != "", p.Description, p.End != "", id); err != nil {
		return 0, err
	}
	c := collection.Service{Store: s.Store}
	for i, place := range p.Places {
		card, err := c.Get(ctx, q, actor, place)
		if err != nil {
			return 0, err
		}
		raw, _ := json.Marshal(card)
		if _, err = q.ExecContext(ctx, "INSERT INTO plan_items(plan_id,place_id,position,snapshot) VALUES(?,?,?,?)", id, place, i, string(raw)); err != nil {
			return 0, err
		}
	}
	if err = s.cancelJobs(ctx, q, id); err != nil {
		return 0, err
	}
	var partner int64
	if err = q.QueryRowContext(ctx, "SELECT user_id FROM pair_members WHERE pair_id=? AND active=1 AND user_id<>?", pair, actor).Scan(&partner); err != nil {
		return 0, err
	}
	preview, err := s.Preview(ctx, q, actor, p)
	if err != nil {
		return 0, err
	}
	if err = s.enqueue(ctx, q, pair, partner, "plan_saved", id, version, map[string]any{"summary": preview, "changed": changed}, s.Store.Now(), s.Store.Now().Add(48*time.Hour)); err != nil {
		return 0, err
	}
	if !p.Draft {
		if err = s.schedule(ctx, q, pair, id, version, start, end, pairTimezone.String); err != nil {
			return 0, err
		}
	}
	return id, nil
}
func (s *Service) cancelJobs(ctx context.Context, q store.Query, id int64) error {
	_, err := q.ExecContext(ctx, `UPDATE notifications SET status='cancelled' WHERE status='pending' AND kind IN ('reminder','feedback') AND json_extract(payload,'$.plan_id')=?`, id)
	return err
}
func (s *Service) enqueue(ctx context.Context, q store.Query, pair, recipient int64, kind string, id, version int64, fields map[string]any, at, expires time.Time) error {
	fields["plan_id"] = id
	fields["version"] = version
	raw, _ := json.Marshal(fields)
	_, err := q.ExecContext(ctx, `INSERT OR IGNORE INTO notifications(semantic_key,pair_id,recipient_id,kind,payload,available_at,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`, fmt.Sprintf("plan:%d:v%d:%s:%d", id, version, kind, recipient), pair, recipient, kind, string(raw), at.Unix(), expires.Unix(), s.Store.Now().Unix())
	return err
}
func (s *Service) schedule(ctx context.Context, q store.Query, pair, id, version int64, start, end time.Time, zone string) error {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	local := end.In(loc)
	date := local.AddDate(0, 0, 1)
	var feedbackTime string
	if err = q.QueryRowContext(ctx, "SELECT feedback_time FROM pairs WHERE id=?", pair).Scan(&feedbackTime); err != nil {
		return err
	}
	clock, err := time.Parse("15:04", feedbackTime)
	if err != nil {
		return err
	}
	feedback := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
	rows, err := q.QueryContext(ctx, "SELECT u.id,u.reminder_enabled FROM pair_members m JOIN users u ON u.id=m.user_id WHERE m.pair_id=? AND m.active=1", pair)
	if err != nil {
		return err
	}
	type member struct {
		id       int64
		reminder bool
	}
	var members []member
	for rows.Next() {
		var m member
		if err = rows.Scan(&m.id, &m.reminder); err != nil {
			rows.Close()
			return err
		}
		members = append(members, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, m := range members {
		var dateOnly bool
		if err = q.QueryRowContext(ctx, "SELECT date_only FROM plans WHERE id=?", id).Scan(&dateOnly); err != nil {
			return err
		}
		remindAt := start.Add(-2 * time.Hour)
		expires := start
		if dateOnly {
			remindAt = start
			expires = end
		}
		if m.reminder && start.After(s.Store.Now()) {
			if err = s.enqueue(ctx, q, pair, m.id, "reminder", id, version, map[string]any{"start_at": start.Unix()}, remindAt, expires); err != nil {
				return err
			}
		}
		if err = s.enqueue(ctx, q, pair, m.id, "feedback", id, version, map[string]any{}, feedback, feedback.Add(48*time.Hour)); err != nil {
			return err
		}
	}
	return nil
}
func (s *Service) Cancel(ctx context.Context, q store.Query, actor int64, p Cancel) error {
	old, err := s.Get(ctx, q, actor, p.ID)
	if err != nil {
		return err
	}
	if old.Version != p.Version || old.Status != "planned" {
		return &Stale{ID: p.ID}
	}
	version := p.Version + 1
	if _, err = q.ExecContext(ctx, "UPDATE plans SET status='cancelled',version=? WHERE id=?", version, p.ID); err != nil {
		return err
	}
	if err = s.cancelJobs(ctx, q, p.ID); err != nil {
		return err
	}
	var partner int64
	if err = q.QueryRowContext(ctx, "SELECT user_id FROM pair_members WHERE pair_id=? AND active=1 AND user_id<>?", old.Pair, actor).Scan(&partner); err != nil {
		return err
	}
	return s.enqueue(ctx, q, old.Pair, partner, "plan_cancelled", p.ID, version, map[string]any{"title": old.Title}, s.Store.Now(), s.Store.Now().Add(48*time.Hour))
}

// RefreshFeedback changes future calendar schedules, never saved plan instants.
func (s *Service) RefreshFeedback(ctx context.Context, q store.Query, pair int64, zone string) error {
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return err
	}
	var clockString string
	if err = q.QueryRowContext(ctx, "SELECT feedback_time FROM pairs WHERE id=?", pair).Scan(&clockString); err != nil {
		return err
	}
	clock, err := time.Parse("15:04", clockString)
	if err != nil {
		return err
	}
	rows, err := q.QueryContext(ctx, `SELECT n.id,p.end_at FROM notifications n JOIN plans p ON p.id=json_extract(n.payload,'$.plan_id') WHERE n.pair_id=? AND n.kind='feedback' AND n.status='pending' AND n.available_at>? AND p.status IN ('planned','visited')`, pair, s.Store.Now().Unix())
	if err != nil {
		return err
	}
	type entry struct {
		id  int64
		end float64
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.end); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range entries {
		day := time.Unix(int64(e.end), 0).In(loc).AddDate(0, 0, 1)
		at := time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, loc)
		if _, err = q.ExecContext(ctx, "UPDATE notifications SET available_at=?,expires_at=? WHERE id=?", at.Unix(), at.Add(48*time.Hour).Unix(), e.id); err != nil {
			return err
		}
	}
	return nil
}
