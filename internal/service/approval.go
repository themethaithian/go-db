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
// for an ID that is not waiting is not a decision and is not recorded.
//
// It returns no error: every outcome is a result the UI renders directly.
func (s *AppService) ConfirmPending(ctx context.Context, id string) QueryResult {
	pending, ok := s.pending.Take(id)
	if !ok {
		return unknownPending()
	}

	result := decided(pending)
	record := decision(pending, guard.Confirmed, s.clock())

	conn, err := s.registry.Conn(pending.Profile)
	if err != nil {
		// The human confirmed and nothing ran. That is still a decision, and
		// the reason it went nowhere belongs in the record.
		record.Error = oneLine(err)
		result = notConnected(result, pending.Profile)
		return s.write(result, record)
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
	return s.write(result, decision(pending, guard.Cancelled, s.clock()))
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
// long they took to answer.
func decision(pending guard.Pending, outcome guard.Decision, at time.Time) guard.Record {
	record := guard.Record{
		RequestedAt:    pending.RequestedAt,
		DecidedAt:      at,
		Origin:         pending.Origin,
		Profile:        pending.Profile,
		SQL:            pending.SQL,
		Classification: pending.Classification,
		Decision:       outcome,
		Decider:        guard.DeciderHuman,
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
