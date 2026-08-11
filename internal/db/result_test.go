package db_test

import (
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

// TestZeroResultIsNoArm pins the zero value. It is not an empty table — an
// empty table is a real answer, and a Result nobody filled in is not one — so
// Table() must refuse it rather than hand back a blank ResultSet.
func TestZeroResultIsNoArm(t *testing.T) {
	var result db.Result

	if got := result.Kind(); got == db.ResultTable {
		t.Errorf("Kind() = %q, want the zero value not to pass for a table", got)
	}
	if _, ok := result.Table(); ok {
		t.Error("Table() answered the zero Result, want it refused")
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
