package db_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
)

// These tests state what the Result union promises its callers: a Result knows
// which arm it is, hands that arm over, and refuses to hand over one it is not.
// The refusal is the load-bearing half — it is what stops a caller written for
// SQL from rendering a MongoDB or Redis answer as an empty table.

func TestTableResultCarriesItsRows(t *testing.T) {
	rows := db.ResultSet{
		Columns:   []string{"id", "name"},
		Rows:      [][]*string{{str("1"), nil}},
		Truncated: true,
	}

	result := db.TableResult(rows)

	if got := result.Kind(); got != db.ResultTable {
		t.Errorf("Kind() = %q, want %q", got, db.ResultTable)
	}
	got, ok := result.Table()
	if !ok {
		t.Fatal("Table() reported the arm absent, want the rows it was built from")
	}
	if len(got.Columns) != 2 || got.Columns[0] != "id" || got.Columns[1] != "name" {
		t.Errorf("Columns = %v, want [id name]", got.Columns)
	}
	if len(got.Rows) != 1 || got.Rows[0][0] == nil || *got.Rows[0][0] != "1" {
		t.Errorf("Rows = %v, want one row starting \"1\"", got.Rows)
	}
	if got.Rows[0][1] != nil {
		t.Errorf("Rows[0][1] = %q, want SQL NULL to survive the wrapping as nil", *got.Rows[0][1])
	}
	if !got.Truncated {
		t.Error("Truncated = false, want the cap to survive the wrapping")
	}
}

func TestValueResultCarriesItsReply(t *testing.T) {
	reply := db.Reply{
		Kind: db.ReplyArray,
		Items: []db.Reply{
			{Kind: db.ReplyString, Text: "one"},
			{Kind: db.ReplyNil},
		},
		Truncated: true,
	}

	result := db.ValueResult(reply)

	if got := result.Kind(); got != db.ResultValue {
		t.Errorf("Kind() = %q, want %q", got, db.ResultValue)
	}
	got, ok := result.Value()
	if !ok {
		t.Fatal("Value() reported the arm absent, want the reply it was built from")
	}
	if got.Kind != db.ReplyArray || len(got.Items) != 2 {
		t.Fatalf("reply = %#v, want the two-element array it was built from", got)
	}
	if got.Items[0].Text != "one" || got.Items[1].Kind != db.ReplyNil {
		t.Errorf("items = %#v, want a string then a nil — a nil reply is not an empty string", got.Items)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want the cap to survive the wrapping")
	}
}

// TestAReplyIsSerialisable pins the promise the wire task depends on: the
// service lifts a Reply out of the Value arm the way it lifts a ResultSet out
// of the Table arm, and what it lifts out has to survive being sent.
//
// The two halves are the tag and the absences. Kind is always written, because
// it is the only honest way to read a node — and the scalar fields are
// omitempty, so a zero integer and a false boolean are absent rather than
// present as zeroes. That is safe in exactly one reading order, and this test
// is where it is written down: take the kind, then the field it names, and
// treat an absent field as its zero value.
func TestAReplyIsSerialisable(t *testing.T) {
	reply := db.Reply{
		Kind: db.ReplyMap,
		Entries: []db.ReplyEntry{
			{Key: db.Reply{Kind: db.ReplyString, Text: "count"}, Value: db.Reply{Kind: db.ReplyInteger}},
			{Key: db.Reply{Kind: db.ReplyString, Text: "flag"}, Value: db.Reply{Kind: db.ReplyBoolean}},
			{Key: db.Reply{Kind: db.ReplyString, Text: "items"}, Value: db.Reply{
				Kind:      db.ReplyArray,
				Items:     []db.Reply{{Kind: db.ReplyNil}, {Kind: db.ReplyDouble, Double: 1.5}, {Kind: db.ReplyError, Text: "WRONGTYPE"}},
				Truncated: true,
			}},
		},
	}

	encoded, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("marshalling a Reply: %v", err)
	}

	var decoded db.Reply
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling a Reply: %v", err)
	}
	if decoded.Kind != db.ReplyMap || len(decoded.Entries) != 3 {
		t.Fatalf("decoded = %#v, want the three-entry map it was built from", decoded)
	}

	zeroInteger := decoded.Entries[0].Value
	if zeroInteger.Kind != db.ReplyInteger || zeroInteger.Integer != 0 {
		t.Errorf("the zero integer decoded as %#v, want an integer reading 0", zeroInteger)
	}
	// The kind is written as "kind":"integer"; what must be absent is the
	// field, "integer":0.
	if strings.Contains(string(encoded), `"integer":`) {
		t.Errorf("encoded = %s, want a zero integer left out rather than written", encoded)
	}

	items := decoded.Entries[2].Value
	if !items.Truncated || len(items.Items) != 3 || items.Items[0].Kind != db.ReplyNil {
		t.Errorf("the array decoded as %#v, want its cut marker and its three elements", items)
	}
	if items.Items[2].Kind != db.ReplyError || items.Items[2].Text != "WRONGTYPE" {
		t.Errorf("the nested error decoded as %#v, want an error carried as a value", items.Items[2])
	}
}

