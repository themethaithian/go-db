package guard

// This is the proof behind ClassifyRedis's allowlist. ADR-0006 accepts that
// Redis has no read-only-transaction backstop and answers that with a closed
// allowlist "whose every entry carries proof, and why nothing may be added to
// them without it". This is where the proof is checked: against a real Redis 7's
// own COMMAND INFO, not against what the table's author believed.
//
// It skips — never fails — when Docker is not available, like the integration
// tests in internal/db and internal/service, and shares one throwaway container
// across its tests the way internal/service does.
//
// It is an internal test (package guard, not guard_test) on purpose. The tables
// it walks are unexported and must stay that way — this classifier's whole
// exported surface is ClassifyRedis — and a pin that walked an exported copy
// would be pinning the copy.
//
// No Redis client library is used. The test speaks RESP over a socket in the
// hundred lines at the bottom of this file, which keeps a client dependency out
// of go.mod for a package that is domain logic and must not grow one, and lets
// COMMAND INFO be asked about container subcommands by their "config|get"
// names — a form the typed helpers in a client library do not model.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	redisImage = "redis:7"

	redisDockerTimeout = 30 * time.Second
	redisReadyTimeout  = 60 * time.Second
)

// flaglessReads are the allowlist entries that carry no readonly flag, with the
// reason each is listed anyway. Redis reserves readonly for keyspace commands;
// these three touch no keyspace at all. PING and ECHO return what they were
// given, and INFO returns the server's own counters — none of them can name a
// key, let alone change one. Every other entry must carry the flag.
var flaglessReads = map[string]string{
	"PING": "a liveness probe: it takes no key and returns PONG or its own argument",
	"ECHO": "returns its argument unchanged; it cannot name a key",
	"INFO": "returns the server's counters; it takes a section name, not a key",
}

// forbiddenFlags are the flags that disqualify a command from the allowlist,
// and the sentence each one is refused with.
//
// "write" is the obvious one. "blocking" is the one that matters: XREAD carries
// Redis's readonly flag and would pass a check that looked only for write, and
// it is out because BLOCK can wedge the connection the editor shares. "pubsub"
// is the same argument for subscriber mode.
var forbiddenFlags = map[string]string{
	"write":    "it writes",
	"blocking": "it can block the connection the editor shares",
	"pubsub":   "it changes the connection's subscriber mode",
}

func TestIntegrationRedisAllowlistCarriesTheReadonlyFlag(t *testing.T) {
	redis := connectRedis(t)

	for _, name := range slices.Sorted(maps.Keys(redisReads)) {
		t.Run(name, func(t *testing.T) {
			if name != strings.ToUpper(name) {
				t.Fatalf("allowlist entry %q is not uppercase, so no command line can ever match it", name)
			}

			flags, known := redis.commandFlags(t, name)
			if !known {
				t.Fatalf("%s is on the allowlist and %s has no such command", name, redisImage)
			}
			for _, flag := range slices.Sorted(maps.Keys(forbiddenFlags)) {
				if slices.Contains(flags, flag) {
					t.Errorf("%s is on the allowlist and %s flags it %q — %s (flags: %v)", name, redisImage, flag, forbiddenFlags[flag], flags)
				}
			}

			readonly := slices.Contains(flags, "readonly")
			why, exempt := flaglessReads[name]
			switch {
			case exempt && readonly:
				t.Errorf("%s is exempted from the readonly requirement (%s) but %s does flag it readonly; drop the exemption", name, why, redisImage)
			case !exempt && !readonly:
				t.Errorf("%s carries no readonly flag in %s (flags: %v); either it does not belong on the allowlist, or it belongs in flaglessReads with a reason", name, redisImage, flags)
			}
		})
	}
}

