package db_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
)

// These tests pin the Profile list's order as the human's own: what is on
// disk is what List and Save return, in exactly that order, never resorted
// by name underneath them. Reorder is the one operation that is allowed to
// rearrange it, and it is all-or-nothing.

func TestSavePreservesInsertionOrderAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())

	want := []string{"zeta", "alpha", "Mid", "beta"}
	for _, name := range want {
		if err := store.Save(db.Profile{Name: name, Host: name + ".internal"}, ""); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
	}

	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Fatalf("List = %v, want %v (insertion order)", got, want)
	}

	// A fresh store over the same directory must see the same order — it is
	// what the file says, not something this process remembers.
	reopened := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	listed, err = reopened.List()
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Errorf("List after reopen = %v, want %v", got, want)
	}
}

func TestSaveOfExistingProfileKeepsItsPosition(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())

	for _, name := range []string{"zeta", "alpha", "Mid"} {
		if err := store.Save(db.Profile{Name: name, Host: name + ".internal"}, ""); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
	}

	if err := store.Save(db.Profile{Name: "alpha", Host: "alpha.new.internal"}, ""); err != nil {
		t.Fatalf("Save(alpha) update: %v", err)
	}

	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"zeta", "alpha", "Mid"}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Fatalf("List = %v, want %v (alpha keeps its position)", got, want)
	}
	if listed[1].Host != "alpha.new.internal" {
		t.Errorf("alpha.Host = %q, want the updated value", listed[1].Host)
	}
}

func TestReorderPermutesAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())

	for _, name := range []string{"zeta", "alpha", "Mid"} {
		if err := store.Save(db.Profile{Name: name, Host: name + ".internal"}, ""); err != nil {
			t.Fatalf("Save(%q): %v", name, err)
		}
	}

	want := []string{"Mid", "zeta", "alpha"}
	if err := store.Reorder(want); err != nil {
		t.Fatalf("Reorder: %v", err)
	}

	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Fatalf("List after Reorder = %v, want %v", got, want)
	}

	// Persisted, not just reordered in memory: a fresh store sees it too.
	reopened := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	listed, err = reopened.List()
	if err != nil {
		t.Fatalf("List after reopen: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Errorf("List after reopen = %v, want %v", got, want)
	}
}

func TestReorderRejectsMissingExtraOrDuplicateNamesAndWritesNothing(t *testing.T) {
	cases := []struct {
		name  string
		order []string
	}{
		{"missing a name", []string{"alpha", "beta"}},
		{"an extra, unknown name", []string{"alpha", "beta", "zeta", "nope"}},
		{"a duplicate", []string{"alpha", "alpha", "beta"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
			for _, name := range []string{"alpha", "beta", "zeta"} {
				if err := store.Save(db.Profile{Name: name, Host: name + ".internal"}, ""); err != nil {
					t.Fatalf("Save(%q): %v", name, err)
				}
			}

			before, err := os.ReadFile(filepath.Join(dir, "profiles.toml"))
			if err != nil {
				t.Fatalf("reading profiles.toml before Reorder: %v", err)
			}

			err = store.Reorder(tc.order)
			if !errors.Is(err, db.ErrProfileOrderMismatch) {
				t.Fatalf("Reorder(%v) error = %v, want ErrProfileOrderMismatch", tc.order, err)
			}

			after, err := os.ReadFile(filepath.Join(dir, "profiles.toml"))
			if err != nil {
				t.Fatalf("reading profiles.toml after Reorder: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("a rejected Reorder wrote a partial file:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestLegacyFileOrderIsPreservedNotSorted(t *testing.T) {
	dir := t.TempDir()
	// Written by hand, in an order the old name-sorting store would never
	// have produced on its own — proof that a pre-existing file's order
	// becomes its starting order rather than being alphabetized on first
	// read.
	legacy := `[[profile]]
name = "zeta"
host = "zeta.internal"

[[profile]]
name = "alpha"
host = "alpha.internal"

[[profile]]
name = "Mid"
host = "mid.internal"
`
	if err := os.WriteFile(filepath.Join(dir, "profiles.toml"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("writing legacy fixture: %v", err)
	}

	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	listed, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"zeta", "alpha", "Mid"}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Errorf("List = %v, want the file's own order %v, not resorted", got, want)
	}
}

func TestGroupRoundTrips(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())

	if err := store.Save(db.Profile{Name: "p", Host: "db.internal", Group: "Staging"}, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := store.Get("p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Group != "Staging" {
		t.Errorf("Group = %q, want %q", got.Group, "Staging")
	}

	// And across a fresh load, not just the in-memory value Save returned.
	reopened := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	got, err = reopened.Get("p")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if got.Group != "Staging" {
		t.Errorf("Group after reopen = %q, want %q", got.Group, "Staging")
	}
}

func TestEmptyGroupOmittedFromTOML(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())

	if err := store.Save(db.Profile{Name: "p", Host: "db.internal"}, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "profiles.toml"))
	if err != nil {
		t.Fatalf("reading profiles.toml: %v", err)
	}
	if strings.Contains(string(data), "group") {
		t.Errorf("profiles.toml = %s, want no group key for an empty Group", data)
	}
}

func profileNames(profiles []db.Profile) []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}
