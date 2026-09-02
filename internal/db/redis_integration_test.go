package db_test

// This is the acceptance test for the Redis adapter: a real Redis 7 in Docker,
// reached through the Driver and Conn ports exactly as the Connection Registry
// will reach it.
//
// It skips — never fails — when Docker is not available, like the tunnel test
// beside it and the integration tests in internal/guard and internal/service,
// and shares one throwaway container across its tests the way those do. The
// server is started with a password, so the ordinary path and the rejected one
// are the same server rather than two setups.
//
// The docker, condense, firstLine and splitAddress helpers are the tunnel
// test's, shared by being in the same package.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
)

const (
	redisImage = "redis:7"

	// The default user's password. Everything but the auth-failure test uses
	// it, which is what makes that test a rejection rather than a different
	// server.
	redisPassword = "redis-integration-pw"

	redisDockerTimeout = 60 * time.Second
	redisReadyTimeout  = 60 * time.Second
)

func TestIntegrationRedisDriver(t *testing.T) {
	address := startRedisServer(t)

	t.Run("a value written through Exec is read back through ReadQuery", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		affected, err := conn.Exec(ctx, `SET greeting "hello world"`)
		if err != nil {
			t.Fatalf("Exec(SET): %v", err)
		}
		// SET answers OK, a string: there is no count in it, and the adapter
		// says 0 rather than inventing one.
		if affected != 0 {
			t.Errorf("Exec(SET) affected = %d, want 0 for a reply that is not a count", affected)
		}

		result, err := conn.ReadQuery(ctx, "GET greeting")
		if err != nil {
			t.Fatalf("ReadQuery(GET): %v", err)
		}
		if got := result.Kind(); got != db.ResultValue {
			t.Fatalf("Kind() = %q, want %q — Redis answers with one typed value", got, db.ResultValue)
		}
		reply, ok := result.Value()
		if !ok {
			t.Fatal("Value() reported the arm absent on a Result tagged value")
		}
		if reply.Kind != db.ReplyString || reply.Text != "hello world" {
			t.Errorf("reply = %#v, want the string \"hello world\"", reply)
		}
	})

	t.Run("a missing key is a nil reply, not a failure", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		result, err := conn.ReadQuery(ctx, "GET no-such-key")
		if err != nil {
			t.Fatalf("ReadQuery(GET) on a missing key: %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyNil {
			t.Errorf("reply = %#v, want a nil reply — the key is absent, and that is the answer", reply)
		}
	})

	t.Run("an integer reply is the affected count", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		for _, command := range []string{"SET doomed-one 1", "SET doomed-two 2"} {
			if _, err := conn.Exec(ctx, command); err != nil {
				t.Fatalf("Exec(%q): %v", command, err)
			}
		}

		affected, err := conn.Exec(ctx, "DEL doomed-one doomed-two no-such-key")
		if err != nil {
			t.Fatalf("Exec(DEL): %v", err)
		}
		if affected != 2 {
			t.Errorf("Exec(DEL) affected = %d, want 2 — the two keys that were there", affected)
		}
	})

	// This is the layer ADR-0006 leaves the classifier carrying alone, running
	// against a server that would happily have done it. The key surviving is
	// the assertion that matters: the refusal happened before execution, not
	// after.
	t.Run("ReadQuery refuses a mutation the server would have run", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		if _, err := conn.Exec(ctx, "SET survivor alive"); err != nil {
			t.Fatalf("Exec(SET): %v", err)
		}

		if _, err := conn.ReadQuery(ctx, "DEL survivor"); !errors.Is(err, db.ErrWriteAttempt) {
			t.Fatalf("ReadQuery(DEL) error = %v, want it to wrap db.ErrWriteAttempt", err)
		}

		result, err := conn.ReadQuery(ctx, "GET survivor")
		if err != nil {
			t.Fatalf("ReadQuery(GET): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyString || reply.Text != "alive" {
			t.Errorf("reply = %#v, want the key still there — the refused DEL must never have reached the server", reply)
		}
	})

	t.Run("a reply past the cap is cut and says so", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		var push strings.Builder
		push.WriteString("RPUSH long-list")
		for i := 0; i < db.MaxReplyItems+50; i++ {
			fmt.Fprintf(&push, " %d", i)
		}
		length, err := conn.Exec(ctx, push.String())
		if err != nil {
			t.Fatalf("Exec(RPUSH): %v", err)
		}
		if want := int64(db.MaxReplyItems + 50); length != want {
			t.Fatalf("Exec(RPUSH) affected = %d, want the new length %d", length, want)
		}

		result, err := conn.ReadQuery(ctx, "LRANGE long-list 0 -1")
		if err != nil {
			t.Fatalf("ReadQuery(LRANGE): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyArray {
			t.Fatalf("reply = %#v, want an array", reply)
		}
		if len(reply.Items) != db.MaxReplyItems {
			t.Errorf("items = %d, want the cap of %d", len(reply.Items), db.MaxReplyItems)
		}
		if !reply.Truncated {
			t.Error("Truncated = false, want the cut marked so the list on screen is not read as the whole one")
		}
		if reply.Items[0].Kind != db.ReplyString || reply.Items[0].Text != "0" {
			t.Errorf("items[0] = %#v, want the list's first element", reply.Items[0])
		}
	})

	// CONFIG GET is the read that proves RESP3 was negotiated: a server
	// speaking RESP2 answers it with a flat array, and the map arm would never
	// be built at all.
	t.Run("a map reply keeps its keys", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		result, err := conn.ReadQuery(ctx, "CONFIG GET maxmemory")
		if err != nil {
			t.Fatalf("ReadQuery(CONFIG GET): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyMap {
			t.Fatalf("reply = %#v, want a map", reply)
		}
		if len(reply.Entries) != 1 || reply.Entries[0].Key.Text != "maxmemory" {
			t.Errorf("entries = %#v, want one entry keyed maxmemory", reply.Entries)
		}
	})

	// The Explorer's introspection, against a real keyspace. The Database tree
	// lists a Redis Profile's keys by paging SCAN through ReadQuery — the loop
	// itself lives in internal/service, which has no server of its own — so
	// what is proved here is the contract that loop is written against: SCAN is
	// a read the classifier and the adapter both allow, and a real server
	// answers it with a cursor and a page of keys, cursor 0 ending the walk.
	t.Run("SCAN pages a keyspace through the read path", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "9")

		// An index of this test's own, emptied first: SCAN answers for the
		// whole database, so a shared one would make this test's answer
		// whatever the rest of the file had left behind.
		if _, err := conn.Exec(ctx, "FLUSHDB"); err != nil {
			t.Fatalf("Exec(FLUSHDB): %v", err)
		}

		const seeded = 250
		want := make(map[string]bool, seeded)
		for i := 0; i < seeded; i++ {
			key := fmt.Sprintf("scan:key:%03d", i)
			if _, err := conn.Exec(ctx, "SET "+key+" v"); err != nil {
				t.Fatalf("Exec(SET %s): %v", key, err)
			}
			want[key] = true
		}

		found := make(map[string]bool, seeded)
		cursor := "0"
		for pages := 0; ; pages++ {
			if pages > 32 {
				t.Fatal("the scan never reached cursor 0")
			}

			result, err := conn.ReadQuery(ctx, "SCAN "+cursor+" COUNT 1000")
			if err != nil {
				t.Fatalf("ReadQuery(SCAN): %v", err)
			}
			if got := result.Kind(); got != db.ResultValue {
				t.Fatalf("Kind() = %q, want %q — Redis answers with one typed value", got, db.ResultValue)
			}

			reply, _ := result.Value()
			if reply.Kind != db.ReplyArray || len(reply.Items) != 2 {
				t.Fatalf("reply = %#v, want an array of a cursor and a page of keys", reply)
			}
			if reply.Items[0].Kind != db.ReplyString {
				t.Fatalf("cursor = %#v, want it as text", reply.Items[0])
			}
			if reply.Items[1].Kind != db.ReplyArray {
				t.Fatalf("page = %#v, want a list of keys", reply.Items[1])
			}
			for _, item := range reply.Items[1].Items {
				if item.Kind != db.ReplyString {
					t.Fatalf("key = %#v, want it as text", item)
				}
				found[item.Text] = true
			}

			cursor = reply.Items[0].Text
			if cursor == "0" {
				break
			}
		}

		for key := range want {
			if !found[key] {
				t.Errorf("the scan never returned %q, want every seeded key", key)
			}
		}
		if len(found) != seeded {
			t.Errorf("the scan returned %d keys, want the %d seeded", len(found), seeded)
		}
	})

	t.Run("Ping answers on a live connection", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		if err := conn.Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
}

