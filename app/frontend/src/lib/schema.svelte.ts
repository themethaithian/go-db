// The Database tree's model: what the tool window knows about each connected
// Profile's schema, and how it comes to know it.
//
// Like the panel sizes, this lives at module scope so the tree the human
// arranged — which Profiles are open, which databases and tables are
// expanded, which table they are looking at — is still there when they come
// back from Connections.
//
// The tree has three levels, because a Profile does not have to name a
// database: Profile → databases → tables. A Profile whose Database field is
// blank connects to a server, not to a schema, and the only honest thing to
// show under it is what the server has. A Profile that does name one is
// opened on it, so nothing changes for the human who set one.
//
// Caching is per Profile *and* database, and lasts until refresh: databases
// are fetched the first time a Profile is expanded, a database's tables the
// first time that database is expanded, a table's columns the first time that
// table is expanded (or the first time the Explorer's Structure view asks for
// them), and none of them is fetched again until the human presses refresh or
// the Profile disconnects. A schema is not something that changes under you
// often enough to justify re-asking the database on every click.
//
// The empty string is a database name here, and a meaningful one: it is the
// schema the connection itself is on (the backend compares against DATABASE()
// for it). The Editor asks this cache for whichever database its tab has
// selected — "" until a human picks one — and gets exactly the tables the
// statements in that tab can name unqualified, because the connection they run
// on has that schema as its own default.
//
// Indexes ride the same cache and the same rule, one step behind: nothing
// fetches them until something asks (today, only the Structure view does —
// the tree has no index row to expand), but once fetched they live on the
// TableNode exactly like columns do, so switching the Structure view away
// from a table and back does not re-ask the database.

import {
  ListColumns,
  ListDatabases,
  ListIndexes,
  ListProfiles,
  ListTables,
} from "../../wailsjs/go/app/App";
import type { service } from "../../wailsjs/go/models";
import { attributePins } from "./pins.svelte";

/** One table under a database, plus its lazily loaded columns and indexes. */
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

/** One database under a Profile, plus its lazily loaded tables. */
export type DatabaseNode = {
  expanded: boolean;
  loading: boolean;
  /** null until the tables have been fetched at least once. */
  tables: TableNode[] | null;
  error: string | null;
};

/** One connected Profile — a root of the tree. */
export type ProfileNode = {
  expanded: boolean;
  loading: boolean;
  /** The server's databases, in tree order — null until they are listed. */
  databases: string[] | null;
  error: string | null;
};

// MySQL's own schemas. They are listed like any other — a human does have
// reason to read information_schema — but they are not what anyone connected
// to look at, so the tree puts them last and dims them.
const SYSTEM_DATABASES = new Set(["information_schema", "mysql", "performance_schema", "sys"]);

/** Whether name is one of MySQL's own schemas rather than a human's. */
export function isSystemDatabase(name: string): boolean {
  return SYSTEM_DATABASES.has(name);
}

// The Engine a Profile is assumed to be on until the saved list says
// otherwise. It is what the backend's own Profile store normalizes an unset
// Engine to, so the two agree rather than drifting.
const MYSQL = "mysql";

const nodes = $state<Record<string, ProfileNode>>({});

// The table caches, one per Profile *and* database, keyed by both. A flat map
// rather than a field on ProfileNode because a database can be asked for
// before — or without — the Profile's database list ever being fetched: the
// Editor wants the connection's own schema ("") the moment a Profile is
// picked, and the tree has nothing to do with that.
const schemas = $state<Record<string, DatabaseNode>>({});

// The Profiles' own Database fields, as saved. Two things need them and
// nothing else does: opening a Profile on the database it names, and
// attributing pins written before there was a databases level.
const configured = $state<Record<string, string>>({});

// The Profiles' own Engines, as saved. The Editor needs one before it can ask
// the gate anything: which classifier judges a statement, and where one
// statement ends, are the Engine's to say (ADR-0006). It rides along with
// `configured` because both are read out of the same Profile list.
const engines = $state<Record<string, string>>({});

// Resolves once the configured map reflects the saved Profiles. loadDatabases
// waits on it so the auto-open cannot lose a race with a human who expands a
// Profile the instant it connects.
let configuredLoad: Promise<void> = Promise.resolve();

