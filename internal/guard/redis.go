package guard

import "strings"

// ClassifyRedis decides whether one Redis command line is provably read-only.
//
// It is the Redis Engine's classifier (ADR-0006), and it carries more weight
// than the SQL one beside it. MySQL reads execute inside a READ ONLY
// transaction that refuses a write the classifier let through; Redis has no
// equivalent, so there is no second layer here. Whatever this function calls a
// read runs unapproved, with nothing behind it.
//
// The whole of the read claim is redisReads and redisContainerReads below: a
// closed allowlist of commands proven to only read, seeded from Redis's own
// readonly/write command flags and pinned against a real server's COMMAND INFO
// by the integration test in this package. Everything else is a Mutation —
// unknown commands, misspelt commands, commands Redis adds after this table was
// written, and known commands nobody has proved yet.
//
// The future Redis adapter in internal/db calls this on its ReadQuery path to
// enforce the same verdict at the connection, as MySQL's read-only transaction
// does for SQL. That import direction is the acyclic one: this package imports
// nothing from internal/db and must keep not doing so.
func ClassifyRedis(command string) Classification {
	// Redis's inline protocol ends a command at the newline. Whitespace around
	// the whole buffer is the editor's, and is trimmed; a newline left inside
	// it means the buffer holds more than one command, and it is refused whole
	// rather than judged on its first line — judging the first line would make
	// the rest a place to hide a second command from a caller that submitted
	// the buffer as it stood.
	line := strings.TrimSpace(command)
	switch {
	case line == "":
		return mutation("there is no command to run")
	case strings.ContainsAny(line, "\n\r"):
		return mutation("the input holds more than one command")
	}

	// Only the first token, and for a container the second, decide anything.
	// Arguments are never inspected: see the note on redisTraps for why that is
	// a deliberate limit rather than an omission.
	fields := strings.Fields(line)
	name := commandName(fields[0])
	if name == "" {
		return mutation("the command is not on the list of Redis commands proven to only read")
	}

	if why, trapped := redisTraps[name]; trapped {
		return mutation(name + " command, and " + why)
	}
	if subcommands, isContainer := redisContainerReads[name]; isContainer {
		return classifyRedisPair(name, subcommands, fields)
	}
	if redisReads[name] {
		return read(name + " command")
	}
	return mutation(name + " is not on the list of Redis commands proven to only read")
}

// classifyRedisPair judges a container command by its (command, subcommand)
// pair. CONFIG GET reads and CONFIG SET does not, so the container's name alone
// says nothing, and a container that arrives without a subcommand has told us
// nothing either.
func classifyRedisPair(name string, subcommands map[string]bool, fields []string) Classification {
	if len(fields) < 2 {
		return mutation(name + " needs a subcommand, and only some of them are proven to only read")
	}
	sub := commandName(fields[1])
	switch {
	case sub == "":
		return mutation("that " + name + " subcommand is not on the list of " + name + " subcommands proven to only read")
	case subcommands[sub]:
		return read(name + " " + sub + " command")
	}
	return mutation(name + " " + sub + " is not on the list of " + name + " subcommands proven to only read")
}

// commandName renders a token as the command name a human would recognise, and
// returns "" for a token that is not one. Redis is case-insensitive about
// command names, so the table is uppercase and the token is folded to match it.
//
// The length and character limits are about the sentence, not about safety: a
// rejected command's name is quoted back into the editor badge and the audit
// log, and a pasted kilobyte or a line of control characters is not something to
// echo there. An unusable token is refused either way — the reason simply stops
// naming it.
func commandName(token string) string {
	const longestCommandName = 32 // GEORADIUSBYMEMBER_RO, the longest here, is 20

	if len(token) > longestCommandName {
		return ""
	}
	for _, r := range token {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return ""
		}
	}
	return strings.ToUpper(token)
}

