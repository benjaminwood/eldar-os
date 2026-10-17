package server

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// AuditEntry is one line of the append-only audit log (JSONL). Same shape as
// the original proxy's, so existing logs and the activity views keep working.
type AuditEntry struct {
	Timestamp      string `json:"timestamp"`
	RequestID      int    `json:"requestId"`
	Action         string `json:"action"`
	Reason         string `json:"reason,omitempty"`
	Profile        string `json:"profile,omitempty"`
	Decision       string `json:"decision"`
	DecisionReason string `json:"decisionReason"`
	Mode           string `json:"mode"`
	Result         *AuditResult `json:"result,omitempty"`

	// Absent (not a command), null (whole profile injected) or a list.
	Credentials json.RawMessage `json:"credentials,omitempty"`
	AwsProfile  string          `json:"awsProfile,omitempty"`
	Cwd         string          `json:"cwd,omitempty"`
	SessionID   string          `json:"sessionId,omitempty"`
	Target      string          `json:"target,omitempty"`
	DurationMs  *int64          `json:"durationMs,omitempty"`
	TimedOut    bool            `json:"timedOut,omitempty"`
	Truncated   bool            `json:"truncated,omitempty"`
}

type AuditResult struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// Audit appends entries to a JSONL file and reads back the most recent ones.
type Audit struct {
	path string
	mu   sync.Mutex
}

func NewAudit(path string) (*Audit, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return &Audit{path: path}, nil
}

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// Log appends one entry. Failures are reported but never block the request.
func (a *Audit) Log(e AuditEntry) {
	if e.Timestamp == "" {
		e.Timestamp = nowISO()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.OpenFile(a.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		logf("audit: %v", err)
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

// Tail returns the most recent limit entries, oldest first. Reads backwards in
// blocks: the log is append-only and unrotated, so it can be large. Malformed
// lines (e.g. an interrupted write) are skipped.
func (a *Audit) Tail(limit int) []AuditEntry {
	f, err := os.Open(a.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const block = 64 * 1024
	pos := st.Size()
	var carry []byte
	var lines [][]byte
	for pos > 0 && len(lines) <= limit {
		size := min(int64(block), pos)
		pos -= size
		buf := make([]byte, size)
		if _, err := f.ReadAt(buf, pos); err != nil && err != io.EOF {
			break
		}
		chunk := append(buf, carry...)
		parts := bytes.Split(chunk, []byte("\n"))
		if pos > 0 {
			carry, parts = parts[0], parts[1:]
		} else {
			carry = nil
		}
		for i := len(parts) - 1; i >= 0 && len(lines) <= limit; i-- {
			if l := bytes.TrimSpace(parts[i]); len(l) > 0 {
				lines = append(lines, l)
			}
		}
	}
	if l := bytes.TrimSpace(carry); len(l) > 0 {
		lines = append(lines, l)
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	out := make([]AuditEntry, 0, len(lines))
	for _, l := range lines {
		var e AuditEntry
		if json.Unmarshal(l, &e) == nil {
			out = append(out, e)
		}
	}
	slices.Reverse(out)
	return out
}
