package db

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/redis/go-redis/v9"

	// The Redis adapter is the one place in this package that reaches into the
	// Approval Gate, and it is allowed to because the dependency runs one way:
	// guard imports nothing from db, and `go list -deps ./internal/guard`
	// says so. ReadQuery explains why it needs the classifier at all.
	"github.com/themethaithian/go-db/internal/guard"
)

// NewRedisDriver returns the Driver that reaches Redis over go-redis.
//
// Like the MySQL one beside it, it is the only place that knows this server's
// error vocabulary: it turns a rejected password into ErrAuthFailed and a
// server that was never reached into ErrUnreachable, so callers never import a
// client package to tell the two apart.
//
// It differs from the MySQL adapter in one way that matters, and the difference
// is a safety property rather than a detail: Redis has no read-only
// transaction, so this adapter runs the Approval Gate's Redis classifier itself
// before executing a read. See redisConn.ReadQuery.
func NewRedisDriver() Driver { return redisDriver{} }

type redisDriver struct{}

// Open dials profile's server and verifies the connection answers before
// returning it. Nothing is left open on failure.
//
// The pool settings are the MySQL adapter's, deliberately: they are a statement
// about this application — a desktop client holding few connections per Profile
// so the editor and the Approval Gate do not serialise on one socket — and not
// about MySQL. go-redis's own default is ten connections per CPU, which is a
// server's setting and not ours.
func (redisDriver) Open(ctx context.Context, profile Profile, password string, dial DialFunc) (Conn, error) {
	index, err := redisDatabaseIndex(profile.Database)
	if err != nil {
		return nil, fmt.Errorf("db: connection settings for profile %q: %w", profile.Name, err)
	}

	options := &redis.Options{
		Addr: profile.Address(),
		// Redis's ACL user and its password. An empty user is the default
		// user, which is what a server with a plain requirepass has, and what
		// go-redis authenticates as when it is given a password and no name.
		Username: profile.User,
		Password: password,
		DB:       index,

		// Bounds reaching the server, so an unreachable host fails fast
		// instead of hanging the UI. It applies to the ordinary dialler only;
		// a Profile with a tunnel is bounded by the tunnel's own dialling,
		// which is where its hops are.
		DialTimeout: dialTimeout,

		PoolSize:        maxOpenConns,
		MaxIdleConns:    maxIdleConns,
		ConnMaxIdleTime: connMaxIdleTime,
		ConnMaxLifetime: connMaxLifetime,

		// No retries, of either kind. Left alone, go-redis retries a command
		// three times and a dial five, which turns one unreachable host into
		// fifteen dial timeouts before the UI is told anything — and a desktop
		// client that says "unreachable" slowly is worse than one that says it
		// once. The case retries exist for is already covered: the pool
		// health-checks a connection before handing it out, so one the server
		// dropped is replaced rather than used and retried.
		MaxRetries:    -1,
		DialerRetries: 1,
	}

	// Transport encryption, or nil for a plaintext connection — the Profile
	// decides, and it decides the same way for every Engine. go-redis reads
	// this field in the dialler it builds for itself, which is the one used
	// when a Profile has no tunnel.
	secure := profile.tlsConfig()
	options.TLSConfig = secure

	if dial != nil {
		// Every connection in the pool goes this way, including ones opened
		// later as the pool grows, and go-redis has no path around a Dialer it
		// was given: a tunnelled Profile cannot quietly connect direct.
		options.Dialer = func(ctx context.Context, _, address string) (net.Conn, error) {
			socket, err := dial(ctx, address)
			if err != nil {
				return nil, err
			}
			if secure == nil {
				return socket, nil
			}

			// The wrap is ours because go-redis's is not: TLSConfig is applied
			// only inside redis.NewDialer, the dialler go-redis builds when it
			// was given none, and a Dialer of our own is used exactly as
			// handed over. Left to the library, a tunnelled TLS Profile would
			// send plaintext RESP at a TLS port and read the failure as the
			// server being broken.
			//
			// The handshake is completed here rather than left to the first
			// read, so a certificate this end will not accept is reported by
			// the dial that caused it instead of by whatever command happened
			// to run next.
			encrypted := tls.Client(socket, secure)
			if err := encrypted.HandshakeContext(ctx); err != nil {
				encrypted.Close() //nolint:errcheck // nothing was usable; the close is cleanup
				return nil, err
			}
			return encrypted, nil
		}
	}

	client := redis.NewClient(options)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close() //nolint:errcheck // nothing was usable; the close is cleanup
		return nil, redisError(err)
	}
	return &redisConn{client: client}, nil
}

