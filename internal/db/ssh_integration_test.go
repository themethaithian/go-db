package db_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
)

// This is the acceptance test for tunnelled Profiles: a MySQL server that is
// only reachable from a bastion, and a Profile that reaches it anyway.
//
// The world is two containers on a private Docker network — MySQL with no
// published port, and an sshd with one — so "the database is unreachable from
// here" is a fact of the setup rather than something the test asserts about
// itself. The control case connects the same Profile without its tunnel and
// must fail; without it, a test that quietly bypassed the bastion would still
// pass.
//
// It skips — never fails — when Docker is not available, like the integration
// tests in internal/service.

const (
	bastionMySQLImage = "mysql:8"

	// The bastion image is built here rather than pulled. The published SSH
	// images are no help: linuxserver/openssh-server ships an sshd_config with
	// AllowTcpForwarding off — the one setting this test is about — and
	// installs its sshd at container start anyway. Installing openssh ourselves
	// at start costs a minute of apk on every run, so it happens in a build
	// instead, which Docker caches after the first one.
	bastionImage = "go-db-test-sshd:1"

	bastionUser     = "tunnel"
	bastionRootPW   = "tunnel-integration-pw"
	bastionDatabase = "godb_tunnelled"

	bastionDockerTimeout = 60 * time.Second
	bastionReadyTimeout  = 120 * time.Second

	// The first build installs openssh over the network; later runs hit
	// Docker's cache and take no time at all.
	bastionBuildTimeout = 180 * time.Second
)

// bastionDockerfile is an Alpine with openssh installed, its host keys made,
// and forwarding turned on.
//
// The sed is load-bearing: Alpine's packaged sshd_config carries
// AllowTcpForwarding no, and OpenSSH takes the first occurrence of a keyword,
// so appending the opposite at the end of the file does nothing at all — which
// is a silent "administratively prohibited" at the first channel we open.
const bastionDockerfile = `FROM alpine:3
RUN apk add --no-cache openssh-server && \
    ssh-keygen -A && \
    sed -i "s/^AllowTcpForwarding no/AllowTcpForwarding yes/" /etc/ssh/sshd_config
`

// sshdScript creates the account we log in as and starts the server.
//
// The account gets a password even though password authentication is off:
// OpenSSH refuses to log in a locked account, and adduser leaves one locked.
const sshdScript = `
adduser -D -s /bin/sh ` + bastionUser + ` &&
echo "` + bastionUser + `:` + bastionUser + `" | chpasswd &&
mkdir -p /home/` + bastionUser + `/.ssh &&
printf "%s\n" "$PUBLIC_KEY" > /home/` + bastionUser + `/.ssh/authorized_keys &&
chown -R ` + bastionUser + `:` + bastionUser + ` /home/` + bastionUser + `/.ssh &&
chmod 700 /home/` + bastionUser + `/.ssh &&
chmod 600 /home/` + bastionUser + `/.ssh/authorized_keys &&
exec /usr/sbin/sshd -D -e -o AllowTcpForwarding=yes -o PasswordAuthentication=no
`

// bastionWorld is the running setup: a database only the bastion can reach, and
// the bastion.
type bastionWorld struct {
	// databaseAddress is the MySQL server as seen from the bastion. It does not
	// resolve on this machine, which is the point.
	databaseAddress string
	tunnel          db.SSHTunnel // host, port and key of the bastion, from here
	knownHosts      string       // a known_hosts file trusting exactly this bastion
}

// profile returns a Profile for the tunnelled database, with or without its
// tunnel.
func (w bastionWorld) profile(name string, tunnelled bool) db.Profile {
	host, port := splitAddress(w.databaseAddress)
	profile := db.Profile{Name: name, Host: host, Port: port, User: "root", Database: bastionDatabase}
	if tunnelled {
		config := w.tunnel
		profile.SSH = &config
	}
	return profile
}

