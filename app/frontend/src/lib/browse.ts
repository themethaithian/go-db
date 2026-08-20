// The statement that browses one thing picked in the Database tree.
//
// Clicking a row in the tree runs a read, and which read that is, is the
// Engine's to say (ADR-0006): a MySQL table is browsed with a SELECT, a MongoDB
// collection with a find, and a Redis key with whichever command reads the type
// it turns out to be. All three go through RunQuery like anything typed by
// hand — the gate classifies them, the audit log records them, and the panel
// shows the statement it ran underneath the results.
//
// It is a module rather than three expressions in the Explorer for mutate.ts's
// reason: the part that must not be wrong is putting a name into a command
// line, and it is worth keeping somewhere it can be read in a sitting with
// nothing reactive anywhere near it.

/** The Engine names the backend uses, as the tree and the Explorer see them. */
export const MYSQL = "mysql";
export const REDIS = "redis";
export const MONGODB = "mongodb";

/** How many elements of a Redis collection a browse reads. */
const REDIS_BROWSE_LIMIT = 1000;

/**
 * How many documents a MongoDB collection browse reads, and the steps "Load
 * more" walks up.
 *
 * It starts small on purpose. A collection browse used to ask for everything
 * and let the adapter cap it at MaxRows, which meant a thousand documents came
 * across the wire and a thousand cards were built before the pane could be
 * scrolled — the first screenful of a browse costs a thousand documents'
 * rendering whether or not anybody reads past the third one. Fifty is a
 * screenful and a bit, and the rest is one press away.
 *
 * The steps are the grid's own row-limit ladder (BROWSE_LIMITS), and the last
 * of them is MaxRows: the adapter caps a read there whatever this asks for, so
 * a step past it would be a promise the backend does not keep.
 */
export const MONGO_BROWSE_STEPS = [50, 200, 500, 1000] as const;

/** Where a fresh collection browse starts. */
export const MONGO_BROWSE_FIRST = MONGO_BROWSE_STEPS[0];

/** The most a collection browse will ever ask for — the backend's own MaxRows. */
export const MONGO_BROWSE_CAP = MONGO_BROWSE_STEPS[MONGO_BROWSE_STEPS.length - 1];

/** The next rung of the ladder, or null at the top of it. */
export function nextMongoBrowseLimit(limit: number): number | null {
  for (const step of MONGO_BROWSE_STEPS) {
    if (step > limit) return step;
  }
  return null;
}

/**
 * The statement that browses one MySQL table: the qualified name, the
 * condition in force if there is one, the row limit, and how far into the
 * table to start (0 for the first page).
 *
 * OFFSET is left off at 0 rather than written as `OFFSET 0`: the common case
 * is the first page, and a statement that says nothing about where it starts
 * reads the same as it always has — anyone already relying on this SQL for
 * offset-less browsing sees no difference.
 *
 * Kept here beside its two siblings rather than moved: it is the same decision
 * for a different Engine, and the three read better together than one of them
 * would apart.
 */
export function browseTableSql(
  database: string,
  table: string,
  filter: string,
  limit: number,
  qualify: (database: string, table: string) => string,
  offset = 0,
): string {
  const condition = filter.trim();
  const where = condition === "" ? "" : ` WHERE ${condition}`;
  const from = offset > 0 ? ` OFFSET ${offset}` : "";
  return `SELECT * FROM ${qualify(database, table)}${where} LIMIT ${limit}${from}`;
}

/**
 * The call that browses one MongoDB collection: its first `limit` documents.
 *
 * It is an aggregate with a $limit rather than a find, because a find has no
 * limit go-db can write — the grammar's find takes a filter and a projection,
 * and the only bound on it is the adapter's own MaxRows. An aggregate carries
 * the bound in the statement itself, where the human can read it in the caption
 * and the classifier can see it: $limit is on the Approval Gate's list of
 * aggregation stages proven to only read (internal/guard/mongo.go), so this
 * browses through exactly the same guarded read path the find did.
 *
 * The collection is spliced in as a plain name because that is the only way
 * go-db's MongoDB grammar has of naming one — no quoting to get right, and no
 * quoting available: a name outside the grammar's plain identifier cannot be
 * written as a call at all, which is what browseMongoCollection returning null
 * says. Such a collection is legal on the server (MongoDB allows almost
 * anything but a dollar or a NUL), and it is refused cleanly here rather than
 * turned into a call that would name a different collection.
 */
export function browseMongoCollection(collection: string, limit: number): string | null {
  if (!isPlainCollection(collection)) return null;
  return `db.${collection}.aggregate([{"$limit": ${Math.trunc(limit)}}])`;
}

/**
 * Whether this collection's name is one go-db's MongoDB grammar can write.
 *
 * The rule is the grammar's own identifier (see internal/mongoql/parse.go), and
 * it is exported because every call the browse pane builds — the find that
 * reads a collection and the replaceOne or deleteOne that writes one document
 * of it — names the collection the same one way, and has the same one thing to
 * say when it cannot.
 */
