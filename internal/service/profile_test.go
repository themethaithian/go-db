package service_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// newFacade builds an App Service over a ProfileStore rooted at dir, backed by
// the given fake Keychain. Calling it twice with the same dir and Keychain
// simulates an app restart: nothing survives except what reached disk or the
// keychain.
func newFacade(t *testing.T, dir string, keychain db.Keychain) *service.AppService {
	t.Helper()
	return service.New(db.NewProfileStore(dir, keychain), guard.NewJSONLAuditLog(dir))
}

// configDirBytes returns every byte stored anywhere under dir, concatenated.
func configDirBytes(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		sb.Write(content)
		return nil
	})
	if err != nil {
		t.Fatalf("walking config dir: %v", err)
	}
	return sb.String()
}

func mustSave(t *testing.T, svc *service.AppService, profile db.Profile, password string) {
	t.Helper()
	if err := svc.SaveProfile(profile, password); err != nil {
		t.Fatalf("SaveProfile(%q): %v", profile.Name, err)
	}
}

func profileNames(profiles []db.Profile) []string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = p.Name
	}
	return names
}

func TestProfilesSurviveRestart(t *testing.T) {
	saved := []db.Profile{
		{
			Name:     "local",
			Host:     "127.0.0.1",
			Port:     3306,
			User:     "root",
			Database: "app_dev",
		},
		{
			Name:     "staging",
			Host:     "db.staging.internal",
			Port:     3307,
			User:     "reader",
			Database: "app_staging",
			SSH: &db.SSHTunnel{
				Host: "bastion.staging.internal",
				Port: 22,
				User: "deploy",
				// The key file is a path, not a key: what survives a save is
				// where to find the identity, never the identity itself.
				KeyFile: "/Users/dev/.ssh/id_ed25519_staging",
			},
		},
	}

	dir := t.TempDir()
	keychain := dbtest.NewFakeKeychain()

	before := newFacade(t, dir, keychain)
	for _, profile := range saved {
		mustSave(t, before, profile, "s3cret-"+profile.Name)
	}

	after := newFacade(t, dir, keychain)

	listed, err := after.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(listed) != len(saved) {
		t.Fatalf("ListProfiles returned %d profiles, want %d", len(listed), len(saved))
	}

	for _, want := range saved {
		t.Run(want.Name, func(t *testing.T) {
			got, err := after.GetProfile(want.Name)
			if err != nil {
				t.Fatalf("GetProfile(%q): %v", want.Name, err)
			}
			assertProfileEqual(t, got, want)

			secret, err := keychain.Get(want.Name)
			if err != nil {
				t.Fatalf("keychain.Get(%q): %v", want.Name, err)
			}
			if secret != "s3cret-"+want.Name {
				t.Errorf("keychain secret = %q, want %q", secret, "s3cret-"+want.Name)
			}
		})
	}
}

func assertProfileEqual(t *testing.T, got, want db.Profile) {
	t.Helper()
	if got.Name != want.Name || got.Host != want.Host || got.Port != want.Port ||
		got.User != want.User || got.Database != want.Database {
		t.Errorf("profile = %+v, want %+v", got, want)
	}
	switch {
	case want.SSH == nil && got.SSH != nil:
		t.Errorf("profile SSH = %+v, want nil", got.SSH)
	case want.SSH != nil && got.SSH == nil:
		t.Errorf("profile SSH = nil, want %+v", want.SSH)
	case want.SSH != nil && *got.SSH != *want.SSH:
		t.Errorf("profile SSH = %+v, want %+v", *got.SSH, *want.SSH)
	}
}

func TestSaveProfileOverwritesExisting(t *testing.T) {
	original := db.Profile{
		Name:     "prod",
		Host:     "db.old.internal",
		Port:     3306,
		User:     "root",
		Database: "app",
	}
	updated := db.Profile{
		Name:     "prod",
		Host:     "db.new.internal",
		Port:     3307,
		User:     "app_rw",
		Database: "app_v2",
		SSH: &db.SSHTunnel{
			Host: "bastion.prod.internal",
			Port: 2222,
			User: "deploy",
		},
	}

	cases := []struct {
		name           string
		savePassword   string
		wantSecret     string
		wantSecretKept bool
	}{
		{
			name:         "empty password keeps the stored secret",
			savePassword: "",
			wantSecret:   "original-secret",
		},
		{
			name:         "non-empty password replaces the stored secret",
			savePassword: "rotated-secret",
			wantSecret:   "rotated-secret",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			keychain := dbtest.NewFakeKeychain()
			svc := newFacade(t, dir, keychain)

			mustSave(t, svc, original, "original-secret")
			mustSave(t, svc, updated, tc.savePassword)

			listed, err := svc.ListProfiles()
			if err != nil {
				t.Fatalf("ListProfiles: %v", err)
			}
			if len(listed) != 1 {
				t.Fatalf("ListProfiles returned %d profiles, want 1", len(listed))
			}

			got, err := svc.GetProfile("prod")
			if err != nil {
				t.Fatalf("GetProfile: %v", err)
			}
			assertProfileEqual(t, got, updated)

			secret, err := keychain.Get("prod")
			if err != nil {
				t.Fatalf("keychain.Get: %v", err)
			}
			if secret != tc.wantSecret {
				t.Errorf("keychain secret = %q, want %q", secret, tc.wantSecret)
			}
		})
	}
}

