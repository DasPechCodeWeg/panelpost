// Package store persists Panelpost's configuration and run history in SQLite.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/DasPechCodeWeg/panelpost/internal/model"
	"github.com/DasPechCodeWeg/panelpost/internal/secret"
)

// ErrNotFound is returned when a record does not exist.
var ErrNotFound = errors.New("not found")

// Store is the SQLite-backed repository.
type Store struct {
	db  *sql.DB
	box *secret.Box
	now func() time.Time
}

// Open opens (and migrates) the database at path.
func Open(path string, box *secret.Box) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has a single writer; keep it simple and safe.
	s := &Store{db: db, box: box, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	`CREATE TABLE connections (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		url TEXT NOT NULL,
		org_id INTEGER NOT NULL DEFAULT 1,
		token TEXT NOT NULL,
		insecure_tls INTEGER NOT NULL DEFAULT 0,
		extra_headers TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL)`,
	`CREATE TABLE reports (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		connection_id TEXT NOT NULL REFERENCES connections(id),
		config TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL)`,
	`CREATE TABLE runs (
		id TEXT PRIMARY KEY,
		report_id TEXT NOT NULL,
		report_name TEXT NOT NULL,
		trigger TEXT NOT NULL,
		status TEXT NOT NULL,
		error TEXT NOT NULL DEFAULT '',
		range_from INTEGER NOT NULL DEFAULT 0,
		range_to INTEGER NOT NULL DEFAULT 0,
		outputs TEXT NOT NULL DEFAULT '[]',
		created_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0,
		finished_at INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX runs_report ON runs(report_id, created_at DESC)`,
	`CREATE INDEX runs_created ON runs(created_at DESC)`,
	`CREATE TABLE api_keys (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		hash TEXT NOT NULL UNIQUE,
		prefix TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		last_used_at INTEGER NOT NULL DEFAULT 0)`,
	`ALTER TABLE runs ADD COLUMN period TEXT NOT NULL DEFAULT ''`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO schema_version(version) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// NewID returns a random identifier.
func NewID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// ------------------------------------------------------------- settings

// GetSetting returns a setting or "" when unset.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting stores a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// DeleteSetting removes a setting.
func (s *Store) DeleteSetting(key string) error {
	_, err := s.db.Exec(`DELETE FROM settings WHERE key = ?`, key)
	return err
}

// GetJSON decodes a JSON setting into v. It reports whether it existed.
func (s *Store) GetJSON(key string, v any) (bool, error) {
	raw, err := s.GetSetting(key)
	if err != nil || raw == "" {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), v)
}

// SetJSON stores v as JSON.
func (s *Store) SetJSON(key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.SetSetting(key, string(raw))
}

// GetSecret decrypts a secret setting.
func (s *Store) GetSecret(key string) (string, error) {
	raw, err := s.GetSetting(key)
	if err != nil {
		return "", err
	}
	return s.box.Open(raw)
}

// SetSecret encrypts and stores a secret setting.
func (s *Store) SetSecret(key, value string) error {
	sealed, err := s.box.Seal(value)
	if err != nil {
		return err
	}
	return s.SetSetting(key, sealed)
}

// ---------------------------------------------------------- connections

