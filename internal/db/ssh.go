package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const (
	defaultSSHPort = 22

	// sshDialTimeout bounds reaching the bastion and finishing its handshake,
	// so an unreachable jump host fails as fast as an unreachable database.
	sshDialTimeout = 10 * time.Second
)

// defaultKeyFiles are the private keys tried when a Profile names none and the
// agent has nothing, in the order ssh itself would prefer them.
var defaultKeyFiles = []string{"id_ed25519", "id_rsa"}

// NewSSHTunnels returns the TunnelDialer that reaches bastions over SSH.
//
// knownHostsPath is the file host keys are verified against; empty means
// ~/.ssh/known_hosts, the one ssh itself keeps, so a bastion this machine has
// already connected to is already trusted here. Verification is not optional
// and there is no way to turn it off: a tunnel exists to protect traffic that
// would otherwise cross an untrusted network, and accepting any host key would
// hand that traffic to whoever answered.
func NewSSHTunnels(knownHostsPath string) TunnelDialer {
	return sshTunnels{knownHostsPath: knownHostsPath}
}

type sshTunnels struct {
	knownHostsPath string
}

// Open dials the bastion, authenticates, and returns the live session.
//
// The order is deliberate: everything that can be decided without the network —
// the host key policy and the credentials to offer — is settled first, so a
// missing known_hosts file or an unreadable key is reported as itself rather
// than as a connection that failed for some reason.
func (t sshTunnels) Open(ctx context.Context, config SSHTunnel) (Tunnel, error) {
	address := config.Address()

	verifyHostKey, err := t.hostKeyCallback()
	if err != nil {
		return nil, err
	}

	auth, closeAuth, err := authMethods(config)
	if err != nil {
		return nil, err
	}
	// The agent connection is only needed while we authenticate; the session
	// that comes out of the handshake does not hold it.
	defer closeAuth()

	dialer := net.Dialer{Timeout: sshDialTimeout}
	tcp, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBastionUnreachable, address, err)
	}

	// The handshake is not context-aware, so it gets a deadline on the socket
	// instead: a bastion that accepts a connection and then says nothing —
	// a load balancer in front of a dead host, a port a firewall swallowed —
	// must not hang the app.
	tcp.SetDeadline(handshakeDeadline(ctx)) //nolint:errcheck // a socket that refuses a deadline still has the client's own timeout below it

	client, err := sshClient(tcp, address, &ssh.ClientConfig{
		User:            config.User,
		Auth:            auth,
		HostKeyCallback: verifyHostKey,
		Timeout:         sshDialTimeout,
	})
	if err != nil {
		tcp.Close()
		return nil, sshError(err, config)
	}
	tcp.SetDeadline(time.Time{}) //nolint:errcheck // clearing the handshake deadline; the session sets its own timeouts

	return &sshTunnel{client: client, bastion: address}, nil
}

// sshClient completes the handshake on an established socket and returns the
// session multiplexed over it.
func sshClient(conn net.Conn, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	session, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		return nil, err
	}
	return ssh.NewClient(session, channels, requests), nil
}

// handshakeDeadline is the caller's deadline, bounded by our own so that a
// context with no deadline still fails fast.
func handshakeDeadline(ctx context.Context) time.Time {
	ours := time.Now().Add(sshDialTimeout)
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(ours) {
		return ours
	}
	return deadline
}

// hostKeyCallback builds the policy every bastion is checked against. A
// known_hosts file that is not there is a refusal with a fix in it, not a
// reason to trust anything.
func (t sshTunnels) hostKeyCallback() (ssh.HostKeyCallback, error) {
	path := t.knownHostsPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("%w: locating ~/.ssh/known_hosts: %v", ErrSSHHostKey, err)
		}
		path = filepath.Join(home, ".ssh", "known_hosts")
	}

	callback, err := knownhosts.New(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: there is no known_hosts file at %s — connect to the bastion once with ssh first", ErrSSHHostKey, path)
		}
		return nil, fmt.Errorf("%w: reading %s: %v", ErrSSHHostKey, path, err)
	}
	return callback, nil
}

