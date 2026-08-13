// Which Engine's Profiles the Database tree is showing right now, and what
// the human had selected on each Engine the last time they were looking at
// it.
//
// The tree used to render every connected Profile of every Engine as one
// list. Segmented by Engine (v2, ADR-0006), it renders one Engine at a time —
// but the schema cache in schema.svelte.ts keeps every connected Profile's
// tree state regardless of which Engine is on screen, so switching back to an
// Engine the human left five minutes ago must not read as switching to a
// fresh one. Two things are not carried by that cache, because they are not a
// schema: which Profile, database and table the human had *selected* — the
// Explorer's browse pane, in schema.svelte.ts's `selected` — and that gets
// overwritten wholesale the moment another Engine's Profile is picked. This
// module is the memory that survives the overwrite: a snapshot of `selected`
// taken the instant the human leaves an Engine, handed back the instant they
// return to it.
//
// switchEngine is the only way the active Engine changes. It does the
// snapshot-old, restore-new dance in one place, so `selected` is never a step
// behind which Engine's tab is actually highlighted — the Explorer's right
// pane derives straight from `selected`, and a caller updating `active`
// without also updating `selected` would show Engine A's browse under
// Engine B's tab for however long it took Svelte to notice.
//
// The active Engine itself is small enough, and useful enough across a
// restart, to persist the way layout.svelte.ts persists panel sizes — read
// once at module load, written on every switch. The per-Engine selection
// snapshots are not: they are a convenience for a session's worth of
// tab-switching, not a promise about what a human will still want open next
// time they launch the app, so they live only in memory and start empty on
// every launch.

import { MONGODB, MYSQL, REDIS } from "./browse";
import { browse, engineOf, selected } from "./schema.svelte";

const STORAGE_KEY = "go-db:explorer-active-engine";

// The order tabs appear in when more than one Engine is connected — MySQL
// first because it is what v1 shipped with, then the two ADR-0006 added in
// the order they were built.
const ENGINE_ORDER = [MYSQL, REDIS, MONGODB];

type Selection = {
  profile: string | null;
  database: string | null;
  table: string | null;
};

function emptySelection(): Selection {
  return { profile: null, database: null, table: null };
}

// A stored Engine is untrusted input like any other — a value from an older
// build, or a hand-edited localStorage — so anything not in ENGINE_ORDER is
// treated the same as never having stored one.
function restore(): string | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    return raw !== null && ENGINE_ORDER.includes(raw) ? raw : null;
  } catch {
    return null;
  }
}

/** The Engine whose Profiles the Database tree currently renders. */
export const explorerEngine = $state<{ active: string | null }>({ active: restore() });

// One selection per Engine, keyed by the same strings ENGINE_ORDER holds.
// Plain state rather than $state: nothing here is rendered directly, only
// read back by switchEngine, so it does not need to be reactive itself.
const snapshots: Record<string, Selection> = {};

function persist() {
  try {
    if (explorerEngine.active === null) localStorage.removeItem(STORAGE_KEY);
    else localStorage.setItem(STORAGE_KEY, explorerEngine.active);
  } catch {
    // A full or disabled storage costs the human a remembered tab on the next
    // launch, and nothing else — never a broken switcher.
  }
}

/**
 * Every Engine with at least one connected Profile, in tab order, each with
 * how many of its Profiles are connected — exactly what the switcher needs to
 * draw its tabs and what DatabaseTree needs to know whether the active Engine
 * has just lost its last one.
 */
export function enginesPresent(connectedProfiles: string[]): { engine: string; count: number }[] {
  const counts: Record<string, number> = {};
  for (const name of connectedProfiles) {
    const engine = engineOf(name);
    counts[engine] = (counts[engine] ?? 0) + 1;
  }
  return ENGINE_ORDER.filter((engine) => (counts[engine] ?? 0) > 0).map((engine) => ({
    engine,
    count: counts[engine],
  }));
}

/**
 * Switches the Database tree to another Engine — the only way `explorerEngine
 * .active` changes. It snapshots `selected` under whichever Engine was active
 * before the switch (so coming back to it later restores what was open), then
 * restores the target Engine's own snapshot, or clears to the empty state if
 * that Engine has none yet or its snapshotted Profile is no longer connected.
 *
 * `selected` is written synchronously, in the same call that changes `active`
 * — never a tick later — because the Explorer's browse pane derives from
 * `selected` alone and has no idea which tab is highlighted. A gap between
 * the two would show the old Engine's table under the new Engine's tab for
 * however long that gap lasted.
 *
 * The restored (or cleared) selection is always a different table than the
 * one the switch left behind — a Profile belongs to one Engine, so leaving
 * one Engine's tab always leaves that Profile too — which is the same
 * condition selectTable clears the WHERE filter on, so this clears it the
 * same way rather than leaving a condition written for one Engine's table
 * silently in force for another's.
 *
 * engine is nullable for the one caller that has to pass null: DatabaseTree,
 * when the last connected Profile of every Engine has just disconnected and
 * there is no Engine left to switch to.
 */
export function switchEngine(engine: string | null, connectedProfiles: string[]) {
  if (explorerEngine.active === engine) return;

  if (explorerEngine.active !== null) {
    snapshots[explorerEngine.active] = { ...selected };
  }
  explorerEngine.active = engine;
  persist();

  const snapshot = engine === null ? undefined : snapshots[engine];
  const restored =
    snapshot !== undefined && snapshot.profile !== null && connectedProfiles.includes(snapshot.profile)
      ? snapshot
      : emptySelection();
  selected.profile = restored.profile;
  selected.database = restored.database;
  selected.table = restored.table;
  browse.filter = "";
}