// SaveConnection inserts or updates a connection.
func (s *Store) SaveConnection(c *model.Connection) error {
	token, err := s.box.Seal(c.Token)
	if err != nil {
		return err
	}
	headers := ""
	if len(c.ExtraHeaders) > 0 {
		raw, _ := json.Marshal(c.ExtraHeaders)
		if headers, err = s.box.Seal(string(raw)); err != nil {
			return err
		}
	}
	now := s.now()
	if c.ID == "" {
		c.ID = NewID()
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	_, err = s.db.Exec(`INSERT INTO connections(id, name, url, org_id, token, insecure_tls, extra_headers, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, url=excluded.url, org_id=excluded.org_id,
			token=excluded.token, insecure_tls=excluded.insecure_tls, extra_headers=excluded.extra_headers,
			updated_at=excluded.updated_at`,
		c.ID, c.Name, c.URL, c.OrgID, token, boolInt(c.InsecureTLS), headers, ms(c.CreatedAt), ms(c.UpdatedAt))
	return err
}

func (s *Store) scanConnection(row interface{ Scan(...any) error }) (*model.Connection, error) {
	var c model.Connection
	var token, headers string
	var insecure int
	var created, updated int64
	if err := row.Scan(&c.ID, &c.Name, &c.URL, &c.OrgID, &token, &insecure, &headers, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var err error
	if c.Token, err = s.box.Open(token); err != nil {
		return nil, err
	}
	if headers != "" {
		raw, err := s.box.Open(headers)
		if err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(raw), &c.ExtraHeaders)
	}
	c.InsecureTLS = insecure == 1
	c.CreatedAt, c.UpdatedAt = fromMS(created), fromMS(updated)
	return &c, nil
}

const connectionCols = `id, name, url, org_id, token, insecure_tls, extra_headers, created_at, updated_at`

// GetConnection loads one connection.
func (s *Store) GetConnection(id string) (*model.Connection, error) {
	return s.scanConnection(s.db.QueryRow(`SELECT `+connectionCols+` FROM connections WHERE id = ?`, id))
}

// ListConnections returns all connections ordered by name.
func (s *Store) ListConnections() ([]*model.Connection, error) {
	rows, err := s.db.Query(`SELECT ` + connectionCols + ` FROM connections ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Connection
	for rows.Next() {
		c, err := s.scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConnection removes a connection that no report uses.
func (s *Store) DeleteConnection(id string) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM reports WHERE connection_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%d report(s) still use this connection", n)
	}
	_, err := s.db.Exec(`DELETE FROM connections WHERE id = ?`, id)
	return err
}

// -------------------------------------------------------------- reports

// SaveReport inserts or updates a report.
func (s *Store) SaveReport(r *model.Report) error {
	now := s.now()
	if r.ID == "" {
		r.ID = NewID()
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO reports(id, name, enabled, connection_id, config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, enabled=excluded.enabled,
			connection_id=excluded.connection_id, config=excluded.config, updated_at=excluded.updated_at`,
		r.ID, r.Name, boolInt(r.Enabled), r.ConnectionID, string(raw), ms(r.CreatedAt), ms(r.UpdatedAt))
	return err
}

func scanReport(row interface{ Scan(...any) error }) (*model.Report, error) {
	var raw string
	var enabled int
	if err := row.Scan(&raw, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	var r model.Report
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, err
	}
	r.Enabled = enabled == 1
	return &r, nil
}

// GetReport loads one report.
func (s *Store) GetReport(id string) (*model.Report, error) {
	return scanReport(s.db.QueryRow(`SELECT config, enabled FROM reports WHERE id = ?`, id))
}

// ListReports returns all reports ordered by name.
func (s *Store) ListReports() ([]*model.Report, error) {
	rows, err := s.db.Query(`SELECT config, enabled FROM reports ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Report
	for rows.Next() {
		r, err := scanReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountReports returns the number of saved reports.
func (s *Store) CountReports() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM reports`).Scan(&n)
	return n, err
}

// DeleteReport removes a report; its run history is kept for auditing.
func (s *Store) DeleteReport(id string) error {
	res, err := s.db.Exec(`DELETE FROM reports WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ----------------------------------------------------------------- runs

// CreateRun inserts a queued run.
func (s *Store) CreateRun(r *model.Run) error {
	if r.ID == "" {
		r.ID = NewID()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.now()
	}
	if r.Status == "" {
		r.Status = model.RunQueued
	}
	return s.writeRun(r, true)
}

// UpdateRun stores the current state of a run.
func (s *Store) UpdateRun(r *model.Run) error { return s.writeRun(r, false) }

func (s *Store) writeRun(r *model.Run, insert bool) error {
	outputs, err := json.Marshal(r.Outputs)
	if err != nil {
		return err
	}
	if insert {
		_, err = s.db.Exec(`INSERT INTO runs(id, report_id, report_name, trigger, status, error, range_from, range_to, outputs, created_at, started_at, finished_at, period)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.ReportID, r.ReportName, r.Trigger, r.Status, r.Error, ms(r.RangeFrom), ms(r.RangeTo), string(outputs),
			ms(r.CreatedAt), ms(r.StartedAt), ms(r.FinishedAt), r.Period)
		return err
	}
	_, err = s.db.Exec(`UPDATE runs SET status=?, error=?, range_from=?, range_to=?, outputs=?, started_at=?, finished_at=?, period=? WHERE id=?`,
		r.Status, r.Error, ms(r.RangeFrom), ms(r.RangeTo), string(outputs), ms(r.StartedAt), ms(r.FinishedAt), r.Period, r.ID)
	return err
}