func TestDocumentsResultCarriesItsDocuments(t *testing.T) {
	documents := []json.RawMessage{
		json.RawMessage(`{"_id":{"$oid":"507f1f77bcf86cd799439011"},"name":"a"}`),
		json.RawMessage(`{"name":"b","n":2}`),
	}

	result := db.DocumentsResult(documents, true)

	if got := result.Kind(); got != db.ResultDocuments {
		t.Errorf("Kind() = %q, want %q", got, db.ResultDocuments)
	}
	got, ok := result.Documents()
	if !ok {
		t.Fatal("Documents() reported the arm absent, want the documents it was built from")
	}
	if len(got.Documents) != 2 {
		t.Fatalf("Documents = %d, want the two it was built from", len(got.Documents))
	}
	if string(got.Documents[1]) != `{"name":"b","n":2}` {
		t.Errorf("Documents[1] = %s, want the document's own bytes", got.Documents[1])
	}
	if !got.Truncated {
		t.Error("Truncated = false, want the cap to survive the wrapping")
	}
}

// TestDocumentsResultHasNoDocumentsRatherThanNone pins the one thing the
// constructor normalises. The arm is lifted out and serialised for a renderer
// that iterates it, and a null where a list belongs is a crash rather than an
// empty result.
func TestDocumentsResultHasNoDocumentsRatherThanNone(t *testing.T) {
	got, ok := db.DocumentsResult(nil, false).Documents()
	if !ok {
		t.Fatal("Documents() reported the arm absent on a Result tagged documents")
	}
	if got.Documents == nil {
		t.Fatal("Documents = nil, want an empty list — no documents is an answer, and it has to survive being sent")
	}

	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshalling an empty DocumentSet: %v", err)
	}
	if !strings.Contains(string(encoded), `"documents":[]`) {
		t.Errorf("encoded = %s, want an empty list rather than a null", encoded)
	}
}