// TestIntegrationRedisDatabaseIndex is the Profile field that means something
// different on this Engine. Redis numbers its databases, and a Profile naming
// index 1 must land there — a connection that quietly stayed on 0 would look
// like it worked while writing to the wrong keyspace.
func TestIntegrationRedisDatabaseIndex(t *testing.T) {
	address := startRedisServer(t)

	one, ctx := openRedis(t, address, "1")
	if _, err := one.Exec(ctx, "SET only-in-one yes"); err != nil {
		t.Fatalf("Exec(SET) on database 1: %v", err)
	}

	zero, ctx := openRedis(t, address, "")
	result, err := zero.ReadQuery(ctx, "GET only-in-one")
	if err != nil {
		t.Fatalf("ReadQuery(GET) on database 0: %v", err)
	}
	reply, _ := result.Value()
	if reply.Kind != db.ReplyNil {
		t.Errorf("reply = %#v on database 0, want nil — the key was written to database 1", reply)
	}

	back, ctx := openRedis(t, address, "1")
	result, err = back.ReadQuery(ctx, "GET only-in-one")
	if err != nil {
		t.Fatalf("ReadQuery(GET) on database 1: %v", err)
	}
	reply, _ = result.Value()
	if reply.Kind != db.ReplyString || reply.Text != "yes" {
		t.Errorf("reply = %#v on database 1, want the key that was written there", reply)
	}
}

