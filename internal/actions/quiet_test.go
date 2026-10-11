package actions

import "testing"

func TestQuietHoursValidation(t *testing.T) {
	for _, q := range []QuietHours{{"23:00", "09:00"}, {"00:00", "00:00"}, {"09:00", "17:00"}} {
		if err := q.Validate(); err != nil {
			t.Fatal(q, err)
		}
	}
	for _, q := range []QuietHours{{"24:00", "09:00"}, {"23:60", "09:00"}, {"9:00", "17:00"}, {"no", "no"}} {
		if err := q.Validate(); err == nil {
			t.Fatal("accepted invalid window", q)
		}
	}
}
