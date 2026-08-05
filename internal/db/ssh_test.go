package db_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/themethaithian/go-db/internal/db"
)

// These tests exercise the SSH adapter's refusals — the outcomes that are
// decided before, or instead of, a successful handshake. The handshake itself
// needs a bastion, and is covered by the integration test in this package.

// keyPair writes a throwaway ed25519 key pair into dir and returns the private
// key's path and its authorized_keys line.
func keyPair(t *testing.T, dir string) (keyFile, authorizedKey string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key pair: %v", err)
	}

	block, err := ssh.MarshalPrivateKey(private, "go-db test key")
	if err != nil {
		t.Fatalf("encoding the private key: %v", err)
	}
	keyFile = filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the private key: %v", err)
	}

	signer, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("encoding the public key: %v", err)
	}
	return keyFile, string(ssh.MarshalAuthorizedKey(signer))
}

// emptyKnownHosts returns the path of a known_hosts file that exists and trusts
// nothing.
func emptyKnownHosts(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("writing known_hosts: %v", err)
	}
	return path
}

// closedPort returns a loopback port nothing is listening on.
func closedPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

func TestTunnelRefusesToDialWithoutAKnownHostsFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "known_hosts")
	tunnels := db.NewSSHTunnels(missing)

	// The bastion is a port nothing answers on: if the host key policy were
	// settled after the dial, this would come back unreachable instead.
	_, err := tunnels.Open(context.Background(), db.SSHTunnel{
		Host: "127.0.0.1", Port: closedPort(t), User: "jump",
	})

	if !errors.Is(err, db.ErrSSHHostKey) {
		t.Fatalf("Open error = %v, want ErrSSHHostKey", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the known_hosts file it wanted", err)
	}
}

func TestTunnelWithNoCredentialsToOfferSaysSo(t *testing.T) {
	// No agent, and a home directory with no keys in it.
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", t.TempDir())

	tunnels := db.NewSSHTunnels(emptyKnownHosts(t))
	_, err := tunnels.Open(context.Background(), db.SSHTunnel{
		Host: "127.0.0.1", Port: closedPort(t), User: "jump",
	})

	if !errors.Is(err, db.ErrSSHAuthFailed) {
		t.Fatalf("Open error = %v, want ErrSSHAuthFailed", err)
	}
	if errors.Is(err, db.ErrAuthFailed) {
		t.Error("an SSH credential failure must not read as the database's: they are fixed in different places")
	}
}

func TestTunnelWithAnUnreadableKeyFileNamesIt(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "id_ed25519")

	tunnels := db.NewSSHTunnels(emptyKnownHosts(t))
	_, err := tunnels.Open(context.Background(), db.SSHTunnel{
		Host: "127.0.0.1", Port: closedPort(t), User: "jump", KeyFile: missing,
	})

	if !errors.Is(err, db.ErrSSHAuthFailed) {
		t.Fatalf("Open error = %v, want ErrSSHAuthFailed", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the key file it could not read", err)
	}
}

func TestTunnelWithAPassphraseProtectedKeyPointsAtTheAgent(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating a key pair: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "locked", []byte("hunter2"))
	if err != nil {
		t.Fatalf("encoding the private key: %v", err)
	}
	keyFile := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing the private key: %v", err)
	}

	tunnels := db.NewSSHTunnels(emptyKnownHosts(t))
	_, err = tunnels.Open(context.Background(), db.SSHTunnel{
		Host: "127.0.0.1", Port: closedPort(t), User: "jump", KeyFile: keyFile,
	})

	if !errors.Is(err, db.ErrSSHAuthFailed) {
		t.Fatalf("Open error = %v, want ErrSSHAuthFailed", err)
	}
	if !strings.Contains(err.Error(), "agent") {
		t.Errorf("error %q does not say where a locked key belongs: v1 asks for no SSH passwords", err)
	}
}

func TestUnreachableBastionIsUnreachableAndNamed(t *testing.T) {
	keyFile, _ := keyPair(t, t.TempDir())
	port := closedPort(t)

	tunnels := db.NewSSHTunnels(emptyKnownHosts(t))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	config := db.SSHTunnel{Host: "127.0.0.1", Port: port, User: "jump", KeyFile: keyFile}
	_, err := tunnels.Open(ctx, config)

	if !errors.Is(err, db.ErrBastionUnreachable) {
		t.Fatalf("Open error = %v, want ErrBastionUnreachable", err)
	}
	// A bastion nobody answers on is a database nobody can reach, so callers
	// that only know about ErrUnreachable still get the right answer.
	if !errors.Is(err, db.ErrUnreachable) {
		t.Error("ErrBastionUnreachable must also be an ErrUnreachable")
	}
	if !strings.Contains(err.Error(), config.Address()) {
		t.Errorf("error %q does not name the bastion it could not reach", err)
	}
}

func TestSSHTunnelAddressDefaultsToPort22(t *testing.T) {
	if got := (db.SSHTunnel{Host: "bastion.example"}).Address(); got != "bastion.example:22" {
		t.Errorf("Address() = %q, want bastion.example:22", got)
	}
	if got := (db.SSHTunnel{Host: "bastion.example", Port: 2222}).Address(); got != "bastion.example:2222" {
		t.Errorf("Address() = %q, want bastion.example:2222", got)
	}
}
