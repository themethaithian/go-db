package guard_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/guard"
)

// at is a fixed instant the fake clock counts from, so every assertion about
// time in these tests is exact rather than approximate.
var at = time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)

// armDeadline bounds a wait for something another goroutine is about to do. It
// is a failure guard, not a delay: every test here reaches it only when the
// thing it is waiting for never happens.
const armDeadline = 5 * time.Second

// fakeClock hands out instants a test chooses. Each read advances by a second,
// so the order of the timestamps in a record is visible and checkable.
type fakeClock struct {
	mu   sync.Mutex
	next time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{next: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.next
	c.next = c.next.Add(time.Second)
	return now
}

// fakeTimer stands in for time.After, so an approval expiring is something a
// test does rather than something it waits out. Every armed timer is recorded
// with the duration it was armed for and fires only when the test fires it,
// which is what makes the auto-reject path assertable without a sleep.
type fakeTimer struct {
	armed chan int // the index of each timer, as it is armed

	mu        sync.Mutex
	chans     []chan time.Time
	durations []time.Duration
}

func newFakeTimer() *fakeTimer { return &fakeTimer{armed: make(chan int, 64)} }

// After implements guard.Timer.
func (f *fakeTimer) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)

	f.mu.Lock()
	f.chans = append(f.chans, ch)
	f.durations = append(f.durations, d)
	index := len(f.chans) - 1
	f.mu.Unlock()

	f.armed <- index
	return ch
}

// next blocks until another timer is armed and returns its index. Arming the
// timer is the last thing a waiter does before it blocks, so this is also how a
// test knows a waiter is waiting.
func (f *fakeTimer) next(t *testing.T) int {
	t.Helper()

	select {
	case index := <-f.armed:
		return index
	case <-time.After(armDeadline):
		t.Fatal("no approval timer was armed; nothing is waiting for a decision")
		return 0
	}
}

func (f *fakeTimer) duration(index int) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.durations[index]
}

// expire fires the timer under index, as the wall clock would at its deadline.
func (f *fakeTimer) expire(index int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.chans[index] <- time.Time{}
}

// fire waits for the next timer to be armed and expires it, reporting how long
// it was armed for.
func (f *fakeTimer) fire(t *testing.T) time.Duration {
	t.Helper()

	index := f.next(t)
	f.expire(index)
	return f.duration(index)
}

// newQueue returns a queue on a fake clock with no timer of its own, for the
// tests that never let an approval expire.
func newQueue(t *testing.T) *guard.Queue {
	t.Helper()
	return guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, newFakeTimer().After)
}

func samplePending() guard.Pending {
	return guard.Pending{
		Profile:        "local",
		SQL:            "UPDATE users SET name = 'x' WHERE id = 1",
		Origin:         guard.OriginHuman,
		Classification: guard.Classify("UPDATE users SET name = 'x' WHERE id = 1"),
		Preview:        guard.Preview{Available: true, Count: 3},
	}
}

func aiPending(sql string) guard.Pending {
	return guard.Pending{
		Profile:        "agent",
		SQL:            sql,
		Origin:         guard.OriginAI,
		Classification: guard.Classify(sql),
		Preview:        guard.Preview{Available: true, Count: 7},
	}
}

// TestQueueAddStampsTheEntry covers what the queue adds to what it is given:
// an opaque identifier and the moment the mutation was requested. The timestamp
// is what the auto-reject deadline is measured from, so it is stored now rather
// than derived later.
func TestQueueAddStampsTheEntry(t *testing.T) {
	clock := newFakeClock(at)
	queue := guard.NewQueue(clock.Now, guard.ApprovalTimeout, newFakeTimer().After)

	got := queue.Add(samplePending())

	if got.ID == "" {
		t.Fatal("the queue handed back an entry with no ID")
	}
	if !got.RequestedAt.Equal(at) {
		t.Errorf("RequestedAt = %s, want the clock's %s", got.RequestedAt, at)
	}
	if got.SQL != samplePending().SQL || got.Profile != "local" || got.Origin != guard.OriginHuman {
		t.Errorf("the queue altered the entry it was given: %+v", got)
	}
	if got.Preview.Count != 3 || !got.Preview.Available {
		t.Errorf("Preview = %+v, want it stored as given", got.Preview)
	}
	if queue.Len() != 1 {
		t.Errorf("Len = %d, want 1", queue.Len())
	}
}

