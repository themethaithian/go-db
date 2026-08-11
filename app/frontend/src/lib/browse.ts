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
 * The statement that browses one MySQL table: the qualified name, the
 * condition in force if there is one, and the row limit.
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
): string {
  const condition = filter.trim();
  const where = condition === "" ? "" : ` WHERE ${condition}`;
  return `SELECT * FROM ${qualify(database, table)}${where} LIMIT ${limit}`;
}

/**
 * The call that browses one MongoDB collection: every document in it, which
 * the adapter caps for us exactly as LIMIT caps a SELECT.
 *
 * The collection is spliced in as a plain name because that is the only way
 * go-db's MongoDB grammar has of naming one — no quoting to get right, and no
 * quoting available: a name outside the grammar's plain identifier cannot be
 * written as a call at all, which is what browseMongoCollection returning null
 * says. Such a collection is legal on the server (MongoDB allows almost
 * anything but a dollar or a NUL), and it is refused cleanly here rather than
 * turned into a call that would name a different collection.
 */
export function browseMongoCollection(collection: string): string | null {
  if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(collection)) return null;
  return `db.${collection}.find({})`;
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
 * A Redis key as one argument of a command line, or null for a key that cannot
 * be written as one.
 *
 * The rules are the adapter's tokenizer, not an approximation of them (see
 * tokenizeRedisCommand in internal/db/redis.go), because the argument is the
 * whole thing at stake: a key with a space in it is a different key from two
 * keys, and a command built wrong would read — or in some other command,
 * change — something the human did not pick.
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
 * The one key that cannot be written is one whose name is not text. Redis keys
 * are arbitrary bytes, and a key holding bytes that are not valid UTF-8 has
 * already lost them by the time it reaches this process — the backend's JSON
 * writes U+FFFD in their place — so a command built from it would name a
 * different key. That is refused rather than guessed at, and the caller shows
 * the refusal.
 */
export function quoteRedisArgument(key: string): string | null {
  if (key.includes("�")) return null;

  let out = '"';
  for (const character of key) {
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
