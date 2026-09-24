package license

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// GracePeriod is how long a validated online key keeps working while the
// license server cannot be reached.
const GracePeriod = 14 * 24 * time.Hour

// RevalidateEvery is how often online keys are checked.
const RevalidateEvery = 24 * time.Hour

// Settings persists license state; the store implements it.
type Settings interface {
	GetSetting(key string) (string, error)
	SetSetting(key, value string) error
	DeleteSetting(key string) error
}

const (
	keySetting   = "license.key"
	stateSetting = "license.state"
)

type state struct {
	License      License `json:"license"`
	Provider     string  `json:"provider,omitempty"`
	ActivationID string  `json:"activation_id,omitempty"`
}

// Manager owns the effective license of the installation.
type Manager struct {
	settings  Settings
	providers []Provider
	trusted   []ed25519.PublicKey
	label     string
	now       func() time.Time
	log       *slog.Logger

	mu      sync.RWMutex
	current License
	st      state
}

// Options configure a Manager.
type Options struct {
	Settings   Settings
	Providers  []Provider
	PublicKeys []ed25519.PublicKey
	// InstanceLabel names this installation when activating online keys.
	InstanceLabel string
	Now           func() time.Time
	Logger        *slog.Logger
}

// NewManager loads the stored key, if any.
func NewManager(opts Options) *Manager {
	m := &Manager{
		settings:  opts.Settings,
		providers: opts.Providers,
		trusted:   opts.PublicKeys,
		label:     opts.InstanceLabel,
		now:       opts.Now,
		log:       opts.Logger,
		current:   Community(),
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.log == nil {
		m.log = slog.Default()
	}
	if m.label == "" {
		m.label = "panelpost"
	}
	m.load()
	return m
}

// DefaultProviders builds the providers from the embedded configuration.
func DefaultProviders() []Provider {
	cfg := EmbeddedProviderConfig()
	var out []Provider
	if cfg.Polar.OrganizationID != "" {
		out = append(out, &Polar{OrganizationID: cfg.Polar.OrganizationID, Benefits: cfg.Polar.Benefits})
	}
	if cfg.LemonSqueezy.StoreID != 0 {
		out = append(out, &LemonSqueezy{StoreID: cfg.LemonSqueezy.StoreID, Variants: cfg.LemonSqueezy.Variants})
	}
	return out
}

func (m *Manager) load() {
	key, _ := m.settings.GetSetting(keySetting)
	if key == "" {
		return
	}
	if strings.HasPrefix(key, OfflinePrefix) {
		lic, err := VerifyOffline(key, m.trusted, m.now())
		if err != nil {
			lic.Tier = Free
			lic.Problem = err.Error()
		}
		m.current = lic
		return
	}
	raw, _ := m.settings.GetSetting(stateSetting)
	var st state
	if raw != "" && json.Unmarshal([]byte(raw), &st) == nil {
		m.st = st
		m.current = m.withGrace(st.License)
	}
}

// withGrace downgrades a cached online license whose last validation is too old.
func (m *Manager) withGrace(l License) License {
	if l.Tier == Free {
		return l
	}
	if l.ValidatedAt.IsZero() || m.now().Sub(l.ValidatedAt) > GracePeriod {
		l.Problem = "the license could not be validated for 14 days; check that this server can reach the license server"
		l.Tier = Free
	}
	return l
}

// Current returns the effective license.
func (m *Manager) Current() License {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

// Key returns the stored key with all but the last characters masked.
func (m *Manager) MaskedKey() string {
	key, _ := m.settings.GetSetting(keySetting)
	if len(key) <= 8 {
		return key
	}
	return strings.Repeat("•", 8) + key[len(key)-6:]
}

// Apply installs a new key, activating it online when needed.
func (m *Manager) Apply(ctx context.Context, key string) (License, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return License{}, errors.New("enter a license key")
	}
	if strings.HasPrefix(key, OfflinePrefix) {
		lic, err := VerifyOffline(key, m.trusted, m.now())
		if err != nil {
			return License{}, err
		}
		if err := m.settings.SetSetting(keySetting, key); err != nil {
			return License{}, err
		}
		_ = m.settings.DeleteSetting(stateSetting)
		m.set(lic, state{License: lic})
		return lic, nil
	}
	var lastErr error
	configured := 0
	for _, p := range m.providers {
		if !p.Configured() {
			continue
		}
		configured++
		st, err := p.Activate(ctx, key, m.label)
		if err != nil {
			lastErr = err
			continue
		}
		lic := License{Tier: st.Tier, ID: st.ID, Licensee: st.Licensee, Email: st.Email,
			Source: p.Name(), ExpiresAt: st.ExpiresAt, ValidatedAt: m.now()}
		s := state{License: lic, Provider: p.Name(), ActivationID: st.ActivationID}
		if err := m.persist(key, s); err != nil {
			return License{}, err
		}
		m.set(lic, s)
		return lic, nil
	}
	if configured == 0 {
		return License{}, errors.New("this build cannot activate online license keys; use an offline key or an official release")
	}
	return License{}, fmt.Errorf("license key was not accepted: %w", lastErr)
}

// Remove deletes the key and returns to the Community edition.
func (m *Manager) Remove() error {
	if err := m.settings.DeleteSetting(keySetting); err != nil {
		return err
	}
	_ = m.settings.DeleteSetting(stateSetting)
	m.set(Community(), state{})
	return nil
}

// Refresh re-validates an online key. It is safe to call often; it only
// contacts the provider when the last validation is older than RevalidateEvery.
func (m *Manager) Refresh(ctx context.Context, force bool) License {
	key, _ := m.settings.GetSetting(keySetting)
	if key == "" {
		return m.Current()
	}
	if strings.HasPrefix(key, OfflinePrefix) {
		lic, err := VerifyOffline(key, m.trusted, m.now())
		if err != nil {
			lic.Tier = Free
			lic.Problem = err.Error()
		}
		m.set(lic, state{License: lic})
		return lic
	}
	m.mu.RLock()
	st := m.st
	m.mu.RUnlock()
	if !force && !st.License.ValidatedAt.IsZero() && m.now().Sub(st.License.ValidatedAt) < RevalidateEvery {
		return m.Current()
	}
	var provider Provider
	for _, p := range m.providers {
		if p.Name() == st.Provider && p.Configured() {
			provider = p
		}
	}
	if provider == nil {
		lic := License{Tier: Free, Problem: "the license provider for this key is not available in this build"}
		m.set(lic, st)
		return lic
	}
	status, err := provider.Validate(ctx, key, st.ActivationID)
	switch {
	case err == nil:
		lic := License{Tier: status.Tier, ID: status.ID, Licensee: status.Licensee, Email: status.Email,
			Source: provider.Name(), ExpiresAt: status.ExpiresAt, ValidatedAt: m.now()}
		st.License = lic
		if perr := m.persist(key, st); perr != nil {
			m.log.Warn("could not store license state", "err", perr)
		}
		m.set(lic, st)
		return lic
	case IsInvalid(err):
		lic := st.License
		lic.Tier = Free
		lic.Problem = err.Error()
		st.License = lic
		_ = m.persist(key, st)
		m.set(lic, st)
		return lic
	default:
		m.log.Warn("license validation failed; using grace period", "err", err)
		lic := m.withGrace(st.License)
		m.set(lic, st)
		return lic
	}
}

// Run revalidates periodically until the context ends.
func (m *Manager) Run(ctx context.Context) {
	m.Refresh(ctx, false)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.Refresh(ctx, false)
		}
	}
}

func (m *Manager) persist(key string, s state) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := m.settings.SetSetting(keySetting, key); err != nil {
		return err
	}
	return m.settings.SetSetting(stateSetting, string(raw))
}

func (m *Manager) set(l License, s state) {
	m.mu.Lock()
	m.current = l
	m.st = s
	m.mu.Unlock()
}
