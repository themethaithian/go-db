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
	// host does not resolve, nothing is listening, or the dial timed out. A
	// Profile with a tunnel lands here too when its bastion is down or its
	// database is not reachable from the bastion — nothing answered, and the
	// Message says which hop.
	ConnectionUnreachable ConnectionStatus = "unreachable"
	// ConnectionSSHAuthFailed reports that the Profile's SSH bastion refused
	// our credentials. It is its own status rather than an authentication
	// failure because the thing to fix is an SSH key, not a database password.
	ConnectionSSHAuthFailed ConnectionStatus = "ssh_auth_failed"
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
	if profile.SSH != nil {
		return ConnectionTest{
			Status: ConnectionOK,
			Message: fmt.Sprintf("Connected to %s as %s through the SSH bastion %s.",
				profile.Address(), profile.User, profile.SSH.Address()),
		}
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
	conn, err := s.registry.Conn(ctx, profileName, "")
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
// that matters is who refused and what the human has to change: a wrong
// password and an unreachable host look identical in a raw driver error, and a
// Profile with a tunnel doubles every question — which of its two hosts is the
// one to go and look at.
//
// The tunnel cases are ordered before the plain ones because they are narrower:
// a bastion that was never reached is an unreachable database too, and saying
// so about the database would send the human to the wrong machine.
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

	case errors.Is(err, db.ErrSSHAuthFailed):
		return ConnectionTest{
			Status: ConnectionSSHAuthFailed,
			Message: fmt.Sprintf("The SSH bastion %s refused the key for %s: add the key to your SSH agent, or set the Profile's key file.",
				bastion(profile), bastionUser(profile)),
		}

	case errors.Is(err, db.ErrSSHHostKey):
		return ConnectionTest{
			Status: ConnectionUnreachable,
			Message: fmt.Sprintf("The SSH bastion %s presented a host key that is not in known_hosts: connect to it once with ssh, and check why its key changed if it has.",
				bastion(profile)),
		}

	case errors.Is(err, db.ErrBastionUnreachable):
		return ConnectionTest{
			Status: ConnectionUnreachable,
			Message: fmt.Sprintf("Could not reach the SSH bastion %s: check the bastion host, port, and network.",
				bastion(profile)),
		}

	case errors.Is(err, db.ErrUnreachable) && profile.SSH != nil:
		return ConnectionTest{
			Status: ConnectionUnreachable,
			Message: fmt.Sprintf("Could not reach %s from the SSH bastion %s: check the host and port as the bastion sees them.",
				profile.Address(), bastion(profile)),
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

// bastion names the Profile's jump host for a message. A tunnel error on a
// Profile with no tunnel cannot happen, but a message is a bad place to panic.
func bastion(profile db.Profile) string {
	if profile.SSH == nil {
		return "the SSH bastion"
	}
	return profile.SSH.Address()
}

func bastionUser(profile db.Profile) string {
	if profile.SSH == nil {
		return "its user"
	}
	return profile.SSH.User
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
