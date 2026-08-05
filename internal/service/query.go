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
	// QueryRequiresApproval reports that the query did not run because it is
	// not provably read-only. Classification says why. The policy that
	// follows — Inline Confirm for a human, the Approval Console for an AI —
	// belongs to the Approval Gate; withholding the query is what happens
	// here.
	QueryRequiresApproval QueryStatus = "requires_approval"
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
// origin is recorded and returned with the result. The gate applies a different
// policy to each Origin — Inline Confirm and the Approval Console — which
// arrives with the Approval Gate slice; the seam takes the Origin now so it
// does not have to change then.
func (s *AppService) RunQuery(ctx context.Context, profileName, sql string, origin guard.Origin) QueryResult {
	classification := guard.Classify(sql)
	result := QueryResult{Classification: classification, Origin: origin}

	if !classification.IsRead() {
		return withheld(result)
	}

	conn, err := s.registry.Conn(profileName)
	if err != nil {
		result.Status = QueryNotConnected
		result.Message = fmt.Sprintf("Profile %q is not connected: connect it and run the query again.", profileName)
		return result
	}

	rows, err := conn.ReadQuery(ctx, sql)
	switch {
	case errors.Is(err, db.ErrWriteAttempt):
		// The classifier was wrong and the database caught it. The query is a
		// mutation from here on, and takes the same route as one that had been
		// recognised upfront.
		result.Classification = guard.Backstopped()
		return withheld(result)

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

// withheld completes the result for a query the gate did not let run. It is one
// function so a mutation caught by the classifier and one caught by the
// database are indistinguishable downstream — which is the point of the
// backstop.
func withheld(result QueryResult) QueryResult {
	result.Status = QueryRequiresApproval
	result.Message = fmt.Sprintf("This query was not run: %s. It needs approval first.", result.Classification.Reason)
	return result
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
