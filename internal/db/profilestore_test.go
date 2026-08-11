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

// These tests pin Engine's backward-compatible arrival: a profiles.toml
// written by v1 (no engine key at all) must still load as though it said
// "mysql", and ProfileStore.Save must refuse to write anything the classifier
// downstream would not recognise as one of the three Engines.

// legacyProfilesTOML is what v1 wrote to disk before Engine existed: a
// profile.toml with no engine key. It is written by hand, not round-tripped
// through Save, so this test actually exercises the old file shape rather
// than whatever this package's own marshalling happens to produce today.
const legacyProfilesTOML = `[[profile]]
name = "legacy"
host = "db.internal"
port = 3306
user = "root"
database = "app"
`

func TestLoadLegacyProfileWithNoEngineDefaultsToMySQL(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "profiles.toml"), []byte(legacyProfilesTOML), 0o600); err != nil {
		t.Fatalf("writing legacy fixture: %v", err)
	}

	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	got, err := store.Get("legacy")
	if err != nil {
		t.Fatalf("Get(legacy): %v", err)
	}
	if got.Engine != db.EngineMySQL {
		t.Errorf("Engine = %q, want %q", got.Engine, db.EngineMySQL)
	}
}

func TestSaveAndLoadRoundTripsEachEngine(t *testing.T) {
	engines := []db.Engine{db.EngineMySQL, db.EngineRedis, db.EngineMongoDB}

	for _, engine := range engines {
		t.Run(string(engine), func(t *testing.T) {
			store := db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain())
			profile := db.Profile{Name: "p", Host: "db.internal", Engine: engine}

			if err := store.Save(profile, ""); err != nil {
				t.Fatalf("Save: %v", err)
			}

			got, err := store.Get("p")
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if got.Engine != engine {
				t.Errorf("Engine = %q, want %q", got.Engine, engine)
			}
		})
	}
}

func TestSaveWithAnInvalidEngineFailsAndWritesNothing(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	profile := db.Profile{Name: "p", Host: "db.internal", Engine: db.Engine("postgres")}

	err := store.Save(profile, "")
	if !errors.Is(err, db.ErrUnknownEngine) {
		t.Fatalf("Save error = %v, want ErrUnknownEngine", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "profiles.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a rejected Save left a profiles.toml behind, want none written")
	}
}

func TestSaveWithUnsetEnginePersistsMySQLInTheFile(t *testing.T) {
	dir := t.TempDir()
	store := db.NewProfileStore(dir, dbtest.NewFakeKeychain())
	profile := db.Profile{Name: "p", Host: "db.internal"}

	if err := store.Save(profile, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "profiles.toml"))
	if err != nil {
		t.Fatalf("reading profiles.toml: %v", err)
	}
	if !strings.Contains(string(data), "engine = 'mysql'") {
		t.Errorf("profiles.toml = %s, want it to say engine = %q", data, db.EngineMySQL)
	}

	got, err := store.Get("p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Engine != db.EngineMySQL {
		t.Errorf("Engine = %q, want %q", got.Engine, db.EngineMySQL)
	}
}
