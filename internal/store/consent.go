package store

import "context"

// AIFor checks consent for every active member, deriving scope from actor.
// It never accepts a model-supplied user or pair id.
func AIFor(ctx context.Context, q Query, actor int64) (int64, error) {
	if _, err := UserFor(ctx, q, actor, false); err != nil {
		return 0, err
	}
	pair, err := PairFor(ctx, q, actor)
	if err != nil {
		return 0, err
	}
	var missing int
	if pair == 0 {
		err = q.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE id=? AND NOT EXISTS(SELECT 1 FROM consents WHERE user_id=? AND kind='ai' AND enabled=1)", actor, actor).Scan(&missing)
	} else {
		err = q.QueryRowContext(ctx, `SELECT count(*) FROM pair_members m WHERE pair_id=? AND active=1 AND NOT EXISTS(SELECT 1 FROM consents c WHERE c.user_id=m.user_id AND c.kind='ai' AND c.enabled=1)`, pair).Scan(&missing)
	}
	if err != nil {
		return 0, err
	}
	if missing != 0 {
		return 0, ErrDenied
	}
	return pair, nil
}
