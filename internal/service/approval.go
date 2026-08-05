package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
)

// ConfirmPending runs the mutation waiting under id and records the decision.
//
// It is the whole of "one extra keypress": one call, one execution, no second
// look at anything. The statement that runs is the one the gate withheld, taken
// from the queue by ID, so nothing the UI sends back can change what was
// approved. The Impact Preview is not recomputed either — it was advisory when
// it was shown, and computing a second, different answer at the moment of
// execution would only make the human doubt the one they decided on.
//
// The decision is written to the audit log whatever became of it: confirmed and
// executed, confirmed and failed, confirmed with nowhere to run. A confirmation
// for an ID that is not waiting is not a decision and is not recorded — nor is
// one for an Approval Console entry, which has its own caller waiting on it and
// is answered with ApprovePending.
//
// It returns no error: every outcome is a result the UI renders directly.
func (s *AppService) ConfirmPending(ctx context.Context, id string) QueryResult {
	pending, ok := s.pending.Take(id)
	if !ok {
		return unknownPending()
	}
	return s.execute(ctx, pending, decided(pending), decision(pending, s.decidedNow(guard.Confirmed)))
}

// CancelPending discards the mutation waiting under id without running it, and
// records that the human refused it. A cancelled DROP TABLE is exactly the kind
// of thing someone reads the audit log to find, so refusals are recorded as
// carefully as approvals.
func (s *AppService) CancelPending(id string) QueryResult {
	pending, ok := s.pending.Take(id)
	if !ok {
		return unknownPending()
	}

	result := decided(pending)
	result.Status = QueryCancelled
	result.Message = "This query was cancelled and did not run."
	return s.write(result, decision(pending, s.decidedNow(guard.Cancelled)))
}

// ApprovalAck is the Approval Console's answer to a decision: whether a
// mutation was still waiting under that ID, and one line of prose to show.
//
// It carries no result, and that is the design. The statement runs on the
// goroutine that submitted it and is still blocked in RunQuery, so the rows it
// changed travel back to the agent that asked for them. The human who approved
// it gets an acknowledgement now rather than a result they did not ask for and
// cannot use.
type ApprovalAck struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ListPendingApprovals returns what the Approval Console shows: every
// AI-originated mutation currently waiting for a human, oldest first, each with
// its Impact Preview and how long is left before it auto-rejects.
//
// An Inline Confirm never appears here. It belongs to the editor that raised
// it, and offering it in the console would ask the human for the same decision
// twice.
func (s *AppService) ListPendingApprovals() []guard.Waiting {
	return s.pending.Console()
}

// ApprovePending lets the mutation waiting under id run. It returns as soon as
// the waiting caller has been told, not when the statement finishes: the agent
// that submitted it is what executes it, and what the database did goes back
// there. See ApprovalAck.
func (s *AppService) ApprovePending(id string) ApprovalAck {
	return ack(s.pending.Decide(id, true, guard.DeciderHuman))
}

// RejectPending refuses the mutation waiting under id. Nothing runs; the
// waiting caller is told so, and the refusal is recorded by it.
func (s *AppService) RejectPending(id string) ApprovalAck {
	return ack(s.pending.Decide(id, false, guard.DeciderHuman))
}

func ack(decided bool) ApprovalAck {
	if !decided {
		return ApprovalAck{Message: "There is no query waiting for approval under that reference; it may already have been decided, or it may have timed out."}
	}
	return ApprovalAck{OK: true, Message: "Sent to the query that was waiting for it."}
}

// awaitApproval puts an AI-originated mutation in the Approval Console and
// blocks on it — which is the Approval Console's whole shape from this side.
//
// It blocks in the caller's own goroutine rather than handing the statement to
// a background worker, and that is deliberate: the agent asked a question and
// is still holding the line, so the rows the mutation really changed can be the
// answer to it. A design where the approving human's click executed the
// statement would leave the agent with a status to poll for and a human holding
// a result nobody asked them for.
//
// Every way out of the wait is recorded. A refusal, a deadline that passed, and
// a caller that gave up are all things somebody will want to find in the log
// later, and none of them is an error.
func (s *AppService) awaitApproval(ctx context.Context, pending guard.Pending) QueryResult {
	pending, waiter := s.pending.Submit(pending)
	outcome := waiter.Await(ctx)

	result := decided(pending)
	record := decision(pending, outcome)

	if outcome.Decision != guard.Approved {
		result.Status, result.Message = refused(outcome.Decision, s.pending.Timeout())
		return s.write(result, record)
	}
	return s.execute(ctx, pending, result, record)
}

// refused says what became of a mutation that was decided against, in the words
// the agent reads. Each one says the same two things: that nothing ran, and who
// or what settled it — an agent that cannot tell a refusal from a timeout will
// retry the wrong one.
func refused(outcome guard.Decision, timeout time.Duration) (QueryStatus, string) {
	switch outcome {
	case guard.Rejected:
		return QueryRejected, "A human rejected this query in the go-db Approval Console. It did not run."
	case guard.TimedOut:
		return QueryTimedOut, fmt.Sprintf(
			"Nobody approved this query in the go-db Approval Console within %s, so it was rejected automatically. It did not run.", timeout)
	default:
		return QueryCancelled, "This query was withdrawn before anyone decided it. It did not run."
	}
}

