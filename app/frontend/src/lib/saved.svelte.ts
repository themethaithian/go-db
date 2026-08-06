// Saved queries: the editor's library of named SQL a human wants back later —
// across tab closes, across restarts — distinct from the open tabs in
// documents.svelte.ts, which are the working set for *right now*.
//
// A saved query is a name and a statement, nothing else: no Profile, no
// results, no folder. Saving under a name that is already in the library
// overwrites that entry rather than growing a second copy — "save" reads as
// "keep this version" the same way it would in any editor, not as "add
// another".

import { openDocument } from "./documents.svelte";

/** One saved query, as stored and as held. */
export type SavedQuery = {
  id: string;
  name: string;
  sql: string;
  /** ISO timestamp of the most recent save under this name. */
  savedAt: string;
};

const STORAGE_KEY = "go-db:saved-queries";

// Same rationale as documents.svelte.ts: the buffer being saved is small, but
// a synchronous write on every mutation is a cost worth deferring.
const PERSIST_DEBOUNCE_MS = 300;

let counter = 0;

function freshId(): string {
  counter += 1;
  return `${Date.now().toString(36)}-${counter.toString(36)}`;
}

// Plain objects in an array, so $state deep-proxies them and the popover
// hears about a save or a delete without anything being re-assigned.
const library = $state<SavedQuery[]>(restore());

/**
 * The saved queries, newest save first — the order the popover lists them
 * in, so the query someone just saved is the one they see at the top.
 */
export const saved = {
  get all(): readonly SavedQuery[] {
    return [...library].sort((a, b) => b.savedAt.localeCompare(a.savedAt));
  },
  get count(): number {
    return library.length;
  },
};

/**
 * Saves `sql` under `name`. An existing entry with the same name
 * (case-sensitive) has its sql and savedAt updated in place — same id, so a
 * human re-saving "bangkok users" a dozen times still has one entry, not a
 * dozen. A new name gets a new entry.
 */
export function saveQuery(name: string, sql: string) {
  const existing = library.find((entry) => entry.name === name);
  const savedAt = new Date().toISOString();
  if (existing !== undefined) {
    existing.sql = sql;
    existing.savedAt = savedAt;
  } else {
    library.push({ id: freshId(), name, sql, savedAt });
  }
  persist();
}

/** Removes a saved query. Immediate — this is local state, not a server record. */
export function deleteSaved(id: string) {
  const at = library.findIndex((entry) => entry.id === id);
  if (at === -1) return;
  library.splice(at, 1);
  persist();
}

/**
 * Opens a saved query as a tab, named after the saved entry. Unknown ids are
 * ignored. Reuses an existing tab whose sql is byte-identical rather than
 * stacking up a copy — the same handover rule openDocument already applies
 * for the Explorer, so a saved query already open is simply brought forward.
 */
export function openSaved(id: string) {
  const entry = library.find((item) => item.id === id);
  if (entry === undefined) return;
  openDocument(entry.sql, entry.name);
}

function persist() {
  clearTimeout(pending);
  pending = setTimeout(flush, PERSIST_DEBOUNCE_MS);
}

let pending: ReturnType<typeof setTimeout> | undefined;

function flush() {
  clearTimeout(pending);
  pending = undefined;
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(
        library.map((entry) => ({
          id: entry.id,
          name: entry.name,
          sql: entry.sql,
          savedAt: entry.savedAt,
        })),
      ),
    );
  } catch {
    // A full or disabled storage costs the human their library on the next
    // launch, and nothing else — never a broken editor.
  }
}

// A stored library is untrusted input like any other: anything that is not a
// saved query is dropped, repeated ids are dropped (they would collide as
// keys in the popover list).
function restore(): SavedQuery[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];

    const seen = new Set<string>();
    const entries: SavedQuery[] = [];
    for (const entry of parsed) {
      if (!isSavedQuery(entry) || seen.has(entry.id)) continue;
      seen.add(entry.id);
      entries.push({ id: entry.id, name: entry.name, sql: entry.sql, savedAt: entry.savedAt });
    }
    return entries;
  } catch {
    return [];
  }
}

function isSavedQuery(value: unknown): value is SavedQuery {
  const entry = value as SavedQuery | null;
  return (
    typeof entry === "object" &&
    entry !== null &&
    typeof entry.id === "string" &&
    entry.id !== "" &&
    typeof entry.name === "string" &&
    typeof entry.sql === "string" &&
    typeof entry.savedAt === "string"
  );
}

// A reload landing behind the debounce would lose the save that triggered it
// — the one loss this store exists to prevent.
if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flush);
}