// redisReads is the closed allowlist of plain Redis commands proven to only
// read. Membership is the entire read claim; a command absent from here waits
// at the Approval Gate.
//
// Three rules govern what may be added, and the integration test in this
// package enforces the first two against a real Redis 7:
//
//  1. The entry carries Redis's own readonly flag and neither write, blocking
//     nor pubsub — except for the handful of introspection commands that carry
//     no flags at all (PING, ECHO, INFO), which are listed on the separate
//     ground that they touch no keyspace and are named in the test's exemption
//     list.
//
//  2. The command's write-ness does not depend on its arguments. This is the
//     rule that keeps the table provable by flags alone, and it is why SORT,
//     GEORADIUS, GEORADIUSBYMEMBER, BITFIELD, GETEX and XREAD are absent while
//     their _RO twins are present. Do not "fix" that by scanning arguments for
//     STORE: an argument scanner is a parser for a grammar Redis does not
//     publish, it would have to be right about every option of every command
//     forever, and being wrong once is a lost key. The _RO commands exist
//     precisely so this table does not have to.
//
//  3. The command does not block or change the connection's mode. The editor
//     shares one connection, and a command the gate waved through must not be
//     able to wedge it or leave it in subscriber, transaction or another
//     database. Those are listed in redisTraps rather than merely omitted, so
//     the human is told why.
//
// Scope is a client's day-to-day browsing: fetch a value, size a collection,
// page through a keyspace. Readonly commands outside that are deliberately
// absent — random sampling (SRANDMEMBER, HRANDFIELD, ZRANDMEMBER), serialisation
// (DUMP), string algorithms (LCS), server-enforced read-only scripting
// (EVAL_RO, FCALL_RO) — because adding one later is cheap and removing one
// later is an incident.
//
// Two readonly-flagged commands are held out on judgement rather than on flags,
// because their side effect is inside the key where COMMAND INFO does not look:
// PFCOUNT rewrites the cached cardinality in the HyperLogLog header, and TOUCH
// updates the idle time of every key it names. Both are in redisTraps.
var redisReads = map[string]bool{
	// Strings and bitmaps. GETEX and GETDEL are not here; GETEX rewrites the
	// expiry and GETDEL is a write outright.
	"GET":         true,
	"GETRANGE":    true,
	"MGET":        true,
	"STRLEN":      true,
	"GETBIT":      true,
	"BITCOUNT":    true,
	"BITPOS":      true,
	"BITFIELD_RO": true, // BITFIELD itself takes SET and INCRBY

	// Keyspace introspection. KEYS is O(N) and can stall a busy server, which
	// is a performance question rather than a safety one: it writes nothing.
	"EXISTS":      true,
	"TYPE":        true,
	"TTL":         true,
	"PTTL":        true,
	"EXPIRETIME":  true,
	"PEXPIRETIME": true,
	"SCAN":        true,
	"KEYS":        true,
	"RANDOMKEY":   true,
	"DBSIZE":      true,

	// Hashes.
	"HGET":    true,
	"HMGET":   true,
	"HGETALL": true,
	"HKEYS":   true,
	"HVALS":   true,
	"HLEN":    true,
	"HEXISTS": true,
	"HSTRLEN": true,
	"HSCAN":   true,

	// Lists. Every popping and blocking form writes; none is here.
	"LRANGE": true,
	"LLEN":   true,
	"LINDEX": true,
	"LPOS":   true,

	// Sets. The set-algebra commands are safe to list because their storing
	// forms are separately named commands — SINTERSTORE, SUNIONSTORE,
	// SDIFFSTORE — rather than an option on these, which is rule 2 satisfied by
	// Redis's own naming.
	"SMEMBERS":   true,
	"SCARD":      true,
	"SISMEMBER":  true,
	"SMISMEMBER": true,
	"SSCAN":      true,
	"SINTER":     true,
	"SUNION":     true,
	"SDIFF":      true,
	"SINTERCARD": true,

	// Sorted sets. ZRANGESTORE, ZINTERSTORE, ZUNIONSTORE and ZDIFFSTORE are the
	// writing forms and are separate commands, so the same reasoning holds.
	"ZRANGE":           true,
	"ZRANGEBYSCORE":    true,
	"ZRANGEBYLEX":      true,
	"ZREVRANGE":        true,
	"ZREVRANGEBYSCORE": true,
	"ZREVRANGEBYLEX":   true,
	"ZRANK":            true,
	"ZREVRANK":         true,
	"ZSCORE":           true,
	"ZMSCORE":          true,
	"ZCARD":            true,
	"ZCOUNT":           true,
	"ZLEXCOUNT":        true,
	"ZSCAN":            true,
	"ZDIFF":            true,
	"ZINTER":           true,
	"ZUNION":           true,
	"ZINTERCARD":       true,

	// Streams, read side. XREAD is absent although Redis flags it readonly: its
	// optional BLOCK argument is rule 2 and rule 3 at once.
	"XRANGE":    true,
	"XREVRANGE": true,
	"XLEN":      true,

	// Geo. GEOSEARCHSTORE is the storing form of GEOSEARCH and is a separate
	// command; GEORADIUS and GEORADIUSBYMEMBER instead take STORE and STOREDIST
	// as options, which is why only their _RO twins are here.
	"GEOPOS":               true,
	"GEODIST":              true,
	"GEOHASH":              true,
	"GEOSEARCH":            true,
	"GEORADIUS_RO":         true,
	"GEORADIUSBYMEMBER_RO": true,

	// Generic. SORT_RO is SORT without the STORE option.
	"SORT_RO": true,

	// Server and connection introspection. These three carry no readonly flag
	// because they are not keyspace commands; they are listed on the ground
	// that they write nothing at all, and the integration test exempts exactly
	// these three by name.
	"PING": true,
	"ECHO": true,
	"INFO": true,
}