// redisConn is one Profile's connection pool. The pool is private: callers get
// only what the Conn interface exposes.
type redisConn struct {
	client *redis.Client
}

func (c *redisConn) Ping(ctx context.Context) error {
	if err := c.client.Ping(ctx).Err(); err != nil {
		return redisError(err)
	}
	return nil
}

// ReadQuery runs one command and returns its reply as the Result union's Value
// arm — Redis answers with one typed value, and that is the whole of what this
// Engine ever tags them (ADR-0006).
//
// It classifies the command here, at the connection, and that is the point of
// the method rather than a duplicated check. MySQL's ReadQuery can hand the
// database a statement it only believes is a read because the READ ONLY
// transaction checks the claim too; Redis has no such transaction, and
// ADR-0006 accepts that by making the classifier the only layer. So this
// adapter is its own second layer: whatever the Approval Gate decided upstream,
// nothing writes through this method, because the same verdict is taken again
// with the command that is about to run rather than one that resembles it.
//
// A command the classifier calls a Mutation comes back wrapping ErrWriteAttempt
// — the same outcome MySQL produces when the database refuses a write, and the
// same reroute: the caller takes the command back to the gate rather than
// showing a failure.
//
// The order is deliberate. Classification happens before tokenizing, so a
// command that cannot be tokenized has already been judged, and the tokens the
// classifier read — the command, and a container's subcommand — are the tokens
// that reach the server. That agreement is not luck: any quoting in either
// token puts a character in it the classifier refuses outright, so a line where
// the two could disagree never gets this far.
//
// Implementations must not interpolate anything into command; it is executed
// exactly as given.
func (c *redisConn) ReadQuery(ctx context.Context, command string) (Result, error) {
	if verdict := guard.ClassifyRedis(command); !verdict.IsRead() {
		return Result{}, fmt.Errorf("%w: %s", ErrWriteAttempt, verdict.Reason)
	}

	arguments, err := redisArguments(command)
	if err != nil {
		return Result{}, err
	}

	reply, err := c.client.Do(ctx, arguments...).Result()
	if err != nil {
		// A nil reply is an answer, not a failure: the key was not there. The
		// client library reports it as an error, and this is the one place
		// that reading is corrected.
		if errors.Is(err, redis.Nil) {
			return ValueResult(Reply{Kind: ReplyNil}), nil
		}
		return Result{}, redisError(err)
	}
	return ValueResult(buildRedisReply(reply, 0)), nil
}

// Exec runs one approved mutation and reports what Redis says it changed.
//
// It does not classify, and the omission is the same one the port describes:
// this is the method the Approval Gate calls once a human has confirmed a
// specific mutation by ID, and refusing it here would refuse the very command
// that was approved. The safety is upstream, not in this function.
//
// Redis has no rows and no affected-row count. What it has is an idiom: the
// commands that change a keyspace answer with an integer counting what they
// touched — DEL and HDEL the keys and fields removed, SADD and ZADD the members
// added, LPUSH the new length. So an integer reply is reported as the affected
// count and everything else as 0, which reads SET's "OK" and a script's string
// reply the way MySQL's Exec reads DDL: the command ran, and it changed no
// number this method can report.
//
// The idiom is not a promise, and the imprecision is deliberate rather than
// hidden. INCRBY answers with the counter's new value, which is a number but
// not a count, and it is reported as it is: what Redis said, rather than a
// guess at what it meant.
//
// Implementations must not interpolate anything into command; it is executed
// exactly as given.
func (c *redisConn) Exec(ctx context.Context, command string) (int64, error) {
	arguments, err := redisArguments(command)
	if err != nil {
		return 0, err
	}

	reply, err := c.client.Do(ctx, arguments...).Result()
	if err != nil {
		// A nil reply is a command that ran and changed nothing it counts.
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, redisError(err)
	}
	if count, ok := reply.(int64); ok {
		return count, nil
	}
	return 0, nil
}

