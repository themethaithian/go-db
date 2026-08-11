package db

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// ErrProfileNotFound reports that no Profile is saved under the given name.
var ErrProfileNotFound = errors.New("db: profile not found")

// ErrProfileNameRequired reports a Profile saved without a name. The name is
// the Profile's key, so it cannot be empty.
var ErrProfileNameRequired = errors.New("db: profile name is required")

// ErrUnknownEngine reports a Profile saved with an Engine that is neither
// unset nor one of the three go-db knows. An unset Engine is not this error —
// it is normalized to EngineMySQL before the Profile is written — but a typo
// or a future Engine this build does not understand must not silently reach
// disk, where every later reader would trust it as though it were real.
var ErrUnknownEngine = errors.New("db: unknown engine")

const (
	profilesFile = "profiles.toml"
	dirPerm      = 0o700
	filePerm     = 0o600
)

// ProfileStore persists Profiles and routes their passwords to the Keychain.
//
// Profiles live in a single TOML file inside the store's directory; passwords
// never appear there — they are handed to the Keychain port, keyed by Profile
// name. The file layout is private to this package: callers see only Profiles.
//
// A ProfileStore reads through to disk on every call, so a store constructed
// over a directory an earlier run wrote sees exactly that run's Profiles. It is
// safe for concurrent use.
type ProfileStore struct {
	dir      string
	keychain Keychain

	mu sync.Mutex
}

// NewProfileStore returns a store that keeps its Profiles in dir and its
// passwords in keychain. The directory is created on first write; it need not
// exist yet.
func NewProfileStore(dir string, keychain Keychain) *ProfileStore {
	return &ProfileStore{dir: dir, keychain: keychain}
}

// DefaultProfileDir returns the per-user configuration directory go-db stores
// Profiles in (~/Library/Application Support/go-db on macOS).
func DefaultProfileDir() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("db: locating user config dir: %w", err)
	}
	return filepath.Join(configDir, "go-db"), nil
}

// Save creates or replaces the Profile stored under p.Name.
//
// A non-empty password replaces the Profile's stored secret. An empty password
// leaves the keychain untouched: saving an existing Profile keeps its secret,
// and saving a new one simply leaves it without a secret.
func (s *ProfileStore) Save(p Profile, password string) error {
	if p.Name == "" {
		return ErrProfileNameRequired
	}
	if p.Engine == "" {
		p.Engine = EngineMySQL
	} else if !p.Engine.Valid() {
		return fmt.Errorf("%w: %q", ErrUnknownEngine, p.Engine)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	profiles, err := s.load()
	if err != nil {
		return err
	}

	if i := indexOf(profiles, p.Name); i >= 0 {
		profiles[i] = p
	} else {
		profiles = append(profiles, p)
	}

	if err := s.store(profiles); err != nil {
		return err
	}

	if password != "" {
		if err := s.keychain.Set(p.Name, password); err != nil {
			return fmt.Errorf("db: storing secret for profile %q: %w", p.Name, err)
		}
	}
	return nil
}

// Delete removes the Profile saved under name along with its keychain secret.
// It reports ErrProfileNotFound if no such Profile exists.
func (s *ProfileStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	profiles, err := s.load()
	if err != nil {
		return err
	}

	i := indexOf(profiles, name)
	if i < 0 {
		return fmt.Errorf("%w: %q", ErrProfileNotFound, name)
	}

	if err := s.store(append(profiles[:i:i], profiles[i+1:]...)); err != nil {
		return err
	}

	// A Profile may never have had a secret; that is not a failure to delete.
	if err := s.keychain.Delete(name); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return fmt.Errorf("db: deleting secret for profile %q: %w", name, err)
	}
	return nil
}

// Get returns the Profile saved under name, or ErrProfileNotFound.
func (s *ProfileStore) Get(name string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	profiles, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	if i := indexOf(profiles, name); i >= 0 {
		return profiles[i], nil
	}
	return Profile{}, fmt.Errorf("%w: %q", ErrProfileNotFound, name)
}

// Credentials returns the Profile saved under name together with the secret
// the Keychain holds for it, or ErrProfileNotFound.
//
// It exists so the Connection Registry can resolve a Profile name into
// everything needed to dial without a password passing through a caller that
// has no business seeing one. A Profile with no stored secret yields an empty
// password rather than an error: a passwordless database is legitimate.
func (s *ProfileStore) Credentials(name string) (Profile, string, error) {
	profile, err := s.Get(name)
	if err != nil {
		return Profile{}, "", err
	}

	secret, err := s.keychain.Get(name)
	if errors.Is(err, ErrSecretNotFound) {
		return profile, "", nil
	}
	if err != nil {
		return Profile{}, "", fmt.Errorf("db: reading secret for profile %q: %w", name, err)
	}
	return profile, secret, nil
}

// List returns every saved Profile, ordered by name.
func (s *ProfileStore) List() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.load()
}

// profileFile is the on-disk shape of the Profile list. It exists so the TOML
// document has a named array of tables rather than a bare top-level array.
type profileFile struct {
	Profiles []Profile `toml:"profile"`
}

// load reads the Profile list from disk, ordered by name. A store whose
// directory or file does not exist yet holds no Profiles.
//
// This is the one place an empty Engine is normalized to EngineMySQL: a
// profiles.toml written before Engine existed has no engine key at all, and
// every caller of this package must see a concrete Engine rather than
// re-deriving "empty means mysql" for itself.
func (s *ProfileStore) load() ([]Profile, error) {
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: reading profiles: %w", err)
	}

	var file profileFile
	if err := toml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("db: parsing profiles: %w", err)
	}
	for i := range file.Profiles {
		if file.Profiles[i].Engine == "" {
			file.Profiles[i].Engine = EngineMySQL
		}
	}
	sortByName(file.Profiles)
	return file.Profiles, nil
}

// store writes the Profile list to disk, ordered by name so the file is stable
// across runs. The write is atomic: a crash mid-write leaves the previous list
// intact rather than a truncated one.
func (s *ProfileStore) store(profiles []Profile) error {
	sortByName(profiles)

	data, err := toml.Marshal(profileFile{Profiles: profiles})
	if err != nil {
		return fmt.Errorf("db: encoding profiles: %w", err)
	}

	if err := os.MkdirAll(s.dir, dirPerm); err != nil {
		return fmt.Errorf("db: creating config dir: %w", err)
	}
	if err := os.Chmod(s.dir, dirPerm); err != nil {
		return fmt.Errorf("db: securing config dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.dir, ".profiles-*.toml")
	if err != nil {
		return fmt.Errorf("db: creating profiles file: %w", err)
	}
	defer os.Remove(tmp.Name()) // no-op once the rename below succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("db: writing profiles: %w", err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		tmp.Close()
		return fmt.Errorf("db: securing profiles file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("db: writing profiles: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return fmt.Errorf("db: replacing profiles file: %w", err)
	}
	return nil
}

func (s *ProfileStore) path() string {
	return filepath.Join(s.dir, profilesFile)
}

func sortByName(profiles []Profile) {
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
}

func indexOf(profiles []Profile, name string) int {
	for i, p := range profiles {
		if p.Name == name {
			return i
		}
	}
	return -1
}