func TestDeleteProfile(t *testing.T) {
	dir := t.TempDir()
	keychain := dbtest.NewFakeKeychain()
	svc := newFacade(t, dir, keychain)

	mustSave(t, svc, db.Profile{Name: "keep", Host: "keep.internal", Port: 3306, User: "root"}, "keep-secret")
	mustSave(t, svc, db.Profile{Name: "doomed", Host: "doomed.internal", Port: 3306, User: "root"}, "doomed-secret")

	if err := svc.DeleteProfile("doomed"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	listed, err := svc.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if names := profileNames(listed); len(names) != 1 || names[0] != "keep" {
		t.Errorf("ListProfiles = %v, want [keep]", names)
	}

	if _, err := svc.GetProfile("doomed"); !errors.Is(err, db.ErrProfileNotFound) {
		t.Errorf("GetProfile after delete error = %v, want ErrProfileNotFound", err)
	}

	if stored := configDirBytes(t, dir); strings.Contains(stored, "doomed") {
		t.Errorf("config dir still mentions the deleted profile:\n%s", stored)
	}

	if _, err := keychain.Get("doomed"); !errors.Is(err, db.ErrSecretNotFound) {
		t.Errorf("keychain.Get after delete error = %v, want ErrSecretNotFound", err)
	}
	if _, err := keychain.Get("keep"); err != nil {
		t.Errorf("keychain.Get(keep) after unrelated delete: %v", err)
	}
}

func TestMissingProfile(t *testing.T) {
	cases := []struct {
		name string
		call func(svc *service.AppService) error
	}{
		{
			name: "GetProfile on an unknown name",
			call: func(svc *service.AppService) error {
				_, err := svc.GetProfile("nope")
				return err
			},
		},
		{
			name: "DeleteProfile on an unknown name",
			call: func(svc *service.AppService) error {
				return svc.DeleteProfile("nope")
			},
		},
		{
			name: "GetProfile on an empty store",
			call: func(svc *service.AppService) error {
				_, err := svc.GetProfile("")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFacade(t, t.TempDir(), dbtest.NewFakeKeychain())
			mustSave(t, svc, db.Profile{Name: "known", Host: "known.internal", Port: 3306, User: "root"}, "secret")

			if err := tc.call(svc); !errors.Is(err, db.ErrProfileNotFound) {
				t.Errorf("error = %v, want ErrProfileNotFound", err)
			}
		})
	}
}

func TestPasswordsNeverTouchDisk(t *testing.T) {
	dir := t.TempDir()
	keychain := dbtest.NewFakeKeychain()
	svc := newFacade(t, dir, keychain)

	passwords := []string{"hunter2-plaintext", "correct horse battery staple", "p@ss:w0rd\"#[]="}
	for i, password := range passwords {
		mustSave(t, svc, db.Profile{
			Name:     "profile-" + string(rune('a'+i)),
			Host:     "db.internal",
			Port:     3306,
			User:     "root",
			Database: "app",
		}, password)
	}

	// Overwrite one profile and delete another; neither may leave a trace.
	mustSave(t, svc, db.Profile{Name: "profile-a", Host: "db.internal", Port: 3306, User: "root"}, "rotated-plaintext")
	if err := svc.DeleteProfile("profile-b"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}

	stored := configDirBytes(t, dir)
	for _, password := range append(passwords, "rotated-plaintext") {
		if strings.Contains(stored, password) {
			t.Errorf("password %q found on disk under %s", password, dir)
		}
	}
}

func TestConfigDirAndFilePermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-db")
	svc := newFacade(t, dir, dbtest.NewFakeKeychain())
	mustSave(t, svc, db.Profile{Name: "local", Host: "127.0.0.1", Port: 3306, User: "root"}, "secret")

	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat config dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("config dir mode = %o, want 700", perm)
	}

	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", path, perm)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking config dir: %v", err)
	}
}

func TestListProfilesOrderIsStable(t *testing.T) {
	dir := t.TempDir()
	keychain := dbtest.NewFakeKeychain()
	svc := newFacade(t, dir, keychain)

	for _, name := range []string{"zeta", "alpha", "Mid", "beta"} {
		mustSave(t, svc, db.Profile{Name: name, Host: name + ".internal", Port: 3306, User: "root"}, "secret")
	}

	want := []string{"Mid", "alpha", "beta", "zeta"}

	listed, err := svc.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Errorf("ListProfiles = %v, want %v", got, want)
	}

	// The same order must come back after a restart, and after a delete.
	restarted := newFacade(t, dir, keychain)
	listed, err = restarted.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles after restart: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, want) {
		t.Errorf("ListProfiles after restart = %v, want %v", got, want)
	}

	if err := restarted.DeleteProfile("beta"); err != nil {
		t.Fatalf("DeleteProfile: %v", err)
	}
	listed, err = restarted.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles after delete: %v", err)
	}
	if got := profileNames(listed); !equalStrings(got, []string{"Mid", "alpha", "zeta"}) {
		t.Errorf("ListProfiles after delete = %v, want [Mid alpha zeta]", got)
	}
}

func TestListProfilesOnEmptyStore(t *testing.T) {
	svc := newFacade(t, filepath.Join(t.TempDir(), "never-created"), dbtest.NewFakeKeychain())

	listed, err := svc.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("ListProfiles = %v, want empty", profileNames(listed))
	}
}

func TestSaveProfileRejectsEmptyName(t *testing.T) {
	svc := newFacade(t, t.TempDir(), dbtest.NewFakeKeychain())

	if err := svc.SaveProfile(db.Profile{Host: "db.internal", Port: 3306}, "secret"); err == nil {
		t.Fatal("SaveProfile with an empty Name succeeded, want error")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