// TestIntegrationRedisDatabaseIndexRefusesAName pins the failure that must not
// be a silent default. The address is a server that is really there, so the
// only thing being tested is that a Profile naming a database Redis cannot have
// fails before anything is dialled.
func TestIntegrationRedisDatabaseIndexRefusesAName(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, "production"), redisPassword, nil)
	if err == nil {
		opened.Close() //nolint:errcheck // it should never have opened
		t.Fatal("Open with a named database succeeded, want it refused rather than defaulted to index 0")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("Open error = %v, want it to name the database it could not read", err)
	}
}

// TestIntegrationRedisAuthFailure and its sibling below are the two failures
// the ports promise to tell apart, because they call for different fixes.
func TestIntegrationRedisAuthFailure(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, test := range []struct {
		name     string
		password string
	}{
		{"the wrong password", "not-the-password"},
		{"no password at all", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, ""), test.password, nil)
			if err == nil {
				opened.Close() //nolint:errcheck // it should never have opened
				t.Fatal("Open succeeded, want it refused")
			}
			if !errors.Is(err, db.ErrAuthFailed) {
				t.Errorf("Open error = %v, want it to wrap db.ErrAuthFailed", err)
			}
			if errors.Is(err, db.ErrUnreachable) {
				t.Error("Open reported the server unreachable; it answered, and said no")
			}
		})
	}
}

func TestIntegrationRedisUnreachable(t *testing.T) {
	// Docker is not needed to prove this one, but the test lives with its
	// sibling and skips with it, so a run with no Docker reports one story.
	startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(closedAddress(t), ""), redisPassword, nil)
	if err == nil {
		opened.Close() //nolint:errcheck // it should never have opened
		t.Fatal("Open on a closed port succeeded")
	}
	if !errors.Is(err, db.ErrUnreachable) {
		t.Errorf("Open error = %v, want it to wrap db.ErrUnreachable", err)
	}
	if errors.Is(err, db.ErrAuthFailed) {
		t.Error("Open reported the credentials rejected; nothing was there to reject them")
	}
}

