package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
)

// QueryStatus is what became of a submitted query. It exists so the UI can
// branch — render a table, raise an Inline Confirm, offer a Connect button —
// without reading prose.
type QueryStatus string

const (
	// QueryOK reports that the query was a read and it ran; Columns and Rows
	// hold its answer.
	QueryOK QueryStatus = "ok"
	// QueryRequiresConfirmation reports that the query did not run because it
	// is not provably read-only, and that it is waiting on one confirmation
	// from the human who submitted it — Inline Confirm. PendingID names it and
	// Preview says what it would do; ConfirmPending runs it, CancelPending
	// discards it.
	QueryRequiresConfirmation QueryStatus = "requires_confirmation"
	// QueryRequiresApproval reports that an AI-originated query did not run
	// because it is not provably read-only. It waits in the Approval Console,
	// which is a later slice; until then it is simply withheld.
	QueryRequiresApproval QueryStatus = "requires_approval"
	// QueryExecuted reports that a confirmed mutation ran. AffectedRows is
	// what the database really changed.
	QueryExecuted QueryStatus = "executed"
	// QueryCancelled reports that a withheld mutation was discarded unrun.
	QueryCancelled QueryStatus = "cancelled"
	// QueryUnknownPending reports that no mutation is waiting under the given
	// ID — it was already confirmed, already cancelled, or never existed.
	QueryUnknownPending QueryStatus = "unknown_pending"
	// QueryNotConnected reports that the Profile has no open connection, so
	// there was nowhere to run the query.
	QueryNotConnected QueryStatus = "not_connected"
	// QueryFailed reports that the database refused the query on its own
	// terms — an unknown column, a dropped connection. Message carries what
	// it said.
	QueryFailed QueryStatus = "failed"
)

// QueryResult is everything the UI needs to render one submitted query,
// whatever became of it. Message is one line of prose meant to be shown as-is.
//
// Classification travels with the result rather than being recomputed, because
// it can change during the run: a query the classifier called a read, which the
// database then refused inside its read-only transaction, comes back a
// mutation. That reclassification is the reroute into the Approval Gate, and
// the human has to be able to see it happen.
type QueryResult struct {
	Status         QueryStatus          `json:"status"`
	Classification guard.Classification `json:"classification"`
	Origin         guard.Origin         `json:"origin"`
	Message        string               `json:"message"`

	// Columns, Rows, and Truncated are set only when Status is QueryOK.
	Columns   []string    `json:"columns,omitempty"`
	Rows      [][]*string `json:"rows,omitempty"`
	Truncated bool        `json:"truncated"`

	// PendingID names the withheld mutation when Status is
	// QueryRequiresConfirmation, and is what ConfirmPending and CancelPending
	// are given. It is opaque: it identifies a statement the gate is holding,
	// so the statement that runs is the one that was previewed and not
	// whatever text came back from the UI.
	PendingID string `json:"pending_id,omitempty"`

	// Preview is the Impact Preview shown with a confirmation, and echoed back
	// with the outcome so the human can see the estimate beside the result. It
	// is nil for a query that was never withheld; when it is present it always
	// says something, including that there is no preview.
	Preview *guard.Preview `json:"preview,omitempty"`

	// AffectedRows is what the mutation really changed, set when Status is
	// QueryExecuted. It is the number the audit log records beside the
	// preview's advisory estimate.
	AffectedRows int64 `json:"affected_rows"`
}

// OK reports whether the query ran and returned rows.
func (r QueryResult) OK() bool { return r.Status == QueryOK }

// Classify reports whether sql is provably read-only, without connecting to
// anything or running it. It is what the editor's read/mutation badge shows
// while the human is still typing, and it is the same verdict RunQuery will
// reach for the same text.
func (s *AppService) Classify(sql string) guard.Classification {
	return guard.Classify(sql)
}

