// Package license implements Panelpost's editions and license keys.
//
// Two kinds of key are supported:
//
//   - Offline keys ("PP1-..."), signed with Ed25519 by the vendor. They work
//     in air-gapped networks and never phone home.
//   - Merchant-of-record keys issued automatically at checkout by Polar or
//     Lemon Squeezy. They are activated once and re-validated daily, with a
//     grace period so a network outage never breaks scheduled reports.
package license

import (
	"strings"
	"time"
)

// Tier is a commercial edition.
type Tier string

const (
	Free     Tier = "free"
	Pro      Tier = "pro"
	Business Tier = "business"
)

// ParseTier converts user or provider input into a known tier.
func ParseTier(s string) (Tier, bool) {
	switch Tier(strings.ToLower(strings.TrimSpace(s))) {
	case Free:
		return Free, true
	case Pro:
		return Pro, true
	case Business, "msp":
		return Business, true
	}
	return "", false
}

// Title is the marketing name of the tier.
func (t Tier) Title() string {
	switch t {
	case Pro:
		return "Pro"
	case Business:
		return "Business"
	}
	return "Community"
}

// Limits describe what an edition may do. Zero numeric limits mean unlimited.
type Limits struct {
	MaxReports      int
	MaxConnections  int
	MaxBurstTargets int
	HistoryDays     int
	Bursting        bool
	Branding        bool
	RemoveCredit    bool
	Webhooks        bool
	API             bool
	WhiteLabel      bool
}

// LimitsFor returns the limits of a tier.
func LimitsFor(t Tier) Limits {
	switch t {
	case Pro:
		return Limits{
			MaxConnections:  3,
			MaxBurstTargets: 25,
			HistoryDays:     365,
			Bursting:        true,
			Branding:        true,
			RemoveCredit:    true,
			Webhooks:        true,
			API:             true,
		}
	case Business:
		return Limits{
			HistoryDays:  730,
			Bursting:     true,
			Branding:     true,
			RemoveCredit: true,
			Webhooks:     true,
			API:          true,
			WhiteLabel:   true,
		}
	}
	return Limits{
		MaxReports:     3,
		MaxConnections: 1,
		HistoryDays:    14,
	}
}

// License is the effective license of this installation.
type License struct {
	Tier      Tier      `json:"tier"`
	ID        string    `json:"id,omitempty"`
	Licensee  string    `json:"licensee,omitempty"`
	Email     string    `json:"email,omitempty"`
	Source    string    `json:"source,omitempty"` // offline, polar, lemonsqueezy
	ExpiresAt time.Time `json:"expires_at,omitempty"`
	// ValidatedAt is the last successful online validation.
	ValidatedAt time.Time `json:"validated_at,omitempty"`
	// Problem explains why a key is not (or no longer) effective.
	Problem string `json:"problem,omitempty"`
}

// Community is the license of an installation without a key.
func Community() License { return License{Tier: Free} }

// Limits returns the limits of the effective tier.
func (l License) Limits() Limits { return LimitsFor(l.Tier) }

// Paid reports whether a paid tier is active.
func (l License) Paid() bool { return l.Tier == Pro || l.Tier == Business }
