package guard_test

import (
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of the Redis half of the Approval
// Gate's safety claim, and it carries more weight than its SQL sibling: Redis
// has no read-only transaction to back the classifier up (ADR-0006), so a
// command called a read here runs with nothing behind it. Every verdict is
// therefore asymmetric in the same direction — calling a read a mutation costs
// a keypress, calling a mutation a read loses data — and when a case is
// arguable it is Mutation.
//
// Reasons are asserted where the wording is the point: the traps in this table
// are commands a reasonable person would call reads, so the line the human is
// shown has to say what they missed.

type redisCase struct {
	name    string
	command string
	want    guard.Kind
	// reason, when set, is a substring the Reason must contain.
	reason string
}

func TestClassifyRedisReads(t *testing.T) {
	runRedisCases(t, []redisCase{
		// Strings and bitmaps.
		{name: "get", command: "GET user:1", want: guard.Read, reason: "GET"},
		{name: "get with no argument", command: "GET", want: guard.Read},
		{name: "lowercase get", command: "get user:1", want: guard.Read, reason: "GET"},
		{name: "mixed case and surrounding whitespace", command: "  GeT   user:1  ", want: guard.Read, reason: "GET"},
		{name: "get with a spaced-out argument list", command: "MGET a b c", want: guard.Read, reason: "MGET"},
		{name: "getrange", command: "GETRANGE user:1 0 -1", want: guard.Read},
		{name: "strlen", command: "STRLEN user:1", want: guard.Read},
		{name: "getbit", command: "GETBIT flags 7", want: guard.Read},
		{name: "bitcount", command: "BITCOUNT flags", want: guard.Read},
		{name: "bitpos", command: "BITPOS flags 1", want: guard.Read},

		// Keyspace introspection.
		{name: "exists", command: "EXISTS user:1 user:2", want: guard.Read},
		{name: "type", command: "TYPE user:1", want: guard.Read},
		{name: "ttl", command: "TTL user:1", want: guard.Read},
		{name: "pttl", command: "PTTL user:1", want: guard.Read},
		{name: "expiretime", command: "EXPIRETIME user:1", want: guard.Read},
		{name: "scan with options", command: "SCAN 0 MATCH user:* COUNT 100 TYPE hash", want: guard.Read, reason: "SCAN"},
		{name: "keys", command: "KEYS *", want: guard.Read},
		{name: "randomkey", command: "RANDOMKEY", want: guard.Read},
		{name: "dbsize", command: "DBSIZE", want: guard.Read},

		// Hashes.
		{name: "hget", command: "HGET user:1 name", want: guard.Read},
		{name: "hgetall", command: "HGETALL user:1", want: guard.Read, reason: "HGETALL"},
		{name: "hmget", command: "HMGET user:1 name email", want: guard.Read},
		{name: "hkeys", command: "HKEYS user:1", want: guard.Read},
		{name: "hvals", command: "HVALS user:1", want: guard.Read},
		{name: "hlen", command: "HLEN user:1", want: guard.Read},
		{name: "hexists", command: "HEXISTS user:1 name", want: guard.Read},
		{name: "hstrlen", command: "HSTRLEN user:1 name", want: guard.Read},
		{name: "hscan", command: "HSCAN user:1 0 COUNT 10", want: guard.Read},

		// Lists.
		{name: "lrange", command: "LRANGE queue 0 -1", want: guard.Read, reason: "LRANGE"},
		{name: "llen", command: "LLEN queue", want: guard.Read},
		{name: "lindex", command: "LINDEX queue 0", want: guard.Read},
		{name: "lpos", command: "LPOS queue a", want: guard.Read},

		// Sets. The STORE forms are separately named commands (SINTERSTORE and
		// friends), which is what makes these safe to list at all.
		{name: "smembers", command: "SMEMBERS tags", want: guard.Read, reason: "SMEMBERS"},
		{name: "scard", command: "SCARD tags", want: guard.Read},
		{name: "sismember", command: "SISMEMBER tags a", want: guard.Read},
		{name: "smismember", command: "SMISMEMBER tags a b", want: guard.Read},
		{name: "sscan", command: "SSCAN tags 0", want: guard.Read},
		{name: "sinter", command: "SINTER a b", want: guard.Read},
		{name: "sunion", command: "SUNION a b", want: guard.Read},
		{name: "sdiff", command: "SDIFF a b", want: guard.Read},
		{name: "sintercard", command: "SINTERCARD 2 a b LIMIT 10", want: guard.Read},

		// Sorted sets.
		{name: "zrange", command: "ZRANGE board 0 -1 WITHSCORES", want: guard.Read, reason: "ZRANGE"},
		{name: "zrangebyscore", command: "ZRANGEBYSCORE board 0 100", want: guard.Read},
		{name: "zrevrange", command: "ZREVRANGE board 0 -1", want: guard.Read},
		{name: "zcard", command: "ZCARD board", want: guard.Read},
		{name: "zcount", command: "ZCOUNT board 0 100", want: guard.Read},
		{name: "zscore", command: "ZSCORE board alice", want: guard.Read},
		{name: "zrank", command: "ZRANK board alice", want: guard.Read},
		{name: "zscan", command: "ZSCAN board 0", want: guard.Read},
		{name: "zdiff", command: "ZDIFF 2 a b", want: guard.Read},
		{name: "zintercard", command: "ZINTERCARD 2 a b", want: guard.Read},

		// Streams, read side only.
		{name: "xrange", command: "XRANGE events - +", want: guard.Read, reason: "XRANGE"},
		{name: "xrevrange", command: "XREVRANGE events + -", want: guard.Read},
		{name: "xlen", command: "XLEN events", want: guard.Read},

		// Geo. The two commands whose write-ness depends on their arguments are
		// out; their _RO twins are the entries.
		{name: "geopos", command: "GEOPOS places a", want: guard.Read},
		{name: "geodist", command: "GEODIST places a b", want: guard.Read},
		{name: "geohash", command: "GEOHASH places a", want: guard.Read},
		{name: "geosearch", command: "GEOSEARCH places FROMMEMBER a BYRADIUS 5 km ASC", want: guard.Read},
		{name: "georadius_ro", command: "GEORADIUS_RO places 15 37 200 km", want: guard.Read, reason: "GEORADIUS_RO"},
		{name: "georadiusbymember_ro", command: "GEORADIUSBYMEMBER_RO places a 200 km", want: guard.Read},

		// The _RO forms of the argument-dependent commands.
		{name: "sort_ro", command: "SORT_RO queue LIMIT 0 10 ALPHA", want: guard.Read, reason: "SORT_RO"},
		{name: "bitfield_ro", command: "BITFIELD_RO counters GET u8 0", want: guard.Read, reason: "BITFIELD_RO"},

		// Server and connection introspection. These carry no readonly flag —
		// they are not keyspace commands at all — and are listed on the
		// separate ground that they write nothing.
		{name: "ping", command: "PING", want: guard.Read, reason: "PING"},
		{name: "ping with a message", command: "PING hello", want: guard.Read},
		{name: "echo", command: "ECHO hello", want: guard.Read},
		{name: "info", command: "INFO", want: guard.Read},
		{name: "info section", command: "INFO memory", want: guard.Read},

		// Container commands, classified by the (command, subcommand) pair.
		{name: "config get", command: "CONFIG GET maxmemory", want: guard.Read, reason: "CONFIG GET"},
		{name: "config get lowercase", command: "config get maxmemory", want: guard.Read, reason: "CONFIG GET"},
		{name: "client list", command: "CLIENT LIST", want: guard.Read, reason: "CLIENT LIST"},
		{name: "client info", command: "CLIENT INFO", want: guard.Read, reason: "CLIENT INFO"},
		{name: "client id", command: "CLIENT ID", want: guard.Read},
		{name: "client getname", command: "CLIENT GETNAME", want: guard.Read},
		{name: "object encoding", command: "OBJECT ENCODING user:1", want: guard.Read, reason: "OBJECT ENCODING"},
		{name: "object refcount", command: "OBJECT REFCOUNT user:1", want: guard.Read},
		{name: "object idletime", command: "OBJECT IDLETIME user:1", want: guard.Read},
		{name: "object freq", command: "OBJECT FREQ user:1", want: guard.Read},
		{name: "xinfo stream", command: "XINFO STREAM events", want: guard.Read, reason: "XINFO STREAM"},
		{name: "xinfo groups", command: "XINFO GROUPS events", want: guard.Read},
		{name: "xinfo consumers", command: "XINFO CONSUMERS events g", want: guard.Read},
		{name: "command docs", command: "COMMAND DOCS GET", want: guard.Read, reason: "COMMAND DOCS"},
		{name: "command count", command: "COMMAND COUNT", want: guard.Read},
		{name: "command info", command: "COMMAND INFO GET", want: guard.Read},
		{name: "command list", command: "COMMAND LIST", want: guard.Read},
		{name: "memory usage", command: "MEMORY USAGE user:1", want: guard.Read, reason: "MEMORY USAGE"},
		{name: "memory stats", command: "MEMORY STATS", want: guard.Read},
		{name: "memory doctor", command: "MEMORY DOCTOR", want: guard.Read},
		{name: "latency latest", command: "LATENCY LATEST", want: guard.Read},
		{name: "latency history", command: "LATENCY HISTORY event", want: guard.Read},
		{name: "acl whoami", command: "ACL WHOAMI", want: guard.Read},
		{name: "acl cat", command: "ACL CAT", want: guard.Read},
		{name: "acl getuser", command: "ACL GETUSER default", want: guard.Read},
	})
}

func TestClassifyRedisMutations(t *testing.T) {
	runRedisCases(t, []redisCase{
		// Plain writes, for the floor: nothing that writes is on the list.
		{name: "set", command: "SET user:1 alice", want: guard.Mutation, reason: "SET"},
		{name: "lowercase del", command: "del user:1", want: guard.Mutation, reason: "DEL"},
		{name: "getdel", command: "GETDEL user:1", want: guard.Mutation},
		{name: "getset", command: "GETSET user:1 bob", want: guard.Mutation},
		{name: "incr", command: "INCR counter", want: guard.Mutation},
		{name: "expire", command: "EXPIRE user:1 60", want: guard.Mutation},
		{name: "rename", command: "RENAME a b", want: guard.Mutation},
		{name: "hset", command: "HSET user:1 name alice", want: guard.Mutation},
		{name: "lpush", command: "LPUSH queue a", want: guard.Mutation},
		{name: "zadd", command: "ZADD board 1 alice", want: guard.Mutation},
		{name: "xadd", command: "XADD events * a 1", want: guard.Mutation},
		{name: "sinterstore", command: "SINTERSTORE dest a b", want: guard.Mutation},
		{name: "zrangestore", command: "ZRANGESTORE dest board 0 -1", want: guard.Mutation},
		{name: "geosearchstore", command: "GEOSEARCHSTORE dest places FROMMEMBER a BYRADIUS 5 km", want: guard.Mutation},
		{name: "flushall", command: "FLUSHALL", want: guard.Mutation, reason: "FLUSHALL"},
		{name: "flushdb", command: "FLUSHDB", want: guard.Mutation},
		{name: "shutdown", command: "SHUTDOWN NOSAVE", want: guard.Mutation},
		{name: "debug", command: "DEBUG SLEEP 100", want: guard.Mutation},
		{name: "eval", command: "EVAL return 1 0", want: guard.Mutation},

		// Principle 2: a command whose write-ness depends on its arguments is
		// never a read, even when the arguments in front of us are harmless.
		// The table is provable from Redis's own flags alone, and scanning
		// arguments for STORE tokens is exactly what it refuses to do.
		{name: "sort without store is still not a read", command: "SORT queue ALPHA", want: guard.Mutation, reason: "STORE"},
		{name: "sort with store", command: "SORT queue STORE dest", want: guard.Mutation, reason: "SORT_RO"},
		{name: "lowercase sort", command: "sort queue", want: guard.Mutation, reason: "STORE"},
		{name: "georadius", command: "GEORADIUS places 15 37 200 km", want: guard.Mutation, reason: "GEORADIUS_RO"},
		{name: "georadius with store", command: "GEORADIUS places 15 37 200 km STORE dest", want: guard.Mutation, reason: "STORE"},
		{name: "georadiusbymember", command: "GEORADIUSBYMEMBER places a 200 km", want: guard.Mutation, reason: "GEORADIUSBYMEMBER_RO"},
		{name: "bitfield reading only", command: "BITFIELD counters GET u8 0", want: guard.Mutation, reason: "BITFIELD_RO"},
		{name: "bitfield with set", command: "BITFIELD counters SET u8 0 1", want: guard.Mutation, reason: "SET"},
		{name: "getex", command: "GETEX user:1", want: guard.Mutation, reason: "expiry"},
		{name: "getex with persist", command: "GETEX user:1 PERSIST", want: guard.Mutation, reason: "expiry"},

		// XREAD carries Redis's readonly flag and is still out: its optional
		// BLOCK argument is the same argument-dependence, applied to the
		// connection rather than to a key.
		{name: "xread", command: "XREAD COUNT 2 STREAMS events 0", want: guard.Mutation, reason: "BLOCK"},
		{name: "xread with block", command: "XREAD BLOCK 0 STREAMS events $", want: guard.Mutation, reason: "BLOCK"},
		{name: "xreadgroup", command: "XREADGROUP GROUP g c STREAMS events >", want: guard.Mutation},

		// Principle 4: blocking commands wedge a shared editor connection.
		{name: "blpop", command: "BLPOP queue 0", want: guard.Mutation, reason: "blocks the connection"},
		{name: "brpop", command: "BRPOP queue 0", want: guard.Mutation, reason: "blocks the connection"},
		{name: "blmove", command: "BLMOVE src dst LEFT RIGHT 0", want: guard.Mutation, reason: "blocks the connection"},
		{name: "blmpop", command: "BLMPOP 0 1 queue LEFT", want: guard.Mutation, reason: "blocks the connection"},
		{name: "brpoplpush", command: "BRPOPLPUSH src dst 0", want: guard.Mutation, reason: "blocks the connection"},
		{name: "bzpopmin", command: "BZPOPMIN board 0", want: guard.Mutation, reason: "blocks the connection"},
		{name: "bzmpop", command: "BZMPOP 0 1 board MIN", want: guard.Mutation, reason: "blocks the connection"},
		{name: "wait", command: "WAIT 1 100", want: guard.Mutation, reason: "blocks the connection"},
		{name: "waitaof", command: "WAITAOF 1 0 100", want: guard.Mutation, reason: "blocks the connection"},

		// Principle 4: commands that mode-switch the connection.
		{name: "subscribe", command: "SUBSCRIBE news", want: guard.Mutation, reason: "subscriber mode"},
		{name: "psubscribe", command: "PSUBSCRIBE news.*", want: guard.Mutation, reason: "subscriber mode"},
		{name: "ssubscribe", command: "SSUBSCRIBE news", want: guard.Mutation, reason: "subscriber mode"},
		{name: "unsubscribe", command: "UNSUBSCRIBE news", want: guard.Mutation, reason: "subscriber mode"},
		{name: "monitor", command: "MONITOR", want: guard.Mutation, reason: "connection"},
		{name: "multi", command: "MULTI", want: guard.Mutation, reason: "transaction"},
		{name: "exec", command: "EXEC", want: guard.Mutation, reason: "transaction"},
		{name: "discard", command: "DISCARD", want: guard.Mutation, reason: "transaction"},
		{name: "watch", command: "WATCH user:1", want: guard.Mutation, reason: "watch"},
		{name: "unwatch", command: "UNWATCH", want: guard.Mutation, reason: "watch"},

		// SELECT reads nothing and writes nothing, and changes which database
		// every command after it lands on — on a connection the editor shares.
		{name: "select", command: "SELECT 3", want: guard.Mutation, reason: "which database"},

		// Readonly-flagged commands left out on judgement rather than on flags:
		// their side effects are inside the key or its metadata, where
		// COMMAND INFO does not look.
		{name: "pfcount", command: "PFCOUNT hll", want: guard.Mutation, reason: "cached cardinality"},
		{name: "touch", command: "TOUCH user:1", want: guard.Mutation, reason: "idle time"},

		// Principle 3: a container command is judged by the pair, and an
		// unlisted or missing subcommand is a mutation.
		{name: "config set", command: "CONFIG SET maxmemory 0", want: guard.Mutation, reason: "CONFIG SET"},
		{name: "config resetstat", command: "CONFIG RESETSTAT", want: guard.Mutation, reason: "subcommand"},
		{name: "config rewrite", command: "CONFIG REWRITE", want: guard.Mutation},
		{name: "config with no subcommand", command: "CONFIG", want: guard.Mutation, reason: "needs a subcommand"},
		{name: "client kill", command: "CLIENT KILL ID 4", want: guard.Mutation, reason: "CLIENT KILL"},
		{name: "client no-evict", command: "CLIENT NO-EVICT on", want: guard.Mutation},
		{name: "client with no subcommand", command: "CLIENT", want: guard.Mutation, reason: "needs a subcommand"},
		{name: "object help", command: "OBJECT HELP", want: guard.Mutation, reason: "OBJECT HELP"},
		{name: "object with no subcommand", command: "OBJECT", want: guard.Mutation, reason: "needs a subcommand"},
		{name: "xinfo with no subcommand", command: "XINFO", want: guard.Mutation, reason: "needs a subcommand"},
		{name: "command with no subcommand", command: "COMMAND", want: guard.Mutation, reason: "needs a subcommand"},
		{name: "command getkeys", command: "COMMAND GETKEYS GET k", want: guard.Mutation},
		{name: "memory purge", command: "MEMORY PURGE", want: guard.Mutation, reason: "MEMORY PURGE"},
		{name: "latency reset", command: "LATENCY RESET", want: guard.Mutation, reason: "LATENCY RESET"},
		{name: "acl setuser", command: "ACL SETUSER bob on", want: guard.Mutation, reason: "ACL SETUSER"},
		{name: "acl deluser", command: "ACL DELUSER bob", want: guard.Mutation},

		// A read command is not a subcommand of anything: pairing one with a
		// container name does not make the container listed.
		{name: "container name with a read subcommand", command: "MEMORY GET", want: guard.Mutation, reason: "MEMORY GET"},

		// Unknown commands, which is what a typo and a Redis 8 addition look
		// like from here. Both wait at the gate.
		{name: "unknown command", command: "FOOBAR", want: guard.Mutation, reason: "FOOBAR"},
		{name: "unknown command with arguments", command: "NOTACOMMAND a b", want: guard.Mutation, reason: "NOTACOMMAND"},
		{name: "a read's name with a typo", command: "GTE user:1", want: guard.Mutation, reason: "GTE"},
		{name: "prefix of a read", command: "GE user:1", want: guard.Mutation},

		// Nothing to run.
		{name: "empty", command: "", want: guard.Mutation, reason: "no command"},
		{name: "whitespace only", command: "   \t  ", want: guard.Mutation, reason: "no command"},
		{name: "newline only", command: "\n", want: guard.Mutation, reason: "no command"},

		// Redis's inline protocol ends a command at the newline, so a buffer
		// with a newline in it is more than one command. It is refused whole
		// rather than judged on its first line, which would be a way to smuggle
		// the second one past a caller that submitted the buffer as it stood.
		{name: "read then a write", command: "GET user:1\nFLUSHALL", want: guard.Mutation, reason: "more than one command"},
		{name: "two reads are still two commands", command: "GET a\nGET b", want: guard.Mutation, reason: "more than one command"},
		{name: "carriage returns count too", command: "GET a\r\nDEL b", want: guard.Mutation, reason: "more than one command"},
		{name: "a read wrapped across lines", command: "GET\nuser:1", want: guard.Mutation, reason: "more than one command"},
		{name: "trailing newline is just whitespace", command: "FLUSHALL\n", want: guard.Mutation, reason: "FLUSHALL"},

		// A command name is not somewhere to put arbitrary text, so an
		// unusable token is refused without being echoed back into the badge.
		{name: "punctuation for a command", command: "!!! a b", want: guard.Mutation, reason: "not on the list"},
		{name: "a very long first token", command: strings.Repeat("A", 200), want: guard.Mutation, reason: "not on the list"},
		{name: "an unusable subcommand", command: "CONFIG !!!", want: guard.Mutation, reason: "subcommand"},
	})
}

// TestClassifyRedisTrailingNewlineOnAReadIsTolerated pins the one place the
// newline rule bends: whitespace around the whole line, newline included, is
// trimmed before anything else, so the editor's trailing newline does not turn
// a read into a mutation.
func TestClassifyRedisTrailingNewlineOnAReadIsTolerated(t *testing.T) {
	for _, command := range []string{"GET user:1\n", "\nGET user:1", "\n  GET user:1  \r\n"} {
		if got := guard.ClassifyRedis(command); !got.IsRead() {
			t.Errorf("ClassifyRedis(%q) = %q (%s), want a read", command, got.Kind, got.Reason)
		}
	}
}

func runRedisCases(t *testing.T, cases []redisCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.ClassifyRedis(tc.command)

			if got.Kind != tc.want {
				t.Errorf("ClassifyRedis(%q).Kind = %q, want %q (reason: %s)", tc.command, got.Kind, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Fatalf("ClassifyRedis(%q) gave no reason; the human is told why their command was stopped", tc.command)
			}
			if strings.ContainsAny(got.Reason, "\n\r") {
				t.Errorf("reason spans lines, want one line of prose:\n%s", got.Reason)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("ClassifyRedis(%q).Reason = %q, want it to mention %q", tc.command, got.Reason, tc.reason)
			}
			if got.IsRead() != (tc.want == guard.Read) {
				t.Errorf("IsRead() = %v, disagrees with Kind %q", got.IsRead(), got.Kind)
			}
		})
	}
}
