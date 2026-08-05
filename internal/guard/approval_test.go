package guard_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/guard"
)

// at is a fixed instant the fake clock counts from, so every assertion about
// time in these tests is exact rather than approximate.
var at = time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)

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

func samplePending() guard.Pending {
	return guard.Pending{
		Profile:        "local",
		SQL:            "UPDATE users SET name = 'x' WHERE id = 1",
		Origin:         guard.OriginHuman,
		Classification: guard.Classify("UPDATE users SET name = 'x' WHERE id = 1"),
		Preview:        guard.Preview{Available: true, Count: 3},
	}
}

// TestQueueAddStampsTheEntry covers what the queue adds to what it is given:
// an opaque identifier and the moment the mutation was requested. The timestamp
// is what a timeout will be measured from when auto-reject lands, so it is
// stored now rather than derived later.
func TestQueueAddStampsTheEntry(t *testing.T) {
	clock := newFakeClock(at)
	queue := guard.NewQueue(clock.Now)

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
	queue := guard.NewQueue(newFakeClock(at).Now)

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

// TestQueueTakeRemoves is the whole of the decision protocol: an entry can be
// taken exactly once, so one keypress executes one mutation and a second
// keypress on the same confirmation executes nothing.
func TestQueueTakeRemoves(t *testing.T) {
	queue := guard.NewQueue(newFakeClock(at).Now)
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
	queue := guard.NewQueue(newFakeClock(at).Now)

	if _, ok := queue.Take("no-such-id"); ok {
		t.Error("Take reported an entry for an ID that was never issued")
	}
}

// TestQueueHoldsSeveralEntries: the editor and the MCP server both feed the
// gate, so entries coexist and a decision touches only its own.
func TestQueueHoldsSeveralEntries(t *testing.T) {
	queue := guard.NewQueue(newFakeClock(at).Now)

	first := queue.Add(guard.Pending{Profile: "editor", SQL: "DELETE FROM a", Origin: guard.OriginHuman})
	second := queue.Add(guard.Pending{Profile: "agent", SQL: "DELETE FROM b", Origin: guard.OriginAI})

	if queue.Len() != 2 {
		t.Fatalf("Len = %d, want 2", queue.Len())
	}
	got, ok := queue.Take(second.ID)
	if !ok || got.SQL != "DELETE FROM b" || got.Origin != guard.OriginAI {
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
	queue := guard.NewQueue(newFakeClock(at).Now)

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
