package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	// QueryExecuted reports that a confirmed or approved mutation ran.
	// AffectedRows is what the database really changed.
	QueryExecuted QueryStatus = "executed"
	// QueryCancelled reports that a withheld mutation was discarded unrun:
	// the human cancelled their own Inline Confirm, or the caller that
	// submitted an AI query withdrew it before anyone answered.
	QueryCancelled QueryStatus = "cancelled"
	// QueryRejected reports that a human refused an AI-originated mutation in
	// the Approval Console. Nothing was executed.
	QueryRejected QueryStatus = "rejected"
	// QueryTimedOut reports that nobody answered an AI-originated mutation
	// before its deadline, so the Approval Console rejected it. Nothing was
	// executed.
	QueryTimedOut QueryStatus = "timed_out"
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

	// Columns, Rows, and Truncated are set only when Status is QueryOK, and
	// only for an Engine whose answers are columns and rows.
	Columns   []string    `json:"columns,omitempty"`
	Rows      [][]*string `json:"rows,omitempty"`
	Truncated bool        `json:"truncated"`

	// Value is the answer of an Engine that replies with one typed value —
	// Redis — and is set only when Status is QueryOK. It is the Result union's
	// Value arm carried out to the UI unchanged (ADR-0006), so the renderer
	// sees the reply tree the database sent rather than a flattening of it.
	//
	// It and Columns/Rows are alternatives, never both: a read comes back in
	// exactly one shape, and which one is a fact about the Engine. A pointer
	// so it is absent from the JSON rather than present as an empty reply,
	// which would be a value the database never sent — and so that a table
	// answer serialises exactly as it did before this field existed.
	Value *db.Reply `json:"value,omitempty"`

	// Documents is the answer of an Engine that replies with a list of JSON
	// documents — MongoDB — and is set only when Status is QueryOK. It is the
	// Result union's Documents arm carried out to the UI unchanged (ADR-0006),
	// so the renderer sees the same relaxed extended JSON the adapter built
	// rather than a re-encoding of it.
	//
	// It, Value, and Columns/Rows/Truncated are alternatives, never more than
	// one: a read comes back in exactly one shape, and which one is a fact
	// about the Engine. A pointer for the reason Value is one — absent from
	// the JSON rather than present as an empty list, which would be a value
	// the database never sent — and so that a table or value answer's own
	// wire shape is unchanged by this field existing.
	Documents *db.DocumentSet `json:"documents,omitempty"`

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

// Classify reports whether statement is provably read-only on engine, without
// connecting to anything or running it. It is what the editor's read/mutation
// badge shows while the human is still typing, and it is the same verdict
// RunQuery will reach for the same text on a Profile of that Engine.
//
// The Engine is a parameter here and a lookup in RunQuery, and the difference
// is deliberate. This is the badge: it is advisory, it is asked on every
// keystroke, and the caller already knows which Profile the editor is pointed
// at — so it says which language to read the text in rather than making the
// facade re-read the Profile store to find out. Nothing executes on the
// strength of the answer. What executes goes through RunQuery, which takes the
// Engine from the saved Profile precisely because no caller may choose it.
func (s *AppService) Classify(engine db.Engine, statement string) guard.Classification {
	return classifyFor(engine, statement)
}

// SplitStatements reports where every statement in buffer begins and ends on
// engine, so the editor can run the one under the cursor instead of the whole
// buffer. The boundaries come from the same place the Engine's classifier
// reads, so the span the badge is computed from is the text RunQuery is given.
func (s *AppService) SplitStatements(engine db.Engine, buffer string) []guard.StatementSpan {
	return splitFor(engine, buffer)
}