// TestQueueIDsAreOpaqueAndUnique matters because an ID travels to the UI and
// comes back as the thing that decides which statement runs. A guessable or
// repeated ID would let one confirmation execute a different mutation.
func TestQueueIDsAreOpaqueAndUnique(t *testing.T) {
	queue := newQueue(t)

	seen := make(map[string]bool)
	for range 1000 {
		id := queue.Add(samplePending()).ID
		if seen[id] {
			t.Fatalf("the queue issued %q twice", id)
		}
		seen[id] = true
		if len(id) < 16 {
			t.Fatalf("ID %q is short enough to guess", id)
		}
		if strings.ContainsAny(id, " \t\n\"") {
			t.Fatalf("ID %q is not safe to carry as an opaque token", id)
		}
	}
}

// TestQueueTakeRemoves is the whole of the Inline Confirm decision protocol: an
// entry can be taken exactly once, so one keypress executes one mutation and a
// second keypress on the same confirmation executes nothing.
func TestQueueTakeRemoves(t *testing.T) {
	queue := newQueue(t)
	added := queue.Add(samplePending())

	got, ok := queue.Take(added.ID)
	if !ok {
		t.Fatal("Take on a freshly added entry reported it missing")
	}
	if got.ID != added.ID || got.SQL != added.SQL {
		t.Errorf("Take returned %+v, want the entry that was added", got)
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d after taking the only entry, want 0", queue.Len())
	}

	if _, ok := queue.Take(added.ID); ok {
		t.Error("the same entry was taken twice; a decision must be final")
	}
}

func TestQueueTakeUnknownID(t *testing.T) {
	queue := newQueue(t)

	if _, ok := queue.Take("no-such-id"); ok {
		t.Error("Take reported an entry for an ID that was never issued")
	}
}

// TestQueueHoldsSeveralEntries: the editor and the MCP server both feed the
// gate, so entries coexist and a decision touches only its own.
func TestQueueHoldsSeveralEntries(t *testing.T) {
	queue := newQueue(t)

	first := queue.Add(guard.Pending{Profile: "editor", SQL: "DELETE FROM a", Origin: guard.OriginHuman})
	second := queue.Add(guard.Pending{Profile: "editor", SQL: "DELETE FROM b", Origin: guard.OriginHuman})

	if queue.Len() != 2 {
		t.Fatalf("Len = %d, want 2", queue.Len())
	}
	got, ok := queue.Take(second.ID)
	if !ok || got.SQL != "DELETE FROM b" {
		t.Fatalf("Take(second) = %+v, %v", got, ok)
	}
	if _, ok := queue.Take(first.ID); !ok {
		t.Error("deciding one entry removed another")
	}
}

// TestQueueIsSafeUnderConcurrency: the queue is shared by the editor and the
// MCP server, and exactly one caller may win a race to take an entry — two
// winners would run the mutation twice.
func TestQueueIsSafeUnderConcurrency(t *testing.T) {
	queue := newQueue(t)

	const entries = 50
	ids := make([]string, entries)
	var adding sync.WaitGroup
	for i := range entries {
		adding.Add(1)
		go func() {
			defer adding.Done()
			ids[i] = queue.Add(samplePending()).ID
		}()
	}
	adding.Wait()

	const takers = 4
	taken := make(chan string, entries*takers)
	var taking sync.WaitGroup
	for range takers {
		taking.Add(1)
		go func() {
			defer taking.Done()
			for _, id := range ids {
				if p, ok := queue.Take(id); ok {
					taken <- p.ID
				}
			}
		}()
	}
	taking.Wait()
	close(taken)

	won := make(map[string]int)
	for id := range taken {
		won[id]++
	}
	if len(won) != entries {
		t.Errorf("%d of %d entries were taken", len(won), entries)
	}
	for id, count := range won {
		if count != 1 {
			t.Errorf("entry %s was taken %d times, want exactly once", id, count)
		}
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d after every entry was taken, want 0", queue.Len())
	}
}

