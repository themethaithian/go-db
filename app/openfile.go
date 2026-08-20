package app

import (
	"fmt"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// maxSQLFileBytes caps how large a .sql file OpenSQLFile will read into
// memory. The editor is a CodeMirror buffer backed by a Svelte $state
// string, not a dump loader — a multi-gigabyte mysqldump file opened here
// would hang the webview rendering it, not open a query tab. 2 MiB is
// generous for a hand-written or LLM-authored script and stops well short
// of that: a human reaching for something bigger wants an import tool, not
// this button.
const maxSQLFileBytes = 2 * 1024 * 1024

// OpenedFile is what OpenSQLFile hands back to the frontend: either the
// file's name and text, or an account of why there isn't one. Cancelled and
// Err are deliberately distinct fields rather than one error string — a
// human dismissing the OS file picker is an ordinary outcome the editor
// should act on silently, while Err names something that actually went
// wrong and is worth a message. Collapsing the two would force the editor
// to string-match "cancelled" out of an error to tell them apart.
type OpenedFile struct {
	Name      string `json:"name"`
	SQL       string `json:"sql"`
	Cancelled bool   `json:"cancelled"`
	Err       string `json:"err,omitempty"`
}

// OpenSQLFile shows the OS "open file" dialog filtered to *.sql and, if the
// human picks one, reads it into an OpenedFile ready to seed a new query
// tab (documents.svelte.ts's openDocument). It lives here in app/ rather
// than under internal/ because it is a fact about this OS window, not
// domain logic — dependencies point inward, and the Approval Gate, the
// classifier and everything else under internal/ has no business knowing a
// native file picker exists. What comes back is plain text for the editor
// to treat exactly like SQL typed by hand: it still meets Classify, the
// Approval Gate and the audit log on the same terms as anything else the
// human types, gaining no special trust for having arrived from a file.
func (a *App) OpenSQLFile() OpenedFile {
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Open SQL file",
		Filters: []runtime.FileFilter{
			{DisplayName: "SQL files (*.sql)", Pattern: "*.sql"},
		},
	})
	if err != nil {
		return OpenedFile{Err: "couldn't open the file picker"}
	}
	// The dialog reports a cancel as an empty path, not an error — a human
	// closing the picker without choosing anything asked for nothing to
	// happen, not to be told something failed.
	if path == "" {
		return OpenedFile{Cancelled: true}
	}

	info, err := os.Stat(path)
	if err != nil {
		return OpenedFile{Err: "couldn't read that file"}
	}
	if info.Size() > maxSQLFileBytes {
		return OpenedFile{Err: fmt.Sprintf("that file is over %d MB — too large for the editor", maxSQLFileBytes/(1024*1024))}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return OpenedFile{Err: "couldn't read that file"}
	}
	text := string(data)
	if !utf8.ValidString(text) {
		return OpenedFile{Err: "that file isn't text the editor can read"}
	}

	return OpenedFile{Name: filepath.Base(path), SQL: text}
}

// PickedPath is what PickKeyFile hands back to the frontend: either the path
// the human chose, or an account of why there isn't one. Cancelled and Err
// are kept distinct for the same reason OpenedFile keeps them distinct —
// dismissing the OS picker is an ordinary outcome the Profile form should
// treat as a no-op, while Err names something that actually went wrong.
type PickedPath struct {
	Path      string `json:"path"`
	Cancelled bool   `json:"cancelled"`
	Err       string `json:"err,omitempty"`
}

// PickKeyFile shows the OS "open file" dialog so a human setting up an SSH
// tunnel (ProfileForm's "Key file" field) can hand go-db an absolute path
// instead of typing one by hand — a field that has already burned them
// twice, since go-db reads the path exactly as written (no `~` expansion:
// internal/db treats an explicit path as a choice) and a hand-typed path is
// easy to get subtly wrong. It lives here in app/ for the same reason
// OpenSQLFile does: this is a fact about the OS window, not domain logic,
// and internal/ has no business knowing a native file picker exists.
//
// Deliberately no file filter. Unlike OpenSQLFile's *.sql filter, a private
// key has no reliable extension to filter on — the conventional names are
// extensionless (id_ed25519) or arbitrary (cmmn-gw-nonprd-ec2-rds-tunnel-
// appusr) — so a *.pem filter would hide exactly the files this button
// exists to find.
//
// This binding only answers "which file". It does not read or validate the
// key: whether it parses, is passphrase-protected, or is otherwise unusable
// is the tunnel's business at connect time (internal/db/ssh.go already
// reports each of those with a fix named), and duplicating that judgement
// here would just let the two answers drift apart.
func (a *App) PickKeyFile() PickedPath {
	// DefaultDirectory starts the dialog in the human's home directory,
	// where ~/.ssh conventionally lives — but only if we can find it; a
	// failure here isn't worth reporting, since the dialog opens with its
	// own platform default just as well.
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = ""
	}

	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose SSH key file",
		DefaultDirectory: homeDir,
	})
	if err != nil {
		return PickedPath{Err: "couldn't open the file picker"}
	}
	if path == "" {
		return PickedPath{Cancelled: true}
	}

	return PickedPath{Path: path}
}