func TestIntegrationTunnelledProfile(t *testing.T) {
	world := startBastionWorld(t)

	t.Run("the connection test reaches the database through the bastion", func(t *testing.T) {
		registry, _ := world.registry(t, world.knownHosts, world.profile("tunnelled", true))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := registry.Test(ctx, "tunnelled"); err != nil {
			t.Fatalf("Test through the bastion: %v", err)
		}
		if got := registry.Connected(); len(got) != 0 {
			t.Errorf("a connection test left %v connected, want nothing", got)
		}
	})

	t.Run("a read round-trips through the bastion", func(t *testing.T) {
		registry, _ := world.registry(t, world.knownHosts, world.profile("tunnelled", true))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := registry.Connect(ctx, "tunnelled"); err != nil {
			t.Fatalf("Connect through the bastion: %v", err)
		}
		conn, err := registry.Conn("tunnelled")
		if err != nil {
			t.Fatalf("Conn: %v", err)
		}

		result, err := conn.ReadQuery(ctx, "SELECT DATABASE() AS db")
		if err != nil {
			t.Fatalf("ReadQuery through the bastion: %v", err)
		}
		if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != bastionDatabase {
			t.Fatalf("SELECT DATABASE() = %+v, want %q", result.Rows, bastionDatabase)
		}
	})

	t.Run("disconnecting closes the tunnel", func(t *testing.T) {
		registry, tunnels := world.registry(t, world.knownHosts, world.profile("tunnelled", true))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := registry.Connect(ctx, "tunnelled"); err != nil {
			t.Fatalf("Connect through the bastion: %v", err)
		}
		tunnel := tunnels.last(t)
		// The tunnel carries traffic while the connection is held.
		carried, err := tunnel.Dial(ctx, world.databaseAddress)
		if err != nil {
			t.Fatalf("dialling the database through an open tunnel: %v", err)
		}
		carried.Close()

		if err := registry.Disconnect("tunnelled"); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}

		// And carries nothing afterwards: the SSH session is gone, not just
		// forgotten.
		if _, err := tunnel.Dial(ctx, world.databaseAddress); err == nil {
			t.Error("the tunnel still carries traffic after Disconnect, want a closed SSH session")
		}
	})

	t.Run("the same profile without its tunnel cannot reach the database", func(t *testing.T) {
		registry, _ := world.registry(t, world.knownHosts, world.profile("direct", false))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := registry.Test(ctx, "direct")
		if !errors.Is(err, db.ErrUnreachable) {
			t.Fatalf("Test without the tunnel = %v, want ErrUnreachable: if this succeeds, the tunnelled cases prove nothing", err)
		}
	})

	t.Run("a bastion missing from known_hosts is refused", func(t *testing.T) {
		registry, _ := world.registry(t, emptyKnownHosts(t), world.profile("tunnelled", true))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := registry.Test(ctx, "tunnelled")
		if !errors.Is(err, db.ErrSSHHostKey) {
			t.Fatalf("Test against an untrusted bastion = %v, want ErrSSHHostKey", err)
		}
	})

	t.Run("a bastion that refuses our key says so", func(t *testing.T) {
		stranger, _ := keyPair(t, t.TempDir())
		profile := world.profile("tunnelled", true)
		profile.SSH.KeyFile = stranger

		registry, _ := world.registry(t, world.knownHosts, profile)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		err := registry.Test(ctx, "tunnelled")
		if !errors.Is(err, db.ErrSSHAuthFailed) {
			t.Fatalf("Test with a key the bastion does not know = %v, want ErrSSHAuthFailed", err)
		}
	})
}

