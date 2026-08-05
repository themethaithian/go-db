package guard

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Decision is what a human did with a withheld mutation.
type Decision string

const (
	// Confirmed means the human let the mutation run.
	Confirmed Decision = "confirmed"
	// Cancelled means the human refused it. Nothing was executed.
	Cancelled Decision = "cancelled"
)

// DeciderHuman is the only decider there is today. The field exists because
// there will be others — a timeout auto-reject decides without a human, and its
// records must not look like someone pressed a key.
const DeciderHuman = "human"

// Record is one Approval Gate decision, as written to the audit log.
//
// A record is written when a decision is made, not when a query is submitted: a
// request that nobody has answered yet is not a decision, and it is still in
// the queue where it can be seen. RequestedAt travels from the queue entry so
// the record still says how long the human took.
//
// AdvisoryCount and AffectedRows are the pair the log exists for (ADR 0003).
// The first is what the Impact Preview predicted before approval; the second is
// what the database really did. They disagree when the data moved in between,
// and a log that recorded only one of them could not show that.
//
// The pointer fields are pointers so that absent and zero stay different: a
// cancelled mutation has no affected-row count, which is not the same as
// having affected no rows.
type Record struct {
	RequestedAt time.Time  `json:"requested_at"`
	DecidedAt   time.Time  `json:"decided_at"`
	ExecutedAt  *time.Time `json:"executed_at,omitempty"`

	Origin         Origin         `json:"origin"`
	Profile        string         `json:"profile"`
	SQL            string         `json:"sql"`
	Classification Classification `json:"classification"`

	// AdvisoryCount is the Impact Preview's count; NoPreviewReason says why
	// there was none. Exactly one of the two is set.
	AdvisoryCount   *int64 `json:"advisory_count,omitempty"`
	NoPreviewReason string `json:"no_preview_reason,omitempty"`

	Decision Decision `json:"decision"`
	Decider  string   `json:"decider"`

	// AffectedRows is what the mutation really changed, set only when it ran.
	AffectedRows *int64 `json:"affected_rows,omitempty"`
	// Error is what the database said if the confirmed mutation failed.
	Error string `json:"error,omitempty"`
}

// AuditLog is the port every gate decision is written through. Implementations
// append; nothing in this app ever rewrites or deletes a record.
type AuditLog interface {
	Append(Record) error
}

// AuditFileName is the file the JSONL adapter appends to inside the directory
// it is given.
const AuditFileName = "audit.jsonl"

const (
	auditDirPerm  = 0o700
	auditFilePerm = 0o600
)

// NewJSONLAuditLog returns an AuditLog that appends records to AuditFileName in
// dir, one JSON object per line (ADR 0004). The directory is created on first
// write; the caller supplies it — the app passes its config directory — so this
// package never decides where a user's history lives.
//
// The file is opened for append and closed again on every record rather than
// held open. A desktop client writes a line when a human presses a key, so the
// cost is irrelevant, and nothing is buffered in a process that may be killed
// between a mutation and its record.
func NewJSONLAuditLog(dir string) AuditLog {
	return &jsonlAuditLog{dir: dir}
}

type jsonlAuditLog struct {
	dir string

	// mu serialises appends within the process. Two records interleaved
	// mid-line would corrupt both, and the editor and the MCP server decide
	// independently.
	mu sync.Mutex
}

func (l *jsonlAuditLog) Append(record Record) error {
	line, err := encodeRecord(record)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := os.MkdirAll(l.dir, auditDirPerm); err != nil {
		return fmt.Errorf("guard: creating the audit log directory: %w", err)
	}

	file, err := os.OpenFile(l.path(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, auditFilePerm)
	if err != nil {
		return fmt.Errorf("guard: opening the audit log: %w", err)
	}

	// One write of one complete line: an append shorter than the whole record
	// is the one failure that would leave the file unreadable.
	if _, err := file.Write(line); err != nil {
		file.Close()
		return fmt.Errorf("guard: appending to the audit log: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("guard: closing the audit log: %w", err)
	}
	return nil
}

func (l *jsonlAuditLog) path() string { return filepath.Join(l.dir, AuditFileName) }

// encodeRecord renders one record as a line of JSON. HTML escaping is off: the
// log is read by people, and a WHERE clause with a < in it should say so.
func encodeRecord(record Record) ([]byte, error) {
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, fmt.Errorf("guard: encoding an audit record: %w", err)
	}
	return line.Bytes(), nil
}