// RunQuery submits one query on the named Profile and database, on behalf of
// origin.
//
// database names the schema the statement runs in, and it is a real one: the
// Connection Registry hands back a connection whose own default schema it is,
// so an unqualified table name resolves there and nothing has to rewrite the
// statement. A blank database is the Profile's own connection — what the
// localhost API, the MCP proxy and the Explorer all submit, and what the editor
// submits until a human picks a database.
//
// Every query passes the Approval Gate's classifier first — the one the
// Profile's own Engine names, resolved here and taken from the saved Profile
// rather than from the caller (ADR-0006). A Profile that is not there has no
// Engine, so its statement is not judged at all and nothing runs: the query
// fails with what the store said, as it always did.
//
// Anything not provably read-only is withheld and takes its Origin's route. A read executes
// inside a database-enforced read-only transaction, so a statement the
// classifier misjudged is caught a second time: if the database refuses it for
// writing, the query is reclassified and rerouted to the gate on the same
// footing as a mutation the classifier had caught. See db.Conn.ReadQuery for
// what that second layer does and does not cover.
//
// It returns no error: every outcome, including failure, is a result the UI
// renders directly.
//
// origin is recorded and returned with the result, and decides the policy the
// gate applies. A human's mutation raises an Inline Confirm and returns at
// once, to be answered by a second call. An AI's waits in the Approval Console,
// and this call blocks with it: it returns when a human approves it — having
// run it — rejects it, or its deadline passes. That is the whole point of the
// design. An agent cannot be trusted to come back for a result it was told to
// poll for, so the pause lives in the call it already made, and ctx is how the
// caller withdraws: an MCP client that dies takes its pending mutation with it.
func (s *AppService) RunQuery(ctx context.Context, profileName, database, sql string, origin guard.Origin) QueryResult {
	engine, err := s.engineFor(profileName)
	if err != nil {
		// No Profile, no Engine, no classifier: the statement stays unjudged
		// rather than being read as SQL on the strength of nothing. What the
		// human is told is unchanged — a Profile that is not there is not
		// connected, in the same words as one that is saved and not connected,
		// because from the editor those are the same mistake. Anything else
		// wrong with the store is its own failure and is reported as one.
		result := QueryResult{Classification: guard.Unjudged(noEngine), Origin: origin}
		if errors.Is(err, db.ErrProfileNotFound) {
			return notConnected(result, profileName)
		}
		result.Status = QueryFailed
		result.Message = oneLine(err)
		return result
	}

	classification := classifyFor(engine, sql)
	result := QueryResult{Classification: classification, Origin: origin}

	if !classification.IsRead() {
		return s.withhold(ctx, engine, profileName, database, sql, result)
	}

	conn, err := s.registry.Conn(ctx, profileName, database)
	if err != nil {
		return noConnection(result, profileName, err)
	}

	answer, err := conn.ReadQuery(ctx, sql)
	switch {
	case errors.Is(err, db.ErrWriteAttempt):
		// The classifier was wrong and the database caught it. The query is a
		// mutation from here on, and takes the same route as one that had been
		// recognised upfront — including the confirmation and the preview,
		// which will usually have none for a statement nobody can rewrite.
		result.Classification = guard.Backstopped()
		return s.withhold(ctx, engine, profileName, database, sql, result)

	case err != nil:
		result.Status = QueryFailed
		result.Message = oneLine(err)
		return result
	}
	return rendered(result, answer)
}

// rendered fills in the answer a read came back with, whichever arm of the
// Result union holds it.
//
// This is the one place the facade renders a read for the UI, and it takes all
// three arms that exist rather than demanding a table, because which arm
// arrives is a fact about the Engine and not a mistake (ADR-0006). The arms
// are alternatives: a table answer sets Columns, Rows and Truncated exactly as
// it always did, a value answer sets Value, a documents answer sets Documents,
// and none of the three ever sets another's fields.
//
// A Result no adapter tagged is still a loud failure, for the reason readTable
// gives: there is nothing honest to show for it, and an empty table is a real
// answer a database can give, so rendering one here would invent a reply.
// Every ADR-0006 arm renders now, so reaching this path means Conn.ReadQuery
// returned the union's zero value — an adapter bug, not an Engine go-db
// cannot show yet.
func rendered(result QueryResult, answer db.Result) QueryResult {
	if rows, ok := answer.Table(); ok {
		result.Status = QueryOK
		result.Columns, result.Rows, result.Truncated = rows.Columns, rows.Rows, rows.Truncated
		result.Message = summarise(rows)
		return result
	}
	if value, ok := answer.Value(); ok {
		result.Status = QueryOK
		result.Value = &value
		result.Message = summariseValue(value)
		return result
	}
	if docs, ok := answer.Documents(); ok {
		result.Status = QueryOK
		result.Documents = &docs
		result.Message = summariseDocuments(docs)
		return result
	}

	result.Status = QueryFailed
	result.Message = "this answer came back untagged: no arm of the Result union was set, which is an adapter bug rather than an Engine go-db cannot show"
	return result
}

// withhold applies the Origin's policy to a mutation. It is one function so a
// mutation caught by the classifier and one caught by the database take exactly
// the same route — which is the point of the backstop.
//
// Both policies need a connection twice over — to compute the Impact Preview
// now and to run the statement once it is decided — so a Profile that is not
// connected is reported rather than queued against nothing. Both get the same
// preview, computed the same way: what the human is deciding on does not depend
// on who asked.
func (s *AppService) withhold(ctx context.Context, engine db.Engine, profileName, database, sql string, result QueryResult) QueryResult {
	conn, err := s.registry.Conn(ctx, profileName, database)
	if err != nil {
		return noConnection(result, profileName, err)
	}

	pending := guard.Pending{
		Profile:        profileName,
		Database:       database,
		SQL:            sql,
		Origin:         result.Origin,
		Classification: result.Classification,
		Preview:        s.previewImpact(ctx, engine, conn, sql),
	}

	if result.Origin != guard.OriginHuman {
		return s.awaitApproval(ctx, pending)
	}

	confirm := s.pending.Add(pending)
	result.Status = QueryRequiresConfirmation
	result.PendingID = confirm.ID
	result.Preview = &confirm.Preview
	result.Message = confirmationMessage(result.Classification, confirm.Preview)
	return result
}

func notConnected(result QueryResult, profileName string) QueryResult {
	result.Status = QueryNotConnected
	result.Message = fmt.Sprintf("Profile %q is not connected: connect it and run the query again.", profileName)
	return result
}

