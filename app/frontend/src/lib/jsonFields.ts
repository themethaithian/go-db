// Reading and addressing one field inside a parsed JSON value.
//
// JsonTree renders a document; this module is everything that has to be *true*
// about one field of it — what the field is called in MongoDB's own dotted
// language, whether that name can be written at all, how a typed literal is
// read, and how a document is patched when the dotted name is not available.
// It lives beside the tree rather than in it for mutateValue.ts's reason: the
// part that must not be wrong is the part that decides what a statement
// addresses, and it is worth keeping somewhere it can be read in a sitting,
// with nothing reactive anywhere near it.
//
// The extended-JSON half is the reading half. MongoDB's relaxed extended JSON
// writes an ObjectId as {"$oid": "…"} and a date as {"$date": "…"} — legible
// to a parser and hostile to an eye, since a document of them is mostly
// braces. recogniseExtended names the ones go-db knows so the tree can render
// each as the one compact leaf it stands for. It never rewrites the value: the
// document that goes back to the server is the one that came from it.
//
// Nothing here builds a statement. mutateValue.ts does that, using the path
// rules below.

import type { ValueEdits } from "./valueEdits.svelte";

/**
 * Where one value sits inside a document: object keys as strings, array
 * positions as numbers.
 *
 * The two are told apart by their JavaScript type rather than by looking at
 * the document, and every producer builds them that way (JsonTree walks the
 * value it is rendering). It matters for deletes: removing an array element is
 * not the same operation as removing an object key, and the path is what says
 * which one this is.
 */
export type FieldPath = readonly (string | number)[];

/**
 * The context that turns JsonTree's leaves into editable ones. A tree given
 * none of this is exactly as read-only as it was before — which is what keeps
 * the Editor's own value and document results untouched.
 */
export type JsonEditing = {
  /** The open box and its draft, lifted so an Inline Confirm cannot take it away. */
  edits: ValueEdits;
  /**
   * This tree's own prefix for open-box ids. Two document cards on screen are
   * two trees over the same shapes, and a bare path would have them share an
   * open box.
   */
  scope: string;
  /** Commits one value at one path. The caller builds and sends the statement. */
  onSet: (path: FieldPath, value: unknown) => void;
  /** Removes the field or element at one path. */
  onRemove: (path: FieldPath) => void;
  /**
   * Why this path cannot be edited or removed at all, or null. Shown, once,
   * where its pencil and its delete would be.
   */
  refusal?: (path: FieldPath) => string | null;
  /**
   * What the caller wants said about a path whose statement is bigger than the
   * change — an unaddressable field name, an array element whose removal
   * rewrites its array. Appended to the affordance's title, so it is read
   * before the Inline Confirm rather than discovered in it.
   */
  caveat?: (path: FieldPath, action: "set" | "remove") => string | null;
};

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

/**
 * The path as MongoDB's own dotted field name, or null for a path it cannot
 * name.
 *
 * MongoDB addresses a nested field as "a.b" and an array element as "a.0", and
 * that is the whole language — there is no escape for a key that itself holds
 * a dot, so {"a.b": 1} and {"a": {"b": 1}} are two different documents that
 * one dotted name cannot tell apart. A key starting with "$" is refused for
 * the neighbouring reason: inside an update document it would be read as an
 * operator rather than as a field name.
 *
 * Null is not a failure. It is the signal to write the same edit the other way
 * — a replaceOne carrying the patched document — and the caller says so where
 * the human can read it before confirming.
 */
export function dottedPath(path: FieldPath): string | null {
  if (path.length === 0) return null;
  const segments: string[] = [];
  for (const segment of path) {
    if (typeof segment === "number") {
      segments.push(String(segment));
      continue;
    }
    if (segment === "" || segment.includes(".") || segment.startsWith("$")) return null;
    segments.push(segment);
  }
  return segments.join(".");
}

/** Whether `inner` is `outer` or sits somewhere under it. */
export function underPath(outer: FieldPath, inner: FieldPath): boolean {
  if (inner.length < outer.length) return false;
  return outer.every((segment, at) => inner[at] === segment);
}

/**
 * The id of one row's open box: the tree's scope and the path, encoded so that
 * ["a.b"] and ["a", "b"] are different ids — the same distinction dottedPath
 * refuses to blur.
 *
 * `adding` names the other box a container row can open: the new field being
 * typed into it, which has no path of its own yet.
 */
export function fieldId(scope: string, path: FieldPath, adding = false): string {
  return `${scope}|${adding ? "+" : "="}|${JSON.stringify(path)}`;
}

/**
 * The path of whichever box is open in this tree, or null when the open box is
 * some other tree's (or there is none).
 *
 * It is read so that a node can seed itself expanded when an edit is open
 * underneath it. A declined Inline Confirm unmounts the pane and the tree
 * mounts again with its expansion reset, and a draft that survived into a
 * collapsed subtree is a draft the human cannot see.
 */