// TestIntegrationRedisContainerAllowlistExists pins the (command, subcommand)
// pairs. Most of them carry no readonly flag — CONFIG GET, CLIENT LIST and the
// ACL reads are admin and connection commands rather than keyspace ones, so
// Redis never flags them readonly — which is why the claim checked here is the
// weaker one the flags can actually support: the pair exists, and it is not a
// write, a blocking command or a pubsub command. OBJECT, XINFO and MEMORY USAGE
// do carry readonly, and pass the stronger check in the test above's shape
// anyway.
func TestIntegrationRedisContainerAllowlistExists(t *testing.T) {
	redis := connectRedis(t)

	for _, container := range slices.Sorted(maps.Keys(redisContainerReads)) {
		if redisReads[container] {
			t.Errorf("%s is both a container command and a plain allowlist entry; its pairs would never be consulted", container)
		}
		for _, sub := range slices.Sorted(maps.Keys(redisContainerReads[container])) {
			t.Run(container+"_"+sub, func(t *testing.T) {
				if container != strings.ToUpper(container) || sub != strings.ToUpper(sub) {
					t.Fatalf("allowlist pair %q %q is not uppercase, so no command line can ever match it", container, sub)
				}

				// Redis 7 names a subcommand "container|subcommand", and
				// COMMAND INFO answers about it directly.
				flags, known := redis.commandFlags(t, strings.ToLower(container+"|"+sub))
				if !known {
					t.Fatalf("%s %s is on the allowlist and %s has no such subcommand", container, sub, redisImage)
				}
				for _, flag := range slices.Sorted(maps.Keys(forbiddenFlags)) {
					if slices.Contains(flags, flag) {
						t.Errorf("%s %s is on the allowlist and %s flags it %q — %s (flags: %v)", container, sub, redisImage, flag, forbiddenFlags[flag], flags)
					}
				}
			})
		}
	}
}

// TestIntegrationRedisTrapsAreProvablyNotReads is the other half of the pin.
// The allowlist is only as good as what it leaves out, and the commands it
// leaves out for having a write hidden in an argument are the ones most likely
// to be "fixed" back in by someone reading the table quickly.
//
// So: every argument-dependent command really does carry Redis's write flag,
// every _RO twin the table sends people to really is a readonly command, and
// XREAD — which carries readonly and would sail through a write-only check —
// really does carry blocking. That is what lets redisReads be proved from flags
// alone, without a single argument being parsed.
func TestIntegrationRedisTrapsAreProvablyNotReads(t *testing.T) {
	redis := connectRedis(t)

	// Every trap must be absent from the allowlists. Being in redisTraps only
	// supplies the wording; absence from redisReads is what makes it a mutation.
	for _, name := range slices.Sorted(maps.Keys(redisTraps)) {
		if redisReads[name] {
			t.Errorf("%s is in redisTraps and on the read allowlist; the allowlist is what decides, and it says read", name)
		}
		if _, isContainer := redisContainerReads[name]; isContainer {
			t.Errorf("%s is in redisTraps and is a container command; the trap would hide its pairs", name)
		}
	}

	// The write is in an argument, and Redis says so with the write flag. The
	// value is the _RO twin the table sends people to, or "" where there is
	// none.
	argumentDependent := map[string]string{
		"SORT":              "SORT_RO",
		"GEORADIUS":         "GEORADIUS_RO",
		"GEORADIUSBYMEMBER": "GEORADIUSBYMEMBER_RO",
		"BITFIELD":          "BITFIELD_RO",
		"GETEX":             "", // GETEX rewrites the TTL; plain GET is the read
	}
	for _, name := range slices.Sorted(maps.Keys(argumentDependent)) {
		t.Run(name, func(t *testing.T) {
			flags, known := redis.commandFlags(t, name)
			if !known {
				t.Fatalf("%s does not exist in %s; the table explains a command that is gone", name, redisImage)
			}
			if !slices.Contains(flags, "write") {
				t.Errorf("%s carries no write flag in %s (flags: %v); the reason it is excluded no longer holds", name, redisImage, flags)
			}

			twin := argumentDependent[name]
			if twin == "" {
				return
			}
			if !redisReads[twin] {
				t.Fatalf("%s is excluded in favour of %s, which is not on the allowlist", name, twin)
			}
			twinFlags, twinKnown := redis.commandFlags(t, twin)
			if !twinKnown {
				t.Fatalf("%s does not exist in %s, so %s has nowhere to send the human", twin, redisImage, name)
			}
			if !slices.Contains(twinFlags, "readonly") || slices.Contains(twinFlags, "write") {
				t.Errorf("%s is offered as the read-only form of %s, but %s flags it %v", twin, name, redisImage, twinFlags)
			}
		})
	}

	// XREAD is the case that decides the shape of this whole pin: it carries
	// readonly, so "readonly and not write" would have let it in, and its BLOCK
	// argument can hold the editor's connection open indefinitely.
	t.Run("XREAD", func(t *testing.T) {
		flags, known := redis.commandFlags(t, "XREAD")
		if !known {
			t.Fatalf("XREAD does not exist in %s", redisImage)
		}
		if !slices.Contains(flags, "readonly") {
			t.Errorf("XREAD is no longer flagged readonly in %s (flags: %v); that it is, is why this pin checks blocking and not only write", redisImage, flags)
		}
		if !slices.Contains(flags, "blocking") {
			t.Errorf("XREAD carries no blocking flag in %s (flags: %v); the reason it is excluded no longer holds", redisImage, flags)
		}
	})
}

