package actions

import (
	"errors"
	"time"
)

type QuietHours struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func (q QuietHours) Validate() error {
	for _, v := range []string{q.Start, q.End} {
		t, e := time.Parse("15:04", v)
		if e != nil || t.Format("15:04") != v {
			return errors.New("укажи время в формате ЧЧ:ММ")
		}
	}
	return nil
}
