// Which Profile Groups are collapsed in the connection manager's Profile
// list — UI state about how a human likes to look at their own Profiles, not
// anything the backend knows about. Persisted like pins and panel sizes, so a
// group collapsed before quitting is still collapsed on the next launch, and
// so it survives ConnectionsView unmounting when the human switches to
// another view (App.svelte tears the component down, this module does not).
//
// Default is expanded: a group only ever appears here once a human collapses
// it, so a brand-new group — or one nobody has touched yet — starts open.

const STORAGE_KEY = "go-db:collapsed-profile-groups";

// Stored as an object rather than a Set, matching the rest of this app's
// $state records (schema.svelte.ts's `nodes`, `configured`, `engines`):
// plain object keys are exactly what Svelte 5 deep-proxies without reaching
// for a reactivity helper class.
function restore(): Record<string, boolean> {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return {};
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return {};
    const collapsed: Record<string, boolean> = {};
    for (const entry of parsed) {
      if (typeof entry === "string") collapsed[entry] = true;
    }
    return collapsed;
  } catch {
    return {};
  }
}

const collapsed = $state<Record<string, boolean>>(restore());

/** Whether group is currently collapsed in the Profile list. */
export function isGroupCollapsed(group: string): boolean {
  return collapsed[group] === true;
}

/** Collapses an expanded group, expands a collapsed one. */
export function toggleGroupCollapsed(group: string) {
  collapsed[group] = !collapsed[group];
  persist();
}

function persist() {
  try {
    const names = Object.keys(collapsed).filter((name) => collapsed[name]);
    localStorage.setItem(STORAGE_KEY, JSON.stringify(names));
  } catch {
    // A full or disabled storage costs the human their collapsed groups on
    // the next launch, and nothing else — never a broken list.
  }
}