// TestIntegrationRedisACLUser proves the other half of the credential mapping:
// a Profile's User is Redis's ACL username, and an empty one is the default
// user. The reader is created through Exec on the default user's connection,
// which is the gate's own path for a mutation.
func TestIntegrationRedisACLUser(t *testing.T) {
	address := startRedisServer(t)

	admin, ctx := openRedis(t, address, "")
	if _, err := admin.Exec(ctx, "ACL SETUSER reader on >reader-pw ~* +@all"); err != nil {
		t.Fatalf("Exec(ACL SETUSER): %v", err)
	}

	profile := redisProfile(address, "")
	profile.User = "reader"

	reader, err := db.NewRedisDriver().Open(ctx, profile, "reader-pw", nil)
	if err != nil {
		t.Fatalf("Open as the ACL user reader: %v", err)
	}
	t.Cleanup(func() { reader.Close() }) //nolint:errcheck // teardown

	if err := reader.Ping(ctx); err != nil {
		t.Errorf("Ping as the ACL user reader: %v", err)
	}

	if _, err := db.NewRedisDriver().Open(ctx, profile, "not-reader-pw", nil); !errors.Is(err, db.ErrAuthFailed) {
		t.Errorf("Open as reader with the wrong password: error = %v, want it to wrap db.ErrAuthFailed", err)
	}
}

