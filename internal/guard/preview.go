package guard

import (
	"strings"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	"github.com/pingcap/tidb/pkg/parser/format"
)

// PreviewSampleRows is how many of the affected rows a sample shows. It is
// small on purpose: the sample answers "what am I about to touch", not "show me
// the data", and it is fetched while a human waits with a confirmation open.
const PreviewSampleRows = 5

// previewAlias names the derived table the rewrite wraps a mutation's own LIMIT
// in. It is spelled out rather than generated because it appears in SQL a human
// may read in the audit log.
const previewAlias = "impact_preview"

// Preview is an Impact Preview: an advisory estimate of what a mutation would
// do, or the explicit statement that there is none.
//
// Available is the field to read first, and the reason this is not simply a
// count. "There is no preview for this statement" and "the preview found no
// rows" are opposite messages — one means the human is deciding blind, the
// other means the mutation is a no-op — and a bare zero cannot tell them apart.
//
// It is advisory: it was computed before the mutation was approved, and the
// data can change between the two. The actual affected-row count is recorded
// beside it in the audit log after execution.
type Preview struct {
	Available bool `json:"available"`
	// Reason says why there is no preview, in one line for the human. It is
	// set exactly when Available is false.
	Reason string `json:"reason,omitempty"`
	// Count is the advisory affected-row count, meaningful when Available.
	Count int64 `json:"count"`
	// Columns and Rows are a sample of the affected rows, at most
	// PreviewSampleRows of them. A preview can be available with no sample:
	// INSERT ... VALUES knows its count from the statement and has no existing
	// rows to show.
	Columns []string    `json:"columns,omitempty"`
	Rows    [][]*string `json:"rows,omitempty"`
}

// NoPreview returns the Preview of a statement that cannot be previewed. reason
// is one line of prose shown where the count would have been.
func NoPreview(reason string) Preview {
	return Preview{Available: false, Reason: reason}
}

// HasSample reports whether the preview carries example rows.
func (p Preview) HasSample() bool { return p.Available && len(p.Rows) > 0 }

// PreviewPlan is the read-only work that computes one mutation's Impact
// Preview. The Approval Gate plans; the caller runs the plan through the
// Profile's connection, which executes reads inside a read-only transaction —
// so a preview cannot write even if this package were wrong.
//
// CountSQL and StaticCount are alternatives, not both: a statement that carries
// its own rows (INSERT ... VALUES) is counted without asking the database.
type PreviewPlan struct {
	// CountSQL counts the rows the mutation would affect. Empty when
	// StaticCount already holds the answer.
	CountSQL string `json:"count_sql,omitempty"`
	// SampleSQL selects at most PreviewSampleRows of those rows. Empty when no
	// sample is possible.
	SampleSQL string `json:"sample_sql,omitempty"`
	// StaticCount is the affected-row count read off the statement itself,
	// meaningful when CountSQL is empty.
	StaticCount int64 `json:"static_count,omitempty"`
	// Reason says why there is no preview. It is set exactly when PlanPreview
	// reported false, and is then the only field set.
	Reason string `json:"reason,omitempty"`
}

// PlanPreview rewrites a mutation into the reads that describe it (ADR 0003).
//
// A single-table UPDATE or DELETE becomes a COUNT over its own FROM and WHERE
// — respected exactly, so a statement with no WHERE previews the whole table,
// which is precisely the mistake the preview exists to expose — plus a small
// LIMIT-ed sample of the same rows. A mutation carrying its own LIMIT is
// counted inside a derived table that keeps that LIMIT, so the count is what
// the mutation would really touch rather than what its WHERE matches. INSERT
// ... VALUES is counted from its own tuples without a query; INSERT ... SELECT
// counts its source.
//
// The second return reports whether a preview is possible at all. Everything
// else — DDL, a multi-table UPDATE or DELETE, REPLACE, LOAD DATA, input that
// will not parse — returns false with a one-line reason in plan.Reason, and no
// SQL. That refusal is a first-class answer: see Preview.
//
// The SQL is regenerated from the parsed statement by the parser's own restore
// facility, never spliced together from the caller's text, so identifiers and
// literals keep whatever quoting and escaping they were written with.
//
// Every plan is submitted back to Classify before it is returned, and a plan
// the classifier does not call read-only is refused. The rewrite is the one
// place in the app that generates SQL, and generated SQL is held to the same
// bar as typed SQL.
func PlanPreview(sql string) (plan PreviewPlan, ok bool) {
	// The parser is a generated state machine fed untrusted text; a panic in it
	// must produce a refusal, not a preview built from a half-parsed tree.
	defer func() {
		if recovered := recover(); recovered != nil {
			plan, ok = noPlan("the statement could not be parsed")
		}
	}()

	plan, ok = parseAndPlan(sql)
	if !ok {
		return plan, false
	}
	for _, query := range []string{plan.CountSQL, plan.SampleSQL} {
		if query != "" && !Classify(query).IsRead() {
			return noPlan("the preview this statement rewrites to is not provably read-only")
		}
	}
	return plan, true
}

// parseAndPlan borrows a parser for exactly as long as the plan needs it. The
// AST points into buffers the parser owns and overwrites on its next use, so
// every string the plan carries must be rendered before the parser goes back.
func parseAndPlan(sql string) (PreviewPlan, bool) {
	p := parsers.Get().(*parser.Parser)
	stmts, _, err := p.Parse(sql, "", "")

	plan, ok := planFor(stmts, err)

	// A parser that panicked never reaches this line, which is intended.
	parsers.Put(p)
	return plan, ok
}

func planFor(stmts []ast.StmtNode, err error) (PreviewPlan, bool) {
	switch {
	case err != nil:
		return noPlan("the statement could not be parsed")
	case len(stmts) == 0:
		return noPlan("there is no statement to preview")
	case len(stmts) > 1:
		return noPlan("the input holds more than one statement")
	}
	return planStatement(stmts[0])
}

func planStatement(stmt ast.StmtNode) (PreviewPlan, bool) {
	switch n := stmt.(type) {
	case *ast.UpdateStmt:
		if n.MultipleTable {
			return noPlan(overManyTables("UPDATE"))
		}
		return planRowMutation("UPDATE", n.TableRefs, n.Where, n.Order, n.Limit, n.With)

	case *ast.DeleteStmt:
		if n.IsMultiTable {
			return noPlan(overManyTables("DELETE"))
		}
		return planRowMutation("DELETE", n.TableRefs, n.Where, n.Order, n.Limit, n.With)

	case *ast.InsertStmt:
		return planInsert(n)
	}

	// Everything else, including the statements the classifier calls reads. A
	// read reaches here only through the database backstop — the classifier
	// said read, the database said write — and in that case the reassuring
	// answer would be the wrong one. The same sentence is true either way:
	// whatever this would change, it cannot be selected in advance.
	return noPlan(article(keywordName(stmt)) + " has no rows that can be shown in advance")
}

// planRowMutation rewrites the UPDATE and DELETE shapes, which differ only in
// what they do to the rows their FROM and WHERE select.
//
// A CTE travels with the rewrite: without it the WHERE clause would refer to a
// name that no longer exists. It goes on the outermost SELECT, where MySQL
// scopes it to the whole statement including any derived table.
func planRowMutation(verb string, refs *ast.TableRefsClause, where ast.ExprNode, order *ast.OrderByClause, limit *ast.Limit, with *ast.WithClause) (PreviewPlan, bool) {
	if !singleTable(refs) {
		return noPlan(overManyTables(verb))
	}

	if limit != nil {
		// The mutation stops after LIMIT rows, so counting what its WHERE
		// matches would overstate it — sometimes by the whole table. Counting
		// the mutation's own row set inside a derived table keeps the LIMIT,
		// and the ORDER BY that decides which rows the LIMIT keeps.
		counted := selectFrom(refs, where, order, limit, field(literalOne()))
		sampled := selectFrom(refs, where, order, limit, wildcard())
		return render(
			selectFrom(wrap(counted), nil, nil, nil, field(countRows()), with),
			selectFrom(wrap(sampled), nil, nil, sampleLimit(), wildcard(), with),
		)
	}

	// No LIMIT: the WHERE clause is the whole story. ORDER BY without LIMIT
	// does not change which rows a mutation touches, so it is dropped from the
	// count and kept on the sample, where it decides which rows are shown.
	return render(
		selectFrom(refs, where, nil, nil, field(countRows()), with),
		selectFrom(refs, where, order, sampleLimit(), wildcard(), with),
	)
}

// planInsert previews the rows an INSERT would add.
//
// INSERT ... VALUES carries its rows in its own text: the count is the number
// of tuples, and no query is needed. There is no sample either — the values the
// human is being asked about are the ones they just typed, and echoing them
// back would add nothing. The count is what the statement asks for; ON
// DUPLICATE KEY UPDATE can make MySQL report a different number of affected
// rows, which is one of the things the audit log's advisory-versus-actual pair
// is there to show.
func planInsert(n *ast.InsertStmt) (PreviewPlan, bool) {
	if n.IsReplace {
		// REPLACE deletes the rows it collides with, and those rows are not
		// named anywhere in the statement.
		return noPlan("a REPLACE statement also deletes rows it does not name")
	}

	if n.Select != nil {
		// The source select is the row set being inserted; counting it inside a
		// derived table leaves it exactly as written, whatever it does with
		// GROUP BY, DISTINCT, LIMIT, or a set operation.
		return render(
			selectFrom(wrap(n.Select), nil, nil, nil, field(countRows())),
			selectFrom(wrap(n.Select), nil, nil, sampleLimit(), wildcard()),
		)
	}

	if len(n.Lists) == 0 {
		return noPlan("an INSERT statement with no rows in it has nothing to count")
	}
	return PreviewPlan{StaticCount: int64(len(n.Lists))}, true
}

// singleTable reports whether refs names exactly one real table. Anything else
// — a join, a comma list, a derived table — cannot be previewed, because a
// COUNT over the join answers a question nobody asked: which rows of which
// table the mutation changes is not what the join returns.
func singleTable(refs *ast.TableRefsClause) bool {
	if refs == nil || refs.TableRefs == nil || refs.TableRefs.Right != nil {
		return false
	}
	source, ok := refs.TableRefs.Left.(*ast.TableSource)
	if !ok {
		return false
	}
	_, ok = source.Source.(*ast.TableName)
	return ok
}

// selectFrom assembles one SELECT from the parts of a mutation. Nothing here
// interpolates text: the caller's own AST nodes are reused, and the parser
// renders them back to SQL.
func selectFrom(from *ast.TableRefsClause, where ast.ExprNode, order *ast.OrderByClause, limit *ast.Limit, fields []*ast.SelectField, with ...*ast.WithClause) *ast.SelectStmt {
	stmt := &ast.SelectStmt{
		SelectStmtOpts: &ast.SelectStmtOpts{SQLCache: true},
		Kind:           ast.SelectStmtKindSelect,
		From:           from,
		Where:          where,
		OrderBy:        order,
		Limit:          limit,
		Fields:         &ast.FieldList{Fields: fields},
	}
	if len(with) == 1 {
		stmt.With = with[0]
	}
	return stmt
}

// wrap turns any row source — a SELECT, a UNION — into a FROM clause that
// selects from it as a derived table.
func wrap(inner ast.ResultSetNode) *ast.TableRefsClause {
	return &ast.TableRefsClause{TableRefs: &ast.Join{
		Left: &ast.TableSource{Source: inner, AsName: ast.NewCIStr(previewAlias)},
	}}
}

func wildcard() []*ast.SelectField {
	return []*ast.SelectField{{WildCard: &ast.WildCardField{}}}
}

func field(expr ast.ExprNode) []*ast.SelectField {
	return []*ast.SelectField{{Expr: expr}}
}

// countRows is COUNT(1), which is how the parser spells COUNT(*): both count
// rows, and the parser's own AST for COUNT(*) holds the literal 1.
func countRows() ast.ExprNode {
	return &ast.AggregateFuncExpr{F: "count", Args: []ast.ExprNode{literalOne()}}
}

func literalOne() ast.ExprNode { return ast.NewValueExpr(1, "", "") }

func sampleLimit() *ast.Limit {
	return &ast.Limit{Count: ast.NewValueExpr(uint64(PreviewSampleRows), "", "")}
}

// render turns the two planned statements into SQL. A statement the parser
// cannot render is not a preview we are willing to guess at.
func render(count, sample *ast.SelectStmt) (PreviewPlan, bool) {
	countSQL, ok := restore(count)
	if !ok {
		return noPlan("this statement could not be rewritten into a preview")
	}
	sampleSQL, ok := restore(sample)
	if !ok {
		return noPlan("this statement could not be rewritten into a preview")
	}
	return PreviewPlan{CountSQL: countSQL, SampleSQL: sampleSQL}, true
}

// previewRestoreFlags keep the statement faithful rather than pretty: string
// literals stay single-quoted with whatever character-set introducer they were
// parsed with, and identifiers stay back-quoted. A preview whose WHERE clause
// did not mean exactly what the mutation's meant would count the wrong rows.
const previewRestoreFlags = format.DefaultRestoreFlags

func restore(node ast.Node) (string, bool) {
	var sql strings.Builder
	if err := node.Restore(format.NewRestoreCtx(previewRestoreFlags, &sql)); err != nil {
		return "", false
	}
	return sql.String(), true
}

func noPlan(reason string) (PreviewPlan, bool) {
	return PreviewPlan{Reason: reason}, false
}

func overManyTables(verb string) string {
	return "a " + verb + " over more than one table has no single table's rows to count"
}
