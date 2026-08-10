package guard

import (
	"cmp"
	"context"
	"crypto/rand"
	"slices"
	"sync"
	"time"
)

// Clock reports the current time. It is a port so the audit log's timestamps
// can be pinned in tests: a record whose times cannot be asserted is a record
// nobody checks.
type Clock func() time.Time

// Timer reports a channel that delivers once d has elapsed. It is the port the
// Approval Console's auto-reject is measured with, and it exists so a test can
// expire an approval instead of waiting out its deadline — time.After is the
// only implementation that ships.
type Timer func(d time.Duration) <-chan time.Time

// ApprovalTimeout is how long an AI-originated mutation waits in the Approval
// Console before it auto-rejects. It is long enough for a human to notice a
// notification and read an Impact Preview, and short enough that an agent
// blocked on a tool call is not blocked indefinitely by an empty room.
const ApprovalTimeout = 2 * time.Minute

// Pending is one mutation the Approval Gate withheld, waiting for a decision.
//
// It carries everything a decision needs and everything the audit record will
// say, so deciding needs no second look at the statement: the SQL runs exactly
// as submitted, and the preview shown to the human is the one recorded beside
// the outcome. Re-previewing on confirmation would be a second, different
// answer to a question already asked.
//
// RequestedAt is stored now, before anything measures it. The Approval
// Console's auto-reject counts from it, and the moment it counts from has to be
// the moment the human's confirmation appeared, not the moment a timer was
// first wound.
type Pending struct {
	ID      string `json:"id"`
	Profile string `json:"profile"`
	// Database is the schema the statement was classified against and must run
	// in — the Connection Registry's connection for it. Blank is the Profile's
	// own, which is what everything but the editor's database picker submits.
	// It travels with the statement because a mutation confirmed on some other
	// connection than the one it was previewed on is a different statement:
	// unqualified SQL would resolve somewhere else entirely.
	Database       string         `json:"database,omitempty"`
	SQL            string         `json:"sql"`
	Origin         Origin         `json:"origin"`
	Classification Classification `json:"classification"`
	Preview        Preview        `json:"preview"`
	RequestedAt    time.Time      `json:"requested_at"`
}

// Outcome is what became of a submitted mutation: the decision, who made it,
// and when. A timeout and an abandonment are decisions too — that is what the
// Decider field is for, and why nothing here is optional.
type Outcome struct {
	Decision  Decision  `json:"decision"`
	Decider   string    `json:"decider"`
	DecidedAt time.Time `json:"decided_at"`
}

// Waiting is one Approval Console entry as the console renders it: the withheld
// mutation, plus when it will decide itself if nobody else does.
type Waiting struct {
	Pending
	// Deadline is when this entry auto-rejects.
	Deadline time.Time `json:"deadline"`
	// RemainingMillis is how long is left until then, never negative. The
	// console counts down from it rather than from its own clock, so what the
	// human sees is what the gate is measuring.
	RemainingMillis int64 `json:"remaining_ms"`
}

// Queue holds the mutations waiting for a decision, keyed by an opaque ID.
//
// It is the gate's own store, behind both policies, and it keeps them apart.
// An Inline Confirm is Added and Taken: the editor asked, the editor answers,
// and nothing is blocked in between. An Approval Console entry is Submitted,
// which hands the submitting goroutine a Waiter it blocks on until a human
// Decides, the deadline passes, or its caller gives up — so an AI's mutation
// pauses inside the call that asked for it rather than coming back as a status
// the agent has to poll.
//
// Neither policy can answer the other's entry: Take passes over a submission
// and Decide passes over an Inline Confirm. Taking a submission would leave its
// caller blocked with nobody left to answer it.
//
// It is safe for concurrent use — the editor and the MCP server submit
// independently — and an entry is decided exactly once, which is what makes one
// approval execute one mutation.
type Queue struct {
	clock   Clock
	timeout time.Duration
	timer   Timer

	mu      sync.Mutex
	entries map[string]*entry
}

// entry is one queued mutation. outcome is what tells the two policies apart:
// an Inline Confirm has nobody waiting on it and so has no channel, while a
// submission's channel is the one place its verdict is delivered.
type entry struct {
	pending  Pending
	deadline time.Time
	outcome  chan Outcome // nil for an Inline Confirm
}

// NewQueue returns an empty Queue that stamps its entries from clock and
// auto-rejects submissions timeout after they were requested, measured with
// timer. A nil clock means time.Now, a zero timeout means ApprovalTimeout, and
// a nil timer means time.After.
func NewQueue(clock Clock, timeout time.Duration, timer Timer) *Queue {
	if clock == nil {
		clock = time.Now
	}
	if timeout <= 0 {
		timeout = ApprovalTimeout
	}
	if timer == nil {
		timer = time.After
	}
	return &Queue{clock: clock, timeout: timeout, timer: timer, entries: make(map[string]*entry)}
}

// Timeout reports how long a submission waits before it auto-rejects.
func (q *Queue) Timeout() time.Duration { return q.timeout }

// Add stores a mutation awaiting an Inline Confirm and returns it with its ID
// and RequestedAt filled in. Whatever the caller set in those two fields is
// replaced: the queue issues identifiers, so no caller can choose one.
func (q *Queue) Add(pending Pending) Pending {
	stamped, _ := q.add(pending, nil)
	return stamped
}

// Submit stores an AI-originated mutation in the Approval Console and returns
// it alongside the Waiter the submitting goroutine blocks on. The entry is in
// the queue — and so visible to the console — before Submit returns, so a
// decision that arrives before the caller reaches Await is still delivered.
func (q *Queue) Submit(pending Pending) (Pending, *Waiter) {
	stamped, e := q.add(pending, make(chan Outcome, 1))
	return stamped, &Waiter{queue: q, id: stamped.ID, outcome: e.outcome, deadline: e.deadline}
}

