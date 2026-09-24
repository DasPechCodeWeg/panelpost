package license

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

//go:embed providers.json
var providersJSON []byte

// ProviderConfig holds the public identifiers of the vendor's products.
type ProviderConfig struct {
	Polar struct {
		OrganizationID string          `json:"organization_id"`
		Benefits       map[string]Tier `json:"benefits"`
	} `json:"polar"`
	LemonSqueezy struct {
		StoreID  int             `json:"store_id"`
		Variants map[string]Tier `json:"variants"`
	} `json:"lemonsqueezy"`
}

// EmbeddedProviderConfig returns the configuration compiled into this build.
func EmbeddedProviderConfig() ProviderConfig {
	var cfg ProviderConfig
	_ = json.Unmarshal(providersJSON, &cfg)
	return cfg
}

// Status is the answer of an online license provider.
type Status struct {
	Tier         Tier
	Licensee     string
	Email        string
	ID           string
	ActivationID string
	ExpiresAt    time.Time
}

// ErrInvalid means the provider positively rejected the key. Transport
// problems are returned as other errors so that the grace period applies.
type ErrInvalid struct{ Reason string }

func (e ErrInvalid) Error() string { return e.Reason }

// Provider activates and validates keys sold through a merchant of record.
type Provider interface {
	Name() string
	Configured() bool
	Activate(ctx context.Context, key, label string) (Status, error)
	Validate(ctx context.Context, key, activationID string) (Status, error)
}

// ---------------------------------------------------------------- Polar

// Polar validates keys issued by the Polar "License key" benefit.
type Polar struct {
	OrganizationID string
	Benefits       map[string]Tier
	BaseURL        string
	HTTP           *http.Client
}

func (p *Polar) Name() string { return "polar" }

func (p *Polar) Configured() bool { return p != nil && p.OrganizationID != "" }