// Whether the saved Profiles have ever been read. The tree re-reads them on
// every connect, but the tree is not the only thing that needs them any more —
// the Editor's database picker does too, and a human can spend a whole session
// in the Editor without the tree ever syncing.
let configuredRequested = false;

// A NUL cannot appear in a Profile name or a schema name, so a key built with
// it cannot be two different (Profile, database) pairs.
const SEP = "\u0000";

function key(profileName: string, database: string): string {
  return profileName + SEP + database;
}

/**
 * The table the human is looking at: the row the tree highlights, and the one
 * the Explorer's data panel shows. It lives here, next to the tree it belongs
 * to, precisely so those two can never disagree — there is one selection, and
 * both the highlight and the fetch read it. The database is part of it because
 * the browse statement is qualified with it.
 */
export const selected = $state<{
  profile: string | null;
  database: string | null;
  table: string | null;
}>({
  profile: null,
  database: null,
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

/** Makes this database's table the selected one. */
export function selectTable(profileName: string, database: string, table: string) {
  if (selected.profile !== profileName || selected.database !== database || selected.table !== table) {
    browse.filter = "";
  }
  selected.profile = profileName;
  selected.database = database;
  selected.table = table;
}

function freshProfile(): ProfileNode {
  return { expanded: false, loading: false, databases: null, error: null };
}

function freshDatabase(): DatabaseNode {
  return { expanded: false, loading: false, tables: null, error: null };
}

// What a node looks like in the instant between the thing it stands for
// appearing and the store giving it a node of its own: collapsed and empty,
// which is also what it would look like anyway. Frozen because reading it is
// the only legitimate thing to do with it — anything that tried to expand
// *this* object would be writing to a node nobody owns, and should say so
// loudly rather than lose the write.
const PLACEHOLDER_PROFILE: ProfileNode = Object.freeze(freshProfile());
const PLACEHOLDER_DATABASE: DatabaseNode = Object.freeze(freshDatabase());

/**
 * The tree node for profileName — a pure read, safe to call while rendering.
 * Nodes are created by syncProfiles, never here: creating state in the middle
 * of a render is exactly the mutation Svelte refuses.
 */
export function profileNode(profileName: string): ProfileNode {
  return nodes[profileName] ?? PLACEHOLDER_PROFILE;
}

/** The same, one level down: the cache for one database of one Profile. */
export function databaseNode(profileName: string, database: string): DatabaseNode {
  return schemas[key(profileName, database)] ?? PLACEHOLDER_DATABASE;
}

function ensureProfile(profileName: string): ProfileNode {
  if (nodes[profileName] === undefined) nodes[profileName] = freshProfile();
  return nodes[profileName];
}

function ensureDatabase(profileName: string, database: string): DatabaseNode {
  const at = key(profileName, database);
  if (schemas[at] === undefined) schemas[at] = freshDatabase();
  return schemas[at];
}

/**
 * Brings the tree's roots in line with the Profiles that are connected right
 * now: a newly connected Profile gets an empty node, and one that has gone
 * away takes its cached schema with it, so reconnecting shows the database as
 * it is now rather than as it was.
 *
 * It also re-reads the saved Profiles, which is how a Profile created or
 * edited since launch gets opened on the database it names.
 */
export function syncProfiles(connected: string[]) {
  for (const name of Object.keys(nodes)) {
    if (!connected.includes(name)) {
      delete nodes[name];
      forgetSchemas(name);
    }
  }
  for (const name of connected) ensureProfile(name);
  if (selected.profile !== null && !connected.includes(selected.profile)) {
    selected.profile = null;
    selected.database = null;
    selected.table = null;
    browse.filter = "";
  }
  void reloadConfigured();
}

/** Expands or collapses a Profile, loading its databases the first time. */
export function toggleProfile(profileName: string) {
  const node = ensureProfile(profileName);
  node.expanded = !node.expanded;
  if (node.expanded && node.databases === null && !node.loading) {
    void loadDatabases(profileName);
  }
}

/**
 * Loads a Profile's databases if nothing has yet, without expanding it — the
 * Editor's way into the same list the tree shows. The Editor's database
 * picker needs the names the moment a Profile is chosen, which is long before
 * anyone expands anything in the tree.
 */
export function ensureDatabases(profileName: string) {
  const node = ensureProfile(profileName);
  if (!configuredRequested) void reloadConfigured();
  if (node.databases === null && !node.loading) void loadDatabases(profileName);
}

/** The databases cached for a Profile, in tree order, or null before they load. */
export function databasesOf(profileName: string): string[] | null {
  return nodes[profileName]?.databases ?? null;
}

/**
 * The database a Profile names in its saved settings, or "" when it names
 * none. It is the schema the Profile's own connection is already on, which is
 * what the Editor's picker calls the default rather than offering as one more
 * choice beside itself.
 */
export function configuredDatabase(profileName: string): string {
  return configured[profileName] ?? "";
}

/**
 * The Engine of a saved Profile: what language its statements are written in,
 * and so which classifier and which splitter the gate uses on them.
 *
 * Anything not yet known is MySQL — no Profile picked, the Profile list still
 * loading, a name nobody saved. That is the badge being advisory rather than a
 * guess with consequences: nothing runs on this answer, and what does run is
 * judged by the Profile's own Engine, which the backend reads for itself.
 */
export function engineOf(profileName: string | null): string {
  if (profileName === null) return MYSQL;
  if (!configuredRequested) void reloadConfigured();
  return engines[profileName] ?? MYSQL;
}

/** Expands or collapses a database, loading its tables the first time. */
export function toggleDatabase(profileName: string, database: string) {
  const node = ensureDatabase(profileName, database);
  node.expanded = !node.expanded;
  if (node.expanded && node.tables === null && !node.loading) {
    void loadTables(profileName, database);
  }
}

/**
 * Re-reads the schema of every Profile the tree has an opinion about,
 * keeping what is expanded expanded. This is the whole of cache invalidation:
 * there is no staleness heuristic, only a human saying "look again".
 */
export function refreshAll() {
  for (const [profileName, node] of Object.entries(nodes)) {
    for (const [at, schema] of Object.entries(schemas)) {
      if (!at.startsWith(profileName + SEP)) continue;
      const database = at.slice(profileName.length + SEP.length);
      const expandedTables = new Set(
        (schema.tables ?? []).filter((t) => t.expanded).map((t) => t.name),
      );
      schema.tables = null;
      schema.error = null;
      if (schema.expanded) void loadTables(profileName, database, expandedTables);
    }
    node.databases = null;
    node.error = null;
    if (node.expanded) void loadDatabases(profileName);
  }
}

/**
 * Loads a database's tables if nothing has yet, without expanding anything —
 * the Editor's way into the same cache the tree fills. A statement can name a
 * table the human has never clicked on, and asking what its primary key is has
 * to start with knowing the table exists.
 */
export function ensureTables(profileName: string, database: string) {
  const node = ensureDatabase(profileName, database);
  if (node.tables === null && !node.loading) void loadTables(profileName, database);
}

/** Expands or collapses a table, loading its columns the first time. */
export function toggleTable(profileName: string, database: string, table: TableNode) {
  table.expanded = !table.expanded;
  if (table.expanded) ensureColumns(profileName, database, table);
}

/** The tables cached for one database, or null before they have loaded. */
export function tablesOf(profileName: string, database: string): TableNode[] | null {
  return schemas[key(profileName, database)]?.tables ?? null;
}

/**
 * Finds the tree node for table in one database, if the tree has fetched that
 * far — null before the database's tables have loaded, or if no table by that
 * name exists. The Explorer's Structure view uses this to reach the same cache
 * the tree populates, rather than keeping a second copy of it.
 */
export function findTable(
  profileName: string,
  database: string,
  table: string,
): TableNode | null {
  const tables = tablesOf(profileName, database);
  if (tables === null) return null;
  return tables.find((t) => t.name === table) ?? null;
}

/**
 * Loads table's columns into the cache if nothing has fetched them yet — safe
 * to call whether or not the table's tree row is expanded, so the Structure
 * view can ask for columns without touching that unrelated UI state.
 */
export function ensureColumns(profileName: string, database: string, table: TableNode) {
  if (table.columns === null && !table.loading) void loadColumns(profileName, database, table);
}

/** The same as ensureColumns, for the indexes half of the cache. */
export function ensureIndexes(profileName: string, database: string, table: TableNode) {
  if (table.indexes === null && !table.indexesLoading) {
    void loadIndexes(profileName, database, table);
  }
}

/**
 * Forces a fresh fetch of table's columns and indexes, discarding whatever is
 * cached — the Structure view's refresh, the same "look again" contract
 * refreshAll gives the tree.
 */
export function refreshTableStructure(
  profileName: string,
  database: string,
  table: TableNode,
) {
  table.columns = null;
  table.error = null;
  table.indexes = null;
  table.indexesError = null;
  void loadColumns(profileName, database, table);
  void loadIndexes(profileName, database, table);
}

// Every schema of this Profile leaves the cache with it. Iterating the keys is
// the price of a flat map, and it is paid once per disconnect.
function forgetSchemas(profileName: string) {
  for (const at of Object.keys(schemas)) {
    if (at.startsWith(profileName + SEP)) delete schemas[at];
  }
}

// User schemas first, in the order the server gave them (by name), then
// MySQL's own. Sorting here rather than in the backend keeps the domain
// answering what the server said and leaves "which of these did anyone
// actually connect for" as the tree's opinion, which is what it is.
function orderDatabases(names: string[]): string[] {
  return [...names.filter((n) => !isSystemDatabase(n)), ...names.filter(isSystemDatabase)];
}

// Re-reads the saved Profiles into the configured map, and hands it to the
// pins so old ones can be attributed. A failure is not worth reporting: the
// only thing lost is the auto-open, and the tree still lists every database.
function reloadConfigured(): Promise<void> {
  configuredRequested = true;
  configuredLoad = ListProfiles().then(
    (profiles) => {
      for (const name of Object.keys(configured)) delete configured[name];
      for (const name of Object.keys(engines)) delete engines[name];
      for (const profile of profiles) {
        configured[profile.Name] = profile.Database ?? "";
        engines[profile.Name] = profile.Engine || MYSQL;
      }
      attributePins(configured);
    },
    () => {},
  );
  return configuredLoad;
}

async function loadDatabases(profileName: string) {
  const node = ensureProfile(profileName);
  node.loading = true;
  node.error = null;
  try {
    const result = await ListDatabases(profileName);
    if (result.status !== "ok") {
      node.error = result.message;
      node.databases = null;
      return;
    }
    const names = orderDatabases(result.databases ?? []);
    node.databases = names;
    for (const name of names) ensureDatabase(profileName, name);

    // A Profile that names a database is opened on it: the human who set one
    // sees their tables where they have always been, one level in.
    await configuredLoad;
    const home = configured[profileName] ?? "";
    if (home !== "" && names.includes(home)) {
      ensureDatabase(profileName, home).expanded = true;
    }
    for (const name of names) {
      const schema = ensureDatabase(profileName, name);
      if (schema.expanded && schema.tables === null && !schema.loading) {
        void loadTables(profileName, name);
      }
    }
  } catch (err) {
    node.error = String(err);
    node.databases = null;
  } finally {
    node.loading = false;
  }
}

async function loadTables(
  profileName: string,
  database: string,
  reExpand: Set<string> = new Set(),
) {
  const node = ensureDatabase(profileName, database);
  node.loading = true;
  node.error = null;
  try {
    const result = await ListTables(profileName, database);
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
      if (table.expanded) void loadColumns(profileName, database, table);
    }
  } catch (err) {
    node.error = String(err);
    node.tables = null;
  } finally {
    node.loading = false;
  }
}

async function loadColumns(profileName: string, database: string, table: TableNode) {
  table.loading = true;
  table.error = null;
  try {
    const result = await ListColumns(profileName, database, table.name);
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

async function loadIndexes(profileName: string, database: string, table: TableNode) {
  table.indexesLoading = true;
  table.indexesError = null;
  try {
    const result = await ListIndexes(profileName, database, table.name);
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
