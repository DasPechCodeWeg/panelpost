package deliver

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// WebhookPayload is the JSON metadata sent with a report.
type WebhookPayload struct {
	Event     string    `json:"event"`
	Report    string    `json:"report"`
	ReportID  string    `json:"report_id"`
	RunID     string    `json:"run_id"`
	Dashboard string    `json:"dashboard"`
	Target    string    `json:"target,omitempty"`
	From      time.Time `json:"from"`
	To        time.Time `json:"to"`
	Pages     int       `json:"pages"`
	FileName  string    `json:"file_name"`
}

// SignatureHeader carries "sha256=<hex hmac of the request body>".
const SignatureHeader = "X-Panelpost-Signature"

// PostWebhook uploads the PDF as multipart/form-data with fields "metadata"
// (JSON) and "file" (the PDF). With a secret, the body is HMAC-SHA256 signed.
func PostWebhook(ctx context.Context, client *http.Client, url, secret string, meta WebhookPayload, pdf []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	metaJSON, _ := json.Marshal(meta)
	mh := textproto.MIMEHeader{}
	mh.Set("Content-Disposition", `form-data; name="metadata"`)
	mh.Set("Content-Type", "application/json")
	part, err := w.CreatePart(mh)
	if err != nil {
		return err
	}
	if _, err := part.Write(metaJSON); err != nil {
		return err
	}
	fh := textproto.MIMEHeader{}
	fh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, meta.FileName))
	fh.Set("Content-Type", "application/pdf")
	if part, err = w.CreatePart(fh); err != nil {
		return err
	}
	if _, err := part.Write(pdf); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	payload := body.Bytes()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", w.FormDataContentType())
		req.Header.Set("User-Agent", "Panelpost")
		if secret != "" {
			req.Header.Set(SignatureHeader, "sha256="+Sign(secret, payload))
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		resp.Body.Close()
		if resp.StatusCode < 300 {
			return nil
		}
		lastErr = fmt.Errorf("webhook answered HTTP %d", resp.StatusCode)
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			break // the receiver rejected it; retrying will not help
		}
	}
	return lastErr
}

// Sign returns the hex HMAC-SHA256 of body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

// SafeFileName turns arbitrary text into a portable file name.
func SafeFileName(s string) string {
	s = unsafeName.ReplaceAllString(s, "-")
	s = strings.Trim(strings.Join(strings.Fields(s), " "), " .-")
	if s == "" {
		s = "report"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

// WriteToFolder stores the PDF under dir/subdir/name, creating directories.
func WriteToFolder(dir, subdir, name string, pdf []byte) (string, error) {
	target := filepath.Join(dir, SafeFileName(subdir))
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(target, SafeFileName(name))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pdf, 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
}
