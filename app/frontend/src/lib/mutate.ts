// Turning an edited row into a statement.
//
// Inline editing is not a second write path. What an edit produces is an
// UPDATE, in text, submitted through the same RunQuery → Inline Confirm route a
// typed statement takes: the Approval Gate classifies it, previews it and
// audits it without ever knowing a grid was involved. That leaves this module
// two questions, and they are the only two it answers — whether a result set
// may be edited at all, and what the statement for one dirty row says.
//
// Both live here rather than in the views because both have to be answered the
// same way twice (the Explorer's browse and the Editor's SELECT), and because
// the one part of this feature that must not be wrong — putting a value into
// SQL — is worth keeping somewhere it can be read in a sitting, with nothing
// reactive anywhere near it.

/** One cell, as the driver hands it over: its text, or NULL. */
export type CellValue = string | null;

/** A column and the value being written into it, or matched against it. */
export type Assignment = { column: string; value: CellValue };

/**
 * Whether the rows on screen can be edited — and, when they cannot, the line
 * to show in their place. A null reason means "not decided yet": the table's
 * indexes are still on their way, and claiming read-only in that gap would put
 * a hint on screen that disappears a moment later.
 */
export type Editability =
  | { editable: true; table: string; keyColumns: string[] }
  | { editable: false; reason: string | null };

/**
 * MySQL's identifier quoting: wrap in backticks, and double any backtick
 * inside. A table or column named with one is pathological but legal, and a
 * generated statement that breaks on it would be a generated statement nobody
 * could trust.
 */
export function quoteIdentifier(name: string): string {
  return "`" + name.replaceAll("`", "``") + "`";
}

/**
 * A table name as SQL, qualified with its schema when there is one: two
 * identifiers, each quoted on its own, never one identifier with a dot in it.
 * `db`.`table` names a table in db; `db.table` names a table called "db.table"
 * — the difference is the whole reason this is a function rather than string
 * concatenation at each call site.
 *
 * An empty schema means the connection's own default, which is what the
 * Editor's statements run against and what the Explorer used before there was
 * a databases level: the name comes back bare.
 */
export function qualify(schema: string, table: string): string {
  if (schema === "") return quoteIdentifier(table);
  return `${quoteIdentifier(schema)}.${quoteIdentifier(table)}`;
}

// Every character that cannot travel between two quotes as itself, and what it
// travels as. Backslash and quote are the two that matter; the control
// characters are here so a value with a newline in it comes back out of the
// database with that newline, and shows up on one line in the Impact Preview
// on the way. One pass over the string, so no escape can be escaped twice —
// the classic way this goes wrong is replacing quotes and then backslashes.
const ESCAPES: Record<string, string> = {
  "\\": "\\\\",
  "'": "''",
  "\n": "\\n",
  "\r": "\\r",
  "\0": "\\0",
  "\x1a": "\\Z",
};

/**
 * One value as a SQL literal. NULL is the unquoted keyword — an absence, not
 * the word; everything else is quoted, numbers included. MySQL coerces a
 * quoted number into a numeric column on the way in, and quoting uniformly
 * means there is no "is this a number?" guess in the path, which is exactly
 * the kind of guess that puts an unquoted piece of a value into a statement.
 */
