package guard

import (
	"crypto/rand"
	"sync"
	"time"
)

// Clock reports the current time. It is a port so the audit log's timestamps
// can be pinned in tests: a record whose times cannot be asserted is a record
// nobody checks.
type Clock func() time.Time

// Pending is one mutation the Approval Gate withheld, waiting for a decision.
//
// It carries everything a decision needs and everything the audit record will
// say, so deciding needs no second look at the statement: the SQL runs exactly
// as submitted, and the preview shown to the human is the one recorded beside
// the outcome. Re-previewing on confirmation would be a second, different
// answer to a question already asked.
//
// RequestedAt is stored now, before anything measures it. Auto-reject on
// timeout belongs to the Approval Console; the moment it will count from has to
// be the moment the human's confirmation appeared, not the moment a timer was
// first wound.
type Pending struct {
	ID             string         `json:"id"`
	Profile        string         `json:"profile"`
	SQL            string         `json:"sql"`
	Origin         Origin         `json:"origin"`
	Classification Classification `json:"classification"`
	Preview        Preview        `json:"preview"`
	RequestedAt    time.Time      `json:"requested_at"`
}

// Queue holds the mutations waiting for a decision, keyed by an opaque ID.
//
// It is the gate's own store, behind both policies: an Inline Confirm and an
// Approval Console entry are the same Pending, decided by different people at
// different speeds. It is safe for concurrent use — the editor and the MCP
// server submit independently — and an entry can be taken exactly once, which
// is what makes one confirmation execute one mutation.
type Queue struct {
	clock Clock

	mu      sync.Mutex
	entries map[string]Pending
}

// NewQueue returns an empty Queue that stamps its entries from clock. A nil
// clock means time.Now.
func NewQueue(clock Clock) *Queue {
	if clock == nil {
		clock = time.Now
	}
	return &Queue{clock: clock, entries: make(map[string]Pending)}
}

// Add stores a mutation awaiting a decision and returns it with its ID and
// RequestedAt filled in. Whatever the caller set in those two fields is
// replaced: the queue issues identifiers, so no caller can choose one.
func (q *Queue) Add(pending Pending) Pending {
	// The ID travels to the UI and comes back as the thing that decides which
	// statement runs, so it is unguessable rather than a counter: a confirmed
	// ID must be one this queue issued, not one that happened to be next.
	pending.ID = rand.Text()
	pending.RequestedAt = q.clock()

	q.mu.Lock()
	defer q.mu.Unlock()

	q.entries[pending.ID] = pending
	return pending
}

// Take removes the entry under id and returns it, reporting whether there was
// one. It is the only way to read an entry, because reading one is deciding it:
// two callers cannot both take the same entry, so a double confirmation runs
// the mutation once and then finds nothing to run.
func (q *Queue) Take(id string) (Pending, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	pending, ok := q.entries[id]
	delete(q.entries, id)
	return pending, ok
}

// Len reports how many mutations are waiting.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.entries)
}