// TestIntegrationRedisHonoursTheDialFunc pins the seam a tunnelled Profile
// depends on: the Driver dials through the DialFunc it was given and nowhere
// else. The Profile names an address that does not resolve on this machine, so
// a driver that fell back to dialling directly could not possibly succeed.
func TestIntegrationRedisHonoursTheDialFunc(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var asked []string
	dial := func(ctx context.Context, wanted string) (net.Conn, error) {
		mu.Lock()
		asked = append(asked, wanted)
		mu.Unlock()
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}

	profile := redisProfile("redis.invalid:6379", "")
	opened, err := db.NewRedisDriver().Open(ctx, profile, redisPassword, dial)
	if err != nil {
		t.Fatalf("Open through a DialFunc: %v", err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown

	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 {
		t.Fatal("the DialFunc was never called, so the connection went around it")
	}
	if asked[0] != "redis.invalid:6379" {
		t.Errorf("the DialFunc was asked for %q, want the Profile's own address", asked[0])
	}
}

// redisProfile is a Profile for the shared server, on the given database.
func redisProfile(address, database string) db.Profile {
	host, port := splitAddress(address)
	return db.Profile{
		Name:     "redis-integration",
		Host:     host,
		Port:     port,
		Database: database,
		Engine:   db.EngineRedis,
	}
}

// openRedis opens one connection to the shared server and closes it when the
// test ends. The context it returns is the one the test's calls run under.
func openRedis(t *testing.T, address, database string) (db.Conn, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, database), redisPassword, nil)
	if err != nil {
		t.Fatalf("opening %s on database %q: %v", address, database, err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown
	return opened, ctx
}

// closedAddress returns a loopback address nothing is listening on: a port is
// taken and released, so the address is real and refuses.
func closedAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("taking a port to release it: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}
	return address
}

// The container is started at most once per package run and stopped by
// TestMain, so every test in this file shares it.
var (
	redisOnce      sync.Once
	redisAddress   string // host:port the container's Redis is reachable on
	redisSkip      string // why the container is unavailable, if it is
	redisContainer string // container name, empty when nothing was started
)

// TestMain stops every throwaway container this package's integration tests
// started. It lives here because it is one per package and this is the file
// that first needed one; the MongoDB tests beside it hang their own teardown
// off it rather than declaring a second.
func TestMain(m *testing.M) {
	code := m.Run()
	stopRedisContainer()
	stopRedisTLSContainer()
	stopMongoContainer()
	os.Exit(code)
}

// startRedisServer returns the shared server's address, starting the container
// on first use. It skips the calling test when Docker is not available, and
// fails it when Docker is there and the server would not come up: a broken
// container is a real failure, not a reason to skip.
func startRedisServer(t *testing.T) string {
	t.Helper()

	redisOnce.Do(startRedisContainer)
	if redisSkip != "" {
		t.Skipf("skipping integration test: %s", redisSkip)
	}
	if redisAddress == "" {
		t.Fatalf("the %s container did not start", redisImage)
	}
	return redisAddress
}

func startRedisContainer() {
	if out, err := dockerCLI("info", "--format", "{{.ServerVersion}}"); err != nil {
		redisSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	name := fmt.Sprintf("godb-redis-driver-%d", time.Now().UnixNano())
	if out, err := dockerCLI("run", "--detach", "--rm",
		"--name", name, "--publish", "127.0.0.1::6379",
		redisImage, "redis-server", "--requirepass", redisPassword,
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
	address := firstLine(mapped)
	if address == "" {
		redisSkip = fmt.Sprintf("the container published no port (%s)", condense(mapped))
		stopRedisContainer()
		return
	}
	if err := waitForRedis(address); err != nil {
		redisSkip = fmt.Sprintf("%s at %s never accepted a connection (%v)", redisImage, address, err)
		stopRedisContainer()
		return
	}
	redisAddress = address
}

// waitForRedis waits for the server to accept a connection. Redis is listening
// in well under a second, so this is a formality — but a race with the
// container's start would read as an adapter failure, and that is the one thing
// this file must never say by mistake.
func waitForRedis(address string) error {
	deadline := time.Now().Add(redisReadyTimeout)

	var last error
	for time.Now().Before(deadline) {
		socket, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			return socket.Close()
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

func stopRedisContainer() {
	if redisContainer == "" {
		return
	}
	// --rm on the container removes it once stopped.
	dockerCLI("stop", "--time", "0", redisContainer) //nolint:errcheck // teardown
	redisContainer = ""
}

// dockerCLI runs one docker command. It takes no *testing.T because the
// container is started and stopped outside any one test.
func dockerCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redisDockerTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// TestIntegrationRedisTLS is the acceptance test for Profile.TLS on this
// Engine, against a second throwaway server that speaks nothing but TLS.
//
// The certificate is self-signed and generated by the test itself, which is the
// point rather than a shortcut: it is exactly the certificate this machine has
// no reason to trust, which is the situation SkipVerify exists for. So the
// three cases below are the three answers a human can get — waive verification
// and connect, keep it and be told which certificate was refused, or forget TLS
// entirely and fail fast rather than hang.
func TestIntegrationRedisTLS(t *testing.T) {
	address := startRedisTLSServer(t)

	t.Run("TLS with verification waived connects and answers", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		profile := redisProfile(address, "")
		profile.TLS = &db.TLSSettings{SkipVerify: true}

		opened, err := db.NewRedisDriver().Open(ctx, profile, redisPassword, nil)
		if err != nil {
			t.Fatalf("Open with SkipVerify: %v", err)
		}
		t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown

		result, err := opened.ReadQuery(ctx, "PING")
		if err != nil {
			t.Fatalf("ReadQuery(PING) over TLS: %v", err)
		}
		reply, ok := result.Value()
		if !ok {
			t.Fatal("Value() reported the arm absent on a Result tagged value")
		}
		if reply.Kind != db.ReplyString || reply.Text != "PONG" {
			t.Errorf("reply = %#v, want the string \"PONG\"", reply)
		}
	})

	// The wording is the assertion here. ErrAuthFailed would send the human to
	// change a password that is perfectly correct, and ErrUnreachable would
	// send them to a server that answered — so the failure keeps the
	// certificate in it, because installing the CA or waiving verification are
	// the only two things that fix this.
	t.Run("TLS with verification on refuses the self-signed certificate", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		profile := redisProfile(address, "")
		profile.TLS = &db.TLSSettings{}

		opened, err := db.NewRedisDriver().Open(ctx, profile, redisPassword, nil)
		if err == nil {
			opened.Close() //nolint:errcheck // it should never have opened
			t.Fatal("Open succeeded against an untrusted certificate, want it refused")
		}
		if errors.Is(err, db.ErrAuthFailed) {
			t.Errorf("Open error = %v, want it not to wrap db.ErrAuthFailed — the password was right and the server never saw it", err)
		}
		if errors.Is(err, db.ErrUnreachable) {
			t.Errorf("Open error = %v, want it not to wrap db.ErrUnreachable — the server was reached, and this end is what said no", err)
		}
		if !strings.Contains(err.Error(), "the server's TLS certificate was not accepted") {
			t.Errorf("Open error = %v, want the adapter's own sentence about the certificate rather than a bare client error", err)
		}
	})

	// The combination the adapter has to assemble itself. go-redis applies
	// TLSConfig only inside the dialler it builds when it was given none, so a
	// Profile that is both tunnelled and encrypted is the one case where the
	// library would have handed over a plaintext socket — the address below
	// does not resolve on this machine, so nothing but the DialFunc could have
	// reached the server, and PONG is the proof TLS was negotiated over it.
	t.Run("TLS is negotiated over a DialFunc's connection", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		dial := func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
		}

		profile := redisProfile("redis.invalid:6379", "")
		profile.TLS = &db.TLSSettings{SkipVerify: true}

		opened, err := db.NewRedisDriver().Open(ctx, profile, redisPassword, dial)
		if err != nil {
			t.Fatalf("Open with TLS through a DialFunc: %v", err)
		}
		t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown

		result, err := opened.ReadQuery(ctx, "PING")
		if err != nil {
			t.Fatalf("ReadQuery(PING) over a tunnelled TLS connection: %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyString || reply.Text != "PONG" {
			t.Errorf("reply = %#v, want the string \"PONG\"", reply)
		}
	})

	// A Profile that forgot TLS points at a port that will not speak to it.
	// What matters as much as the failure is when it arrives: the dial itself
	// succeeds, so nothing but the client's own bounds ends this, and a desktop
	// client that sits there is worse than one that says no.
	t.Run("no TLS against a TLS server fails, and fails quickly", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		started := time.Now()
		opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, ""), redisPassword, nil)
		elapsed := time.Since(started)

		if err == nil {
			opened.Close() //nolint:errcheck // it should never have opened
			t.Fatal("Open in plaintext against a TLS-only server succeeded")
		}
		if elapsed > 15*time.Second {
			t.Errorf("Open took %s to fail, want it bounded by the client's own dial and read timeouts", elapsed)
		}
	})
}

