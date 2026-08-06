// Pinned tables: the handful a human is actually investigating right now,
// lifted to the top of their Profile in the Explorer tree.
//
// A pin is a bookmark, not a piece of schema — it says something about the
// person, not about the database. So it lives here rather than in
// schema.svelte.ts (which caches what the database said and drops it on
// disconnect), and it is written straight to localStorage: pins are expected
// to outlive a disconnect, a reload, and a restart of the app.
//
// A pin names a table by Profile and table name, and nothing else. If the
// table is dropped or the Profile renamed, the pin simply stops matching:
// nothing is rendered for a pin whose table is not in the Profile's table
// list, and it costs a few bytes of storage until the human pins something
// else.

/** One pinned table, as stored and as held. */
type Pin = {
  profile: string;
  table: string;
};

const STORAGE_KEY = "go-db:pinned-tables";

// Stored pins are untrusted input like any other: anything that is not a pair
// of strings is dropped rather than rendered as an "undefined" row.
function restore(): Pin[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (entry): entry is Pin =>
          typeof entry === "object" &&
          entry !== null &&
          typeof (entry as Pin).profile === "string" &&
          typeof (entry as Pin).table === "string",
      )
      .map((entry) => ({ profile: entry.profile, table: entry.table }));
  } catch {
    return [];
  }
}

// The pinned tables. Plain objects in an array, so $state deep-proxies them
// and every star in the tree hears about a pin the moment it happens.
const pins = $state<Pin[]>(restore());

/** Whether this Profile's table is pinned. A pure read, safe while rendering. */
export function isPinned(profile: string, table: string): boolean {
  return indexOfPin(profile, table) !== -1;
}

/** Pins an unpinned table, unpins a pinned one. */
export function togglePin(profile: string, table: string) {
  const at = indexOfPin(profile, table);
  if (at === -1) pins.push({ profile, table });
  else pins.splice(at, 1);
  persist();
}

/**
 * The pinned members of `tables`, in the order given — so the PINNED group
 * reads in the same order as the list it is a shortcut into, and a table that
 * has gone away is simply absent.
 */
export function pinnedAmong<T extends { name: string }>(profile: string, tables: T[]): T[] {
  return tables.filter((table) => isPinned(profile, table.name));
}

function indexOfPin(profile: string, table: string): number {
  return pins.findIndex((pin) => pin.profile === profile && pin.table === table);
}

function persist() {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(pins.map((pin) => ({ profile: pin.profile, table: pin.table }))),
    );
  } catch {
    // A full or disabled storage costs the human their pins on the next
    // launch, and nothing else — never a broken tree.
  }
}