// registry builds a Connection Registry over the real MySQL driver and the real
// SSH dialler, verifying host keys against knownHosts, with the given Profiles
// saved.
func (w bastionWorld) registry(t *testing.T, knownHosts string, profiles ...db.Profile) (*db.Registry, *recordingTunnels) {
	t.Helper()

	store := db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain())
	for _, profile := range profiles {
		if err := store.Save(profile, bastionRootPW); err != nil {
			t.Fatalf("saving profile %q: %v", profile.Name, err)
		}
	}

	tunnels := &recordingTunnels{TunnelDialer: db.NewSSHTunnels(knownHosts)}
	registry := db.NewRegistryWithTunnels(db.NewMySQLDriver(), tunnels, store)
	t.Cleanup(func() {
		if err := registry.Close(); err != nil {
			t.Errorf("closing the Connection Registry: %v", err)
		}
	})
	return registry, tunnels
}

// recordingTunnels keeps the tunnels the Registry opened, so a test can ask a
// tunnel the Registry has since closed to carry something — the only way to see
// from out here that the SSH session really ended.
type recordingTunnels struct {
	db.TunnelDialer

	mu     sync.Mutex
	opened []db.Tunnel
}

func (r *recordingTunnels) Open(ctx context.Context, config db.SSHTunnel) (db.Tunnel, error) {
	tunnel, err := r.TunnelDialer.Open(ctx, config)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.opened = append(r.opened, tunnel)
	return tunnel, nil
}

func (r *recordingTunnels) last(t *testing.T) db.Tunnel {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.opened) == 0 {
		t.Fatal("no tunnel was opened")
	}
	return r.opened[len(r.opened)-1]
}

