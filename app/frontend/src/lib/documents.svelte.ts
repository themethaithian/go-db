// Query tabs: the documents open in the Editor.
//
// A document is a named buffer of SQL and nothing else — no file behind it, no
// path on disk, no dirty flag. The human keeps several lines of enquiry open
// side by side and expects to find them exactly as they left them after a
// restart, and that is the whole contract. It is why this lives at module
// scope and mirrors itself into localStorage rather than inside EditorView,
// which is unmounted the moment another view is selected and used to take the
// text with it.
//
// What is deliberately not stored: results, classification, a pending Inline
// Confirm. Those belong to a run, not to a document — a result set restored
// from a week ago beside its query would be a screen that lies about what the
// database holds now. Switching tabs clears them; only the SQL, the Profile
// and the database come back.
//
// The Profile and database are per document rather than one shared,
// view-level choice, for the same reason the SQL is: a tab is a line of
// enquiry against a particular connection, and two tabs are free to be two
// different connections at once. EditorView reads and writes both through
// this store rather than holding either itself, so switching tabs switches
// which connection everything downstream — the language mode, the
// completion schema, the database picker, Run itself — is talking about.
//
// There is always at least one document and activeId always names one of them.
// Every reader depends on that, so both are re-established here — on a first
// run, on unreadable storage, and after closing the last tab — rather than
// asked about at each call site.

/** One open tab: a name the human chose (or was given) and the SQL under it. */
export type QueryDocument = {
  id: string;
  name: string;
  sql: string;
  /**
   * The database this tab's statements run in — the Connection Registry opens
   * a connection whose default schema it is, so unqualified SQL resolves
   * there. "" is the Profile's own connection, and the default.
   *
   * It belongs to the document rather than to the view because it is part of
   * what the query means: the same text against two schemas is two different
   * questions, and a tab that came back on the wrong one would answer the
   * wrong question with no sign that it had.
   */
  database: string;
  /**
   * The Profile this tab's statements run against — the other half of the
   * same answer `database` is: which connection. null means none has been
   * picked yet, which is a tab's normal starting state, not an error — the
   * picker simply renders empty rather than guessing.
   *
   * It belongs to the document for the same reason `database` does: a tab
   * left open on api-primary and a tab left open on api-replica are two
   * different questions asked with the same SQL, and a shared, view-level
   * "current Profile" is exactly what would let switching tabs answer the
   * wrong one — which is the bug this field exists to close. A Profile
   * deleted out from under a tab is not repaired to some other Profile here;
   * the picker is left to render what null already renders, empty, rather
   * than silently substituting a guess.
   */
  profile: string | null;
};

const STORAGE_KEY = "go-db:query-documents";

// Storage is written behind the typing rather than on every keystroke: the
// buffer is small, but a synchronous localStorage write per character is a
// cost paid on the one path that has to stay imperceptible.
const PERSIST_DEBOUNCE_MS = 300;

type Workspace = {
  docs: QueryDocument[];
  activeId: string;
};

// Ids only have to be unique within this workspace and stable across a
// restart, and crypto.randomUUID is not available on every scheme a webview
// might serve the app from — a counter beside the clock is enough and cannot
// fail. It is declared up here because restoring the workspace below already
// needs one.
let counter = 0;

function freshId(): string {
  counter += 1;
  return `${Date.now().toString(36)}-${counter.toString(36)}`;
}

// Plain objects in an array, so $state deep-proxies them and the tab strip
// hears about a rename or a keystroke without anything being re-assigned.
const workspace = $state<Workspace>(restore());

/**
 * The open documents and which one is in front. A reading facade: tabs are
 * added, closed, renamed and written to through the functions below, so the
 * mirror to storage cannot be forgotten at a call site.
 */
export const documents = {
  /** Every open tab, in strip order. */
  get open(): readonly QueryDocument[] {
    return workspace.docs;
  },
  /** The document the Editor is showing. Never null — there is always a tab. */
  get active(): QueryDocument {
    return workspace.docs.find((doc) => doc.id === workspace.activeId) ?? workspace.docs[0];
  },
};

/**
 * Opens a new tab and brings it to the front.
 *
 * A handover from another view (the Explorer's "Open in editor") passes the
 * statement and a name for it; the + button passes neither and gets an empty
 * "Query N". Handing over a statement that is already open in some tab
 * activates that tab instead of stacking up identical copies — a human
 * clicking "Open in editor" twice means "show me that", not "give me two".
 * Empty documents are never deduplicated that way, or + would stop making
 * tabs the moment one blank tab existed.
 *
 * A new tab inherits the Profile and database the human is working in — the
 * + button passes the active tab's own, and the Explorer's "Open in editor"
 * passes the ones it was browsing — since a new line of enquiry is almost
 * always about the same connection as the one beside it. Callers that pass
 * neither (a saved query reopened by name, say) get a blank tab: null is a
 * legitimate, ordinary starting point, not a state anything here works
 * around.
 */
export function openDocument(
  sql: string = "",
  name: string = nextName(),
  database: string = "",
  profile: string | null = null,
) {
  if (sql !== "") {
    const existing = workspace.docs.find((doc) => doc.sql === sql);
    if (existing !== undefined) {
      activateDocument(existing.id);
      return;
    }
  }
  const doc: QueryDocument = { id: freshId(), name, sql, database, profile };
  workspace.docs.push(doc);
  workspace.activeId = doc.id;
  persist();
}

/** Brings a tab to the front. Unknown ids are ignored rather than blanking the editor. */
export function activateDocument(id: string) {
  if (!workspace.docs.some((doc) => doc.id === id)) return;
  workspace.activeId = id;
  persist();
}

