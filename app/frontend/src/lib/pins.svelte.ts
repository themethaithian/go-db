// Pinned tables: the handful a human is actually investigating right now,
// lifted to the top of their database in the Explorer tree.
//
// A pin is a bookmark, not a piece of schema — it says something about the
// person, not about the database. So it lives here rather than in
// schema.svelte.ts (which caches what the database said and drops it on
// disconnect), and it is written straight to localStorage: pins are expected
// to outlive a disconnect, a reload, and a restart of the app.
//
// A pin names a table by Profile, database and table name, and nothing else.
// If the table is dropped or the Profile renamed, the pin simply stops
// matching: nothing is rendered for a pin whose table is not in the database's
// table list, and it costs a few bytes of storage until the human pins
// something else.

/** One pinned table, as stored and as held. */
type Pin = {
  profile: string;
  /**
   * null for a pin written before pins named a database — it is a table name
   * under a Profile and nothing more. It matches nothing until attributePins
   * says which database that Profile was on, and is dropped if that Profile
   * named none.
   */
  database: string | null;
  table: string;
};

const STORAGE_KEY = "go-db:pinned-tables";

// Stored pins are untrusted input like any other: anything that is not a pair
// of strings is dropped rather than rendered as an "undefined" row. A missing
// database is the one exception — that is the old format, kept unattributed
// until attributePins can say which database it meant.
function restore(): Pin[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter(
        (entry): entry is { profile: string; database?: unknown; table: string } =>
          typeof entry === "object" &&
          entry !== null &&
          typeof (entry as Pin).profile === "string" &&
          typeof (entry as Pin).table === "string",
      )
      .map((entry) => ({
        profile: entry.profile,
        database: typeof entry.database === "string" ? entry.database : null,
        table: entry.table,
      }));
  } catch {
    return [];
  }
}

// The pinned tables. Plain objects in an array, so $state deep-proxies them
// and every star in the tree hears about a pin the moment it happens.
const pins = $state<Pin[]>(restore());

/**
 * Gives every pin written in the old format the database its Profile was
 * configured with, and drops the ones that cannot be given one — a Profile
 * that named no database was browsing nothing, so there is no schema a pin
 * under it could have meant, and guessing would put a star on the wrong table.
 *
 * Called once the saved Profiles are known (schema.svelte.ts reads them for
 * the tree anyway). It is idempotent: after it has run there is nothing left
 * for it to do.
 */
export function attributePins(configured: Record<string, string>) {
  if (!pins.some((pin) => pin.database === null)) return;
  const migrated = pins.flatMap((pin) => {
    if (pin.database !== null) return [pin];
    const database = configured[pin.profile] ?? "";
    return database === "" ? [] : [{ ...pin, database }];
  });
  pins.splice(0, pins.length, ...migrated);
  persist();
}

/** Whether this database's table is pinned. A pure read, safe while rendering. */
export function isPinned(profile: string, database: string, table: string): boolean {
  return indexOfPin(profile, database, table) !== -1;
}

/** Pins an unpinned table, unpins a pinned one. */
export function togglePin(profile: string, database: string, table: string) {
  const at = indexOfPin(profile, database, table);
  if (at === -1) pins.push({ profile, database, table });
  else pins.splice(at, 1);
  persist();
}

/**
 * The pinned members of `tables`, in the order given — so the PINNED group
 * reads in the same order as the list it is a shortcut into, and a table that
 * has gone away is simply absent.
 */
export function pinnedAmong<T extends { name: string }>(
  profile: string,
  database: string,
  tables: T[],
): T[] {
  return tables.filter((table) => isPinned(profile, database, table.name));
}

function indexOfPin(profile: string, database: string, table: string): number {
  return pins.findIndex(
    (pin) => pin.profile === profile && pin.database === database && pin.table === table,
  );
}

function persist() {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify(
        pins.map((pin) => ({ profile: pin.profile, database: pin.database, table: pin.table })),
      ),
    );
  } catch {
    // A full or disabled storage costs the human their pins on the next
    // launch, and nothing else — never a broken tree.
  }
}