// startBastionWorld brings up the network, the database, and the bastion, and
// tears them all down when the test ends. It skips the test when Docker is
// missing and fails it when Docker is there but the world would not come up.
func startBastionWorld(t *testing.T) bastionWorld {
	t.Helper()

	if out, err := docker(t, "info", "--format", "{{.ServerVersion}}"); err != nil {
		t.Skipf("skipping integration test: Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
	}

	names := fmt.Sprintf("godb-tunnel-%d", time.Now().UnixNano())
	network, database, sshd := names+"-net", names+"-db", names+"-ssh"

	if out, err := docker(t, "network", "create", network); err != nil {
		t.Fatalf("creating the Docker network: %v: %s", err, condense(out))
	}
	t.Cleanup(func() { docker(t, "network", "rm", network) }) //nolint:errcheck // teardown

	// The database publishes no port: from this machine it does not exist. Only
	// containers on the network can reach it, and the bastion is one of them.
	if out, err := docker(t, "run", "--detach", "--rm",
		"--name", database, "--network", network,
		"--env", "MYSQL_ROOT_PASSWORD="+bastionRootPW,
		"--env", "MYSQL_DATABASE="+bastionDatabase,
		"--tmpfs", "/var/lib/mysql:rw,size=512m",
		bastionMySQLImage,
		"--skip-log-bin", "--skip-performance-schema",
	); err != nil {
		t.Fatalf("starting %s: %v: %s", bastionMySQLImage, err, condense(out))
	}
	t.Cleanup(func() { docker(t, "stop", "--time", "0", database) }) //nolint:errcheck // teardown

	keyDir := t.TempDir()
	keyFile, authorizedKey := keyPair(t, keyDir)
	buildBastionImage(t)

	if out, err := docker(t, "run", "--detach", "--rm",
		"--name", sshd, "--network", network,
		"--publish", "127.0.0.1::22",
		"--env", "PUBLIC_KEY="+authorizedKey,
		bastionImage, "sh", "-c", sshdScript,
	); err != nil {
		t.Fatalf("starting the bastion: %v: %s", err, condense(out))
	}
	t.Cleanup(func() { docker(t, "stop", "--time", "0", sshd) }) //nolint:errcheck // teardown

	mapped, err := docker(t, "port", sshd, "22/tcp")
	if err != nil {
		t.Fatalf("reading the bastion's mapped port: %v: %s", err, condense(mapped))
	}
	bastionAddress := firstLine(mapped)
	host, port := splitAddress(bastionAddress)

	knownHosts := writeKnownHosts(t, keyDir, bastionAddress)
	waitForMySQLIn(t, database)

	return bastionWorld{
		databaseAddress: net.JoinHostPort(database, "3306"),
		tunnel:          db.SSHTunnel{Host: host, Port: port, User: bastionUser, KeyFile: keyFile},
		knownHosts:      knownHosts,
	}
}

// waitForMySQLIn blocks until the server in the named container answers a query
// over TCP. The check runs inside the container because nothing outside it can
// reach the server, and it asks over TCP rather than the socket because MySQL's
// initialisation runs a temporary server on the socket alone: a socket that
// answers means nothing yet, a TCP port that answers means the real server.
func waitForMySQLIn(t *testing.T, container string) {
	t.Helper()

	deadline := time.Now().Add(bastionReadyTimeout)
	var last string
	for time.Now().Before(deadline) {
		out, err := docker(t, "exec", container,
			"mysql", "--protocol=TCP", "--host=127.0.0.1",
			"--user=root", "--password="+bastionRootPW, "--execute=SELECT 1")
		if err == nil {
			return
		}
		last = condense(out)
		time.Sleep(time.Second)
	}
	t.Fatalf("MySQL in %s never became ready: %s", container, last)
}

// writeKnownHosts records the bastion's host key, by connecting and asking for
// it until the bastion has one to give — which is also how this waits for sshd,
// since a published port answers from the moment the container starts and the
// container installs openssh before it runs it.
//
// Only the test does this: it stands in for the human who types `ssh bastion`
// once before saving a Profile, and the shipping dialler has no way to do it —
// a host key it has not seen before is one it refuses.
func writeKnownHosts(t *testing.T, dir, address string) string {
	t.Helper()

	deadline := time.Now().Add(bastionReadyTimeout)
	var line string
	var last error
	for line == "" && time.Now().Before(deadline) {
		line, last = hostKeyLine(address)
		if line == "" {
			time.Sleep(500 * time.Millisecond)
		}
	}
	if line == "" {
		t.Fatalf("the bastion never presented a host key: %v", last)
	}

	path := filepath.Join(dir, "known_hosts")
	if err := os.WriteFile(path, []byte(line+"\n"), 0o600); err != nil {
		t.Fatalf("writing known_hosts: %v", err)
	}
	return path
}

// hostKeyLine returns one known_hosts line for whatever the bastion presents.
// No authentication is offered: the host key is exchanged before anyone is
// asked to prove anything, so the handshake fails right after the callback runs
// and the failure is not interesting.
func hostKeyLine(address string) (string, error) {
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()

	var line string
	_, _, _, err = ssh.NewClientConn(conn, address, &ssh.ClientConfig{
		User: "host-key-scan",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			line = knownhosts.Line([]string{knownhosts.Normalize(address)}, key)
			return nil
		},
		Timeout: 5 * time.Second,
	})
	return line, err
}

// buildBastionImage builds the bastion image if it is not already built. The
// image is left behind on purpose: it is a build cache, like a pulled image,
// and rebuilding it for every run is the minute this avoids.
func buildBastionImage(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), bastionBuildTimeout)
	defer cancel()

	build := exec.CommandContext(ctx, "docker", "build", "--tag", bastionImage, "-")
	build.Stdin = strings.NewReader(bastionDockerfile)

	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the bastion image: %v: %s", err, condense(string(out)))
	}
}

func docker(t *testing.T, args ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), bastionDockerTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// condense flattens command output into one line, so a failure reads as a
// sentence rather than a wall of docker chatter.
func condense(out string) string {
	return strings.Join(strings.Fields(out), " ")
}

// firstLine returns the first non-empty line; `docker port` may report several
// bindings, and the first loopback one is ours.
func firstLine(out string) string {
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

func splitAddress(address string) (host string, port int) {
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return address, 0
	}
	fmt.Sscanf(portText, "%d", &port)
	return host, port
}