// redisServer is one connection to the shared throwaway Redis.
type redisServer struct{ conn *respConn }

// commandFlags asks the server for one command's flags. The second return is
// false when Redis has no such command, which is how a typo in the table — or a
// command withdrawn by a later Redis — is caught rather than passed over.
func (r redisServer) commandFlags(t *testing.T, name string) ([]string, bool) {
	t.Helper()

	reply, ok := r.conn.do(t, "COMMAND", "INFO", name).([]any)
	if !ok || len(reply) != 1 {
		t.Fatalf("COMMAND INFO %s: want a one-element array, got %#v", name, reply)
	}
	// A nil entry is Redis saying it has never heard of the command.
	if reply[0] == nil {
		return nil, false
	}

	// An entry is [name, arity, flags, first-key, last-key, step, ...].
	entry, ok := reply[0].([]any)
	if !ok || len(entry) < 3 {
		t.Fatalf("COMMAND INFO %s: want a command entry, got %#v", name, reply[0])
	}
	raw, ok := entry[2].([]any)
	if !ok {
		t.Fatalf("COMMAND INFO %s: want an array of flags, got %#v", name, entry[2])
	}

	flags := make([]string, 0, len(raw))
	for _, flag := range raw {
		text, ok := flag.(string)
		if !ok {
			t.Fatalf("COMMAND INFO %s: want a flag name, got %#v", name, flag)
		}
		flags = append(flags, text)
	}
	return flags, true
}

// The container is started at most once per package run and stopped by
// TestMain, so all three tests in this file share it.
var (
	redisOnce      sync.Once
	redisAddress   string // host:port the container's Redis is reachable on
	redisSkip      string // why the container is unavailable, if it is
	redisContainer string // container name, empty when nothing was started
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopRedisContainer()
	os.Exit(code)
}

// connectRedis returns a connection to the shared throwaway Redis, starting the
// container on first use. It skips the calling test when Docker is not
// available, and fails it when Docker is there and the server would not come up:
// a broken container is a real failure, not a reason to skip.
func connectRedis(t *testing.T) redisServer {
	t.Helper()

	redisOnce.Do(startRedisContainer)
	if redisSkip != "" {
		t.Skipf("skipping integration test: %s", redisSkip)
	}
	if redisAddress == "" {
		t.Fatalf("the %s container did not start", redisImage)
	}
	return redisServer{conn: dialRedis(t, redisAddress)}
}