export function isPlainCollection(collection: string): boolean {
  return /^[A-Za-z_][A-Za-z0-9_]*$/.test(collection);
}

/** The command that asks what kind of value a Redis key holds. */
export function redisTypeCommand(key: string): string | null {
  const argument = quoteRedisArgument(key);
  return argument === null ? null : `TYPE ${argument}`;
}

/**
 * The command that reads a Redis key of the type TYPE reported, or null for a
 * type go-db has no read for.
 *
 * One command per type, and each is on the classifier's allowlist — GET,
 * HGETALL, SMEMBERS and the rest are the read forms, and the popping and
 * blocking ones are not here because they are not reads. The three that take a
 * range take a bounded one: a list, a sorted set or a stream is read a thousand
 * elements at a time, which is the same cap the adapter applies to a reply and
 * the same argument the row limit makes for a table.
 *
 * "none" is Redis's answer for a key that is not there — it expired or was
 * deleted between the tree listing it and the human clicking it — and it has no
 * command, because there is nothing to read.
 */
export function redisReadCommand(key: string, type: string): string | null {
  const argument = quoteRedisArgument(key);
  if (argument === null) return null;

  const last = REDIS_BROWSE_LIMIT - 1;
  switch (type) {
    case "string":
      return `GET ${argument}`;
    case "hash":
      return `HGETALL ${argument}`;
    case "list":
      return `LRANGE ${argument} 0 ${last}`;
    case "set":
      return `SMEMBERS ${argument}`;
    case "zset":
      return `ZRANGE ${argument} 0 ${last} WITHSCORES`;
    case "stream":
      return `XRANGE ${argument} - + COUNT ${REDIS_BROWSE_LIMIT}`;
    default:
      return null;
  }
}

/**
 * One argument of a command line — a key, a hash field, a member, a value —
 * or null for text that cannot be written as one.
 *
 * The rules are the adapter's tokenizer, not an approximation of them (see
 * tokenizeRedisCommand in internal/db/redis.go), because the argument is the
 * whole thing at stake: a key with a space in it is a different key from two
 * keys, and a command built wrong would read — or in some other command,
 * change — something the human did not pick.
 *
 * It is the quoter for every command go-db writes, reads and writes alike:
 * mutateValue.ts builds its SET, HSET, LSET, ZADD and DEL through this same
 * function, because a value being written is the same kind of thing as a key
 * being read — arbitrary bytes that have to survive the trip out as themselves.
 *
 * Double quotes, because that run is the one with escapes in it. Inside it the
 * tokenizer reads \xNN as one byte, \n \r \t \b \a as their characters, and any
 * other escaped character as itself. So a backslash and a quote are escaped as
 * themselves and every control character is written \xNN — which also covers
 * the newline the classifier refuses outright, since a command line holding one
 * is a buffer holding two commands as far as the gate is concerned. Everything
 * else is passed through: the tokenizer works in bytes and appends unknown ones
 * unchanged, so a UTF-8 name arrives as the same bytes it left as.
 *
 * The one argument that cannot be written is one that is not text. Redis keys
 * and values are arbitrary bytes, and bytes that are not valid UTF-8 have
 * already been lost by the time they reach this process — the backend's JSON
 * writes U+FFFD in their place — so a command built from them would name a
 * different key, or set a different value. That is refused rather than guessed
 * at, and the caller shows the refusal.
 */
export function quoteRedisArgument(argument: string): string | null {
  if (argument.includes("�")) return null;

  let out = '"';
  for (const character of argument) {
    const code = character.codePointAt(0) ?? 0;
    if (character === '"' || character === "\\") {
      out += "\\" + character;
    } else if (code < 0x20 || code === 0x7f) {
      out += "\\x" + code.toString(16).padStart(2, "0");
    } else {
      out += character;
    }
  }
  return out + '"';
}

/**
 * The line the Explorer shows when a key cannot be written as a command
 * argument — the one refusal quoteRedisArgument has, in words that say what to
 * do about it.
 */
export const UNQUOTABLE_KEY =
  "This key's name holds bytes that are not text, so go-db cannot write a command that names exactly it.";

/** The line shown for a key that is no longer there when it is clicked. */
export const MISSING_KEY = "This key is not there any more — it expired or was deleted. Refresh the tree.";

/** The line shown for a Redis type go-db has no read for. */
export function noReadForType(type: string): string {
  return `go-db has no read for a key of type ${type} yet.`;
}

/**
 * The line shown for a MongoDB collection whose name the grammar cannot write
 * — see browseMongoCollection.
 */
export const UNNAMEABLE_COLLECTION =
  "go-db's MongoDB grammar names a collection plainly, as in db.users.find({}), and this collection's name is not a plain one.";
