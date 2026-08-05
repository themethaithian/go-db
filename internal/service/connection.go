package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/themethaithian/go-db/internal/db"
)

// ConnectionStatus is the outcome of trying to reach a Profile's database. It
// exists so the UI can branch — and choose an icon — without parsing messages.
type ConnectionStatus string

const (
	// ConnectionOK reports that the database answered.
	ConnectionOK ConnectionStatus = "ok"
	// ConnectionAuthFailed reports that the server was reached but rejected
	// the Profile's user or password.
	ConnectionAuthFailed ConnectionStatus = "auth_failed"
	// ConnectionUnreachable reports that the server was never reached: the
	// host does not resolve, nothing is listening, or the dial timed out.
	ConnectionUnreachable ConnectionStatus = "unreachable"
	// ConnectionUnknownProfile reports that no Profile is saved under the
	// requested name.
	ConnectionUnknownProfile ConnectionStatus = "unknown_profile"
	// ConnectionFailed reports any other failure; its Message carries what
	// the database or driver said.
	ConnectionFailed ConnectionStatus = "failed"
)

// ConnectionTest is the readable result of TestConnection. Message is one line
// of prose meant to be rendered as-is — never a Go error string with wrapping
// noise, and never a password.
type ConnectionTest struct {
	Status  ConnectionStatus `json:"status"`
	Message string           `json:"message"`
}

// OK reports whether the database answered.
func (t ConnectionTest) OK() bool { return t.Status == ConnectionOK }

// TestConnection opens a throwaway connection for the named Profile, using its
// stored keychain secret, and reports what happened. It leaves no connection
// open: the Connection Registry is untouched either way.
//
// It returns no error — every outcome, including failure, is a result the UI
// renders directly.
func (s *AppService) TestConnection(ctx context.Context, profileName string) ConnectionTest {
	profile, err := s.profiles.Get(profileName)
	if err != nil {
		return unknownProfile(profileName)
	}

	if err := s.registry.Test(ctx, profileName); err != nil {
		return failedOutcome(profile, err)
	}
	return ConnectionTest{
		Status:  ConnectionOK,
		Message: fmt.Sprintf("Connected to %s as %s.", profile.Address(), profile.User),
	}
}

// Connect opens a connection for the named Profile and holds it in the
// Connection Registry, where it stays until Disconnect or Close. Connecting an
// already-connected Profile is a no-op.
//
// The returned error wraps db.ErrProfileNotFound, db.ErrAuthFailed, or
// db.ErrUnreachable where they apply; TestConnection is the way to get a
// message fit to show a human.
func (s *AppService) Connect(ctx context.Context, profileName string) error {
	return s.registry.Connect(ctx, profileName)
}

// Disconnect closes the connection held for the named Profile. It reports
// db.ErrNotConnected if the Profile was not connected.
func (s *AppService) Disconnect(profileName string) error {
	return s.registry.Disconnect(profileName)
}

// ConnectedProfiles returns the names of the Profiles the Connection Registry
// currently holds a connection for, ordered by name. Several are open at once
// by design — the editor on one Profile, the MCP server on another.
func (s *AppService) ConnectedProfiles() []string {
	return s.registry.Connected()
}

// Ping verifies the connection held for the named Profile is still usable. It
// reports db.ErrNotConnected if the Profile is not connected.
func (s *AppService) Ping(ctx context.Context, profileName string) error {
	conn, err := s.registry.Conn(profileName)
	if err != nil {
		return err
	}
	return conn.Ping(ctx)
}

func unknownProfile(profileName string) ConnectionTest {
	return ConnectionTest{
		Status:  ConnectionUnknownProfile,
		Message: fmt.Sprintf("There is no Profile named %q.", profileName),
	}
}

// failedOutcome turns a classified connect error into prose. The distinction
// that matters is the first two cases: a wrong password and an unreachable
// host look identical in a raw driver error, and call for entirely different
// fixes.
func failedOutcome(profile db.Profile, err error) ConnectionTest {
	switch {
	case errors.Is(err, db.ErrProfileNotFound):
		return unknownProfile(profile.Name)

	case errors.Is(err, db.ErrAuthFailed):
		return ConnectionTest{
			Status: ConnectionAuthFailed,
			Message: fmt.Sprintf("%s refused the credentials for %s: check the user name and password.",
				profile.Address(), profile.User),
		}

	case errors.Is(err, db.ErrUnreachable):
		return ConnectionTest{
			Status: ConnectionUnreachable,
			Message: fmt.Sprintf("Could not reach %s: check the host, port, and network.",
				profile.Address()),
		}
	}

	return ConnectionTest{Status: ConnectionFailed, Message: oneLine(err)}
}

// oneLine renders an unclassified error as a single line of prose. The
// database's own wording is usually the most useful thing to show, so it is
// kept — stripped of the package prefix and of any line breaks that would
// leak Go error formatting into the UI.
func oneLine(err error) string {
	message := strings.Join(strings.Fields(strings.TrimPrefix(err.Error(), "db: ")), " ")
	if message == "" {
		return "The connection failed for an unknown reason."
	}
	return message
}
