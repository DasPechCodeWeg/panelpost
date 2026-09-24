package license

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type memSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func newMem() *memSettings { return &memSettings{m: map[string]string{}} }

func (s *memSettings) GetSetting(k string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[k], nil
}
func (s *memSettings) SetSetting(k, v string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}
func (s *memSettings) DeleteSetting(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func keypair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pubS, privS, err := GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	pubs, err := ParsePublicKeys(pubS)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := ParsePrivateKey(privS)
	if err != nil {
		t.Fatal(err)
	}
	return pubs[0], priv
}

func TestOfflineKeyRoundTrip(t *testing.T) {
	pub, priv := keypair(t)
	exp := time.Date(2027, 10, 1, 0, 0, 0, 0, time.UTC)
	key, err := Sign(priv, Payload{Tier: Business, Name: "Acme Monitoring BV", Email: "ops@acme.example", ExpiresAt: exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, OfflinePrefix) {
		t.Fatalf("key %q lacks prefix", key)
	}
	lic, err := VerifyOffline(key, []ed25519.PublicKey{pub}, time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if lic.Tier != Business || lic.Licensee != "Acme Monitoring BV" || !lic.ExpiresAt.Equal(exp) {
		t.Errorf("unexpected license %+v", lic)
	}
	if _, err := VerifyOffline(key, []ed25519.PublicKey{pub}, exp.Add(time.Hour)); !errors.Is(err, ErrExpired) {
		t.Errorf("expired key error = %v", err)
	}
}

func TestOfflineKeyTampering(t *testing.T) {
	pub, priv := keypair(t)
	otherPub, _ := keypair(t)
	key, _ := Sign(priv, Payload{Tier: Pro, Name: "Initech"})
	if _, err := VerifyOffline(key, []ed25519.PublicKey{otherPub}, time.Now()); !errors.Is(err, ErrSignature) {
		t.Errorf("foreign key error = %v", err)
	}
	// Flip the tier inside the payload without re-signing.
	body := strings.Split(strings.TrimPrefix(key, OfflinePrefix), ".")
	forged := OfflinePrefix + strings.Replace(body[0], body[0][:4], "eyJ3", 1) + "." + body[1]
	if _, err := VerifyOffline(forged, []ed25519.PublicKey{pub}, time.Now()); err == nil {
		t.Error("forged key verified")
	}
	if _, err := VerifyOffline("PP1-garbage", []ed25519.PublicKey{pub}, time.Now()); !errors.Is(err, ErrMalformed) {
		t.Errorf("garbage error = %v", err)
	}
	if _, err := VerifyOffline(key, nil, time.Now()); !errors.Is(err, ErrNoVerifiers) {
		t.Errorf("no verifier error = %v", err)
	}
	if _, err := Sign(priv, Payload{Tier: Free, Name: "x"}); err == nil {
		t.Error("signing a free key should fail")
	}
}

func TestLimits(t *testing.T) {
	free := LimitsFor(Free)
	if free.MaxReports != 3 || free.Bursting || free.RemoveCredit || free.API {
		t.Errorf("free limits too generous: %+v", free)
	}
	pro := LimitsFor(Pro)
	if pro.MaxReports != 0 || !pro.Bursting || pro.MaxConnections != 3 || pro.WhiteLabel {
		t.Errorf("pro limits: %+v", pro)
	}
	biz := LimitsFor(Business)
	if biz.MaxConnections != 0 || biz.MaxBurstTargets != 0 || !biz.WhiteLabel {
		t.Errorf("business limits: %+v", biz)
	}
}

// fakePolar emulates Polar's customer portal license endpoints.
func fakePolar(t *testing.T, status *string, reachable *bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !*reachable {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["organization_id"] != "org_123" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		if body["key"] != "PANELPOST-GOOD" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"License key not found"}`))
			return
		}
		key := map[string]any{"id": "lk_1", "benefit_id": "ben_pro", "status": *status,
			"customer": map[string]string{"email": "it@globex.example", "name": "Globex"}}
		switch r.URL.Path {
		case "/v1/customer-portal/license-keys/activate":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "act_9", "license_key": key})
		case "/v1/customer-portal/license-keys/validate":
			if body["activation_id"] != "act_9" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(key)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestPolarLifecycleWithGracePeriod(t *testing.T) {
	status, reachable := "granted", true
	srv := fakePolar(t, &status, &reachable)
	defer srv.Close()
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	settings := newMem()
	polar := &Polar{OrganizationID: "org_123", Benefits: map[string]Tier{"ben_pro": Pro}, BaseURL: srv.URL}
	m := NewManager(Options{Settings: settings, Providers: []Provider{polar}, Now: clock})
	ctx := context.Background()

	if _, err := m.Apply(ctx, "PANELPOST-BAD"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("bad key error = %v", err)
	}
	lic, err := m.Apply(ctx, "PANELPOST-GOOD")
	if err != nil {
		t.Fatal(err)
	}
	if lic.Tier != Pro || lic.Licensee != "Globex" || lic.Source != "polar" {
		t.Fatalf("license = %+v", lic)
	}

	// A restart restores the validated state from settings.
	m2 := NewManager(Options{Settings: settings, Providers: []Provider{polar}, Now: clock})
	if m2.Current().Tier != Pro {
		t.Fatalf("restored tier = %s", m2.Current().Tier)
	}

	// Outage: ten days later the server is down; grace keeps Pro.
	reachable = false
	now = now.Add(10 * 24 * time.Hour)
	if got := m2.Refresh(ctx, true); got.Tier != Pro {
		t.Fatalf("tier during grace = %s (%s)", got.Tier, got.Problem)
	}
	// Twenty days without validation exceeds the grace period.
	now = now.Add(10 * 24 * time.Hour)
	if got := m2.Refresh(ctx, true); got.Tier != Free || got.Problem == "" {
		t.Fatalf("tier after grace = %s", got.Tier)
	}
	// Server back, subscription cancelled: definitive downgrade.
	reachable, status = true, "revoked"
	if got := m2.Refresh(ctx, true); got.Tier != Free || !strings.Contains(got.Problem, "revoked") {
		t.Fatalf("revoked = %+v", got)
	}
	// Renewed.
	status = "granted"
	if got := m2.Refresh(ctx, true); got.Tier != Pro {
		t.Fatalf("renewed tier = %s", got.Tier)
	}
	if err := m2.Remove(); err != nil || m2.Current().Tier != Free {
		t.Fatalf("remove: %v %s", err, m2.Current().Tier)
	}
}

func TestLemonSqueezyActivation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("license_key") != "11111111-2222" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"activated":false,"valid":false,"error":"license_key not found."}`))
			return
		}
		_, _ = w.Write([]byte(`{"activated":true,"valid":true,"error":null,
			"license_key":{"id":7,"status":"active","expires_at":null},
			"instance":{"id":"inst_1"},
			"meta":{"store_id":42,"variant_id":900,"customer_name":"Hooli","customer_email":"a@hooli.example"}}`))
	}))
	defer srv.Close()
	ls := &LemonSqueezy{StoreID: 42, Variants: map[string]Tier{"900": Business}, BaseURL: srv.URL}
	m := NewManager(Options{Settings: newMem(), Providers: []Provider{ls}})
	lic, err := m.Apply(context.Background(), "11111111-2222")
	if err != nil {
		t.Fatal(err)
	}
	if lic.Tier != Business || lic.Licensee != "Hooli" {
		t.Errorf("license = %+v", lic)
	}
	if _, err := m.Apply(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v", err)
	}
	wrongStore := &LemonSqueezy{StoreID: 1, Variants: map[string]Tier{"900": Pro}, BaseURL: srv.URL}
	if _, err := NewManager(Options{Settings: newMem(), Providers: []Provider{wrongStore}}).Apply(context.Background(), "11111111-2222"); err == nil {
		t.Error("key from another store accepted")
	}
}