type polarKey struct {
	ID        string     `json:"id"`
	BenefitID string     `json:"benefit_id"`
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at"`
	Customer  *struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"customer"`
}

func (p *Polar) base() string {
	if p.BaseURL != "" {
		return strings.TrimRight(p.BaseURL, "/")
	}
	return "https://api.polar.sh"
}

func (p *Polar) Activate(ctx context.Context, key, label string) (Status, error) {
	var out struct {
		ID         string   `json:"id"`
		LicenseKey polarKey `json:"license_key"`
	}
	err := p.post(ctx, "/v1/customer-portal/license-keys/activate", map[string]any{
		"key": key, "organization_id": p.OrganizationID, "label": label,
	}, &out)
	if err != nil {
		// Benefits configured without an activation limit cannot be activated;
		// such keys are validated directly instead. A reached limit still fails.
		msg := strings.ToLower(err.Error())
		if IsInvalid(err) && strings.Contains(msg, "activation") && !strings.Contains(msg, "limit") {
			return p.Validate(ctx, key, "")
		}
		return Status{}, err
	}
	st, err := p.status(out.LicenseKey)
	st.ActivationID = out.ID
	return st, err
}

func (p *Polar) Validate(ctx context.Context, key, activationID string) (Status, error) {
	body := map[string]any{"key": key, "organization_id": p.OrganizationID}
	if activationID != "" {
		body["activation_id"] = activationID
	}
	var out polarKey
	if err := p.post(ctx, "/v1/customer-portal/license-keys/validate", body, &out); err != nil {
		return Status{}, err
	}
	st, err := p.status(out)
	st.ActivationID = activationID
	return st, err
}

func (p *Polar) status(k polarKey) (Status, error) {
	if k.Status != "granted" {
		return Status{}, ErrInvalid{Reason: fmt.Sprintf("license key is %s", nonEmpty(k.Status, "not active"))}
	}
	tier, ok := p.Benefits[k.BenefitID]
	if !ok {
		return Status{}, ErrInvalid{Reason: "license key belongs to a different product"}
	}
	st := Status{Tier: tier, ID: k.ID}
	if k.Customer != nil {
		st.Licensee, st.Email = k.Customer.Name, k.Customer.Email
	}
	if k.ExpiresAt != nil {
		st.ExpiresAt = k.ExpiresAt.UTC()
		if time.Now().After(st.ExpiresAt) {
			return st, ErrInvalid{Reason: "license key has expired"}
		}
	}
	return st, nil
}

func (p *Polar) post(ctx context.Context, path string, body any, out any) error {
	buf, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base()+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doJSON(httpClient(p.HTTP), req, out)
}

// --------------------------------------------------------- Lemon Squeezy

// LemonSqueezy validates keys issued by Lemon Squeezy's license feature.
type LemonSqueezy struct {
	StoreID  int
	Variants map[string]Tier
	BaseURL  string
	HTTP     *http.Client
}

func (l *LemonSqueezy) Name() string { return "lemonsqueezy" }

func (l *LemonSqueezy) Configured() bool { return l != nil && l.StoreID != 0 }

type lsResponse struct {
	Activated  bool    `json:"activated"`
	Valid      bool    `json:"valid"`
	Error      *string `json:"error"`
	LicenseKey struct {
		ID        int     `json:"id"`
		Status    string  `json:"status"`
		ExpiresAt *string `json:"expires_at"`
	} `json:"license_key"`
	Instance *struct {
		ID string `json:"id"`
	} `json:"instance"`
	Meta struct {
		StoreID       int    `json:"store_id"`
		VariantID     int    `json:"variant_id"`
		CustomerName  string `json:"customer_name"`
		CustomerEmail string `json:"customer_email"`
	} `json:"meta"`
}

func (l *LemonSqueezy) base() string {
	if l.BaseURL != "" {
		return strings.TrimRight(l.BaseURL, "/")
	}
	return "https://api.lemonsqueezy.com"
}

func (l *LemonSqueezy) Activate(ctx context.Context, key, label string) (Status, error) {
	var out lsResponse
	if err := l.post(ctx, "/v1/licenses/activate", url.Values{"license_key": {key}, "instance_name": {label}}, &out); err != nil {
		return Status{}, err
	}
	if !out.Activated {
		return Status{}, ErrInvalid{Reason: nonEmpty(deref(out.Error), "license key could not be activated")}
	}
	st, err := l.status(out)
	if out.Instance != nil {
		st.ActivationID = out.Instance.ID
	}
	return st, err
}

func (l *LemonSqueezy) Validate(ctx context.Context, key, activationID string) (Status, error) {
	form := url.Values{"license_key": {key}}
	if activationID != "" {
		form.Set("instance_id", activationID)
	}
	var out lsResponse
	if err := l.post(ctx, "/v1/licenses/validate", form, &out); err != nil {
		return Status{}, err
	}
	if !out.Valid {
		return Status{}, ErrInvalid{Reason: nonEmpty(deref(out.Error), "license key is not valid")}
	}
	st, err := l.status(out)
	st.ActivationID = activationID
	return st, err
}

func (l *LemonSqueezy) status(r lsResponse) (Status, error) {
	if r.Meta.StoreID != l.StoreID {
		return Status{}, ErrInvalid{Reason: "license key belongs to a different store"}
	}
	tier, ok := l.Variants[strconv.Itoa(r.Meta.VariantID)]
	if !ok {
		return Status{}, ErrInvalid{Reason: "license key belongs to a different product"}
	}
	switch r.LicenseKey.Status {
	case "active", "inactive":
	default:
		return Status{}, ErrInvalid{Reason: "license key is " + nonEmpty(r.LicenseKey.Status, "not active")}
	}
	st := Status{Tier: tier, ID: strconv.Itoa(r.LicenseKey.ID), Licensee: r.Meta.CustomerName, Email: r.Meta.CustomerEmail}
	if r.LicenseKey.ExpiresAt != nil && *r.LicenseKey.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, *r.LicenseKey.ExpiresAt); err == nil {
			st.ExpiresAt = t.UTC()
		}
	}
	return st, nil
}

func (l *LemonSqueezy) post(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.base()+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return doJSON(httpClient(l.HTTP), req, out)
}

// -------------------------------------------------------------- helpers

func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 20 * time.Second}
}

// doJSON performs the request. 4xx answers are definitive rejections;
// everything else that fails is treated as a transient problem.
func doJSON(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach license server: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("could not read license server response: %w", err)
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		reason := extractError(body)
		// Lemon Squeezy answers invalid keys with 4xx and a JSON body that
		// still carries the structured fields; surface its message.
		return ErrInvalid{Reason: nonEmpty(reason, fmt.Sprintf("license key rejected (HTTP %d)", resp.StatusCode))}
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("license server returned HTTP %d", resp.StatusCode)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("unexpected license server response: %w", err)
	}
	return nil
}

func extractError(body []byte) string {
	var v struct {
		Error  any    `json:"error"`
		Detail any    `json:"detail"`
		Msg    string `json:"message"`
	}
	if json.Unmarshal(body, &v) != nil {
		return ""
	}
	for _, x := range []any{v.Error, v.Detail} {
		switch t := x.(type) {
		case string:
			if t != "" {
				return t
			}
		case []any:
			if len(t) > 0 {
				if m, ok := t[0].(map[string]any); ok {
					if s, ok := m["msg"].(string); ok {
						return s
					}
				}
			}
		}
	}
	return v.Msg
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// IsInvalid reports whether err is a definitive rejection.
func IsInvalid(err error) bool {
	var inv ErrInvalid
	return errors.As(err, &inv)
}
