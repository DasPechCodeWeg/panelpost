package deliver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebhookSignedMultipart(t *testing.T) {
	var gotMeta WebhookPayload
	var gotPDF []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if sig := r.Header.Get(SignatureHeader); sig != "sha256="+Sign("s3cret", body) {
			t.Errorf("signature = %q", sig)
		}
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal([]byte(r.FormValue("metadata")), &gotMeta)
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		gotPDF, _ = io.ReadAll(f)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	meta := WebhookPayload{Event: "report.delivered", Report: "SLA", FileName: "sla.pdf", Pages: 3, From: time.Now()}
	if err := PostWebhook(context.Background(), nil, srv.URL, "s3cret", meta, []byte("%PDF-1.7 test")); err != nil {
		t.Fatal(err)
	}
	if gotMeta.Report != "SLA" || gotMeta.Pages != 3 || string(gotPDF) != "%PDF-1.7 test" {
		t.Errorf("got %+v %q", gotMeta, gotPDF)
	}
}

func TestWebhookDoesNotRetryRejections(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	err := PostWebhook(context.Background(), nil, srv.URL, "", WebhookPayload{FileName: "x.pdf"}, []byte("x"))
	if err == nil || calls != 1 {
		t.Errorf("err = %v, calls = %d", err, calls)
	}
}

func TestPlaceholdersAndNames(t *testing.T) {
	p := Placeholders{Report: "Monthly SLA", Period: "1 – 31 August 2026", Target: "Acme", Pages: 4}
	if got := p.Expand("{{report}} for {{target}} · {{period}} ({{pages}} pages)"); got != "Monthly SLA for Acme · 1 – 31 August 2026 (4 pages)" {
		t.Errorf("expand = %q", got)
	}
	if got := SafeFileName(`../../etc/passwd: "SLA" <Acme>`); strings.ContainsAny(got, `/\:"<>`) || strings.HasPrefix(got, ".") {
		t.Errorf("unsafe name %q", got)
	}
	if SafeFileName("   ") != "report" {
		t.Error("empty name not defaulted")
	}
	dir := t.TempDir()
	path, err := WriteToFolder(dir, "Monthly SLA", "sla-2026-08.pdf", []byte("pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "Monthly SLA") {
		t.Errorf("path = %s", path)
	}
	if b, _ := os.ReadFile(path); string(b) != "pdf" {
		t.Error("content mismatch")
	}
}

func TestTextToHTMLEscapes(t *testing.T) {
	out := textToHTML("Hi <script>\n\nBye & thanks")
	if strings.Contains(out, "<script>") || !strings.Contains(out, "&lt;script&gt;") || !strings.Contains(out, "Bye &amp; thanks") {
		t.Errorf("html = %s", out)
	}
}

func TestMailerRequiresConfiguration(t *testing.T) {
	m := &Mailer{}
	if err := m.Send(context.Background(), Email{To: []string{"a@b.c"}}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Errorf("error = %v", err)
	}
}
