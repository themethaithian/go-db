package guard

import (
	"math"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
)

// Window is the page of rows one statement asks for.
//
// Pageable is the field to read first, and the reason this is not simply a pair
// of numbers. "This statement has no page to turn" and "this statement asks for
// rows 0 to 1000" are different answers, and a bare zero size cannot tell them
// apart — the first has to hide the pager, the second has to draw it.
//
// Size and Offset are the window: Size rows starting after Offset of them. They
// are what the statement itself asks for, which after a run is what the human
// is looking at.
//
// SQL is the statement that asks for this window. Repage sets it; PageWindow
// leaves it empty, because it describes a statement the caller already has.
type Window struct {
	Pageable bool `json:"pageable"`
	// Reason says why there is no page to turn, in one line for the human. It
	// is set exactly when Pageable is false.
	Reason string `json:"reason,omitempty"`
	// Size is how many rows the window holds, meaningful when Pageable.
	Size int64 `json:"size"`
	// Offset is how many rows the window starts after, meaningful when
	// Pageable.
	Offset int64 `json:"offset"`
	// SQL is the statement asking for this window, set by Repage.
	SQL string `json:"sql,omitempty"`
}

// NoWindow returns the Window of a statement that cannot be paged. reason is
// one line of prose shown where the pager would have been.
//
// It is exported for the callers that know a statement has no window without
// reading it — the facade, for an Engine whose answers are not rows at all — so
// that refusal arrives in the same shape as every refusal this package makes.
func NoWindow(reason string) Window { return Window{Reason: reason} }

// PageWindow reports the window of rows sql asks for: whether it can be paged
// at all, and which rows it currently selects.
//
// It is what the editor asks after a statement has run, to decide whether to
// offer a next page and where that page starts. Nothing is executed and nothing
// is connected to; this reads the statement and nothing else.
//
// A statement is pageable when it is exactly one plain top-level SELECT that
// the classifier proves read-only, and when the window it asks for can be read
// off it. Everything else says so and why: a mutation, DDL, SHOW, a set
// operation, more than one statement, a statement that will not parse, a SELECT
// that writes a file or takes locks, and a LIMIT that is not a plain number.
// See Window.
//
// defaultSize is the page size of a statement carrying no LIMIT of its own —
// the cap the caller's driver really bounded the result at (db.MaxRows). It is
// a parameter rather than a constant here because internal/db imports this
// package, so the dependency only runs one way; and it has to be the real cap,
// because a statement with no LIMIT returned exactly that many rows and the
// next page starts after them.
func PageWindow(sql string, defaultSize int64) (window Window) {
	// The parser is a generated state machine fed untrusted text; a panic in it
	// must produce a refusal, not a window read off a half-parsed tree.
	defer func() {
		if recovered := recover(); recovered != nil {
			window = NoWindow(unparseable)
		}
	}()

	if defaultSize <= 0 {
		return NoWindow(emptyPage)
	}
	return readWindow(sql, defaultSize)
}

// Repage returns sql asking for a different window: the same statement with its
// LIMIT and OFFSET set to size and offset, whatever it had before.
//
// The frontend runs what comes back through the ordinary RunQuery path, so the
// classifier, the Approval Gate and the audit log all see the statement that
// really executes. That is the whole design of editor paging: nothing pages
// around the gate, so the rewrite has to produce SQL rather than a private
// instruction to the driver.
//
// Only the statement's own window moves. A LIMIT inside a derived table, a
// subquery or a CTE body is part of what the statement means and is left
// exactly as written; the LIMIT that is added or replaced is the outermost one.
//
// The SQL is regenerated from the parsed statement by the parser's own restore
// facility, never spliced onto the caller's text, so a trailing comment or a
// semicolon cannot end up after the LIMIT that was appended to it. What survives
// is the statement; the human's comments and whitespace do not, which is the
// price of appending nothing by hand.
//
// A statement Repage will not page reports it the same way PageWindow does, on
// the same terms and for the same reasons — the two agree on what has a window,
// so a Next button the editor drew is one the rewrite can honour. The rewritten
// statement is submitted back to Classify before it is returned, and one the
// classifier does not call read-only is refused: generated SQL is held to the
// same bar as typed SQL.
func Repage(sql string, size, offset int64) (window Window) {
	defer func() {
		if recovered := recover(); recovered != nil {
			window = NoWindow(unparseable)
		}
	}()

	switch {
	case size <= 0:
		return NoWindow(emptyPage)
	case offset < 0:
		return NoWindow("a page cannot start before the first row")
	}

	window = rewrite(sql, size, offset)
	if !window.Pageable {
		return window
	}
	if !Classify(window.SQL).IsRead() {
		return NoWindow("the statement go-db rewrote to turn the page is not provably read-only")
	}
	return window
}

// readWindow borrows a parser for exactly as long as reading the window takes. The
// borrow follows the rule Classify's does: the AST points into buffers the
// parser owns and overwrites on its next use, so the numbers are off it before
// the parser goes back.
func readWindow(sql string, defaultSize int64) Window {
	p := parsers.Get().(*parser.Parser)
	stmts, _, err := p.Parse(sql, "", "")

	_, window := pageable(stmts, err, defaultSize)

	// A parser that panicked never reaches this line, which is intended.
	parsers.Put(p)
	return window
}

