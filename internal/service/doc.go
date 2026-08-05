// Package service is the App Service facade: the single interface the Wails
// shell and the localhost API call into. Both are thin adapters — they
// translate transport (bound methods, HTTP handlers) and nothing more.
//
// The facade is the primary testing seam. Behaviour is specified by tests here,
// against real domain collaborators wired over temporary directories and fakes
// for the ports that reach outside the process (the Keychain, the database
// Driver, and the SSH tunnel dialler). Tests state external
// behaviour only, so the machinery behind the seam — file layout, TOML library,
// internal type names — can change without touching them.
//
// The facade itself holds no logic. It wires domain collaborators together and
// delegates; anything worth a decision belongs in internal/db, internal/guard,
// or a sibling domain package.
package service