func TestOnlineKeyWithoutProviders(t *testing.T) {
	m := NewManager(Options{Settings: newMem()})
	if _, err := m.Apply(context.Background(), "SOMETHING"); err == nil || !strings.Contains(err.Error(), "cannot activate") {
		t.Errorf("error = %v", err)
	}
}

func TestManagerOfflineKey(t *testing.T) {
	pub, priv := keypair(t)
	key, _ := Sign(priv, Payload{Tier: Pro, Name: "Umbrella"})
	settings := newMem()
	m := NewManager(Options{Settings: settings, PublicKeys: []ed25519.PublicKey{pub}})
	if _, err := m.Apply(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if m.Current().Tier != Pro {
		t.Fatalf("tier = %s", m.Current().Tier)
	}
	// Loaded again at startup.
	if NewManager(Options{Settings: settings, PublicKeys: []ed25519.PublicKey{pub}}).Current().Tier != Pro {
		t.Error("offline key not restored")
	}
	// A build without the vendor key refuses it.
	if got := NewManager(Options{Settings: settings}).Current(); got.Tier != Free || got.Problem == "" {
		t.Errorf("untrusted build license = %+v", got)
	}
	if m.MaskedKey() == key || !strings.HasSuffix(key, m.MaskedKey()[len(m.MaskedKey())-6:]) {
		t.Error("masked key leaks or is wrong")
	}
}