// rewrite borrows a parser for as long as the rewrite takes. Everything the new
// statement is made of belongs to the parser, so it is rendered to SQL before
// the parser goes back.
func rewrite(sql string, size, offset int64) Window {
	p := parsers.Get().(*parser.Parser)
	stmts, _, err := p.Parse(sql, "", "")

	window := repaged(stmts, err, size, offset)

	parsers.Put(p)
	return window
}

func repaged(stmts []ast.StmtNode, err error, size, offset int64) Window {
	// The requested size stands in for the driver's cap while the statement is
	// judged: it is only there to make the window readable, and it is about to
	// be overwritten either way.
	stmt, window := pageable(stmts, err, size)
	if stmt == nil {
		return window
	}

	stmt.Limit = limitClause(size, offset)
	sql, ok := restore(stmt)
	if !ok {
		return NoWindow("this statement could not be written back out with a new window")
	}
	return Window{Pageable: true, Size: size, Offset: offset, SQL: sql}
}

// pageable narrows a parse down to the one statement shape that has a window to
// move, and reads the window it currently asks for. A statement with no window
// comes back nil, with the refusal to hand the caller.
//
// The allowlist is deliberately narrower than the classifier's: every read the
// classifier proves is safe to run, but only a plain SELECT has a LIMIT that
// means "the rows on this page". SHOW takes a LIMIT on some forms and not
// others; TABLE and VALUES are spellings the editor's pager was not written
// for; and a set operation's LIMIT belongs to the whole result or to its last
// branch depending on how it was written, which is not a distinction to make on
// a human's behalf while they are clicking Next. Paging one would mean wrapping
// it in a derived table, and that renames its columns.
func pageable(stmts []ast.StmtNode, err error, defaultSize int64) (*ast.SelectStmt, Window) {
	switch {
	case err != nil:
		return nil, NoWindow(unparseable)
	case len(stmts) == 0:
		return nil, NoWindow("there is no statement to page")
	case len(stmts) > 1:
		return nil, NoWindow("the input holds more than one statement")
	}

	stmt := stmts[0]
	if _, isSetOpr := stmt.(*ast.SetOprStmt); isSetOpr {
		return nil, NoWindow(onlyASelect("set operation"))
	}
	selected, ok := stmt.(*ast.SelectStmt)
	if !ok {
		return nil, NoWindow(onlyASelect(keywordName(stmt)))
	}
	if selected.Kind != ast.SelectStmtKindSelect {
		return nil, NoWindow(onlyASelect(selectKeyword(selected) + " statement"))
	}

	// The gate has the last word on whether this may be rerun at all: a SELECT
	// that takes row locks or writes a file on the server is not a page to
	// turn, and neither is one holding a statement that is not a read.
	if verdict := classifyStatement(selected); !verdict.IsRead() {
		return nil, NoWindow("a page can only be turned on a read, and " + verdict.Reason)
	}

	size, offset, ok := windowOf(selected.Limit, defaultSize)
	if !ok {
		return nil, NoWindow("go-db cannot tell which rows this statement's LIMIT asks for, so it cannot ask for the next ones")
	}
	if size == 0 {
		return nil, NoWindow("a statement with LIMIT 0 asks for no rows, so there is no page to turn")
	}
	return selected, Window{Pageable: true, Size: size, Offset: offset}
}

// windowOf reads a LIMIT clause as a window. A statement without one asks for
// everything and was bounded by the driver's cap instead, so that cap is its
// page size.
func windowOf(limit *ast.Limit, defaultSize int64) (size, offset int64, ok bool) {
	if limit == nil {
		return defaultSize, 0, true
	}
	if size, ok = rowCount(limit.Count); !ok {
		return 0, 0, false
	}
	if limit.Offset == nil {
		return size, 0, true
	}
	if offset, ok = rowCount(limit.Offset); !ok {
		return 0, 0, false
	}
	return size, offset, true
}

// rowCount reads one arm of a LIMIT as a number of rows. Both arms are plain
// integers in MySQL's grammar or they are a placeholder — LIMIT ? — which names
// no rows until something binds it, and a statement whose window is bound
// somewhere else is not one this package will move.
//
// MySQL's LIMIT is unsigned and goes up to 2^64-1, which is how a statement
// says "everything from here on". A count that large is not a page: it does not
// fit the arithmetic the caller pages with, and truncating it into one would
// turn "all the rest" into a small negative window. It is unreadable instead.
func rowCount(expr ast.ExprNode) (int64, bool) {
	value, ok := expr.(ast.ValueExpr)
	if !ok {
		return 0, false
	}
	switch n := value.GetValue().(type) {
	case uint64:
		return int64(n), n <= math.MaxInt64
	case int64:
		return n, n >= 0
	}
	return 0, false
}

// limitClause is the LIMIT a rewritten statement carries. An offset of zero is
// left off rather than written as LIMIT 0,n: the first page of a statement
// should read like the statement.
func limitClause(size, offset int64) *ast.Limit {
	limit := &ast.Limit{Count: ast.NewValueExpr(uint64(size), "", "")}
	if offset > 0 {
		limit.Offset = ast.NewValueExpr(uint64(offset), "", "")
	}
	return limit
}

// onlyASelect says why a statement that is not a plain SELECT has no page.
func onlyASelect(what string) string {
	return "go-db pages a SELECT, and this is " + article(what)
}

const unparseable = "the statement could not be parsed"

const emptyPage = "a page has to hold at least one row"