// noConnection reports why there was nowhere to run a query. Two things can go
// wrong now that a query names a database as well as a Profile, and they call
// for different fixes: a Profile nobody has connected is a Connect away, while
// a schema the server will not open a connection on — it does not exist, the
// credentials cannot see it — is the database's own refusal and is shown in its
// own words.
func noConnection(result QueryResult, profileName string, err error) QueryResult {
	if errors.Is(err, db.ErrNotConnected) {
		return notConnected(result, profileName)
	}
	result.Status = QueryFailed
	result.Message = oneLine(err)
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

// readTable runs one read on conn and unwraps the Table arm of what comes back.
// It is what every read that genuinely needs a table goes through — the
// Database tree's introspection, an Impact Preview's count — as opposed to the
// editor's own query, which renders whichever arm arrives and goes through
// rendered instead.
//
// The unwrapping can fail, and it must fail loudly. These callers are asking a
// SQL question and doing arithmetic on the answer: counting a preview's rows,
// listing a schema's tables. An Engine whose answers are documents or one typed
// value has no table to give them (ADR-0006), and there is nothing sensible to
// substitute — the Table arm's zero value is an empty columns-and-rows answer,
// and an empty answer is a real result a database can return, so reporting one
// here would invent a reply the database never gave. The shape mismatch is
// returned as an error instead, which every caller already knows how to report,
// and it names the kind it was given so the fix is obvious. When those Engines
// grow introspection of their own, this is the seam it hangs off.
func readTable(ctx context.Context, conn db.Conn, sql string) (db.ResultSet, error) {
	result, err := conn.ReadQuery(ctx, sql)
	if err != nil {
		return db.ResultSet{}, err
	}
	rows, ok := result.Table()
	if !ok {
		return db.ResultSet{}, fmt.Errorf(
			"this answer came back as %s, which is not a table and cannot be shown as one", resultShape(result))
	}
	return rows, nil
}

// resultShape names a Result's kind for the line a human reads. The kinds are
// ADR-0006's vocabulary; an unset one is a Result no adapter filled in, which is
// a bug in an adapter rather than a shape.
func resultShape(result db.Result) string {
	switch kind := result.Kind(); kind {
	case db.ResultDocuments:
		return "a list of documents"
	case db.ResultValue:
		return "a single value"
	case "":
		return "no result at all"
	default:
		return fmt.Sprintf("%q", kind)
	}
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

// summariseDocuments is summarise for an Engine that answers with a list of
// JSON documents — MongoDB. It says the same two things summarise says about a
// table — how many came back, and whether that is all of it — in Documents'
// own unit, mirroring summarise's wording (and Truncated's meaning) exactly so
// the two read as the same sentence in different units rather than two
// different voices.
func summariseDocuments(docs db.DocumentSet) string {
	if docs.Truncated {
		return fmt.Sprintf("Showing the first %d documents; the result was truncated.", len(docs.Documents))
	}
	if len(docs.Documents) == 1 {
		return "1 document."
	}
	return fmt.Sprintf("%d documents.", len(docs.Documents))
}

// summariseValue is summarise for an Engine that answers with one typed value.
// It says the same two things — how much came back, and whether that is all of
// it — in the units a reply is measured in, and it names the kind because a
// value's kind is the answer as much as its contents are: nil and the empty
// string are different replies, and only the kind tells them apart.
//
// Only the outermost node is described. A container cut short deep inside the
// tree marks itself, and the renderer says "and more" where the more actually
// is; repeating that up here would say it in the wrong place.
func summariseValue(reply db.Reply) string {
	switch reply.Kind {
	case db.ReplyNil:
		return "1 value: nothing is stored there."
	case db.ReplyArray:
		return "1 value: " + containerOf("a list", len(reply.Items), "items", reply.Truncated)
	case db.ReplyMap:
		return "1 value: " + containerOf("a map", len(reply.Entries), "entries", reply.Truncated)
	case db.ReplyError:
		return "1 value: an error the server sent back inside the reply."
	default:
		return "1 value: " + replyKindName(reply.Kind) + "."
	}
}

// containerOf describes a reply's list or map: what it is, how much of it there
// is, and whether that is the whole of it.
func containerOf(what string, size int, unit string, truncated bool) string {
	if truncated {
		return fmt.Sprintf("%s, showing the first %d %s; it was truncated.", what, size, unit)
	}
	if size == 1 {
		return fmt.Sprintf("%s of 1 %s.", what, strings.TrimSuffix(unit, "s"))
	}
	return fmt.Sprintf("%s of %d %s.", what, size, unit)
}

// replyKindName is a scalar reply's kind as a human writes it. An unknown kind
// is quoted rather than described: it is an adapter's word, not one from here.
func replyKindName(kind db.ReplyKind) string {
	switch kind {
	case db.ReplyString:
		return "a string"
	case db.ReplyInteger:
		return "an integer"
	case db.ReplyDouble:
		return "a number"
	case db.ReplyBoolean:
		return "a boolean"
	default:
		return fmt.Sprintf("%q", kind)
	}
}