/**
 * Closes a tab. Closing the front one falls to its neighbour — the tab that
 * slid into its place, or the last one if it was the last — and closing the
 * only one leaves a fresh empty "Query 1" behind, since an Editor with no
 * document is a state nothing here is prepared to render.
 */
export function closeDocument(id: string) {
  const at = workspace.docs.findIndex((doc) => doc.id === id);
  if (at === -1) return;

  workspace.docs.splice(at, 1);
  if (workspace.docs.length === 0) {
    const fresh: QueryDocument = { id: freshId(), name: nextName(), sql: "", database: "", profile: null };
    workspace.docs.push(fresh);
    workspace.activeId = fresh.id;
  } else if (workspace.activeId === id) {
    workspace.activeId = workspace.docs[Math.min(at, workspace.docs.length - 1)].id;
  }
  persist();
}

/**
 * Renames a tab. A name that is only whitespace is refused rather than
 * committed — an unnameable tab is worse than the name it had.
 */
export function renameDocument(id: string, name: string) {
  const trimmed = name.trim();
  const doc = workspace.docs.find((entry) => entry.id === id);
  if (doc === undefined || trimmed === "") return;
  doc.name = trimmed;
  persist();
}

/** Records what the human has typed into the front tab. */
export function writeSql(sql: string) {
  const doc = documents.active;
  if (doc.sql === sql) return;
  doc.sql = sql;
  persist();
}

/**
 * Records the database the front tab's statements run in. Flushed straight
 * to storage rather than going through the typing debounce above: a database
 * pick is one discrete choice, not a run of keystrokes, so there is nothing
 * to buffer it behind and no reason to risk losing it to a reload that lands
 * in the debounce window.
 */
export function writeDatabase(database: string) {
  const doc = documents.active;
  if (doc.database === database) return;
  doc.database = database;
  flush();
}

/**
 * Records the Profile the front tab's statements run against. Same
 * immediate-flush contract as writeDatabase, and for the same reason: a
 * Profile pick is a discrete choice the human just made, not typing.
 */
export function writeProfile(profile: string | null) {
  const doc = documents.active;
  if (doc.profile === profile) return;
  doc.profile = profile;
  flush();
}

// Names count from the highest "Query N" in use rather than from the number of
// tabs, so closing Query 2 of three does not hand its name to the next new tab
// while Query 3 is still on screen.
const GENERATED_NAME = /^Query (\d+)$/;

function nextName(): string {
  let highest = 0;
  for (const doc of workspace.docs) {
    const match = GENERATED_NAME.exec(doc.name);
    if (match !== null) highest = Math.max(highest, Number(match[1]));
  }
  return `Query ${highest + 1}`;
}

// A stored workspace is untrusted input like any other: anything that is not a
// document is dropped, repeated ids are dropped (they would collide as keys in
// the tab strip), and a file that survives none of that starts the human on a
// clean tab rather than an empty strip.
function restore(): Workspace {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return blank();

    const parsed = JSON.parse(raw) as { docs?: unknown; activeId?: unknown };
    const stored = Array.isArray(parsed?.docs) ? parsed.docs : [];

    const seen = new Set<string>();
    const docs: QueryDocument[] = [];
    for (const entry of stored) {
      if (!isDocument(entry) || seen.has(entry.id)) continue;
      seen.add(entry.id);
      docs.push({
        id: entry.id,
        name: entry.name,
        sql: entry.sql,
        // Tabs written before there was a database picker have none, and the
        // Profile's own connection is what they ran on.
        database: typeof entry.database === "string" ? entry.database : "",
        // Tabs written before a Profile belonged to the tab (it used to be
        // one shared, view-level choice) have none recorded either, and
        // there is nothing honest to backfill it with but "not picked yet".
        profile: typeof entry.profile === "string" ? entry.profile : null,
      });
    }
    if (docs.length === 0) return blank();

    const activeId = typeof parsed.activeId === "string" ? parsed.activeId : "";
    return { docs, activeId: seen.has(activeId) ? activeId : docs[0].id };
  } catch {
    return blank();
  }
}

function isDocument(value: unknown): value is QueryDocument {
  const doc = value as QueryDocument | null;
  return (
    typeof doc === "object" &&
    doc !== null &&
    typeof doc.id === "string" &&
    doc.id !== "" &&
    typeof doc.name === "string" &&
    typeof doc.sql === "string"
  );
}

function blank(): Workspace {
  const doc: QueryDocument = { id: freshId(), name: "Query 1", sql: "", database: "", profile: null };
  return { docs: [doc], activeId: doc.id };
}

let pending: ReturnType<typeof setTimeout> | undefined;

function persist() {
  clearTimeout(pending);
  pending = setTimeout(flush, PERSIST_DEBOUNCE_MS);
}

function flush() {
  clearTimeout(pending);
  pending = undefined;
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        docs: workspace.docs.map((doc) => ({
          id: doc.id,
          name: doc.name,
          sql: doc.sql,
          database: doc.database,
          profile: doc.profile,
        })),
        activeId: workspace.activeId,
      }),
    );
  } catch {
    // A full or disabled storage costs the human their tabs on the next
    // launch, and nothing else — never a broken editor.
  }
}

// The debounce is short, but a reload landing inside it would lose the last
// few characters typed — the one loss this store exists to prevent. Leaving
// the page writes whatever is outstanding first.
if (typeof window !== "undefined") {
  window.addEventListener("pagehide", flush);
}
