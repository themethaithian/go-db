package db

// ResultKind names the shape a read came back in. It is the tag of the Result
// union, and its vocabulary is fixed by ADR-0006: one kind per Engine's answer
// shape, because the shape of results is part of what an Engine means.
//
// All three tags exist from the day the union does, even though only Table has
// a payload yet. A caller that branches over the vocabulary now is a caller the
// other two Engines will not have to revisit; a caller that assumes Table is the
// only tag is one that would silently render a document tree as an empty table.
type ResultKind string

const (
	// ResultTable is a flat columns-and-rows answer — SQL's shape, carried in
	// the Table arm as the ResultSet that predates this union.
	ResultTable ResultKind = "table"
	// ResultDocuments is a list of JSON documents — MongoDB's shape. It has no
	// payload type yet; it arrives with the MongoDB adapter.
	ResultDocuments ResultKind = "documents"
	// ResultValue is one typed reply tree — Redis's shape. It has no payload
	// type yet; it arrives with the Redis adapter.
	ResultValue ResultKind = "value"
)

// Result is one read's answer, tagged with the shape it came back in.
//
// It is the union ADR-0006 decides on, and it exists so the read path can stay
// one path across three Engines without widening the table until nested
// documents and typed Redis replies fit in cells — a flattening that is lossy
// both ways, and which the ADR rejects outright.
//
// Only the Table arm is built today, because MySQL is the only Engine there is
// an adapter for. Documents and Value are deliberately absent rather than
// stubbed: a payload type invented before the driver that fills it is a guess,
// and the ADR is explicit that their shapes come from Mongo's documents and
// Redis's reply tree, not from what seemed reasonable in advance. What the union
// must get right now is the seam — every caller already asks which arm it holds
// and handles being told "not that one" — so adding an arm later adds a branch
// where a branch already is, and changes no existing call.
//
// The arms are unexported and reached through accessors on purpose. A Result
// can only be built by one of this package's constructors, so a tag can never
// disagree with the payload beside it, and the zero value is no arm at all
// rather than a plausible-looking empty table.
type Result struct {
	kind  ResultKind
	table ResultSet
}

// TableResult tags rows as the Table arm. It is what an Engine whose answers
// are columns and rows returns from Conn.ReadQuery.
func TableResult(rows ResultSet) Result {
	return Result{kind: ResultTable, table: rows}
}

// Kind reports which arm this Result holds. The zero Result's Kind is the empty
// string, which is no arm and not a fourth kind — the same posture Engine takes
// towards its own zero value.
func (r Result) Kind() ResultKind { return r.kind }

// Table returns the Table arm's rows, and whether this Result is that arm.
//
// The second return is not a formality. A caller written for tables that is
// handed a Documents or Value Result has been given an answer it cannot
// render, and the only honest thing it can do is say so: reporting ok is false
// is how it finds out, instead of showing a blank grid and calling it the
// database's answer.
func (r Result) Table() (ResultSet, bool) {
	if r.kind != ResultTable {
		return ResultSet{}, false
	}
	return r.table, true
}
