// Turning an edited value or document into a statement.
//
// mutate.ts's law, for the Engines whose answer is a value or a list of
// documents rather than rows. Editing one of those is not a second write path
// either: what an edit produces here is a Redis command or a MongoDB call, in
// text, submitted through the same RunQuery → Inline Confirm route a typed
// statement takes — so the Approval Gate classifies it, previews it and audits
// it without ever knowing a browse pane was involved.
//
// That leaves this module the same two questions mutate.ts answers, asked of a
// value instead of a row: whether a thing on screen may be edited at all, and
// what the statement for one edit says. Both live here rather than in the views
// because the part that must not be wrong — putting a value into a command line
// or a call — is worth keeping somewhere it can be read in a sitting, with
// nothing reactive anywhere near it.
//
// Nothing here quotes anything itself. Redis arguments go through browse.ts's
// quoteRedisArgument, which is the adapter's own tokenizer written backwards,
// and MongoDB values go through JSON.stringify, whose output is inside
// mongoql's grammar by construction: double-quoted keys and strings, JSON
// numbers, and the three bare words. A value neither of those can represent is
// refused — a thrown Error whose message is fit to show — rather than turned
// into a statement that would mean something else.

import type { db } from "../../wailsjs/go/models";
import { isPlainCollection, quoteRedisArgument, UNNAMEABLE_COLLECTION, UNQUOTABLE_KEY } from "./browse";
import { dottedPath, patchedAt, prunedAt, valueAt, type FieldPath } from "./jsonFields";

// ---------------------------------------------------------------------------
// Redis
// ---------------------------------------------------------------------------

/**
 * The one place in a browsed Redis key that an edit or a delete addresses.
 *
 * Which shapes exist is the key's type, and each is addressed the way Redis
 * itself addresses it: a string has one value, a hash has named fields, a list
 * has positions, a set has members, and a sorted set has members carrying a
 * score. There is no slot for a set member's *text* or a hash field's *name*,
 * because neither can be changed by one command, and one command is the unit
 * the Approval Gate confirms.
 */
export type Slot =
  | { at: "value" }
  | { at: "field"; field: string }
  | { at: "index"; index: number }
  | { at: "member"; member: string }
  | { at: "score"; member: string };

/**
 * Why a set member cannot be typed over. It is the gate's grain showing
 * through, and it is worth saying plainly: SREM then SADD is two statements,
 * the human confirms one at a time, and a half-done pair would leave the set
 * missing a member nobody asked to remove.
 */
export const NO_SET_MEMBER_EDIT =
  "A set member cannot be changed in one statement — it is an SREM and then an SADD, and the Approval Gate confirms one statement at a time. Delete this member and add the new one.";

/**
 * Why a list element cannot be deleted on its own. LREM's argument is a value,
 * not a position, so the honest description of what it would do is "remove
 * every element holding this text" — which is not what a delete button beside
 * one row can be taken to mean.
 */
export const NO_LIST_ELEMENT_DELETE =
  "A list element cannot be deleted in one statement — LREM removes by value rather than by position, so it would take out every element holding this same text.";

/**
 * Why a string key's value has no delete of its own: there is nothing to
 * remove but the key, and the pane header already offers exactly that.
 */
export const NO_STRING_VALUE_DELETE =
  "A string key has nothing to delete but itself — the pane header's Delete key does exactly that.";

/** The refusal for a value, field or member that came back as U+FFFD. */
const UNWRITABLE_VALUE =
  "This value holds U+FFFD, the character go-db shows in place of bytes that are not text, so it cannot write a command that sets exactly it.";
const UNWRITABLE_FIELD =
  "This hash field's name holds bytes that are not text, so go-db cannot write a command that names exactly it.";
const UNWRITABLE_MEMBER =
  "This member's name holds bytes that are not text, so go-db cannot write a command that names exactly it.";

/**
 * A sorted set's score, as Redis writes and reads one: a JSON-ish number, or an
 * infinity. The infinities are here because they are real stored scores — the
 * adapter renders a non-finite double as the very token RESP uses for it — so a
 * pane that showed "inf" and then refused to write it back would be refusing to
 * save a score it had just displayed.
 */