func (c *redisConn) Close() error {
	if err := c.client.Close(); err != nil {
		return fmt.Errorf("db: closing connection: %w", err)
	}
	return nil
}

// redisDatabaseIndex reads a Profile's Database as what it means for this
// Engine: Redis numbers its databases rather than naming them, so the field
// that holds a schema name for MySQL holds an index here.
//
// An empty Database is index 0, the database a client that never selects one is
// already on. Anything that is not an index is refused rather than defaulted:
// a Profile pointed at "production" is pointed somewhere, and quietly opening
// database 0 instead would put every command on the wrong keyspace while
// looking like it worked.
func redisDatabaseIndex(database string) (int, error) {
	name := strings.TrimSpace(database)
	if name == "" {
		return 0, nil
	}

	index, err := strconv.Atoi(name)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("%w: %q", errRedisDatabaseIndex, database)
	}
	return index, nil
}

var errRedisDatabaseIndex = errors.New("db: a Redis profile's database is an index — a whole number from 0 upwards — not a name")

// redisArguments tokenizes one command line into the arguments the client sends.
func redisArguments(command string) ([]any, error) {
	tokens, err := tokenizeRedisCommand(command)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return nil, errRedisNoCommand
	}

	arguments := make([]any, len(tokens))
	for i, token := range tokens {
		arguments[i] = token
	}
	return arguments, nil
}

var errRedisNoCommand = errors.New("db: there is no command to run")

// tokenizeRedisCommand splits one command line into its arguments the way Redis
// splits an inline command: runs of whitespace separate arguments, a
// double-quoted run carries escapes, and a single-quoted run carries only an
// escaped quote.
//
// It is Redis's own rules rather than a reasonable approximation of them,
// because the argument a human typed is the thing at stake — a key with a space
// in it is a different key from two keys — and the editor's line is the same
// text redis-cli would be given.
//
// Both ways of getting it wrong fail loudly, and fail before anything executes:
// a quote that never closes, and a closing quote with more text glued to it. A
// line nobody can split is not run on a best guess.
func tokenizeRedisCommand(command string) ([]string, error) {
	input := []byte(command)

	var tokens []string
	for at := 0; ; {
		for at < len(input) && isRedisSpace(input[at]) {
			at++
		}
		if at == len(input) {
			return tokens, nil
		}

		token, next, err := readRedisArgument(input, at)
		if err != nil {
			return nil, err
		}
		tokens, at = append(tokens, token), next
	}
}

// readRedisArgument reads one argument beginning at at, and returns it with the
// position just past it.
//
// A quote may open part-way through an argument, which is Redis's behaviour and
// not an accident of this loop: foo"bar baz" is the single argument foobar baz.
// Nothing else joins two runs, because a closing quote must be followed by
// whitespace or nothing.
func readRedisArgument(input []byte, at int) (string, int, error) {
	var token []byte

	for at < len(input) {
		switch c := input[at]; {
		case isRedisSpace(c):
			return string(token), at + 1, nil

		case c == '"':
			quoted, next, err := readRedisQuoted(input, at+1)
			if err != nil {
				return "", 0, err
			}
			token, at = append(token, quoted...), next

		case c == '\'':
			quoted, next, err := readRedisSingleQuoted(input, at+1)
			if err != nil {
				return "", 0, err
			}
			token, at = append(token, quoted...), next

		default:
			token, at = append(token, c), at+1
		}
	}
	return string(token), at, nil
}

// readRedisQuoted reads a double-quoted run, starting just past the opening
// quote. Escapes are Redis's: \xNN is one byte, \n \r \t \b \a are their
// characters, and any other escaped character is itself — which is how \" and
// \\ work without being named.
func readRedisQuoted(input []byte, at int) ([]byte, int, error) {
	var text []byte

	for at < len(input) {
		switch c := input[at]; {
		case c == '"':
			if at+1 < len(input) && !isRedisSpace(input[at+1]) {
				return nil, 0, errRedisQuoteRun
			}
			return text, at + 1, nil

		case c == '\\' && at+3 < len(input) && input[at+1] == 'x' && isHexDigit(input[at+2]) && isHexDigit(input[at+3]):
			text, at = append(text, hexValue(input[at+2])<<4|hexValue(input[at+3])), at+4

		case c == '\\' && at+1 < len(input):
			text, at = append(text, redisEscape(input[at+1])), at+2

		default:
			text, at = append(text, c), at+1
		}
	}
	return nil, 0, errRedisUnterminatedQuote
}