// startRedisTLSServer returns the TLS-only server's address, starting the
// container on first use. It skips and fails on exactly the terms
// startRedisServer does.
func startRedisTLSServer(t *testing.T) string {
	t.Helper()

	redisTLSOnce.Do(startRedisTLSContainer)
	if redisTLSSkip != "" {
		t.Skipf("skipping integration test: %s", redisTLSSkip)
	}
	if redisTLSAddress == "" {
		t.Fatalf("the %s TLS container did not start", redisImage)
	}
	return redisTLSAddress
}

// The TLS server is a second container, not a second port on the first: a
// server started with --port 0 speaks nothing but TLS, which is what makes the
// plaintext case above a real refusal rather than a connection to the other
// listener.
var (
	redisTLSOnce      sync.Once
	redisTLSAddress   string // host:port the container's Redis is reachable on
	redisTLSSkip      string // why the container is unavailable, if it is
	redisTLSContainer string // container name, empty when nothing was started
)

// redisTLSPath is where the server reads its certificate from, inside the
// container.
const redisTLSPath = "/tls"

// startRedisTLSContainer creates the container, copies a freshly generated
// certificate into it, and only then starts it.
//
// The three steps are why this does not simply `docker run` the way its sibling
// does. The certificate has to exist inside the container before redis-server
// looks for it, and a bind mount cannot put it there on this machine: Docker
// runs in a VM (colima), and the directory t.TempDir and os.MkdirTemp hand out
// on macOS is under /var/folders, which that VM does not share — the mount
// arrives empty and the server exits before anything can connect. `docker cp`
// goes through the daemon rather than the filesystem, so it does not care.
func startRedisTLSContainer() {
	if out, err := dockerCLI("info", "--format", "{{.ServerVersion}}"); err != nil {
		redisTLSSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	dir, err := os.MkdirTemp("", "godb-redis-tls-")
	if err != nil {
		redisTLSSkip = fmt.Sprintf("could not make a directory for the certificate (%v)", err)
		return
	}
	defer os.RemoveAll(dir) //nolint:errcheck // the copy into the container is what matters
	if err := writeSelfSignedCertificate(dir); err != nil {
		redisTLSSkip = fmt.Sprintf("could not generate a certificate (%v)", err)
		return
	}

	name := fmt.Sprintf("godb-redis-tls-%d", time.Now().UnixNano())
	if out, err := dockerCLI("create",
		"--name", name, "--publish", "127.0.0.1::6379",
		redisImage, "redis-server",
		// --port 0 turns the plaintext listener off entirely.
		"--tls-port", "6379", "--port", "0",
		"--tls-cert-file", redisTLSPath+"/server.crt",
		"--tls-key-file", redisTLSPath+"/server.key",
		// The certificate is its own CA, and the server is told so only
		// because it insists on being told something; --tls-auth-clients no
		// means it never asks a client for one.
		"--tls-ca-cert-file", redisTLSPath+"/server.crt",
		"--tls-auth-clients", "no",
		"--requirepass", redisPassword,
	); err != nil {
		redisTLSSkip = fmt.Sprintf("could not create the %s TLS container (%v: %s)", redisImage, err, condense(out))
		return
	}
	redisTLSContainer = name

	if out, err := dockerCLI("cp", dir, name+":"+redisTLSPath); err != nil {
		redisTLSSkip = fmt.Sprintf("could not copy the certificate into the container (%v: %s)", err, condense(out))
		stopRedisTLSContainer()
		return
	}
	if out, err := dockerCLI("start", name); err != nil {
		redisTLSSkip = fmt.Sprintf("could not start the %s TLS container (%v: %s)", redisImage, err, condense(out))
		stopRedisTLSContainer()
		return
	}

	mapped, err := dockerCLI("port", name, "6379/tcp")
	if err != nil {
		redisTLSSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(mapped))
		stopRedisTLSContainer()
		return
	}
	address := firstLine(mapped)
	if address == "" {
		redisTLSSkip = fmt.Sprintf("the container published no port (%s)", condense(mapped))
		stopRedisTLSContainer()
		return
	}
	if err := waitForRedis(address); err != nil {
		redisTLSSkip = fmt.Sprintf("%s at %s never accepted a connection (%v)", redisImage, address, err)
		stopRedisTLSContainer()
		return
	}
	redisTLSAddress = address
}

