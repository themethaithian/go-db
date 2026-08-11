package db

import "encoding/json"

// ResultKind names the shape a read came back in. It is the tag of the Result
// union, and its vocabulary is fixed by ADR-0006: one kind per Engine's answer
// shape, because the shape of results is part of what an Engine means.
//
// All three tags exist from the day the union does. A caller that branches over
// the vocabulary is a caller no Engine makes revisit; a caller that assumes
// Table is the only tag is one that would silently render a document tree as an
// empty table.
type ResultKind string

const (
	// ResultTable is a flat columns-and-rows answer — SQL's shape, carried in
	// the Table arm as the ResultSet that predates this union.
	ResultTable ResultKind = "table"
	// ResultDocuments is a list of JSON documents — MongoDB's shape, carried in
	// the Documents arm as a DocumentSet.
	ResultDocuments ResultKind = "documents"
	// ResultValue is one typed reply tree — Redis's shape, carried in the Value
	// arm as a Reply.
	ResultValue ResultKind = "value"
)

// Result is one read's answer, tagged with the shape it came back in.
//
// It is the union ADR-0006 decides on, and it exists so the read path can stay
// one path across three Engines without widening the table until nested
// documents and typed Redis replies fit in cells — a flattening that is lossy
// both ways, and which the ADR rejects outright.
//
// All three arms are here now, and each arrived with the adapter that fills it
// rather than before it: a payload type invented ahead of its driver is a
// guess, and the ADR is explicit that each shape comes from the Engine's own
// answers rather than from what seemed reasonable in advance. What the union
// had to get right was the seam — every caller asks which arm it holds and
// handles being told "not that one" — so each arm added a branch where a branch
// already was, and changed no existing call.
//
// The arms are unexported and reached through accessors on purpose. A Result
// can only be built by one of this package's constructors, so a tag can never
// disagree with the payload beside it, and the zero value is no arm at all
// rather than a plausible-looking empty table.
type Result struct {
	kind      ResultKind
	table     ResultSet
	documents DocumentSet
	value     Reply
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

// DocumentSet is one read's answer from an Engine that answers in documents,
// and the Documents arm's payload.
//
// Each document is already-rendered JSON rather than a Go map, and that is the
// point of the type. A document is nested and heterogeneous — the whole reason
// ADR-0006 refuses to flatten one into table cells — so the only useful thing
// to do with it is show it, and JSON is both what the renderer wants and what
// the wire carries. Rendering once, here, means the same bytes the adapter read
// off the server reach the screen, instead of being decoded into Go values and
// guessed back into JSON by whoever serialises them.
//
// json.RawMessage rather than []byte is deliberate: a []byte is base64 on the
// wire, and a document that arrives at the frontend as a base64 string is a
// document nobody can render without decoding it again. A RawMessage is spliced
// into the JSON as the object it already is.
//
// The documents are relaxed extended JSON. MongoDB writes two flavours, and the
// difference is what happens to values JSON can carry on its own: canonical
// wraps every one of them in its BSON type, so the integer 1 is written
// {"$numberInt": "1"} and a date is {"$date": {"$numberLong": "…"}}, while
// relaxed writes 1 and "2020-01-01T00:00:00Z" and reserves the $-wrappers for
// the types JSON genuinely has no syntax for — $oid, $binary, $regularExpression
// and the rest. Relaxed is chosen for two reasons. It is what a human reads: a
// document view where every number is a two-key object is a view of the
// encoding rather than of the data. And it is what a human wrote: go-db's
// MongoDB grammar takes plain JSON and passes $-wrapped extended JSON through
// untouched (see internal/mongoql), so a document rendered relaxed comes back
// looking like the filter that found it.
//
// What relaxed gives up is numeric width — an int32, an int64 and a double that
// all hold 1 are all written 1, and reading the document back would make them
// one type. That is a real loss, and it is the right one to take: nothing in
// this client round-trips a document through the editor, the width is not shown
// anywhere, and the alternative makes every ordinary document unreadable to buy
// a distinction nobody is looking at.
type DocumentSet struct {
	// Documents are the documents read, in the order the server sent them.
	Documents []json.RawMessage `json:"documents"`
	// Truncated reports that the answer was cut off at MaxRows and there was
	// more to come. It carries ResultSet.Truncated's meaning exactly.
	Truncated bool `json:"truncated"`
}

// DocumentsResult tags documents as the Documents arm. It is what an Engine
// whose answers are documents returns from Conn.ReadQuery — today, MongoDB.
//
// truncated says the answer was cut at MaxRows, and MaxRows is the cap for the
// same reason it caps a table: a desktop client renders what a human reads, the
// performance budget is a feature, and a find with no filter over a collection
// of millions is the unbounded SELECT in different syntax. A result cut short
// says so, so the documents on screen are never mistaken for the whole answer.
//
// No documents is an empty list rather than an absent one. The arm is lifted
// out and serialised for a renderer that will iterate it, and a null where a
// list belongs is a crash rather than an empty result — so a nil slice is
// normalised here, once, instead of at every caller.
func DocumentsResult(documents []json.RawMessage, truncated bool) Result {
	if documents == nil {
		documents = []json.RawMessage{}
	}
	return Result{kind: ResultDocuments, documents: DocumentSet{Documents: documents, Truncated: truncated}}
}

// Documents returns the Documents arm's documents, and whether this Result is
// that arm.
//
// The second return carries Table's weight: a caller handed the wrong arm has
// been given an answer it cannot render, and being told so is how it finds out
// rather than drawing a blank.
func (r Result) Documents() (DocumentSet, bool) {
	if r.kind != ResultDocuments {
		return DocumentSet{}, false
	}
	return r.documents, true
}

// ValueResult tags reply as the Value arm. It is what an Engine whose answers
// are one typed value returns from Conn.ReadQuery — today, Redis.
func ValueResult(reply Reply) Result {
	return Result{kind: ResultValue, value: reply}
}

// Value returns the Value arm's reply, and whether this Result is that arm.
//
// The second return carries the same weight as Table's: a caller handed the
// wrong arm has been given an answer it cannot render, and being told so is
// how it finds out rather than drawing a blank.
func (r Result) Value() (Reply, bool) {
	if r.kind != ResultValue {
		return Reply{}, false
	}
	return r.value, true
}

// MaxReplyItems is the most elements this package builds into any one array or
// map of a reply tree. It is MaxRows' argument one shape over: a desktop client
// renders what a human reads, and LRANGE key 0 -1 across a million-element list
// is the unbounded SELECT wearing different syntax. A container cut short says
// so, so a short list on screen is never mistaken for the whole one.
//
// The cap bounds what is kept and handed on, not what arrives: the reply is
// already read off the socket in full by the client library before this package
// sees a value of it. What it protects is where the budget is actually spent —
// the payload the wire carries and the tree the renderer walks.
const MaxReplyItems = 1000

// MaxReplyDepth is how many levels of containers a reply tree is built before
// the nesting itself is cut. It covers the dimension MaxReplyItems does not:
// a thousand elements at each of a thousand levels is a thousand times the tree
// either cap alone allows.
//
// Sixteen is past anything Redis's own commands answer with — XINFO STREAM FULL
// and COMMAND DOCS, the deepest replies a read on the classifier's allowlist can
// produce, nest a handful of levels — so in practice this is the guard against a
// shape nobody expected rather than a limit on one anybody meant.
const MaxReplyDepth = 16

// ReplyKind names the type of one node in a Redis reply tree. It is RESP3's own
// vocabulary, minus the distinctions that do not survive the trip through a Go
// client: a set arrives as an array, a verbatim string as a string, and a
// number too large for an integer as its digits.
type ReplyKind string

const (
	// ReplyNil is Redis's nil — the key was not there. It is not an empty
	// string, and the two must not render alike: GET on a missing key and GET
	// on a key holding "" are different answers.
	ReplyNil ReplyKind = "nil"
	// ReplyString is a bulk or simple string. Redis strings are bytes, so this
	// one may not be valid UTF-8; it is carried exactly as it arrived.
	ReplyString  ReplyKind = "string"
	ReplyInteger ReplyKind = "integer"
	ReplyDouble  ReplyKind = "double"
	ReplyBoolean ReplyKind = "boolean"
	ReplyArray   ReplyKind = "array"
	// ReplyMap is a RESP3 map. Only a server speaking RESP3 sends one; against
	// an older server the same answer arrives as a flat array, which is what
	// that server said and what is shown.
	ReplyMap ReplyKind = "map"
	// ReplyError is an error Redis put inside a reply rather than instead of
	// one — one element of an array that failed while its neighbours
	// succeeded. It is a value because that is what it is: the command
	// succeeded, and part of its answer is that something did not. An error
	// returned instead of a reply is not this; it fails the read.
	ReplyError ReplyKind = "error"
)

// Reply is one node of a Redis reply tree, and the Value arm's payload.
//
// One struct carries every kind rather than an interface with a case per kind,
// because this type exists to be read by a caller that did not write it: the
// service lifts it out of the Result and the frontend renders it, and both want
// a tag they can switch on and fields they can reach. Kind is that tag, and it
// is the only honest way in — a node's Text says nothing about whether the node
// is a string, and reading it without asking is how an integer becomes an empty
// string on screen.
//
// The scalar fields are omitempty, so a zero integer, a false boolean and an
// empty string are absent from the JSON rather than present as zeroes. That is
// safe in exactly one reading order: take Kind first, then the field it names,
// and treat an absent field as its zero value — which is the value. It is not
// safe to infer the kind from which fields are present.
//
// Truncated belongs to this node and no other. A cut deep inside a tree marks
// the container that was cut, not the root, so a caller can say "and more"
// where the more actually is.
type Reply struct {
	Kind ReplyKind `json:"kind"`

	// Text is the bytes of a ReplyString, or the message of a ReplyError.
	Text string `json:"text,omitempty"`
	// Integer is a ReplyInteger's value.
	Integer int64 `json:"integer,omitempty"`
	// Double is a ReplyDouble's value, and is always finite. Redis's double
	// can be inf, -inf or nan, which JSON has no way to write; those three
	// arrive as a ReplyString holding RESP's own token for them, so a tree
	// carrying one still serialises.
	Double float64 `json:"double,omitempty"`
	// Boolean is a ReplyBoolean's value.
	Boolean bool `json:"boolean,omitempty"`

	// Items are a ReplyArray's elements, in the order Redis sent them.
	Items []Reply `json:"items,omitempty"`
	// Entries are a ReplyMap's pairs. They are a list rather than a Go map for
	// two reasons: a Redis map key is itself a reply and need not be a string,
	// and a list has an order a map has not — see ReplyEntry.
	Entries []ReplyEntry `json:"entries,omitempty"`

	// Truncated reports that this container held more than was kept, because
	// it ran past MaxReplyItems or MaxReplyDepth. It carries ResultSet's
	// Truncated meaning exactly: the answer on screen is not the whole answer,
	// and it says so rather than letting a number be misread.
	Truncated bool `json:"truncated,omitempty"`
}

// ReplyEntry is one key and value of a ReplyMap.
//
// Entries are ordered by their key's text form. Order is not something a RESP3
// map promises, and the map arrives here as a Go map whose iteration order is
// deliberately random — so with nothing done, the same reply would render
// differently every time it was asked for. Sorting is what makes an answer the
// same answer twice, and it is also what makes the kept half of a map that ran
// past MaxReplyItems the same half twice.
type ReplyEntry struct {
	Key   Reply `json:"key"`
	Value Reply `json:"value"`
}