export function sqlLiteral(value: CellValue): string {
  if (value === null) return "NULL";
  return "'" + value.replace(/[\\'\n\r\0\x1a]/g, (char) => ESCAPES[char]) + "'";
}

/**
 * The UPDATE for one dirty row: its changed columns, matched on its primary
 * key. The key's values are the row's *original* ones — the row is identified
 * by what it was when it was fetched, never by what the human has just typed
 * over it, even when they typed over the key itself.
 *
 * schema qualifies the table when the rows came from a browse of a named
 * database — the Explorer's do, since it browses `db`.`table`, and an UPDATE
 * that dropped the schema would aim at whatever the connection's default is
 * instead. Empty (the default, and the Editor's case) leaves the name bare.
 *
 * Throws when the row cannot be addressed: no changes, no key, or a key column
 * that is NULL (which `=` cannot match, so a statement built on it would
 * silently update nothing). The caller shows that as a failed save rather than
 * submitting a statement that means something else.
 */
export function buildUpdate(
  table: string,
  changes: Assignment[],
  keys: Assignment[],
  schema = "",
): string {
  if (changes.length === 0) throw new Error("nothing to update in this row");
  if (keys.length === 0) throw new Error("no primary key to match this row on");
  for (const key of keys) {
    if (key.value === null) {
      throw new Error(`this row's ${key.column} is NULL, so it cannot be matched`);
    }
  }
  const set = changes
    .map((change) => `${quoteIdentifier(change.column)} = ${sqlLiteral(change.value)}`)
    .join(", ");
  const where = keys
    .map((key) => `${quoteIdentifier(key.column)} = ${sqlLiteral(key.value)}`)
    .join(" AND ");
  return `UPDATE ${qualify(schema, table)} SET ${set} WHERE ${where}`;
}

/**
 * The table an editable statement reads from, or null.
 *
 * This is the Editor's whole answer to "which table am I looking at", and it is
 * deliberately a narrow one: a plain SELECT of plain columns from one plain
 * table, with at most a WHERE, an ORDER BY and a LIMIT after it. Anything else
 * — a join, a union, a grouped or aggregated list, a subquery in the FROM, an
 * alias, a schema-qualified name, a second statement — reads as null and the
 * rows stay read-only.
 *
 * It says no far more readily than a parser would, and that is the point: a
 * false yes here means an UPDATE aimed at the wrong table, while a false no
 * means the human runs their edit as SQL, which is what they would have done
 * anyway. The gate still classifies and previews whatever this leads to; this
 * only decides whether to offer the affordance.
 */
export function selectTarget(sql: string): string | null {
  const statement = sql.trim().replace(/;\s*$/, "").trim();
  // A second statement in the buffer means the rows on screen came from one of
  // them and there is no telling which.
  if (statement === "" || statement.includes(";")) return null;

  const match = SIMPLE_SELECT.exec(statement);
  if (match === null) return null;
  const [, list, table, tail = ""] = match;

  // Bare column names and stars only. A parenthesis in the list is a function
  // call or a subquery, a quote is a literal, and either one means the rows are
  // not the table's own rows.
  if (!PLAIN_LIST.test(list) || DISTINCT.test(list)) return null;

  const rest = tail.trim();
  if (rest !== "" && !TAIL_START.test(rest)) return null;
  if (COMPOSITE.test(rest)) return null;

  return unquoteIdentifier(table);
}

/**
 * Whether these rows can be edited: a table to write to, a primary key to
 * address its rows by, and every one of that key's columns present in the
 * result. Given a null table (the Editor, on a statement selectTarget refused)
 * or a table with no primary key, it says why instead.
 *
 * Indexes are null while they are still being fetched, and that is not the same
 * as a table without a key — it answers "not yet" and shows nothing.
 */
export function editability(
  table: string | null,
  columns: string[],
  indexes: { columns: string[]; primary: boolean }[] | null,
): Editability {
  if (table === null) {
    return { editable: false, reason: "read-only — only a plain single-table SELECT can be edited" };
  }
  if (indexes === null) return { editable: false, reason: null };

  const primary = indexes.find((index) => index.primary);
  if (primary === undefined || primary.columns.length === 0) {
    return { editable: false, reason: "read-only — no primary key" };
  }

  const present = new Set(columns);
  const missing = primary.columns.filter((column) => !present.has(column));
  if (missing.length > 0) {
    return {
      editable: false,
      reason: `read-only — the result is missing the primary key (${missing.join(", ")})`,
    };
  }
  return { editable: true, table, keyColumns: primary.columns };
}

// SELECT <list> FROM <table> [<tail>], with the table either backticked or a
// bare word. The tail has to start with whitespace, which is what makes
// `FROM users, orders` and `FROM users u` fail to match at all rather than
// arrive here as a table named `users`.
const SIMPLE_SELECT = /^select\s+([\s\S]+?)\s+from\s+(`(?:[^`]|``)+`|[A-Za-z0-9_$]+)(\s[\s\S]*)?$/i;
const PLAIN_LIST = /^[A-Za-z0-9_$`*,.\s]+$/;
const DISTINCT = /\bdistinct\b/i;
const TAIL_START = /^(where|order\s+by|limit|offset)\b/i;
const COMPOSITE =
  /\b(join|union|group\s+by|having|into|procedure|straight_join|for\s+update|lock\s+in\s+share\s+mode)\b/i;

function unquoteIdentifier(token: string): string {
  if (!token.startsWith("`")) return token;
  return token.slice(1, -1).replaceAll("``", "`");
}
