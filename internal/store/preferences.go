package store

// Preferences contains only explicitly saved personal choices, never inferred
// silently from conversation. No identity or arbitrary additional fields.
type Preferences struct {
	Interests     []string `json:"interests,omitempty"`
	Unwanted      []string `json:"unwanted,omitempty"`
	Food          []string `json:"food,omitempty"`
	Accessibility []string `json:"accessibility,omitempty"`
	Transport     []string `json:"transport,omitempty"`
}