// awaiting starts a goroutine blocked in Await and hands back the channel its
// outcome will arrive on, so a test can assert both that it has not returned
// and what it returns when it does.
func awaiting(waiter *guard.Waiter, ctx context.Context) <-chan guard.Outcome {
	outcomes := make(chan guard.Outcome, 1)
	go func() { outcomes <- waiter.Await(ctx) }()
	return outcomes
}

// stillWaiting asserts that nothing has come back yet. It is called only once a
// timer has been armed — the last thing Await does before it blocks — so this
// is a statement about a goroutine that is definitely inside its select.
func stillWaiting(t *testing.T, outcomes <-chan guard.Outcome) {
	t.Helper()

	select {
	case got := <-outcomes:
		t.Fatalf("Await returned %+v before anybody decided anything", got)
	default:
	}
}

func received(t *testing.T, outcomes <-chan guard.Outcome) guard.Outcome {
	t.Helper()

	select {
	case got := <-outcomes:
		return got
	case <-time.After(armDeadline):
		t.Fatal("Await never returned; the submitting goroutine is stuck")
		return guard.Outcome{}
	}
}

// TestSubmitBlocksUntilTheConsoleApproves is the Approval Console's whole
// shape: the goroutine that submitted an AI-originated mutation waits, in the
// call, until a human answers — and it is the answer, not a status code, that
// comes back.
func TestSubmitBlocksUntilTheConsoleApproves(t *testing.T) {
	clock := newFakeClock(at)
	timer := newFakeTimer()
	queue := guard.NewQueue(clock.Now, guard.ApprovalTimeout, timer.After)

	pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
	outcomes := awaiting(waiter, context.Background())
	timer.next(t)

	stillWaiting(t, outcomes)
	if queue.Len() != 1 {
		t.Errorf("Len = %d while a submission waits, want it still queued", queue.Len())
	}

	if !queue.Decide(pending.ID, true, guard.DeciderHuman) {
		t.Fatal("Decide reported nothing waiting under the ID it was just given")
	}

	got := received(t, outcomes)
	if got.Decision != guard.Approved {
		t.Errorf("Decision = %q, want %q", got.Decision, guard.Approved)
	}
	if got.Decider != guard.DeciderHuman {
		t.Errorf("Decider = %q, want %q", got.Decider, guard.DeciderHuman)
	}
	if got.DecidedAt.Before(pending.RequestedAt) {
		t.Errorf("DecidedAt = %s, want it at or after RequestedAt %s", got.DecidedAt, pending.RequestedAt)
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d after the decision, want the entry gone", queue.Len())
	}
}

// TestSubmitBlocksUntilTheConsoleRejects: the refusal reaches the waiter as
// plainly as the approval does. Nothing about it is an error.
func TestSubmitBlocksUntilTheConsoleRejects(t *testing.T) {
	timer := newFakeTimer()
	queue := guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, timer.After)

	pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
	outcomes := awaiting(waiter, context.Background())
	timer.next(t)
	stillWaiting(t, outcomes)

	if !queue.Decide(pending.ID, false, guard.DeciderHuman) {
		t.Fatal("Decide reported nothing waiting")
	}

	got := received(t, outcomes)
	if got.Decision != guard.Rejected {
		t.Errorf("Decision = %q, want %q", got.Decision, guard.Rejected)
	}
	if got.Decider != guard.DeciderHuman {
		t.Errorf("Decider = %q, want %q", got.Decider, guard.DeciderHuman)
	}
}