// execute runs a mutation that has been decided in its favour and finishes its
// audit record with what really happened. It is one function so a confirmed
// mutation and an approved one run and are recorded identically: the difference
// between the two policies is who answers, not what happens next.
//
// The decision is recorded whatever became of it — executed, failed, or with
// nowhere left to run. All three are decisions, and the reason one went nowhere
// belongs in the record beside it.
func (s *AppService) execute(ctx context.Context, pending guard.Pending, result QueryResult, record guard.Record) QueryResult {
	conn, err := s.registry.Conn(pending.Profile)
	if err != nil {
		record.Error = oneLine(err)
		return s.write(notConnected(result, pending.Profile), record)
	}

	affected, err := conn.Exec(ctx, pending.SQL)
	executedAt := s.clock()
	record.ExecutedAt = &executedAt

	if err != nil {
		record.Error = oneLine(err)
		result.Status = QueryFailed
		result.Message = oneLine(err)
		return s.write(result, record)
	}

	record.AffectedRows = &affected
	result.Status = QueryExecuted
	result.AffectedRows = affected
	result.Message = executedMessage(pending.Preview, affected)
	return s.write(result, record)
}

// decidedNow is the Outcome of a decision a human just made in the editor. The
// Approval Console's outcomes come from the queue instead, which is where the
// waiting and the deadline are.
func (s *AppService) decidedNow(outcome guard.Decision) guard.Outcome {
	return guard.Outcome{Decision: outcome, Decider: guard.DeciderHuman, DecidedAt: s.clock()}
}

// previewImpact computes one mutation's Impact Preview by running the gate's
// plan through conn.
//
// Every query it runs goes through ReadQuery, so the preview executes inside
// the database's read-only transaction: even a rewrite this app got wrong
// cannot write. The mutation itself is never executed to describe itself, which
// is the decision ADR 0003 records — a preview that ran the write would hold
// row locks for as long as the human took to read it.
//
// A preview that will not run is a missing preview, not a failed query. The
// count is advisory, the statement is still the human's to confirm, and the
// database's own words about why it could not be counted are more useful in the
// confirmation than an error where the query used to be.
func (s *AppService) previewImpact(ctx context.Context, conn db.Conn, sql string) guard.Preview {
	plan, ok := guard.PlanPreview(sql)
	if !ok {
		return guard.NoPreview(plan.Reason)
	}

	count := plan.StaticCount
	if plan.CountSQL != "" {
		rows, err := conn.ReadQuery(ctx, plan.CountSQL)
		if err != nil {
			return guard.NoPreview("the affected rows could not be counted: " + oneLine(err))
		}
		count, err = countFrom(rows)
		if err != nil {
			return guard.NoPreview("the affected rows could not be counted: " + oneLine(err))
		}
	}

	preview := guard.Preview{Available: true, Count: count}
	if plan.SampleSQL != "" {
		// A sample that will not run costs the human nothing they need: the
		// count, which is the number they are deciding on, is already in hand.
		if rows, err := conn.ReadQuery(ctx, plan.SampleSQL); err == nil {
			preview.Columns, preview.Rows = rows.Columns, rows.Rows
		}
	}
	return preview
}

// countFrom reads the single number a count query returns.
func countFrom(rows db.ResultSet) (int64, error) {
	if len(rows.Rows) != 1 || len(rows.Rows[0]) != 1 || rows.Rows[0][0] == nil {
		return 0, fmt.Errorf("the count query answered %d rows, want one number", len(rows.Rows))
	}
	count, err := strconv.ParseInt(strings.TrimSpace(*rows.Rows[0][0]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("the count query answered %q, which is not a number", *rows.Rows[0][0])
	}
	return count, nil
}

// decided starts the result for a mutation that has been decided, carrying back
// what the human was looking at when they decided it.
func decided(pending guard.Pending) QueryResult {
	preview := pending.Preview
	return QueryResult{
		Classification: pending.Classification,
		Origin:         pending.Origin,
		PendingID:      pending.ID,
		Preview:        &preview,
	}
}

// decision starts the audit record for one decision. RequestedAt comes from the
// queue entry, so the record still says when the human was first asked however
// long they took to answer — or never did.
func decision(pending guard.Pending, outcome guard.Outcome) guard.Record {
	record := guard.Record{
		RequestedAt:    pending.RequestedAt,
		DecidedAt:      outcome.DecidedAt,
		Origin:         pending.Origin,
		Profile:        pending.Profile,
		SQL:            pending.SQL,
		Classification: pending.Classification,
		Decision:       outcome.Decision,
		Decider:        outcome.Decider,
	}
	if pending.Preview.Available {
		count := pending.Preview.Count
		record.AdvisoryCount = &count
	} else {
		record.NoPreviewReason = pending.Preview.Reason
	}
	return record
}

// write appends the record and hands the result on. An audit append that fails
// is added to the message rather than swallowed: the statement has already run,
// so there is nothing to undo, and a decision missing from the log is something
// the human has to know about while they still remember making it.
func (s *AppService) write(result QueryResult, record guard.Record) QueryResult {
	if err := s.audit.Append(record); err != nil {
		result.Message += fmt.Sprintf(" (This decision could not be written to the audit log: %s.)", oneLine(err))
	}
	return result
}

func unknownPending() QueryResult {
	return QueryResult{
		Status:  QueryUnknownPending,
		Message: "There is no query waiting for confirmation under that reference; it may already have been confirmed or cancelled.",
	}
}

// executedMessage says what happened, and says so beside what was predicted
// when the two disagree — the data moved between the preview and the keypress,
// and noticing that is the point of keeping both numbers.
func executedMessage(preview guard.Preview, affected int64) string {
	if preview.Available && preview.Count != affected {
		return fmt.Sprintf("The query ran and changed %s; the preview estimated %s.",
			rowCount(affected), rowCount(preview.Count))
	}
	return fmt.Sprintf("The query ran and changed %s.", rowCount(affected))
}

func rowCount(n int64) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}