func startRedisContainer() {
	if out, err := dockerCLI("info", "--format", "{{.ServerVersion}}"); err != nil {
		redisSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	name := fmt.Sprintf("godb-redis-flags-%d", time.Now().UnixNano())
	if out, err := dockerCLI("run", "--detach", "--rm",
		"--name", name, "--publish", "127.0.0.1::6379",
		redisImage,
	); err != nil {
		redisSkip = fmt.Sprintf("could not start %s (%v: %s)", redisImage, err, condense(out))
		return
	}
	redisContainer = name

	mapped, err := dockerCLI("port", name, "6379/tcp")
	if err != nil {
		redisSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(mapped))
		stopRedisContainer()
		return
	}
	// `docker port` may report several bindings; the first loopback one wins.
	if redisAddress = firstLine(mapped); redisAddress == "" {
		redisSkip = fmt.Sprintf("the container published no port (%s)", condense(mapped))
		stopRedisContainer()
	}
}

func stopRedisContainer() {
	if redisContainer == "" {
		return
	}
	// --rm on the container removes it once stopped.
	dockerCLI("stop", "--time", "0", redisContainer) //nolint:errcheck // teardown
	redisContainer = ""
}

// dialRedis opens one connection and waits for the server to answer PING. Redis
// is listening in well under a second, so the wait is a formality — but a flaky
// first connection would read here as a table error, and that is the one thing
// this test must never say by mistake.
func dialRedis(t *testing.T, address string) *respConn {
	t.Helper()

	deadline := time.Now().Add(redisReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(200 * time.Millisecond)
			continue
		}

		t.Cleanup(func() { conn.Close() }) //nolint:errcheck // teardown
		client := &respConn{conn: conn, reader: bufio.NewReader(conn)}
		if reply, ok := client.do(t, "PING").(string); !ok || reply != "PONG" {
			t.Fatalf("%s at %s answered PING with %#v", redisImage, address, reply)
		}
		return client
	}
	t.Fatalf("%s at %s never accepted a connection: %v", redisImage, address, lastErr)
	return nil
}

// respConn speaks RESP2 over one socket: send a command as an array of bulk
// strings, read one reply. It is the whole client this test needs.
type respConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

// do sends one command and returns its reply as nil, a string, an int64, or a
// []any of the same. An error reply fails the test: every command sent here is
// a COMMAND INFO or a PING, so an error means the test is wrong, not the table.
func (c *respConn) do(t *testing.T, args ...string) any {
	t.Helper()

	var request strings.Builder
	fmt.Fprintf(&request, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&request, "$%d\r\n%s\r\n", len(arg), arg)
	}

	if err := c.conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("setting the connection deadline: %v", err)
	}
	if _, err := io.WriteString(c.conn, request.String()); err != nil {
		t.Fatalf("sending %v: %v", args, err)
	}
	return c.reply(t)
}

func (c *respConn) reply(t *testing.T) any {
	t.Helper()

	line := c.line(t)
	if line == "" {
		t.Fatal("empty reply line")
	}

	body := line[1:]
	switch line[0] {
	case '+': // simple string
		return body
	case '-': // error
		t.Fatalf("redis replied with an error: %s", body)
	case ':': // integer
		return c.number(t, body)
	case '$': // bulk string, negative length for nil
		size := c.number(t, body)
		if size < 0 {
			return nil
		}
		bulk := make([]byte, size+2) // the payload and its trailing CRLF
		if _, err := io.ReadFull(c.reader, bulk); err != nil {
			t.Fatalf("reading a %d-byte bulk string: %v", size, err)
		}
		return string(bulk[:size])
	case '*': // array, negative length for nil
		count := c.number(t, body)
		if count < 0 {
			return nil
		}
		items := make([]any, count)
		for i := range items {
			items[i] = c.reply(t)
		}
		return items
	}
	t.Fatalf("unrecognised reply type %q in %q", line[0], line)
	return nil
}

func (c *respConn) line(t *testing.T) string {
	t.Helper()

	line, err := c.reader.ReadString('\n')
	if err != nil {
		t.Fatalf("reading a reply line: %v", err)
	}
	return strings.TrimRight(line, "\r\n")
}

func (c *respConn) number(t *testing.T, text string) int {
	t.Helper()

	n, err := strconv.Atoi(text)
	if err != nil {
		t.Fatalf("reading %q as a number: %v", text, err)
	}
	return n
}

func dockerCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redisDockerTimeout)
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
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