// TestSubmitAutoRejectsAtTheDeadline is the timeout, measured from RequestedAt
// with the injected clock and fired by the injected timer — so the auto-reject
// is a thing this test performs rather than a thing it waits two minutes for.
func TestSubmitAutoRejectsAtTheDeadline(t *testing.T) {
	timer := newFakeTimer()
	const timeout = 90 * time.Second
	queue := guard.NewQueue(newFakeClock(at).Now, timeout, timer.After)

	pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
	outcomes := awaiting(waiter, context.Background())

	armed := timer.fire(t)
	// The wait is armed for what is left of the deadline, counted from the
	// moment the mutation was requested: the clock has ticked once since.
	if armed > timeout || armed < timeout-5*time.Second {
		t.Errorf("the timer was armed for %s, want about the %s left of the deadline", armed, timeout)
	}

	got := received(t, outcomes)
	if got.Decision != guard.TimedOut {
		t.Errorf("Decision = %q, want %q", got.Decision, guard.TimedOut)
	}
	if got.Decider != guard.DeciderTimeout {
		t.Errorf("Decider = %q, want %q: nobody pressed a key", got.Decider, guard.DeciderTimeout)
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d after the deadline passed, want the entry gone", queue.Len())
	}
	if queue.Decide(pending.ID, true, guard.DeciderHuman) {
		t.Error("an expired entry could still be approved")
	}
}

// TestAwaitReturnsWhenTheCallerGivesUp: the MCP client was killed, so nobody is
// left to receive an approval. The entry leaves the queue rather than sitting in
// the console offering the human a decision that can no longer reach anyone.
func TestAwaitReturnsWhenTheCallerGivesUp(t *testing.T) {
	timer := newFakeTimer()
	queue := guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, timer.After)

	ctx, cancel := context.WithCancel(context.Background())
	pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
	outcomes := awaiting(waiter, ctx)
	timer.next(t)
	stillWaiting(t, outcomes)

	cancel()

	got := received(t, outcomes)
	if got.Decision != guard.Cancelled {
		t.Errorf("Decision = %q, want %q", got.Decision, guard.Cancelled)
	}
	if got.Decider != guard.DeciderCaller {
		t.Errorf("Decider = %q, want %q", got.Decider, guard.DeciderCaller)
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d after the caller gave up, want the entry gone", queue.Len())
	}
	if len(queue.Console()) != 0 {
		t.Error("an abandoned mutation is still offered in the Approval Console")
	}
	if queue.Decide(pending.ID, true, guard.DeciderHuman) {
		t.Error("an abandoned mutation could still be approved")
	}
}

// TestDecideTwiceFindsNothing: a decision is final, and the second press of the
// button says so cleanly rather than deciding something else.
func TestDecideTwiceFindsNothing(t *testing.T) {
	timer := newFakeTimer()
	queue := guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, timer.After)

	pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
	outcomes := awaiting(waiter, context.Background())
	timer.next(t)

	if !queue.Decide(pending.ID, true, guard.DeciderHuman) {
		t.Fatal("the first decision found nothing")
	}
	received(t, outcomes)

	if queue.Decide(pending.ID, false, guard.DeciderHuman) {
		t.Error("the same submission was decided twice")
	}
}

func TestDecideUnknownID(t *testing.T) {
	queue := newQueue(t)

	if queue.Decide("never-issued", true, guard.DeciderHuman) {
		t.Error("Decide reported an entry for an ID that was never issued")
	}
}

