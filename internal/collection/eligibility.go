package collection

// AutomaticEligible is the shared pre-ranking gate for future AI tools.
// Manual plan selection deliberately uses authorization rather than this gate.
func AutomaticEligible(c Card, now float64) bool {
	return !c.Archived && !c.Refused && c.FreezeUntil <= now
}
