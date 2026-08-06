// The Database tree's model: what the tool window knows about each connected
// Profile's schema, and how it comes to know it.
//
// Like the panel sizes, this lives at module scope so the tree the human
// arranged — which Profiles are open, which tables are expanded, which table
// they are looking at — is still there when they come back from Connections.
//
// Caching is per Profile and lasts until refresh: tables are fetched the
// first time a Profile is expanded, a table's columns the first time that
// table is expanded (or the first time the Explorer's Structure view asks
// for them), and neither is fetched again until the human presses refresh or
// the Profile disconnects. A schema is not something that changes under you
// often enough to justify re-asking the database on every click.
//
// Indexes ride the same cache and the same rule, one step behind: nothing
// fetches them until something asks (today, only the Structure view does —
// the tree has no index row to expand), but once fetched they live on the
// TableNode exactly like columns do, so switching the Structure view away
// from a table and back does not re-ask the database.

import { ListColumns, ListIndexes, ListTables } from "../../wailsjs/go/app/App";
import type { service } from "../../wailsjs/go/models";

/** One table under a Profile, plus its lazily loaded columns and indexes. */
export type TableNode = {
  name: string;
  rowEstimate: number | null;
  expanded: boolean;
  loading: boolean;
  /** null until the columns have been fetched at least once. */
  columns: service.ColumnInfo[] | null;
  error: string | null;
  indexesLoading: boolean;
  /** null until the indexes have been fetched at least once. */
  indexes: service.IndexInfo[] | null;
  indexesError: string | null;
};

/** One connected Profile — a root of the tree. */
export type ProfileNode = {
  expanded: boolean;
  loading: boolean;
  /** null until the tables have been fetched at least once. */
  tables: TableNode[] | null;
  error: string | null;
};

const nodes = $state<Record<string, ProfileNode>>({});

/**
 * The table the human is looking at: the row the tree highlights, and the one
 * the Explorer's data panel shows. It lives here, next to the tree it belongs
 * to, precisely so those two can never disagree — there is one selection, and
 * both the highlight and the fetch read it.
 */
export const selected = $state<{
  profile: string | null;
  table: string | null;
}>({
  profile: null,
  table: null,
});

/** The row counts the Explorer offers, smallest first. */
export const BROWSE_LIMITS = [50, 200, 500, 1000] as const;

/**
 * How the selected table is being looked at: the WHERE condition in force
 * (empty for none) and how many rows to ask for. It lives beside the
 * selection because it is scoped by it — a condition written for `orders`
 * means nothing against `users`, so selecting another table drops it, and
 * that rule is enforced in one place (selectTable) rather than in whichever
 * component happened to notice.
 *
 * The limit is deliberately not scoped that way: it says how much data this
 * human wants to see at a time, which is true of the next table too. It stays
 * put for the session, and is not persisted — the next launch starts at the
 * default, which is the size that comes back instantly on anything.
 */
export const browse = $state<{ filter: string; limit: number }>({
  filter: "",
  limit: 200,
});

/** Makes this Profile's table the selected one. */
export function selectTable(profileName: string, table: string) {
  if (selected.profile !== profileName || selected.table !== table) {
    browse.filter = "";
  }
  selected.profile = profileName;
  selected.table = table;
}

function fresh(): ProfileNode {
  return { expanded: false, loading: false, tables: null, error: null };
}

// What a Profile looks like in the instant between it appearing in the
// connected list and syncProfiles giving it a node of its own: collapsed and
// empty, which is also what it would look like anyway. Frozen because reading
// it is the only legitimate thing to do with it — anything that tried to
// expand *this* object would be writing to a node no Profile owns, and should
// say so loudly rather than lose the write.
const PLACEHOLDER: ProfileNode = Object.freeze(fresh());

/**
 * The tree node for profileName — a pure read, safe to call while rendering.
 * Nodes are created by syncProfiles, never here: creating state in the middle
 * of a render is exactly the mutation Svelte refuses.
 */
export function profileNode(profileName: string): ProfileNode {
  return nodes[profileName] ?? PLACEHOLDER;
}

function ensure(profileName: string): ProfileNode {
  if (nodes[profileName] === undefined) nodes[profileName] = fresh();
  return nodes[profileName];
}

/**
 * Brings the tree's roots in line with the Profiles that are connected right
 * now: a newly connected Profile gets an empty node, and one that has gone
 * away takes its cached schema with it, so reconnecting shows the database as
 * it is now rather than as it was.
 */
export function syncProfiles(connected: string[]) {
  for (const name of Object.keys(nodes)) {
    if (!connected.includes(name)) delete nodes[name];
  }
  for (const name of connected) ensure(name);
  if (selected.profile !== null && !connected.includes(selected.profile)) {
    selected.profile = null;
    selected.table = null;
    browse.filter = "";
  }
}