// readRedisSingleQuoted reads a single-quoted run, starting just past the
// opening quote. Only \' is an escape; every other backslash is a backslash,
// which is why a Lua script or a Windows path is easier to paste in these.
func readRedisSingleQuoted(input []byte, at int) ([]byte, int, error) {
	var text []byte

	for at < len(input) {
		switch c := input[at]; {
		case c == '\\' && at+1 < len(input) && input[at+1] == '\'':
			text, at = append(text, '\''), at+2

		case c == '\'':
			if at+1 < len(input) && !isRedisSpace(input[at+1]) {
				return nil, 0, errRedisQuoteRun
			}
			return text, at + 1, nil

		default:
			text, at = append(text, c), at+1
		}
	}
	return nil, 0, errRedisUnterminatedQuote
}

var (
	errRedisUnterminatedQuote = errors.New("db: the command has a quote that is never closed")
	errRedisQuoteRun          = errors.New("db: a closing quote must end its argument, and this one has more text glued to it")
)

func redisEscape(c byte) byte {
	switch c {
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'b':
		return '\b'
	case 'a':
		return '\a'
	}
	return c
}

// isRedisSpace reports whether c separates two arguments. It is C's isspace,
// which is what Redis's own splitter asks.
func isRedisSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// buildRedisReply renders one value from the client library as a node of the
// Value arm's tree, at depth levels of containers below the root.
//
// The type switch is the whole of what a RESP reply can arrive as. Three RESP3
// types have no kind of their own and are folded into one that carries the same
// information: a set is an array, a verbatim string is a string, and a number
// too large for an integer is its own digits — inventing three more tags for
// distinctions the client has already dropped would be inventing them for the
// renderer to ignore.
//
// An error is a value here because of where it can appear: Redis puts an error
// inside an array when one element of an answer failed and its neighbours did
// not, and dropping it would report a partial answer as a whole one. An error
// returned instead of a reply never reaches this function — that is a failed
// command, and it is returned as an error.
func buildRedisReply(value any, depth int) Reply {
	switch v := value.(type) {
	case nil:
		return Reply{Kind: ReplyNil}
	case string:
		return Reply{Kind: ReplyString, Text: v}
	case []byte:
		return Reply{Kind: ReplyString, Text: string(v)}
	case int64:
		return Reply{Kind: ReplyInteger, Integer: v}
	case float64:
		// RESP's double has three values JSON cannot write at all, and a tree
		// holding one could not be serialised — the whole answer would fail
		// over one element of it. They are carried as the very tokens RESP
		// itself uses, which is both what the server sent and what a human
		// reads. Every finite double stays a double.
		switch {
		case math.IsInf(v, 1):
			return Reply{Kind: ReplyString, Text: "inf"}
		case math.IsInf(v, -1):
			return Reply{Kind: ReplyString, Text: "-inf"}
		case math.IsNaN(v):
			return Reply{Kind: ReplyString, Text: "nan"}
		}
		return Reply{Kind: ReplyDouble, Double: v}
	case bool:
		return Reply{Kind: ReplyBoolean, Boolean: v}
	case *big.Int:
		return Reply{Kind: ReplyString, Text: v.String()}
	case []any:
		return buildRedisArray(v, depth)
	case map[any]any:
		return buildRedisMap(v, depth)
	case error:
		return Reply{Kind: ReplyError, Text: v.Error()}
	}

	// A reply type this adapter does not model. Showing what it was beats
	// failing a read that succeeded, and the Go rendering is at least the
	// truth about what arrived.
	return Reply{Kind: ReplyString, Text: fmt.Sprint(value)}
}

func buildRedisArray(items []any, depth int) Reply {
	if depth >= MaxReplyDepth {
		return Reply{Kind: ReplyArray, Truncated: true}
	}

	reply := Reply{Kind: ReplyArray, Items: make([]Reply, 0, min(len(items), MaxReplyItems))}
	for _, item := range items {
		if len(reply.Items) == MaxReplyItems {
			reply.Truncated = true
			break
		}
		reply.Items = append(reply.Items, buildRedisReply(item, depth+1))
	}
	return reply
}