// authMethods returns what we will offer the bastion, and a function that
// releases whatever holding them needs.
//
// A Profile that names a key file means that key and nothing else — being
// explicit is a choice, and silently falling back to another identity would
// make a mis-typed path look like a refused key. Otherwise the running agent is
// asked first, as ssh does, and the default keys are offered after it.
func authMethods(config SSHTunnel) ([]ssh.AuthMethod, func(), error) {
	if config.KeyFile != "" {
		method, err := keyFileAuth(config.KeyFile)
		if err != nil {
			return nil, func() {}, err
		}
		return []ssh.AuthMethod{method}, func() {}, nil
	}

	var methods []ssh.AuthMethod
	agentConn, agentMethod := agentAuth()
	if agentMethod != nil {
		methods = append(methods, agentMethod)
	}

	home, err := os.UserHomeDir()
	if err == nil {
		for _, name := range defaultKeyFiles {
			method, err := keyFileAuth(filepath.Join(home, ".ssh", name))
			if err != nil {
				continue // a default key that is absent or locked is not an error; it is just not offered
			}
			methods = append(methods, method)
		}
	}

	closeAgent := func() {
		if agentConn != nil {
			agentConn.Close()
		}
	}
	if len(methods) == 0 {
		closeAgent()
		return nil, func() {}, fmt.Errorf("%w: no SSH key to offer %s — start an agent with your key, or set the Profile's key file",
			ErrSSHAuthFailed, config.Address())
	}
	return methods, closeAgent, nil
}

// keyFileAuth reads one private key. A passphrase-protected key is refused with
// the fix named: v1 asks for no SSH passwords, and an agent already holds the
// unlocked key for exactly this.
func keyFileAuth(path string) (ssh.AuthMethod, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading the key file %s: %v", ErrSSHAuthFailed, path, err)
	}

	signer, err := ssh.ParsePrivateKey(pem)
	var needsPassphrase *ssh.PassphraseMissingError
	if errors.As(err, &needsPassphrase) {
		return nil, fmt.Errorf("%w: the key file %s is passphrase-protected — add it to your SSH agent instead", ErrSSHAuthFailed, path)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: reading the key file %s: %v", ErrSSHAuthFailed, path, err)
	}
	return ssh.PublicKeys(signer), nil
}

// agentAuth connects to the running SSH agent, if there is one. No agent is not
// a failure — most machines have one, and the ones that do not fall back to
// key files.
func agentAuth() (io.Closer, ssh.AuthMethod) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, nil
	}

	conn, err := net.Dial("unix", socket)
	if err != nil {
		return nil, nil
	}
	// Signers is read at authentication time, so a key added to the agent
	// between now and the handshake is still offered.
	return conn, ssh.PublicKeysCallback(agent.NewClient(conn).Signers)
}

// sshError turns a handshake failure into one of this package's outcomes. The
// distinction that matters is the same one the database has — who refused, and
// what the human has to change — except that here there is a third answer: we
// refused them.
func sshError(err error, config SSHTunnel) error {
	var keyErr *knownhosts.KeyError
	if errors.As(err, &keyErr) {
		if len(keyErr.Want) == 0 {
			return fmt.Errorf("%w: %s is not in known_hosts — connect to it once with ssh first", ErrSSHHostKey, config.Address())
		}
		return fmt.Errorf("%w: %s presented a different host key than the one in known_hosts", ErrSSHHostKey, config.Address())
	}
	var revoked *knownhosts.RevokedError
	if errors.As(err, &revoked) {
		return fmt.Errorf("%w: the host key of %s has been revoked", ErrSSHHostKey, config.Address())
	}

	// x/crypto/ssh reports a refused authentication as a message rather than a
	// type; these are the two it uses, and matching them is what keeps SSH's
	// wording out of the UI.
	message := err.Error()
	if strings.Contains(message, "unable to authenticate") || strings.Contains(message, "no supported methods remain") {
		return fmt.Errorf("%w: %s refused the SSH key for %s", ErrSSHAuthFailed, config.Address(), config.User)
	}

	if unreachable(err) {
		return fmt.Errorf("%w: %s: %v", ErrBastionUnreachable, config.Address(), err)
	}
	return fmt.Errorf("db: connecting to the bastion %s: %w", config.Address(), err)
}

// sshTunnel is one live SSH session. The client is private: callers get only
// Dial and Close, so nothing can open a shell or forward a port through it.
type sshTunnel struct {
	client  *ssh.Client
	bastion string
}

// Dial opens a connection to address from the bastion. The name is resolved
// there — a database host that means nothing on this machine is the ordinary
// case for a tunnelled Profile — so a failure names both ends: what we asked
// for, and who could not reach it.
func (t *sshTunnel) Dial(ctx context.Context, address string) (net.Conn, error) {
	conn, err := t.client.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("%w: %s is not reachable from the bastion %s: %v", ErrUnreachable, address, t.bastion, err)
	}
	return conn, nil
}

// Close ends the session, which ends every connection carried on it.
func (t *sshTunnel) Close() error {
	if err := t.client.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("db: closing the tunnel to %s: %w", t.bastion, err)
	}
	return nil
}

var (
	_ TunnelDialer = sshTunnels{}
	_ Tunnel       = (*sshTunnel)(nil)
)