const SCORE = /^[+-]?(inf|infinity|(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?)$/i;

const NOT_A_SCORE =
  "A sorted set's score is a number — write it as 1, -2.5 or 1e3, or as inf or -inf.";

/**
 * Why this slot cannot be typed over, or null when it can.
 *
 * The views ask this before drawing anything, so a slot that cannot be edited
 * shows a muted reason where its pencil would be rather than a pencil that
 * refuses when pressed — mutate.ts's editability, at the grain of one element.
 */
export function writeRefusal(slot: Slot): string | null {
  return slot.at === "member" ? NO_SET_MEMBER_EDIT : null;
}

/** Why this slot cannot be removed on its own, or null when it can. */
export function removeRefusal(slot: Slot): string | null {
  switch (slot.at) {
    case "index":
      return NO_LIST_ELEMENT_DELETE;
    case "value":
      return NO_STRING_VALUE_DELETE;
    default:
      return null;
  }
}

/**
 * The command that writes `text` into one slot of `key`.
 *
 * One command per slot, each the plainest one Redis has for it, and each a
 * whole statement in itself — nothing here builds a pair. The key, the field,
 * the member and the value are all quoted the same one way; the list index and
 * the sorted set's score are the two arguments that are not text, and both are
 * written bare because both have already been proved to be a number.
 *
 * Throws when the edit cannot be written honestly: a slot that has no
 * one-command edit, an argument that is not text, or a score that is not a
 * number. The caller shows the message and sends nothing.
 */
export function writeCommand(key: string, slot: Slot, text: string): string {
  const target = argument(key, UNQUOTABLE_KEY);
  switch (slot.at) {
    case "value":
      return `SET ${target} ${argument(text, UNWRITABLE_VALUE)}`;
    case "field":
      return `HSET ${target} ${argument(slot.field, UNWRITABLE_FIELD)} ${argument(text, UNWRITABLE_VALUE)}`;
    case "index":
      return `LSET ${target} ${slot.index} ${argument(text, UNWRITABLE_VALUE)}`;
    case "score": {
      // ZADD's own order — the score, then the member it belongs to. Trimmed
      // because a box a human typed in collects spaces at both ends, and a
      // score is the one argument here where they mean nothing.
      const score = text.trim();
      if (!SCORE.test(score)) throw new Error(NOT_A_SCORE);
      return `ZADD ${target} ${score} ${argument(slot.member, UNWRITABLE_MEMBER)}`;
    }
    case "member":
      throw new Error(NO_SET_MEMBER_EDIT);
  }
}

/**
 * The command that removes one slot from `key` — the three removals that are
 * genuinely one statement, and a refusal for the two that are not.
 */
export function removeCommand(key: string, slot: Slot): string {
  const target = argument(key, UNQUOTABLE_KEY);
  switch (slot.at) {
    case "field":
      return `HDEL ${target} ${argument(slot.field, UNWRITABLE_FIELD)}`;
    case "member":
      return `SREM ${target} ${argument(slot.member, UNWRITABLE_MEMBER)}`;
    case "score":
      return `ZREM ${target} ${argument(slot.member, UNWRITABLE_MEMBER)}`;
    case "index":
      throw new Error(NO_LIST_ELEMENT_DELETE);
    case "value":
      throw new Error(NO_STRING_VALUE_DELETE);
  }
}

/** The command that removes the whole key, whatever type it holds. */
export function deleteKeyCommand(key: string): string {
  return `DEL ${argument(key, UNQUOTABLE_KEY)}`;
}

/** One row of a browsed Redis key: what it is, what it shows, and its text. */
export type Element = {
  /** The row's own name — a hash field, a list position, a member. */
  label: string;
  /** Where an edit or a delete on this row lands. */
  slot: Slot;
  /** The reply this row renders, exactly as the browse returned it. */
  value: db.Reply;
  /** What an edit box on this row opens holding. */
  text: string;
};

/**
 * The rows of a browsed key, as slots against the reply that produced them —
 * or null for a reply that is not the shape this key's type browses as.
 *
 * Null is the honest answer rather than a guess, and the pane falls back to the
 * read-only reply tree when it comes back: an element the pane cannot place is
 * an element it cannot address, and offering to write to it would be offering
 * to write somewhere.
 *
 * Two protocols reach this function. Under RESP3 — which is what go-redis opens
 * by default, and so what this app sees — a hash comes back as a map and a
 * sorted set as an array of member/score pairs. Under RESP2, against an older
 * server the client falls back to, both are flat arrays of alternating values.
 * Both are read here, because which one arrived is not something the pane
 * should have to know, and because a pane that silently stopped offering to
 * edit against an old server would look broken rather than careful.
 */
export function browsedElements(type: string, reply: db.Reply): Element[] | null {
  switch (type) {
    case "hash":
      return hashElements(reply);
    case "list":
      return listElements(reply);
    case "set":
      return setElements(reply);
    case "zset":
      return zsetElements(reply);
    default:
      // string is the scalar the pane renders itself, and stream has no
      // one-command edit go-db writes yet.
      return null;
  }
}

function hashElements(reply: db.Reply): Element[] | null {
  if (reply.kind === "map") {
    const elements: Element[] = [];
    for (const entry of reply.entries ?? []) {
      const field = replyText(entry.key);
      if (field === null) return null;
      elements.push({
        label: field,
        slot: { at: "field", field },
        value: entry.value,
        text: replyText(entry.value) ?? "",
      });
    }
    return elements;
  }

  // RESP2's flat field, value, field, value.
  return pairedElements(reply, (field, value) => ({
    label: field,
    slot: { at: "field", field },
    value,
    text: replyText(value) ?? "",
  }));
}

function listElements(reply: db.Reply): Element[] | null {
  if (reply.kind !== "array") return null;
  return (reply.items ?? []).map((item, index) => ({
    label: String(index),
    slot: { at: "index", index },
    value: item,
    text: replyText(item) ?? "",
  }));
}

function setElements(reply: db.Reply): Element[] | null {
  if (reply.kind !== "array") return null;
  const elements: Element[] = [];
  for (const item of reply.items ?? []) {
    const member = replyText(item);
    if (member === null) return null;
    elements.push({ label: member, slot: { at: "member", member }, value: item, text: member });
  }
  return elements;
}

function zsetElements(reply: db.Reply): Element[] | null {
  if (reply.kind !== "array") return null;
  const items = reply.items ?? [];

  // RESP3's array of [member, score] pairs. A member is always a scalar, so an
  // array as the first item is what tells the two protocols apart — there is no
  // sorted set whose members are themselves arrays for this to misread.
  if (items.length > 0 && items[0].kind === "array") {
    const elements: Element[] = [];
    for (const pair of items) {
      const parts = pair.items ?? [];
      if (pair.kind !== "array" || parts.length !== 2) return null;
      const member = replyText(parts[0]);
      if (member === null) return null;
      elements.push({
        label: member,
        slot: { at: "score", member },
        value: parts[1],
        text: replyText(parts[1]) ?? "",
      });
    }
    return elements;
  }

  // RESP2's flat member, score, member, score.
  return pairedElements(reply, (member, score) => ({
    label: member,
    slot: { at: "score", member },
    value: score,
    text: replyText(score) ?? "",
  }));
}

// pairedElements reads a flat array as alternating name and value, which is
// what both RESP2 shapes above are. An odd length, or a name that is not text,
// means the reply is not the shape it was taken for and nothing is offered.
function pairedElements(
  reply: db.Reply,
  build: (name: string, value: db.Reply) => Element,
): Element[] | null {
  if (reply.kind !== "array") return null;
  const items = reply.items ?? [];
  if (items.length % 2 !== 0) return null;

  const elements: Element[] = [];
  for (let at = 0; at < items.length; at += 2) {
    const name = replyText(items[at]);
    if (name === null) return null;
    elements.push(build(name, items[at + 1]));
  }
  return elements;
}

/**
 * A reply as the text an edit box holds, or null for a reply that has none.
 *
 * Redis hands back a hash value, a list element and a set member as strings,
 * whatever they look like, and the box edits the string it was given — there is
 * no coercion here in either direction. The two numeric kinds are here for the
 * one place they genuinely arrive, a RESP3 sorted set's score, and they are
 * rendered the way ValueView renders them so the box opens holding exactly what
 * the row was showing.
 */
function replyText(reply: db.Reply): string | null {
  switch (reply.kind) {
    case "string":
      return reply.text ?? "";
    case "integer":
      return String(reply.integer ?? 0);
    case "double":
      return String(reply.double ?? 0);
    default:
      return null;
  }
}

function argument(text: string, refusal: string): string {
  const quoted = quoteRedisArgument(text);
  if (quoted === null) throw new Error(refusal);
  return quoted;
}

// ---------------------------------------------------------------------------
// MongoDB
// ---------------------------------------------------------------------------

/**
 * Why a document with no _id cannot be edited. Every write go-db builds here
 * addresses one document by its own _id, and a document without one has no
 * filter that means "this one and no other" — a replaceOne on any other filter
 * could land on a document nobody was looking at.
 */
export const DOCUMENT_WITHOUT_ID =
  "This document has no _id, so there is no filter that addresses exactly it — go-db will not write a replaceOne that could match another document.";

/** The refusal for a document holding something JSON cannot write. */
const UNWRITABLE_DOCUMENT =
  "go-db cannot write this document as JSON, so it cannot build a call that carries it.";

/**
 * The document's own _id, wrapped so that "there is no _id" and "the _id is
 * null" are different answers. A null _id is a legal, addressable document; an
 * absent one is not addressable at all.
 */
export function documentId(document: unknown): { id: unknown } | null {
  if (document === null || typeof document !== "object" || Array.isArray(document)) return null;
  const fields = document as Record<string, unknown>;
  if (!Object.hasOwn(fields, "_id")) return null;
  return { id: fields._id };
}

/**
 * The call that replaces one document with the edited one, matched on the _id
 * it came with.
 *
 * The filter carries the _id exactly as the browse handed it over — an ObjectId
 * arrives as {"$oid": "…"}, which is what MongoDB's own relaxed extended JSON
 * writes and what the driver reads back, so it round-trips through mongoql's
 * grammar as the plain object it is (parse.go's package comment says so
 * outright). The replacement keeps its own _id: MongoDB accepts a replacement
 * whose _id is the one it is replacing, and refuses one that differs — which is
 * a refusal worth hearing from the server rather than guessing at here.
 */
export function replaceOneCommand(collection: string, id: unknown, document: unknown): string {
  return `db.${plainly(collection)}.replaceOne(${filter(id)}, ${json(document)})`;
}

/** The call that deletes one document, matched on the _id it came with. */
export function deleteOneCommand(collection: string, id: unknown): string {
  return `db.${plainly(collection)}.deleteOne(${filter(id)})`;
}

/**
 * Why _id itself is not editable. Every write built here addresses the
 * document by it, so typing over it is not an edit to a field but a request
 * for a different document to exist.
 */
export const ID_NOT_EDITABLE =
  "_id is the address every write here uses, so it cannot be typed over — a document with a different _id is a different document. Delete this one and insert the new one.";

/**
 * What the human is told when the field they are editing has a name MongoDB's
 * dotted language cannot write, so the edit goes the long way round.
 *
 * It is a caveat rather than a refusal: the edit still happens, and it still
 * shows in the Inline Confirm before it does — as a whole-document replaceOne
 * rather than as one $set, which is a bigger statement than the change and is
 * worth saying so before it is confirmed.
 */
export const WHOLE_DOCUMENT_FALLBACK =
  "this field's name can't be addressed (it holds a dot, or starts with $), so the whole document will be replaced instead of this one field";

/**
 * What the human is told before removing an array element: the statement is a
 * $set of the array, not an $unset of the element. See removeFieldCommand.
 */
export const ARRAY_ELEMENT_REWRITE =
  "removing an array element is not an $unset — that would leave a null behind — so this writes the array back without it";

/** Why an element cannot be removed from something that is not an array. */
const NOT_AN_ARRAY =
  "go-db read this path as an array element, and what is there is not an array — re-read the collection and try again.";

/**
 * The call that writes one field of one document.
 *
 * The statement the human confirms says exactly what changes:
 *
 *   db.<collection>.updateOne({"_id": <id>}, {"$set": {"<dotted.path>": <value>}})
 *
 * — which is the whole argument for building it here rather than sending a
 * patched document. A replaceOne carrying five hundred unchanged fields and
 * one changed one is a statement nobody can check by reading it, and checking
 * it by reading it is what the Approval Gate is for.
 *
 * A path MongoDB cannot name (see dottedPath) falls back to exactly that
 * replaceOne, patched here rather than on the server. That is the honest
 * ending for a field called "a.b": there is no $set that means it, and
 * refusing the edit outright would be refusing to edit data the browse was
 * happy to show. The caller says which of the two is about to happen —
 * WHOLE_DOCUMENT_FALLBACK — before the confirm, not after.
 */
export function writeFieldCommand(
  collection: string,
  id: unknown,
  document: unknown,
  path: FieldPath,
  value: unknown,
): string {
  const dotted = dottedPath(path);
  if (dotted === null) {
    return replaceOneCommand(collection, id, patchedAt(document, path, value));
  }
  return `db.${plainly(collection)}.updateOne(${filter(id)}, {"$set": {${json(dotted)}: ${json(value)}}})`;
}

/**
 * The call that removes one field — or one array element — from one document.
 *
 * Three endings, and which one it is, is the path's own shape:
 *
 *   - An object key with a name MongoDB can write becomes
 *     {"$unset": {"<dotted.path>": 1}}, the operation that removes a field.
 *
 *   - An array element never becomes $unset. $unset on an array position sets
 *     it to null rather than removing it, which is the one outcome a human
 *     pressing delete beside an element does not mean. It becomes a $set of
 *     the element's own array, patched — one statement, and one whose text
 *     says exactly what the array will hold afterwards.
 *
 *   - Anything whose path cannot be written falls back to the patched
 *     replaceOne, as writeFieldCommand's does and for the same reason.
 */
export function removeFieldCommand(
  collection: string,
  id: unknown,
  document: unknown,
  path: FieldPath,
): string {
  const last = path[path.length - 1];
  if (typeof last === "number") {
    const arrayPath = path.slice(0, -1);
    const array = valueAt(document, arrayPath);
    if (!Array.isArray(array)) throw new Error(NOT_AN_ARRAY);
    const remaining = array.filter((_, at) => at !== last);
    // A document's root is an object, so an array at the empty path is not a
    // shape this can reach — but a $set with no field name would be a call
    // that means nothing, so it is refused rather than written.
    if (arrayPath.length === 0) throw new Error(NOT_AN_ARRAY);
    return writeFieldCommand(collection, id, document, arrayPath, remaining);
  }

  const dotted = dottedPath(path);
  if (dotted === null) {
    return replaceOneCommand(collection, id, prunedAt(document, path));
  }
  return `db.${plainly(collection)}.updateOne(${filter(id)}, {"$unset": {${json(dotted)}: 1}})`;
}

function filter(id: unknown): string {
  return `{"_id": ${json(id)}}`;
}

function json(value: unknown): string {
  // JSON.stringify answers undefined for a value JSON has no form for. Nothing
  // that came out of JSON.parse can be one, so this is the guard that says so
  // rather than a case anyone expects to reach.
  const text: string | undefined = JSON.stringify(value);
  if (text === undefined) throw new Error(UNWRITABLE_DOCUMENT);
  return text;
}

function plainly(collection: string): string {
  if (!isPlainCollection(collection)) throw new Error(UNNAMEABLE_COLLECTION);
  return collection;
}