// RunQuery submits one query on the named Profile, on behalf of origin.
//
// Every query passes the Approval Gate's classifier first. Anything not
// provably read-only does not execute at all and comes back
// QueryRequiresApproval with the reason. A read executes inside a
// database-enforced read-only transaction, so a statement the classifier
// misjudged is caught a second time: if the database refuses it for writing,
// the query is reclassified and rerouted to the gate on the same footing as a
// mutation the classifier had caught. See db.Conn.ReadQuery for what that
// second layer does and does not cover.
//
// It returns no error: every outcome, including failure, is a result the UI
// renders directly.
//
// origin is recorded and returned with the result, and decides the policy the
// gate applies: a human's mutation raises an Inline Confirm here and now, an
// AI's waits in the Approval Console.
func (s *AppService) RunQuery(ctx context.Context, profileName, sql string, origin guard.Origin) QueryResult {
	classification := guard.Classify(sql)
	result := QueryResult{Classification: classification, Origin: origin}

	if !classification.IsRead() {
		return s.withhold(ctx, profileName, sql, result)
	}

	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return notConnected(result, profileName)
	}

	rows, err := conn.ReadQuery(ctx, sql)
	switch {
	case errors.Is(err, db.ErrWriteAttempt):
		// The classifier was wrong and the database caught it. The query is a
		// mutation from here on, and takes the same route as one that had been
		// recognised upfront — including the confirmation and the preview,
		// which will usually have none for a statement nobody can rewrite.
		result.Classification = guard.Backstopped()
		return s.withhold(ctx, profileName, sql, result)

	case err != nil:
		result.Status = QueryFailed
		result.Message = oneLine(err)
		return result
	}

	result.Status = QueryOK
	result.Columns, result.Rows, result.Truncated = rows.Columns, rows.Rows, rows.Truncated
	result.Message = summarise(rows)
	return result
}

// withhold applies the Origin's policy to a mutation. It is one function so a
// mutation caught by the classifier and one caught by the database take exactly
// the same route — which is the point of the backstop.
func (s *AppService) withhold(ctx context.Context, profileName, sql string, result QueryResult) QueryResult {
	if result.Origin != guard.OriginHuman {
		// The Approval Console is a later slice. Until it exists, an
		// AI-originated mutation is withheld and nothing else happens on its
		// behalf: no preview, no queue entry, nothing to confirm.
		result.Status = QueryRequiresApproval
		result.Message = fmt.Sprintf("This query was not run: %s. It needs approval first.", result.Classification.Reason)
		return result
	}

	// Inline Confirm needs a connection twice over — to compute the preview
	// now and to run the statement on confirmation — so a Profile that is not
	// connected is reported rather than queued against nothing.
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return notConnected(result, profileName)
	}

	preview := s.previewImpact(ctx, conn, sql)
	pending := s.pending.Add(guard.Pending{
		Profile:        profileName,
		SQL:            sql,
		Origin:         result.Origin,
		Classification: result.Classification,
		Preview:        preview,
	})

	result.Status = QueryRequiresConfirmation
	result.PendingID = pending.ID
	result.Preview = &pending.Preview
	result.Message = confirmationMessage(result.Classification, preview)
	return result
}

func notConnected(result QueryResult, profileName string) QueryResult {
	result.Status = QueryNotConnected
	result.Message = fmt.Sprintf("Profile %q is not connected: connect it and run the query again.", profileName)
	return result
}

// confirmationMessage is the line beside an Inline Confirm. It says three
// things in the order the human needs them: that nothing has run, why it was
// stopped, and what it would do — or that nobody can say what it would do.
func confirmationMessage(classification guard.Classification, preview guard.Preview) string {
	switch {
	case preview.Available:
		return fmt.Sprintf("This query was not run: %s. It would change about %s. Confirm to run it.",
			classification.Reason, rowCount(preview.Count))

	case preview.Reason == classification.Reason:
		// The gate and the rewriter stopped on the same thing — unparseable
		// input, more than one statement — and saying it twice helps nobody.
		return fmt.Sprintf("This query was not run: %s. There is no Impact Preview either. Confirm to run it.",
			classification.Reason)
	}
	return fmt.Sprintf("This query was not run: %s. There is no Impact Preview: %s. Confirm to run it.",
		classification.Reason, preview.Reason)
}

// summarise is the line shown beside a result table.
func summarise(rows db.ResultSet) string {
	if rows.Truncated {
		return fmt.Sprintf("Showing the first %d rows; the result was truncated.", len(rows.Rows))
	}
	if len(rows.Rows) == 1 {
		return "1 row."
	}
	return fmt.Sprintf("%d rows.", len(rows.Rows))
}
