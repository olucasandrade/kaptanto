package mcp

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// auditMaxBytes is the size rotation threshold (100 MiB).
	auditMaxBytes int64 = 100 * 1024 * 1024
	// auditKeep is how many rotated files to retain (.1 .. .keep).
	auditKeep = 3
)

// Outcome values written to audit lines (MCP-03).
const (
	OutcomeOK     = "ok"
	OutcomeDenied = "denied"
	OutcomeError  = "error"
)

// AuditRecord is one NDJSON audit line. It never carries row data or key material.
type AuditRecord struct {
	TS            time.Time `json:"ts"`
	Key           string    `json:"key"`            // API key name, or "unknown"
	Tool          string    `json:"tool"`
	ParamsSummary []string  `json:"params_summary"` // param names / table names only
	Tables        []string  `json:"tables"`
	Outcome       string    `json:"outcome"` // ok | denied | error
	DurationMS    int64     `json:"duration_ms"`
}

// Auditor writes size-rotated NDJSON audit lines. Write failures are logged
// and swallowed so the call still proceeds (availability over audit in v1).
type Auditor struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
	// sink, when non-nil, replaces the file writer (tests).
	sink io.Writer
	// now is injectable for golden tests.
	now func() time.Time
	log *slog.Logger
}

// NewAuditor opens path for append. Rotation uses path.1 .. path.N.
// A nil logger uses slog.Default().
func NewAuditor(path string, log *slog.Logger) (*Auditor, error) {
	if log == nil {
		log = slog.Default()
	}
	a := &Auditor{
		path: path,
		now:  time.Now,
		log:  log,
	}
	if err := a.open(); err != nil {
		return nil, err
	}
	return a, nil
}

// NewAuditorWriter returns an auditor that writes to w without rotation
// (for golden / unit tests).
func NewAuditorWriter(w io.Writer, log *slog.Logger) *Auditor {
	if log == nil {
		log = slog.Default()
	}
	return &Auditor{sink: w, now: time.Now, log: log}
}

// Close closes the underlying file, if any.
func (a *Auditor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}

// Record writes one audit line. On failure it logs and returns without
// propagating the error (MCP-03 availability policy).
func (a *Auditor) Record(rec AuditRecord) {
	if rec.TS.IsZero() {
		rec.TS = a.now().UTC()
	} else {
		rec.TS = rec.TS.UTC()
	}
	if rec.ParamsSummary == nil {
		rec.ParamsSummary = []string{}
	}
	if rec.Tables == nil {
		rec.Tables = []string{}
	}
	line, err := json.Marshal(rec)
	if err != nil {
		a.log.Error("mcp audit: marshal failed", "err", err)
		return
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()

	if a.sink != nil {
		if _, err := a.sink.Write(line); err != nil {
			a.log.Error("mcp audit: write failed", "err", err)
		}
		return
	}

	if a.file == nil {
		a.log.Error("mcp audit: write failed", "err", "auditor closed")
		return
	}
	if a.size+int64(len(line)) > auditMaxBytes {
		if err := a.rotateLocked(); err != nil {
			a.log.Error("mcp audit: rotate failed", "err", err)
			// Still try to write to the current file.
		}
	}
	n, err := a.file.Write(line)
	a.size += int64(n)
	if err != nil {
		a.log.Error("mcp audit: write failed", "err", err, "path", a.path)
	}
}

func (a *Auditor) open() error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("mcp audit: mkdir: %w", err)
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("mcp audit: open %s: %w", a.path, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("mcp audit: stat %s: %w", a.path, err)
	}
	a.file = f
	a.size = st.Size()
	return nil
}

// rotateLocked renames path → path.1 → … → path.keep and opens a fresh file.
// Caller must hold a.mu.
func (a *Auditor) rotateLocked() error {
	if a.file != nil {
		_ = a.file.Close()
		a.file = nil
	}
	// Drop the oldest, then shift .N-1 → .N.
	oldest := fmt.Sprintf("%s.%d", a.path, auditKeep)
	_ = os.Remove(oldest)
	for i := auditKeep - 1; i >= 1; i-- {
		src := fmt.Sprintf("%s.%d", a.path, i)
		dst := fmt.Sprintf("%s.%d", a.path, i+1)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return fmt.Errorf("mcp audit: rotate %s → %s: %w", src, dst, err)
		}
	}
	if _, err := os.Stat(a.path); err == nil {
		if err := os.Rename(a.path, a.path+".1"); err != nil {
			return fmt.Errorf("mcp audit: rotate active: %w", err)
		}
	}
	return a.open()
}

// SetNow overrides the clock (tests).
func (a *Auditor) SetNow(fn func() time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.now = fn
}

// ForceRotateForTest triggers rotation regardless of size (tests).
func (a *Auditor) ForceRotateForTest() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.rotateLocked()
}

// SetSizeForTest sets the tracked size so the next write triggers rotation.
func (a *Auditor) SetSizeForTest(n int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.size = n
}