// stopRedisTLSContainer removes the container outright rather than stopping it
// and letting --rm do the rest, because this one is created before it is
// started: a container that never started is not removed by stopping it.
func stopRedisTLSContainer() {
	if redisTLSContainer == "" {
		return
	}
	dockerCLI("rm", "--force", "--volumes", redisTLSContainer) //nolint:errcheck // teardown
	redisTLSContainer = ""
}

// writeSelfSignedCertificate writes server.crt and server.key into dir: one
// self-signed certificate naming 127.0.0.1, which is the address the tests
// reach the container on.
//
// The directory and the files are world-readable, and both halves of that
// matter: the redis image drops to its own unprivileged user before running the
// server, and `docker cp` lands what it copies owned by root with the modes it
// was given — so a directory left at MkdirTemp's 0700 is one the server cannot
// even walk into, and it exits saying "Permission denied" on a certificate that
// is plainly there. They are a throwaway key for a throwaway container that
// lives for the length of one test run.
func writeSelfSignedCertificate(dir string) error {
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // see above
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		// The IP SAN is what a client reaching 127.0.0.1 checks, and the DNS
		// name is there for a reader who tries the same server by hand.
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:    []string{"localhost"},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})

	if err := os.WriteFile(filepath.Join(dir, "server.crt"), certificate, 0o644); err != nil { //nolint:gosec // a throwaway certificate the container's own user must read
		return err
	}
	return os.WriteFile(filepath.Join(dir, "server.key"), private, 0o644) //nolint:gosec // as above
}