// redisContainerReads is the allowlist for container commands, which are judged
// by the (command, subcommand) pair. The container's name alone decides
// nothing: CONFIG GET reads and CONFIG SET reconfigures the server; CLIENT INFO
// reads and CLIENT KILL disconnects someone. A container with a subcommand that
// is not here, or with no subcommand at all, is a Mutation.
//
// Most of these subcommands carry no readonly flag — they are admin or
// connection commands rather than keyspace ones — so the integration test pins
// them on the weaker but still checkable claim that they exist and carry
// neither write, blocking nor pubsub. OBJECT, XINFO and MEMORY USAGE do carry
// readonly, and are checked the same way as the plain entries.
var redisContainerReads = map[string]map[string]bool{
	// CONFIG SET, RESETSTAT and REWRITE all change the server; only GET reads.
	"CONFIG": {"GET": true},

	// CLIENT KILL, UNPAUSE, NO-EVICT and SETNAME all act on connections.
	"CLIENT": {"LIST": true, "INFO": true, "ID": true, "GETNAME": true},

	// OBJECT's read subcommands report how a key is stored. HELP is omitted
	// like every other unlisted subcommand: harmless, but the rule is that the
	// pair is proved, not the container.
	"OBJECT": {"ENCODING": true, "REFCOUNT": true, "IDLETIME": true, "FREQ": true},

	// XINFO is the stream read side that XREAD's BLOCK argument rules out.
	"XINFO": {"STREAM": true, "GROUPS": true, "CONSUMERS": true},

	// COMMAND's introspection subcommands. Bare COMMAND lists everything and
	// would be harmless, but it arrives here as a container with no
	// subcommand, and that is a Mutation by the same rule as every other.
	// COMMAND GETKEYS is left out: it parses a command line we did not judge.
	"COMMAND": {"DOCS": true, "COUNT": true, "INFO": true, "LIST": true},

	// MEMORY PURGE releases memory back to the allocator; the rest report.
	"MEMORY": {"USAGE": true, "STATS": true, "DOCTOR": true},

	// LATENCY RESET clears the recorded events, which is a write to what the
	// next reader would have seen.
	"LATENCY": {"LATEST": true, "HISTORY": true, "DOCTOR": true},

	// ACL SETUSER, DELUSER, LOAD and SAVE change who may do what.
	"ACL": {"WHOAMI": true, "CAT": true, "LIST": true, "USERS": true, "GETUSER": true},
}