// buildRedisMap builds every pair before applying the cap, rather than stopping
// at MaxReplyItems as the array does. It has to: the map arrives unordered, so
// the first thousand pairs out of it are a different thousand every time, and
// sorting is what makes the kept ones the same ones twice.
func buildRedisMap(pairs map[any]any, depth int) Reply {
	if depth >= MaxReplyDepth {
		return Reply{Kind: ReplyMap, Truncated: true}
	}

	entries := make([]ReplyEntry, 0, len(pairs))
	for key, value := range pairs {
		entries = append(entries, ReplyEntry{
			Key:   buildRedisReply(key, depth+1),
			Value: buildRedisReply(value, depth+1),
		})
	}
	slices.SortStableFunc(entries, func(a, b ReplyEntry) int {
		return strings.Compare(redisKeyOrder(a.Key), redisKeyOrder(b.Key))
	})

	if len(entries) > MaxReplyItems {
		return Reply{Kind: ReplyMap, Entries: entries[:MaxReplyItems], Truncated: true}
	}
	return Reply{Kind: ReplyMap, Entries: entries}
}

// redisKeyOrder renders a map key as the text its entry is ordered by. A RESP3
// map key is always a scalar — the client refuses a reply whose keys are not —
// so every kind that can appear here has a text form.
func redisKeyOrder(key Reply) string {
	switch key.Kind {
	case ReplyInteger:
		return strconv.FormatInt(key.Integer, 10)
	case ReplyDouble:
		return strconv.FormatFloat(key.Double, 'g', -1, 64)
	case ReplyBoolean:
		return strconv.FormatBool(key.Boolean)
	}
	return key.Text
}

// redisError classifies a failure from the client into one the caller can act
// on, the way classify does for MySQL: ErrAuthFailed when the server rejected
// the credentials, ErrUnreachable when it was never reached. Anything else
// keeps the server's own wording, which is usually the most useful thing we
// could say.
func redisError(err error) error {
	// A failure raised by the tunnel on the way out is already classified, and
	// names the hop that failed — which this adapter could not do, since it
	// does not know there is one. Its wording is the honest one; keep it.
	if errors.Is(err, ErrSSHAuthFailed) || errors.Is(err, ErrSSHHostKey) || errors.Is(err, ErrUnreachable) {
		return err
	}

	// A certificate this end would not accept is a third outcome, and it is
	// kept as one rather than folded into either of the two above: the server
	// answered, and it rejected nothing. It is checked before unreachable
	// below because a handshake failure can arrive wrapped in a network error
	// — the connection is being torn down, after all — and "unreachable" would
	// bury the one sentence that names the fix. certificateRejected says the
	// rest.
	if certificateRejected(err) {
		return fmt.Errorf("db: the server's TLS certificate was not accepted: %w", err)
	}

	// redis.Nil is an error reply as far as the client is concerned, and an
	// answer as far as this package is concerned. Callers convert it before
	// they get here; the check keeps a missed one from being reported as a
	// server error.
	var serverErr redis.Error
	if errors.As(err, &serverErr) && !errors.Is(err, redis.Nil) {
		message := serverErr.Error()
		if redisRejectedCredentials(message) {
			return fmt.Errorf("%w: %s", ErrAuthFailed, message)
		}
		return fmt.Errorf("db: %s", message)
	}

	if unreachable(err) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return fmt.Errorf("db: %w", err)
}

// redisRejectedCredentials reports whether message is Redis saying it reached
// the credentials and would not take them.
//
// NOPERM is deliberately not here. It is the server saying the credentials were
// accepted and the command was not, which is a different fix from a wrong
// password — and ErrAuthFailed exists precisely to keep those two apart.
func redisRejectedCredentials(message string) bool {
	for _, prefix := range []string{
		"WRONGPASS",            // wrong password, or a username with none
		"NOAUTH",               // the server wants a password and got none
		"ERR invalid password", // before ACLs, Redis 5 and earlier
		"ERR Client sent AUTH", // a password was given and the server has none set
		"ERR AUTH",             // the same, in Redis 6 and later's wording
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}