func (q *Queue) add(pending Pending, outcome chan Outcome) (Pending, *entry) {
	// The ID travels to the UI and comes back as the thing that decides which
	// statement runs, so it is unguessable rather than a counter: a decided ID
	// must be one this queue issued, not one that happened to be next.
	pending.ID = rand.Text()
	pending.RequestedAt = q.clock()

	e := &entry{pending: pending, deadline: pending.RequestedAt.Add(q.timeout), outcome: outcome}

	q.mu.Lock()
	defer q.mu.Unlock()

	q.entries[pending.ID] = e
	return pending, e
}

// Take removes the Inline Confirm under id and returns it, reporting whether
// there was one. It is the only way to read an entry, because reading one is
// deciding it: two callers cannot both take the same entry, so a double
// confirmation runs the mutation once and then finds nothing to run.
//
// An Approval Console entry is not takeable. It has a caller blocked on it, and
// removing it here would answer that caller with nothing at all.
func (q *Queue) Take(id string) (Pending, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[id]
	if !ok || e.outcome != nil {
		return Pending{}, false
	}
	delete(q.entries, id)
	return e.pending, true
}

// Decide delivers the Approval Console's verdict on id to whoever is waiting on
// it and removes the entry, reporting whether there was one to decide.
//
// It returns as soon as the waiter has been handed the verdict — it does not
// wait for the mutation to run. The statement executes on the goroutine that
// submitted it, so its rows go back to the agent that asked for them while the
// human who approved it gets an acknowledgement now.
//
// An ID that is not waiting — already decided, already expired, never issued,
// or an Inline Confirm — reports false and changes nothing.
func (q *Queue) Decide(id string, approved bool, decider string) bool {
	decision := Rejected
	if approved {
		decision = Approved
	}
	_, ok := q.resolve(id, decision, decider)
	return ok
}

// Console returns the Approval Console's entries — the submissions somebody is
// blocked on — oldest first, which is the order they should be answered in.
// Inline Confirms are not among them: those are answered in the editor that
// raised them, and showing them here would offer the same mutation twice.
func (q *Queue) Console() []Waiting {
	now := q.clock()

	q.mu.Lock()
	defer q.mu.Unlock()

	waiting := make([]Waiting, 0, len(q.entries))
	for _, e := range q.entries {
		if e.outcome == nil {
			continue
		}
		remaining := max(e.deadline.Sub(now), 0)
		waiting = append(waiting, Waiting{
			Pending:         e.pending,
			Deadline:        e.deadline,
			RemainingMillis: remaining.Milliseconds(),
		})
	}

	// Ties break on the ID so the order is total: two submissions can share an
	// instant, and a console whose rows swapped places between two reads would
	// be a console nobody could click.
	slices.SortFunc(waiting, func(a, b Waiting) int {
		if order := a.RequestedAt.Compare(b.RequestedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return waiting
}

// Len reports how many mutations are waiting, under either policy.
func (q *Queue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()

	return len(q.entries)
}

// resolve settles the submission under id: it removes the entry and delivers
// the outcome to its waiter in one step, under the lock, so a decision that
// races the deadline cannot both remove the entry and lose the verdict. It
// reports false when there is nothing to settle — the entry was already
// decided, or was never a submission.
func (q *Queue) resolve(id string, decision Decision, decider string) (Outcome, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	e, ok := q.entries[id]
	if !ok || e.outcome == nil {
		return Outcome{}, false
	}
	delete(q.entries, id)

	outcome := Outcome{Decision: decision, Decider: decider, DecidedAt: q.clock()}
	// The channel holds one and the entry is delivered once, so this never
	// blocks and never overwrites a verdict.
	e.outcome <- outcome
	return outcome, true
}

// Waiter is the submitting goroutine's side of one Approval Console entry: the
// thing it blocks on while a human looks at what it asked for.
type Waiter struct {
	queue    *Queue
	id       string
	outcome  chan Outcome
	deadline time.Time
}

// Await blocks until the mutation is decided in the Approval Console, its
// deadline passes, or ctx ends, and reports which. It is called once per
// submission, by the goroutine that submitted it.
//
// It always returns an Outcome — there is no error and no zero value. Every way
// out of the wait is a decision somebody or something made, and the caller has
// a statement it has to answer for either way: approved by a human, rejected by
// a human, auto-rejected at the deadline, or withdrawn because the caller
// itself went away. Whichever it is, the entry has left the queue by the time
// this returns, so the mutation is decided exactly once.
func (w *Waiter) Await(ctx context.Context) Outcome {
	// The deadline counts from the moment the mutation was requested, so what
	// is armed here is what is left of it — not a fresh timeout.
	expiry := w.queue.timer(w.deadline.Sub(w.queue.clock()))

	select {
	case outcome := <-w.outcome:
		return outcome
	case <-expiry:
		return w.settle(TimedOut, DeciderTimeout)
	case <-ctx.Done():
		return w.settle(Cancelled, DeciderCaller)
	}
}

// settle ends the wait from the waiter's own side. A human deciding at the
// instant the deadline passes is a real race, and the loser reports the
// decision that was actually delivered rather than the one it was proposing.
func (w *Waiter) settle(decision Decision, decider string) Outcome {
	if outcome, ok := w.queue.resolve(w.id, decision, decider); ok {
		return outcome
	}
	return <-w.outcome
}