// redisTraps names the commands that would otherwise be refused with nothing
// but "not on the list", and says what the human missed. Every one of them is
// a command a reasonable person would call a read.
//
// Each value completes the sentence "<COMMAND> command, and ...". Being listed
// here changes nothing about the verdict — absence from redisReads is what
// makes these mutations, and this map is consulted first only so the wording
// wins. Deleting an entry would lose the explanation, not the safety.
var redisTraps = map[string]string{
	// Rule 2: the write is in an argument, so the command cannot be judged by
	// its name. The _RO twins are the answer, and they are on the allowlist.
	"SORT":              "its STORE option writes a key; SORT_RO is the form proven to only read",
	"GEORADIUS":         "its STORE and STOREDIST options write a key; GEORADIUS_RO is the form proven to only read",
	"GEORADIUSBYMEMBER": "its STORE and STOREDIST options write a key; GEORADIUSBYMEMBER_RO is the form proven to only read",
	"BITFIELD":          "its SET and INCRBY operations write; BITFIELD_RO is the form proven to only read",
	"GETEX":             "it rewrites the key's expiry",
	"XREAD":             "its BLOCK option holds the connection open until data arrives",
	"XREADGROUP":        "it writes the consumer group's delivery state, and its BLOCK option holds the connection open",

	// Rule 3: blocking. The editor shares one connection, and a command that
	// waits on it takes the editor with it.
	"BLPOP":      blockingTake,
	"BRPOP":      blockingTake,
	"BLMPOP":     blockingTake,
	"BZPOPMIN":   blockingTake,
	"BZPOPMAX":   blockingTake,
	"BZMPOP":     blockingTake,
	"BLMOVE":     "it blocks the connection until an element is there, then moves it",
	"BRPOPLPUSH": "it blocks the connection until an element is there, then moves it",
	"WAIT":       "it blocks the connection until replicas have acknowledged the writes before it",
	"WAITAOF":    "it blocks the connection until the log has been written to disk",

	// Rule 3: mode. Each of these leaves the connection somewhere other than
	// where the next command expects to find it.
	"SUBSCRIBE":    subscriberMode,
	"PSUBSCRIBE":   subscriberMode,
	"SSUBSCRIBE":   subscriberMode,
	"UNSUBSCRIBE":  "it changes the connection's subscriber mode",
	"PUNSUBSCRIBE": "it changes the connection's subscriber mode",
	"SUNSUBSCRIBE": "it changes the connection's subscriber mode",
	"MONITOR":      "it turns the connection into a firehose of every command the server runs",
	"MULTI":        "it opens a transaction, and the commands queued inside one are never classified",
	"EXEC":         "it runs everything queued in the transaction",
	"DISCARD":      "it changes the connection's transaction state",
	"WATCH":        "it makes the connection watch keys until a transaction ends",
	"UNWATCH":      "it changes the connection's watch state",
	"SELECT":       "it changes which database this connection is on, and the editor shares it",

	// Readonly-flagged, and still not reads: the side effect is inside the key,
	// where Redis's flags do not look.
	"PFCOUNT": "it rewrites the cached cardinality inside the HyperLogLog it counts",
	"TOUCH":   "it updates the idle time of every key it names",
}

const (
	blockingTake   = "it blocks the connection until an element is there, then removes it"
	subscriberMode = "it puts the connection into subscriber mode, where ordinary replies stop arriving"
)