// TestADocumentSetIsSerialisable pins the promise the wire task depends on: the
// service lifts a DocumentSet out of the Documents arm the way it lifts a
// ResultSet out of the Table arm, and what it lifts out has to survive being
// sent.
//
// The load-bearing half is that a document is spliced in as the object it
// already is. Carrying the documents as []byte would base64 them, and a
// frontend handed a base64 string has a document it cannot render without
// decoding it a second time.
func TestADocumentSetIsSerialisable(t *testing.T) {
	set, _ := db.DocumentsResult([]json.RawMessage{
		json.RawMessage(`{"_id":{"$oid":"507f1f77bcf86cd799439011"},"n":1,"tags":["a","b"],"nested":{"k":null}}`),
	}, true).Documents()

	encoded, err := json.Marshal(set)
	if err != nil {
		t.Fatalf("marshalling a DocumentSet: %v", err)
	}
	if strings.Contains(string(encoded), `"documents":["`) {
		t.Fatalf("encoded = %s, want the document spliced in as an object rather than quoted or base64", encoded)
	}

	var decoded struct {
		Documents []struct {
			ID     map[string]string `json:"_id"`
			N      int               `json:"n"`
			Tags   []string          `json:"tags"`
			Nested map[string]any    `json:"nested"`
		} `json:"documents"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshalling a DocumentSet: %v", err)
	}
	if len(decoded.Documents) != 1 {
		t.Fatalf("decoded %d documents, want 1", len(decoded.Documents))
	}
	document := decoded.Documents[0]
	if document.ID["$oid"] != "507f1f77bcf86cd799439011" {
		t.Errorf("_id = %v, want the extended-JSON object id to survive as an object", document.ID)
	}
	if document.N != 1 || len(document.Tags) != 2 {
		t.Errorf("document = %+v, want its scalars and its array", document)
	}
	if value, present := document.Nested["k"]; !present || value != nil {
		t.Errorf("nested = %v, want an explicit null to stay an explicit null", document.Nested)
	}
	if !decoded.Truncated {
		t.Error("Truncated = false, want the cut marker to survive being sent")
	}
}

// TestAnArmRefusesToBeAnother is the half that keeps a caller honest. A Result
// holding a Redis reply handed to code written for SQL must say so, or the code
// draws an empty grid and calls it the database's answer.
func TestAnArmRefusesToBeAnother(t *testing.T) {
	if _, ok := db.TableResult(db.ResultSet{}).Value(); ok {
		t.Error("Value() answered a Table Result, want it refused")
	}
	if _, ok := db.TableResult(db.ResultSet{}).Documents(); ok {
		t.Error("Documents() answered a Table Result, want it refused")
	}
	if _, ok := db.ValueResult(db.Reply{Kind: db.ReplyNil}).Table(); ok {
		t.Error("Table() answered a Value Result, want it refused")
	}
	if _, ok := db.ValueResult(db.Reply{Kind: db.ReplyNil}).Documents(); ok {
		t.Error("Documents() answered a Value Result, want it refused")
	}
	if _, ok := db.DocumentsResult(nil, false).Table(); ok {
		t.Error("Table() answered a Documents Result, want it refused")
	}
	if _, ok := db.DocumentsResult(nil, false).Value(); ok {
		t.Error("Value() answered a Documents Result, want it refused")
	}
}

// TestZeroResultIsNoArm pins the zero value. It is not an empty table — an
// empty table is a real answer, and a Result nobody filled in is not one — so
// the accessors must refuse it rather than hand back a blank payload.
func TestZeroResultIsNoArm(t *testing.T) {
	var result db.Result

	if got := result.Kind(); got == db.ResultTable {
		t.Errorf("Kind() = %q, want the zero value not to pass for a table", got)
	}
	if _, ok := result.Table(); ok {
		t.Error("Table() answered the zero Result, want it refused")
	}
	if _, ok := result.Value(); ok {
		t.Error("Value() answered the zero Result, want it refused")
	}
	if _, ok := result.Documents(); ok {
		t.Error("Documents() answered the zero Result, want it refused")
	}
}

// TestResultKindsAreTheADRsVocabulary pins the three tags ADR-0006 names. The
// two arms that have no payload type yet still have their tag, so the callers
// written now already branch over the whole vocabulary.
func TestResultKindsAreTheADRsVocabulary(t *testing.T) {
	for kind, want := range map[db.ResultKind]string{
		db.ResultTable:     "table",
		db.ResultDocuments: "documents",
		db.ResultValue:     "value",
	} {
		if string(kind) != want {
			t.Errorf("kind = %q, want %q", kind, want)
		}
	}
}

func str(s string) *string { return &s }