export function openFieldPath(scope: string, open: string | null): FieldPath | null {
  if (open === null) return null;
  for (const marker of ["=", "+"]) {
    const prefix = `${scope}|${marker}|`;
    if (!open.startsWith(prefix)) continue;
    try {
      const parsed: unknown = JSON.parse(open.slice(prefix.length));
      if (Array.isArray(parsed)) return parsed as FieldPath;
    } catch {
      return null;
    }
  }
  return null;
}

/**
 * Whether the open box is one of a tree's own field boxes rather than some
 * other editor of the same ValueEdits.
 *
 * The caller that shows a refusal needs it: a field box shows its own refusal
 * against the row, and a pane footer showing the same line as well would be
 * saying it twice — but a pane footer that stayed quiet because *something*
 * was open would be swallowing it.
 */
export function isFieldBox(open: string | null): boolean {
  if (open === null) return false;
  const at = Math.max(open.lastIndexOf("|=|"), open.lastIndexOf("|+|"));
  if (at === -1) return false;
  try {
    return Array.isArray(JSON.parse(open.slice(at + 3)));
  } catch {
    return false;
  }
}

/** The value at one path, or undefined for a path this value does not have. */
export function valueAt(document: unknown, path: FieldPath): unknown {
  let here: unknown = document;
  for (const segment of path) {
    if (here === null || typeof here !== "object") return undefined;
    here = (here as Record<string | number, unknown>)[segment as never];
  }
  return here;
}

/**
 * The document with `value` written at `path`, as a copy — the original is
 * left exactly as the browse handed it over, since it is still on screen and
 * the write it feeds may yet be declined.
 *
 * Only the containers along the path are copied; everything else is shared,
 * which is enough because nothing here mutates what it shares.
 */
export function patchedAt(document: unknown, path: FieldPath, value: unknown): unknown {
  if (path.length === 0) return value;
  const [head, ...rest] = path;

  if (Array.isArray(document)) {
    const copy = document.slice();
    const at = typeof head === "number" ? head : Number(head);
    copy[at] = patchedAt(copy[at], rest, value);
    return copy;
  }
  // A path that runs past the document's own shape builds the objects it needs
  // — which is what an added field on a fresh path is.
  const fields: Record<string, unknown> =
    document !== null && typeof document === "object"
      ? { ...(document as Record<string, unknown>) }
      : {};
  const key = String(head);
  fields[key] = patchedAt(fields[key], rest, value);
  return fields;
}

/**
 * The document with the field or element at `path` gone, as a copy.
 *
 * An object key is deleted and an array element is spliced out — an array with
 * a hole in it is not what "delete this element" means anywhere a human would
 * read it.
 */
export function prunedAt(document: unknown, path: FieldPath): unknown {
  if (path.length === 0) return document;
  const [head, ...rest] = path;

  if (Array.isArray(document)) {
    const at = typeof head === "number" ? head : Number(head);
    const copy = document.slice();
    if (rest.length === 0) {
      copy.splice(at, 1);
      return copy;
    }
    copy[at] = prunedAt(copy[at], rest);
    return copy;
  }
  if (document === null || typeof document !== "object") return document;

  const fields = { ...(document as Record<string, unknown>) };
  const key = String(head);
  if (rest.length === 0) {
    delete fields[key];
    return fields;
  }
  fields[key] = prunedAt(fields[key], rest);
  return fields;
}

// ---------------------------------------------------------------------------
// Literals
// ---------------------------------------------------------------------------

/** How a typed box will be read — the word the inline hint shows. */
export type LiteralKind = "string" | "number" | "boolean" | "null" | "object" | "array";

export type Literal = { value: unknown; as: LiteralKind };

/**
 * What the text in an inline box means.
 *
 * JSON first, plain string otherwise. `42` is the number, `true` is the
 * boolean, `null` is null, `{"a": 1}` is the object and `"x"` is the string x
 * — and `hello`, which is no JSON literal at all, is the string hello rather
 * than a refusal. That forgiveness is RedisInsight's, and it is the right
 * default for a box a human types a value into: the common case is text, and
 * the uncommon one is written the way JSON writes it.
 *
 * The hint beside the box is what keeps it honest. Nothing here guesses
 * silently — the human is told which way their text was read before they
 * commit it.
 */
export function parseLiteral(text: string): Literal {
  try {
    const value: unknown = JSON.parse(text);
    return { value, as: literalKind(value) };
  } catch {
    return { value: text, as: "string" };
  }
}

function literalKind(value: unknown): LiteralKind {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  const type = typeof value;
  if (type === "string" || type === "number" || type === "boolean") return type;
  return "object";
}