/** Expands or collapses a Profile, loading its tables the first time. */
export function toggleProfile(profileName: string) {
  const node = ensure(profileName);
  node.expanded = !node.expanded;
  if (node.expanded && node.tables === null && !node.loading) {
    void loadTables(profileName);
  }
}

/**
 * Re-reads the schema of every Profile the tree has an opinion about,
 * keeping what is expanded expanded. This is the whole of cache invalidation:
 * there is no staleness heuristic, only a human saying "look again".
 */
export function refreshAll() {
  for (const [name, node] of Object.entries(nodes)) {
    const expandedTables = new Set(
      (node.tables ?? []).filter((t) => t.expanded).map((t) => t.name),
    );
    node.tables = null;
    node.error = null;
    if (node.expanded) void loadTables(name, expandedTables);
  }
}

/**
 * Loads a Profile's tables if nothing has yet, without expanding anything —
 * the Editor's way into the same cache the tree fills. A statement can name a
 * table the human has never clicked on, and asking what its primary key is has
 * to start with knowing the table exists.
 */
export function ensureTables(profileName: string) {
  const node = ensure(profileName);
  if (node.tables === null && !node.loading) void loadTables(profileName);
}

/** Expands or collapses a table, loading its columns the first time. */
export function toggleTable(profileName: string, table: TableNode) {
  table.expanded = !table.expanded;
  if (table.expanded) ensureColumns(profileName, table);
}

/**
 * Finds the tree node for table under profileName, if the tree has fetched
 * that far — null before the Profile's tables have loaded, or if no table by
 * that name exists. The Explorer's Structure view uses this to reach the same
 * cache the tree populates, rather than keeping a second copy of it.
 */
export function findTable(profileName: string, table: string): TableNode | null {
  const tables = nodes[profileName]?.tables;
  if (tables == null) return null;
  return tables.find((t) => t.name === table) ?? null;
}

/**
 * Loads table's columns into the cache if nothing has fetched them yet — safe
 * to call whether or not the table's tree row is expanded, so the Structure
 * view can ask for columns without touching that unrelated UI state.
 */
export function ensureColumns(profileName: string, table: TableNode) {
  if (table.columns === null && !table.loading) void loadColumns(profileName, table);
}

/** The same as ensureColumns, for the indexes half of the cache. */
export function ensureIndexes(profileName: string, table: TableNode) {
  if (table.indexes === null && !table.indexesLoading) void loadIndexes(profileName, table);
}

/**
 * Forces a fresh fetch of table's columns and indexes, discarding whatever is
 * cached — the Structure view's refresh, the same "look again" contract
 * refreshAll gives the tree.
 */
export function refreshTableStructure(profileName: string, table: TableNode) {
  table.columns = null;
  table.error = null;
  table.indexes = null;
  table.indexesError = null;
  void loadColumns(profileName, table);
  void loadIndexes(profileName, table);
}

async function loadTables(profileName: string, reExpand: Set<string> = new Set()) {
  const node = ensure(profileName);
  node.loading = true;
  node.error = null;
  try {
    const result = await ListTables(profileName);
    if (result.status !== "ok") {
      node.error = result.message;
      node.tables = null;
      return;
    }
    node.tables = (result.tables ?? []).map((info) => ({
      name: info.name,
      rowEstimate: info.row_estimate ?? null,
      expanded: reExpand.has(info.name),
      loading: false,
      columns: null,
      error: null,
      indexesLoading: false,
      indexes: null,
      indexesError: null,
    }));
    for (const table of node.tables) {
      if (table.expanded) void loadColumns(profileName, table);
    }
  } catch (err) {
    node.error = String(err);
    node.tables = null;
  } finally {
    node.loading = false;
  }
}

async function loadColumns(profileName: string, table: TableNode) {
  table.loading = true;
  table.error = null;
  try {
    const result = await ListColumns(profileName, table.name);
    if (result.status !== "ok") {
      table.error = result.message;
      table.columns = null;
      return;
    }
    table.columns = result.columns ?? [];
  } catch (err) {
    table.error = String(err);
    table.columns = null;
  } finally {
    table.loading = false;
  }
}

async function loadIndexes(profileName: string, table: TableNode) {
  table.indexesLoading = true;
  table.indexesError = null;
  try {
    const result = await ListIndexes(profileName, table.name);
    if (result.status !== "ok") {
      table.indexesError = result.message;
      table.indexes = null;
      return;
    }
    table.indexes = result.indexes ?? [];
  } catch (err) {
    table.indexesError = String(err);
    table.indexes = null;
  } finally {
    table.indexesLoading = false;
  }
}
