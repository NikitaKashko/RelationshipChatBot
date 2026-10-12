package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/NikitaKashko/RelationshipChatBot/internal/store"
	"github.com/NikitaKashko/RelationshipChatBot/internal/texts"
)

// Billing is bookkeeping, never admission control. A zero exchange rate leaves
// rubles unknown. Rates are a dated snapshot, not the provider's invoice.
type Billing struct {
	USDRUB    float64
	TargetRUB float64
}

// Estimate uses peak rates as a conservative estimate: no Chinese holiday
// calendar or assumption about the provider's eventual billing timestamp.
// Prices checked 2026-10-11: https://api-docs.deepseek.com/quick_start/pricing/
func Estimate(model string, u *Usage, fx float64) (usd, rub *float64) {
	if u == nil || u.Input < 0 || u.Cached < 0 || u.Cached > u.Input || u.Output < 0 {
		return nil, nil
	}
	var miss, hit, out float64
	switch model {
	case "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp":
		miss, hit, out = .3, .006, 1.2
	case "deepseek-v4-pro":
		miss, hit, out = 1.32, .044, 3.96
	default:
		return nil, nil
	}
	d := (float64(u.Input-u.Cached)*miss + float64(u.Cached)*hit + float64(u.Output)*out) / 1e6
	usd = &d
	if fx > 0 && !math.IsNaN(fx) && !math.IsInf(fx, 0) {
		r := d * fx
		if !math.IsInf(r, 0) {
			rub = &r
		}
	}
	return
}
func (s *Service) account(ctx context.Context, id int64, u *Usage, success bool) error {
	return s.Store.Write(ctx, func(q store.Query) error {
		var input, cached, output any
		usd, rub := Estimate(s.Model, u, s.Billing.USDRUB)
		if u != nil {
			input, cached, output = u.Input, u.Cached, u.Output
		}
		_, err := q.ExecContext(ctx, "UPDATE api_usage SET input_tokens=?,cached_tokens=?,output_tokens=?,estimated_usd=?,estimated_rub=?,uncertain=CASE WHEN provider='deepseek_search' THEN 1 ELSE ? END,success=? WHERE id=?", input, cached, output, usd, rub, usd == nil, success, id)
		if err != nil {
			return err
		}
		if s.Billing.TargetRUB <= 0 || rub == nil {
			return nil
		}
		now := s.Store.Now().UTC()
		start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
		var total float64
		var unknown int
		if err = q.QueryRowContext(ctx, "SELECT coalesce(sum(estimated_rub),0),coalesce(sum(estimated_rub IS NULL OR uncertain=1),0) FROM api_usage WHERE created_at>=? AND created_at<?", start.Unix(), start.AddDate(0, 1, 0).Unix()).Scan(&total, &unknown); err != nil {
			return err
		}
		if total < s.Billing.TargetRUB {
			return nil
		}
		payload, _ := json.Marshal(map[string]string{"text": fmt.Sprintf(texts.T("internal.ai.billing.001"), start.Format("2006-01"), total, s.Billing.TargetRUB, unknown)})
		_, err = q.ExecContext(ctx, `INSERT OR IGNORE INTO notifications(semantic_key,recipient_id,kind,payload,available_at,expires_at,created_at) SELECT 'budget:'||?||':'||id,id,'budget_target',?,?,?,? FROM users WHERE role='owner' AND status='ready'`, start.Format("2006-01"), string(payload), now.Unix(), now.Add(7*24*time.Hour).Unix(), now.Unix())
		return err
	})
}