/** The text an inline box opens holding, for a value already on screen. */
export function literalText(value: unknown): string {
  if (typeof value === "string") return value;
  return JSON.stringify(value) ?? "";
}

// ---------------------------------------------------------------------------
// Extended JSON
// ---------------------------------------------------------------------------

/**
 * One extended-JSON wrapper, as the tree shows it: the compact form a mongosh
 * user reads, and the muted type tag for the ones whose compact form is just
 * digits.
 */
export type Extended = { text: string; tag: string | null };

const BINARY_SHOWN = 16;
const CODE_SHOWN = 48;

/**
 * The wrapper this value stands for, or null for an ordinary object.
 *
 * Only a single-key object whose key is one go-db knows, and only when what is
 * under that key is the shape that wrapper has. Anything else is an ordinary
 * object and renders as one — a document with a genuine field called "$date"
 * holding something else is not a date, and quietly showing it as one would be
 * this module lying about the data.
 *
 * These render as leaves and do not open for editing (v1): a wrapper is one
 * value spelled across two levels, and typing over the "$oid" *inside* it
 * would be editing the spelling rather than the value.
 */
export function recogniseExtended(value: unknown): Extended | null {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return null;
  const fields = value as Record<string, unknown>;
  const keys = Object.keys(fields);
  if (keys.length !== 1) return null;

  const key = keys[0];
  const inner = fields[key];
  switch (key) {
    case "$oid":
      return typeof inner === "string" ? { text: `ObjectId("${inner}")`, tag: null } : null;
    case "$date":
      return date(inner);
    case "$numberLong":
      return digits(inner, "long");
    case "$numberDecimal":
      return digits(inner, "decimal");
    case "$numberDouble":
      return digits(inner, "double");
    case "$numberInt":
      return digits(inner, "int");
    case "$binary":
      return binary(inner);
    case "$timestamp":
      return timestamp(inner);
    case "$regularExpression":
      return regularExpression(inner);
    case "$code":
      return typeof inner === "string"
        ? { text: `Code("${clip(inner, CODE_SHOWN)}")`, tag: null }
        : null;
    case "$symbol":
      return typeof inner === "string" ? { text: `Symbol("${inner}")`, tag: null } : null;
    case "$uuid":
      return typeof inner === "string" ? { text: `UUID("${inner}")`, tag: null } : null;
    case "$minKey":
      return inner === 1 ? { text: "MinKey()", tag: null } : null;
    case "$maxKey":
      return inner === 1 ? { text: "MaxKey()", tag: null } : null;
    case "$undefined":
      return inner === true ? { text: "undefined", tag: null } : null;
    default:
      return null;
  }
}

// Relaxed extended JSON writes a date in range as an ISO-8601 string and one
// outside it (and canonical extended JSON writes every date) as milliseconds
// under $numberLong. Both are dates, so both read as one.
function date(inner: unknown): Extended | null {
  if (typeof inner === "string") return { text: `ISODate("${inner}")`, tag: null };
  if (inner === null || typeof inner !== "object" || Array.isArray(inner)) return null;

  const millis = (inner as Record<string, unknown>).$numberLong;
  if (typeof millis !== "string") return null;
  const at = new Date(Number(millis));
  if (Number.isNaN(at.getTime())) return { text: `ISODate(${millis} ms)`, tag: null };
  return { text: `ISODate("${at.toISOString()}")`, tag: null };
}

// The three numeric wrappers show their digits and say which width they are in
// the muted tag — the digits are the value, and the width is the thing the
// wrapper was there to carry.
function digits(inner: unknown, tag: string): Extended | null {
  if (typeof inner === "string") return { text: inner, tag };
  if (typeof inner === "number") return { text: String(inner), tag };
  return null;
}

function binary(inner: unknown): Extended | null {
  if (inner === null || typeof inner !== "object" || Array.isArray(inner)) return null;
  const { base64, subType } = inner as Record<string, unknown>;
  if (typeof base64 !== "string" || typeof subType !== "string") return null;
  return { text: `Binary("${clip(base64, BINARY_SHOWN)}", 0x${subType})`, tag: null };
}

function timestamp(inner: unknown): Extended | null {
  if (inner === null || typeof inner !== "object" || Array.isArray(inner)) return null;
  const { t, i } = inner as Record<string, unknown>;
  if (typeof t !== "number" || typeof i !== "number") return null;
  return { text: `Timestamp(${t}, ${i})`, tag: null };
}

function regularExpression(inner: unknown): Extended | null {
  if (inner === null || typeof inner !== "object" || Array.isArray(inner)) return null;
  const { pattern, options } = inner as Record<string, unknown>;
  if (typeof pattern !== "string" || typeof options !== "string") return null;
  return { text: `/${pattern}/${options}`, tag: null };
}

function clip(text: string, at: number): string {
  return text.length > at ? `${text.slice(0, at)}…` : text;
}