// TestConsoleShowsSubmissionsOldestFirst is what the Approval Console renders:
// everything an AI is waiting on, in the order it was asked, with the preview
// the human decides on and how long is left before it decides itself.
func TestConsoleShowsSubmissionsOldestFirst(t *testing.T) {
	timer := newFakeTimer()
	const timeout = 2 * time.Minute
	queue := guard.NewQueue(newFakeClock(at).Now, timeout, timer.After)

	first, firstWaiter := queue.Submit(aiPending("DELETE FROM users"))
	second, secondWaiter := queue.Submit(aiPending("UPDATE orders SET paid = 1"))
	awaiting(firstWaiter, context.Background())
	awaiting(secondWaiter, context.Background())

	got := queue.Console()
	if len(got) != 2 {
		t.Fatalf("the console holds %d entries, want 2: %+v", len(got), got)
	}
	if got[0].ID != first.ID || got[1].ID != second.ID {
		t.Errorf("console order = [%s %s], want the oldest request first", got[0].SQL, got[1].SQL)
	}
	if got[0].SQL != "DELETE FROM users" || got[0].Profile != "agent" || got[0].Origin != guard.OriginAI {
		t.Errorf("console entry = %+v, want the submission as it was made", got[0])
	}
	if !got[0].Preview.Available || got[0].Preview.Count != 7 {
		t.Errorf("console preview = %+v, want the Impact Preview the human decides on", got[0].Preview)
	}
	if want := first.RequestedAt.Add(timeout); !got[0].Deadline.Equal(want) {
		t.Errorf("deadline = %s, want %s: the timeout counts from the request", got[0].Deadline, want)
	}
	if got[0].RemainingMillis <= 0 || got[0].RemainingMillis > timeout.Milliseconds() {
		t.Errorf("remaining = %dms, want what is left of %s", got[0].RemainingMillis, timeout)
	}

	if !queue.Decide(first.ID, true, guard.DeciderHuman) {
		t.Fatal("Decide found nothing")
	}
	if after := queue.Console(); len(after) != 1 || after[0].ID != second.ID {
		t.Errorf("the console holds %+v after one decision, want only the undecided one", after)
	}
}

// TestConsoleIgnoresInlineConfirms is the Origin split, stated where it is
// enforced. An Inline Confirm belongs to the editor that raised it and is
// answered there; putting it in the Approval Console would offer the human the
// same mutation twice.
func TestConsoleIgnoresInlineConfirms(t *testing.T) {
	timer := newFakeTimer()
	queue := guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, timer.After)

	inline := queue.Add(samplePending())
	submitted, waiter := queue.Submit(aiPending("DELETE FROM users"))
	awaiting(waiter, context.Background())

	got := queue.Console()
	if len(got) != 1 || got[0].ID != submitted.ID {
		t.Fatalf("the console holds %+v, want only the submission", got)
	}

	// Neither policy can answer the other's entry: an Inline Confirm cannot be
	// approved from the console, and a console entry cannot be taken by the
	// editor — which would leave its caller blocked with nobody to answer it.
	if queue.Decide(inline.ID, true, guard.DeciderHuman) {
		t.Error("an Inline Confirm was decided from the Approval Console")
	}
	if _, ok := queue.Take(submitted.ID); ok {
		t.Error("an Approval Console entry was taken as an Inline Confirm")
	}
	if queue.Len() != 2 {
		t.Errorf("Len = %d, want both entries untouched", queue.Len())
	}
}

// TestSubmissionsAreDecidedIndependently: several agents can be waiting at
// once, and each answer must reach exactly the caller it belongs to.
func TestSubmissionsAreDecidedIndependently(t *testing.T) {
	timer := newFakeTimer()
	queue := guard.NewQueue(newFakeClock(at).Now, guard.ApprovalTimeout, timer.After)

	const submissions = 20
	ids := make([]string, submissions)
	outcomes := make([]<-chan guard.Outcome, submissions)
	for i := range submissions {
		pending, waiter := queue.Submit(aiPending("DELETE FROM users"))
		ids[i] = pending.ID
		outcomes[i] = awaiting(waiter, context.Background())
	}
	for range submissions {
		timer.next(t)
	}

	// Every other one is approved, in no particular order, from four deciders
	// at once — the console, a second window, whatever. Each waiter must get
	// its own verdict.
	var deciding sync.WaitGroup
	for i := range submissions {
		deciding.Add(1)
		go func() {
			defer deciding.Done()
			if !queue.Decide(ids[i], i%2 == 0, guard.DeciderHuman) {
				t.Errorf("Decide(%d) found nothing waiting", i)
			}
		}()
	}
	deciding.Wait()

	for i := range submissions {
		want := guard.Rejected
		if i%2 == 0 {
			want = guard.Approved
		}
		if got := received(t, outcomes[i]); got.Decision != want {
			t.Errorf("submission %d received %q, want %q", i, got.Decision, want)
		}
	}
	if queue.Len() != 0 {
		t.Errorf("Len = %d, want every submission decided", queue.Len())
	}
}