const runCols = `id, report_id, report_name, trigger, status, error, range_from, range_to, outputs, created_at, started_at, finished_at, period`

func scanRun(row interface{ Scan(...any) error }) (*model.Run, error) {
	var r model.Run
	var from, to, created, started, finished int64
	var outputs string
	if err := row.Scan(&r.ID, &r.ReportID, &r.ReportName, &r.Trigger, &r.Status, &r.Error, &from, &to, &outputs, &created, &started, &finished, &r.Period); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal([]byte(outputs), &r.Outputs)
	r.RangeFrom, r.RangeTo = fromMS(from), fromMS(to)
	r.CreatedAt, r.StartedAt, r.FinishedAt = fromMS(created), fromMS(started), fromMS(finished)
	return &r, nil
}

// GetRun loads one run.
func (s *Store) GetRun(id string) (*model.Run, error) {
	return scanRun(s.db.QueryRow(`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
}

// ListRuns returns recent runs, optionally for one report.
func (s *Store) ListRuns(reportID string, limit int) ([]*model.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if reportID != "" {
		rows, err = s.db.Query(`SELECT `+runCols+` FROM runs WHERE report_id = ? ORDER BY created_at DESC LIMIT ?`, reportID, limit)
	} else {
		rows, err = s.db.Query(`SELECT `+runCols+` FROM runs ORDER BY created_at DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LastRuns returns the most recent run of every report, keyed by report ID.
func (s *Store) LastRuns() (map[string]*model.Run, error) {
	rows, err := s.db.Query(`SELECT ` + runCols + ` FROM runs r WHERE created_at = (
		SELECT MAX(created_at) FROM runs WHERE report_id = r.report_id AND trigger != 'preview')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*model.Run{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out[r.ReportID] = r
	}
	return out, rows.Err()
}

// RunsOlderThan returns runs created before the cutoff, for retention cleanup.
func (s *Store) RunsOlderThan(cutoff time.Time) ([]*model.Run, error) {
	rows, err := s.db.Query(`SELECT `+runCols+` FROM runs WHERE created_at < ?`, ms(cutoff))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteRun removes a run record.
func (s *Store) DeleteRun(id string) error {
	_, err := s.db.Exec(`DELETE FROM runs WHERE id = ?`, id)
	return err
}

// FailInterrupted marks runs left running by a crash or restart as failed.
func (s *Store) FailInterrupted() (int64, error) {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, error = ?, finished_at = ? WHERE status IN (?, ?)`,
		model.RunFailed, "interrupted by a restart", ms(s.now()), model.RunQueued, model.RunRunning)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// -------------------------------------------------------------- API keys

// CreateAPIKey stores a hashed API key.
func (s *Store) CreateAPIKey(k *model.APIKey) error {
	if k.ID == "" {
		k.ID = NewID()
	}
	k.CreatedAt = s.now()
	_, err := s.db.Exec(`INSERT INTO api_keys(id, name, hash, prefix, created_at) VALUES (?, ?, ?, ?, ?)`,
		k.ID, k.Name, k.Hash, k.Prefix, ms(k.CreatedAt))
	return err
}

// ListAPIKeys returns all API keys without their hashes.
func (s *Store) ListAPIKeys() ([]*model.APIKey, error) {
	rows, err := s.db.Query(`SELECT id, name, prefix, created_at, last_used_at FROM api_keys ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.APIKey
	for rows.Next() {
		var k model.APIKey
		var created, used int64
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &created, &used); err != nil {
			return nil, err
		}
		k.CreatedAt, k.LastUsedAt = fromMS(created), fromMS(used)
		out = append(out, &k)
	}
	return out, rows.Err()
}

// FindAPIKeyByHash looks up a key and records its use.
func (s *Store) FindAPIKeyByHash(ctx context.Context, hash string) (*model.APIKey, error) {
	var k model.APIKey
	var created, used int64
	err := s.db.QueryRowContext(ctx, `SELECT id, name, prefix, created_at, last_used_at FROM api_keys WHERE hash = ?`, hash).
		Scan(&k.ID, &k.Name, &k.Prefix, &created, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	k.CreatedAt, k.LastUsedAt = fromMS(created), fromMS(used)
	_, _ = s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = ? WHERE id = ?`, ms(s.now()), k.ID)
	return &k, nil
}

// DeleteAPIKey revokes a key.
func (s *Store) DeleteAPIKey(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_keys WHERE id = ?`, id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// Ping checks the database.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }
