package guard

import (
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"

	// The parser needs a value-expression implementation to build literals
	// into the AST. test_driver is the standalone one shipped with the parser
	// module, for callers that do not link the rest of TiDB; its name is a
	// historical accident, not a statement about where it belongs.
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

// Origin is the source that submitted a query: the human via the SQL editor, or
// an AI agent via the MCP server. Every query request carries its Origin, and
// the Approval Gate applies a different policy to each — Inline Confirm for a
// human, the Approval Console for an AI.
type Origin string

const (
	// OriginHuman is a query submitted by the human from the SQL editor.
	OriginHuman Origin = "human"
	// OriginAI is a query submitted by an AI agent through the MCP server.
	OriginAI Origin = "ai"
)

// Valid reports whether o is one of the known Origins. An Origin arrives from
// an adapter — a bound method's argument, a JSON field — so it is worth
// checking before policy is chosen by it.
func (o Origin) Valid() bool { return o == OriginHuman || o == OriginAI }

// Kind is the verdict of the query classifier.
type Kind string

const (
	// Read is a statement proven to only read. It executes directly, inside a
	// read-only transaction that backs the proof up.
	Read Kind = "read"
	// Mutation is everything else. It does not execute until the Approval
	// Gate says so.
	Mutation Kind = "mutation"
)

// Classification is what the Approval Gate decided about one query, and why.
//
// Reason is one line of prose written for the human whose query was stopped;
// it is shown in the editor badge, in Inline Confirm, and in the Approval
// Console, so it names the statement rather than describing the parse.
type Classification struct {
	Kind   Kind   `json:"kind"`
	Reason string `json:"reason"`
}

// IsRead reports whether the query may execute without approval.
func (c Classification) IsRead() bool { return c.Kind == Read }

// Classify decides whether sql is provably read-only.
//
// The bar is proof, not suspicion: a statement is Read only when the parser
// produced exactly one statement, that statement is one of a small allowlist of
// read-only forms, and nothing anywhere inside it takes a lock, writes a file,
// or is itself a statement outside the allowlist. Everything else — including
// input that will not parse, input that is not a statement at all, and input
// holding more than one statement — is a Mutation and enters the gate.
//
// Classify is the first of the Approval Gate's two layers and does not pretend
// to be complete: no static reading of a SELECT can tell whether a function it
// calls writes. That is what the read-only transaction reads execute inside is
// for; a write that slips past here is refused by the database and rerouted to
// the gate. See Backstopped.
//
// The two layers do not overlap everywhere, so this one cannot be relaxed on
// the assumption that the other is behind it: MySQL commits implicitly before
// DDL, which therefore escapes the read-only transaction entirely. For CREATE,
// DROP, ALTER and TRUNCATE, the allowlist below is the only thing standing in
// the way.
func Classify(sql string) (result Classification) {
	// The parser is a large generated state machine fed untrusted text. A
	// panic in it must not become a query that ran unclassified, so the one
	// outcome this recovery can produce is the safe one.
	defer func() {
		if recovered := recover(); recovered != nil {
			result = mutation("the statement could not be parsed")
		}
	}()
	return parseAndClassify(sql)
}

// parseAndClassify borrows a parser for exactly as long as the verdict needs
// it. The borrow is the delicate part: the AST points into buffers the parser
// owns and overwrites on its next use, so returning the parser to the pool
// before the verdict is in would let another goroutine rewrite the statement
// being judged — and the corruption would be silent, not a crash.
func parseAndClassify(sql string) Classification {
	p := parsers.Get().(*parser.Parser)
	stmts, _, err := p.Parse(sql, "", "")

	verdict := verdictFor(stmts, err)

	// Only here is the AST finished with. A parser that panicked never reaches
	// this line, which is intended: it is not fit to hand to anyone else.
	parsers.Put(p)
	return verdict
}

func verdictFor(stmts []ast.StmtNode, err error) Classification {
	switch {
	case err != nil:
		return mutation("the statement could not be parsed")
	case len(stmts) == 0:
		return mutation("there is no statement to run")
	case len(stmts) > 1:
		return mutation("the input holds more than one statement")
	}
	return classifyStatement(stmts[0])
}

// Backstopped returns the Classification of a query the classifier called a
// read and the database then refused to run inside its read-only transaction —
// the Approval Gate's second layer firing. The query is a mutation from that
// moment on, and the reclassification travels with the result so the human sees
// why a query they were told was a read is asking for approval.
func Backstopped() Classification {
	return mutation("the database rejected a write inside the read-only transaction this query ran in")
}

// Unjudged returns the Classification of a statement no classifier here can
// judge at all, with reason saying why in one line for the human.
//
// It is the verdict for an Engine whose classifier is not written yet, and it
// exists so that gap is a Mutation rather than a hole. ADR-0006 gives each
// Engine one classifier and makes every one of them a closed allowlist that
// fails closed; an Engine with no allowlist at all has proved nothing about
// anything, so everything it submits is withheld — a MongoDB find() as surely
// as a MongoDB drop(). Nothing is quietly waved through while the classifier
// that would have judged it is still being written.
//
// The caller says why, because the reason names an Engine and this package
// does not import the one that defines them.
func Unjudged(reason string) Classification { return mutation(reason) }

// classifyStatement judges one parsed statement, in two passes. The first asks
// whether the statement's own form is read-only; the second walks everything
// underneath it, because a lock or a nested statement anywhere in the tree
// makes the whole thing a mutation however innocent the outermost SELECT looks.
func classifyStatement(stmt ast.StmtNode) Classification {
	form := classifyForm(stmt)
	if form.Kind == Mutation {
		return form
	}
	if nested := scan(stmt); nested != nil {
		return *nested
	}
	return form
}

// classifyForm judges a statement by its own shape, ignoring what is nested
// inside it. The allowlist is the whole of the read-only claim: a statement
// type absent from this switch is a mutation, so a parser upgrade that adds
// statements cannot quietly widen what runs unapproved.
func classifyForm(stmt ast.StmtNode) Classification {
	switch s := stmt.(type) {
	case *ast.SelectStmt:
		return read(selectKeyword(s) + " statement")

	case *ast.SetOprStmt:
		// UNION, INTERSECT, EXCEPT: a set operation over SELECTs.
		return read("SELECT statement")

	case *ast.ShowStmt:
		return read("SHOW statement")

	case *ast.UseStmt:
		return read("USE statement")

	case *ast.ExplainStmt:
		// The parser models DESCRIBE t as an EXPLAIN wrapping a SHOW.
		if _, isShow := s.Stmt.(*ast.ShowStmt); isShow {
			return read("DESCRIBE statement")
		}
		explained := classifyStatement(s.Stmt)
		switch {
		case explained.IsRead():
			return read("EXPLAIN of a read-only statement")
		case s.Analyze:
			// EXPLAIN ANALYZE runs the statement it explains and reports what
			// really happened, so explaining a write performs it.
			return mutation("EXPLAIN ANALYZE runs the statement it explains, and " + article(explained.Reason))
		default:
			return mutation("EXPLAIN of " + article(explained.Reason))
		}
	}
	return mutation(statementName(stmt))
}

// scan walks everything beneath a statement looking for the three ways a tree
// that starts read-only stops being one: a locking read, a read that writes a
// file, and a nested statement that is not itself read-only. Depth is the point
// — FOR UPDATE inside a derived table, a subquery, a CTE body, or one branch of
// a UNION is as real as one on the outermost SELECT.
func scan(stmt ast.StmtNode) *Classification {
	finder := &scanner{}
	stmt.Accept(finder)
	return finder.found
}

type scanner struct {
	found *Classification
}

func (v *scanner) Enter(node ast.Node) (ast.Node, bool) {
	if v.found != nil {
		return node, true // a verdict is already in; stop descending
	}

	switch n := node.(type) {
	case *ast.SelectStmt:
		if n.LockInfo != nil && n.LockInfo.LockType != ast.SelectLockNone {
			// FOR UPDATE and FOR SHARE take row locks and hold them for the
			// transaction, so a "read" can block every writer on the table.
			v.reject(mutation("SELECT ... " + strings.ToUpper(n.LockInfo.LockType.String()) + " takes row locks"))
		}
		if n.SelectIntoOpt != nil {
			v.reject(mutation("SELECT ... INTO writes a file on the database server"))
		}

	case ast.StmtNode:
		// A statement nested inside another — a CTE body, an explained
		// statement — is held to the same allowlist as a top-level one.
		if form := classifyForm(n); form.Kind == Mutation {
			v.reject(form)
		}
	}
	return node, false
}

func (v *scanner) Leave(node ast.Node) (ast.Node, bool) { return node, true }

func (v *scanner) reject(c Classification) { v.found = &c }

// parsers pools parser instances. One is a large generated state machine that
// is expensive to build and not safe to share, and the editor and the MCP
// server classify concurrently.
var parsers = sync.Pool{New: func() any { return parser.New() }}

func read(reason string) Classification     { return Classification{Kind: Read, Reason: reason} }
func mutation(reason string) Classification { return Classification{Kind: Mutation, Reason: reason} }

// selectKeyword returns the keyword a SELECT-shaped statement was written with,
// so the reason echoes what the human typed: SELECT, TABLE, or VALUES.
func selectKeyword(s *ast.SelectStmt) string {
	switch s.Kind {
	case ast.SelectStmtKindTable:
		return "TABLE"
	case ast.SelectStmtKindValues:
		return "VALUES"
	}
	return "SELECT"
}

// reasons overrides the derived name where the statement's danger is not in its
// keyword. The rest of MySQL's grammar is covered by statementName; a
// hand-written line here has to earn itself.
var reasons = map[string]string{
	"CALL":    "CALL statement, and a stored procedure can write",
	"DO":      "DO statement, and the expression it evaluates can write",
	"SET":     "SET statement, and a session variable can change how later statements behave",
	"PREPARE": "PREPARE statement, and the statement it prepares is hidden in a string",
}

// statementName renders a statement type as the SQL keywords a human would
// recognise: *ast.DropTableStmt becomes "DROP TABLE statement". Deriving it
// from the type keeps every statement MySQL has — and every one a parser
// upgrade adds — explicable without a hand-maintained table.
func statementName(stmt ast.StmtNode) string {
	if reason, ok := reasons[spaceCamel(strings.TrimSuffix(shortTypeName(stmt), "Stmt"))]; ok {
		return reason
	}
	return keywordName(stmt)
}

// keywordName is statementName without the explanations: just the keywords,
// for sentences that supply their own reason. "SET statement" reads correctly
// after "no preview is available for a"; the classifier's fuller "SET
// statement, and a session variable can change how later statements behave"
// does not.
func keywordName(stmt ast.StmtNode) string {
	name := spaceCamel(strings.TrimSuffix(shortTypeName(stmt), "Stmt"))
	if name == "" {
		return "statement that is not provably read-only"
	}
	return name + " statement"
}

// shortTypeName is "DropTable" for a *ast.DropTableStmt.
func shortTypeName(stmt ast.StmtNode) string {
	name := fmt.Sprintf("%T", stmt)
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return name
}

// spaceCamel turns "DropTable" into "DROP TABLE", leaving runs of capitals —
// "NonTransactionalDML" — as one word.
func spaceCamel(name string) string {
	runes := []rune(name)
	var out strings.Builder
	for i, r := range runes {
		startsWord := i > 0 && unicode.IsUpper(r) &&
			(unicode.IsLower(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1])))
		if startsWord {
			out.WriteRune(' ')
		}
		out.WriteRune(unicode.ToUpper(r))
	}
	return out.String()
}

// article prefixes a reason with the indefinite article that suits it, so
// "EXPLAIN of " + article("UPDATE statement") reads as a sentence.
func article(reason string) string {
	if reason == "" {
		return reason
	}
	switch unicode.ToLower([]rune(reason)[0]) {
	case 'a', 'e', 'i', 'o', 'u':
		return "an " + reason
	}
	return "a " + reason
}
