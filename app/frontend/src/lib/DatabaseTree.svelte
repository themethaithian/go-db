<script lang="ts">
  // The Database tool window: every connected Profile as a root, the server's
  // databases beneath it, that database's tables beneath those, and a table's
  // columns beneath that. Disconnected Profiles are simply absent — this
  // window answers "what can I browse right now", and the Connections view is
  // where a Profile becomes browsable.
  //
  // The databases level exists because a Profile does not have to name one. A
  // Profile with a blank Database field connects to a server, and until there
  // was a level for them its tables were unreachable — nothing to list. A
  // Profile that does name a database is opened on it (schema.svelte.ts), so
  // the human who set one still finds their tables one click in.
  //
  // The same three levels carry the other two Engines (ADR-0006), and only the
  // nouns change: a Redis Profile shows the one index it is connected on and
  // that index's keys, a MongoDB Profile the one database it names and its
  // collections. Both stop there — a key and a collection have no columns to
  // expand — so their rows carry no caret, and everything else about them is
  // the same row: the same filter, the same star, the same click that browses
  // what is inside. The words come from engineNouns below, and nothing else in
  // this component branches on the Engine.
  //
  // MySQL's own schemas are listed too, last and dimmed: worth reading
  // occasionally, never what anyone connected for.
  //
  // Clicking a table's name is the point of the whole thing: it selects the
  // table, and the Explorer shows its rows beside the tree. The caret is a
  // separate control from the name for exactly that reason — expanding a
  // table to read its columns must not change what is on screen to its right.
  //
  // A table can also be starred. Starred ("pinned") tables repeat in a PINNED
  // group at the top of their database, which is the whole feature: during an
  // investigation the two or three tables that matter stop being wherever the
  // alphabet put them. The pin is a shortcut, not a move — the table keeps its
  // place in the full list, starred there too.
  //
  // All schema state (what is loaded, what is expanded, what is selected)
  // lives in schema.svelte.ts and the pins in pins.svelte.ts, both at module
  // scope; this component only renders them.
  import { untrack } from "svelte";
  import { isPinned, pinnedAmong, togglePin } from "./pins.svelte";
  import { MONGODB, REDIS } from "./browse";
  import {
    databaseNode,
    engineLabel,
    engineOf,
    hasStructure,
    isSystemDatabase,
    profileNode,
    refreshAll,
    selected,
    selectTable,
    syncProfiles,
    toggleDatabase,
    toggleProfile,
    toggleTable,
    type DatabaseNode,
    type TableNode,
  } from "./schema.svelte";

  // What an Engine's two levels are called, for the handful of places the tree
  // says a noun out loud: the empty state under a database, the truncation
  // line, and the titles on the rows. Everything structural is shared — this
  // is vocabulary, not behaviour.
  //
  // An Engine this app does not know falls back to MySQL's words rather than
  // to the raw Engine string: the fallback is a label under a row that is
  // already listed, and "tables" reads better there than a guess at a name.
  type EngineNouns = { one: string; many: string; container: string };

  function engineNouns(engine: string): EngineNouns {
    switch (engine) {
      case REDIS:
        return { one: "key", many: "keys", container: "database" };
      case MONGODB:
        return { one: "collection", many: "collections", container: "database" };
      default:
        return { one: "table", many: "tables", container: "schema" };
    }
  }

  function nounsOf(profileName: string): EngineNouns {
    return engineNouns(engineOf(profileName));
  }

  let {
    connectedProfiles,
    width,
    onGoToConnections,
  }: {
    connectedProfiles: string[];
    width: number;
    onGoToConnections: () => void;
  } = $props();

  // The tree's roots are exactly the connected Profiles, so every change to
  // that list adds or drops a root — and dropping one drops its cached schema.
  $effect(() => {
    syncProfiles(connectedProfiles);
  });

  function rowEstimateLabel(estimate: number | null): string {
    return estimate === null ? "" : `~${estimate.toLocaleString()}`;
  }

  // MySQL's own key flag, shortened to something that fits in a tree row.
  // Anything outside these three is no key at all.
  function keyChip(key: string): { text: string; primary: boolean } | null {
    if (key === "PRI") return { text: "PK", primary: true };
    if (key === "UNI") return { text: "UQ", primary: false };
    if (key === "MUL") return { text: "IX", primary: false };
    return null;
  }

  function isSelected(profileName: string, database: string, table: TableNode): boolean {
    return (
      selected.profile === profileName &&
      selected.database === database &&
      selected.table === table.name
    );
  }

  // --- Filter ----------------------------------------------------------
  //
  // A compact, case-insensitive substring filter over the tree, matching
  // both table names and database names. It lives entirely in this
  // component rather than schema.svelte.ts: it is not something anyone
  // needs back after a reload, and it never changes what is cached, only
  // what is drawn from it — tablesToShow and databaseVisible below are pure
  // reads over node.tables, never a write to it.
  //
  // The one exception is expansion. A database with a hit has to actually
  // open so the hit is visible, and that means writing the same
  // node.expanded a click would — but a filter is transient, so whatever it
  // opens has to close again the moment the box empties, back to however the
  // human had left it. That is what expansionSnapshot is for: captured once,
  // the instant the first character lands (never again while a query stays
  // non-empty, or every subsequent auto-expand would overwrite the very
  // state being preserved), and written back the instant the box empties.
  // Capturing and restoring both read and write node.expanded without
  // wanting to depend on it — untrack keeps that read from making this
  // effect its own trigger.
  let filterQuery = $state("");
  let needle = $derived(filterQuery.trim().toLowerCase());
  let filtering = $derived(needle !== "");

  let filterInputEl: HTMLInputElement | null = $state(null);
  let treeBodyEl: HTMLDivElement | null = $state(null);

  // Databases nested under the Profile that owns them, rather than a flat
  // map keyed by some joined string — a Profile name can contain almost
  // anything, so a composite key would need its own escaping to stay
  // collision-free. Nesting sidesteps the question entirely.
  type ExpansionSnapshot = {
    profiles: Record<string, boolean>;
    databases: Record<string, Record<string, boolean>>;
  };
  let expansionSnapshot: ExpansionSnapshot | null = null;

  function captureExpansion(): ExpansionSnapshot {
    const profiles: Record<string, boolean> = {};
    const databases: Record<string, Record<string, boolean>> = {};
    for (const profileName of connectedProfiles) {
      const pNode = profileNode(profileName);
      profiles[profileName] = pNode.expanded;
      const perDatabase: Record<string, boolean> = {};
      for (const database of pNode.databases ?? []) {
        perDatabase[database] = databaseNode(profileName, database).expanded;
      }
      databases[profileName] = perDatabase;
    }
    return { profiles, databases };
  }

  function restoreExpansion(snapshot: ExpansionSnapshot) {
    for (const profileName of connectedProfiles) {
      const pNode = profileNode(profileName);
      if (profileName in snapshot.profiles) pNode.expanded = snapshot.profiles[profileName];
      const perDatabase = snapshot.databases[profileName];
      if (perDatabase === undefined) continue;
      for (const database of pNode.databases ?? []) {
        if (database in perDatabase) databaseNode(profileName, database).expanded = perDatabase[database];
      }
    }
  }

  // Whether name contains the filter, case-insensitively. Always false while
  // the box is empty, so callers do not each need their own "is a filter
  // even active" check.
  function nameMatches(name: string): boolean {
    return filtering && name.toLowerCase().includes(needle);
  }

  // The tables a database shows while filtering: every table, unfiltered, if
  // the database's own name is the match (typing "inventory" should reveal
  // the inventory schema whole, not hunt inside it for a table also called
  // that) — otherwise only the tables whose own name hits.
  function tablesToShow(database: string, tables: TableNode[]): TableNode[] {
    if (!filtering) return tables;
    if (nameMatches(database)) return tables;
    return tables.filter((t) => nameMatches(t.name));
  }

  // Whether a database's row survives the filter at all. A database whose
  // tables have never been fetched stays visible unconditionally — the only
  // way to know it has no match would be to load it, and a filter box is not
  // licence to fetch every schema on the server.
  function databaseVisible(database: string, node: DatabaseNode): boolean {
    if (!filtering) return true;
    if (node.tables === null) return true;
    return tablesToShow(database, node.tables).length > 0;
  }

  // Opens every already-loaded database that has a hit (and the Profile
  // above it), so a match is on screen the instant it exists — never a
  // database that is merely visible because it has not loaded yet, which
  // would open onto nothing and cost a fetch this filter has no business
  // asking for.
  function autoExpandMatches() {
    for (const profileName of connectedProfiles) {
      const pNode = profileNode(profileName);
      let anyDbVisible = false;
      for (const database of pNode.databases ?? []) {
        const dNode = databaseNode(profileName, database);
        if (dNode.tables === null) {
          anyDbVisible = true;
          continue;
        }
        if (databaseVisible(database, dNode)) {
          anyDbVisible = true;
          dNode.expanded = true;
        }
      }
      if (anyDbVisible) pNode.expanded = true;
    }
  }

  // Whether anything at all survives the filter, across every connected
  // Profile — the empty-state line replaces the whole tree exactly when
  // this is false, the same all-or-nothing shape "No profile connected"
  // already uses above it.
  function anyMatchVisible(): boolean {
    if (!filtering) return true;
    for (const profileName of connectedProfiles) {
      const pNode = profileNode(profileName);
      for (const database of pNode.databases ?? []) {
        if (databaseVisible(database, databaseNode(profileName, database))) return true;
      }
    }
    return false;
  }

  $effect(() => {
    if (filtering) {
      if (expansionSnapshot === null) {
        untrack(() => {
          expansionSnapshot = captureExpansion();
        });
      }
      autoExpandMatches();
    } else if (expansionSnapshot !== null) {
      const snapshot = expansionSnapshot;
      expansionSnapshot = null;
      untrack(() => restoreExpansion(snapshot));
    }
  });

  function clearFilter() {
    filterQuery = "";
    filterInputEl?.focus();
  }

  function handleFilterKeydown(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    event.preventDefault();
    filterQuery = "";
    treeBodyEl?.focus();
  }
</script>

<!--
  The star, rendered the same in both groups: filled and warm when pinned, and
  otherwise only there while the pointer (or the keyboard focus) is on the row,
  so an unpinned tree stays a list of table names.
-->
{#snippet star(profileName: string, database: string, tableName: string)}
  {@const pinned = isPinned(profileName, database, tableName)}
  <button
    type="button"
    class="flex h-6 w-5 shrink-0 items-center justify-center transition-colors {pinned
      ? 'text-warning hover:text-text'
      : 'text-text-subtle opacity-0 group-hover:opacity-100 hover:text-text focus-visible:opacity-100'}"
    onclick={() => togglePin(profileName, database, tableName)}
    aria-pressed={pinned}
    title={pinned ? `Unpin ${tableName}` : `Pin ${tableName} to the top`}
    aria-label={pinned ? `Unpin ${tableName}` : `Pin ${tableName} to the top`}
  >
    <svg
      class="h-3.5 w-3.5"
      viewBox="0 0 16 16"
      fill={pinned ? "currentColor" : "none"}
      stroke="currentColor"
      stroke-width="1.3"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M8 2.4l1.72 3.49 3.85.56-2.79 2.71.66 3.84L8 11.19l-3.44 1.81.66-3.84-2.79-2.71 3.85-.56z" />
    </svg>
  </button>
{/snippet}

<!-- A name with its filter hit picked out, if it has one. indexOf rather than
     a RegExp built from the raw query: a table called "a.b(c)" must not turn
     the query into a pattern, only ever a literal to search for. -->
{#snippet highlighted(name: string)}
  {@const at = name.toLowerCase().indexOf(needle)}
  {#if at === -1}
    {name}
  {:else}
    {name.slice(0, at)}<span class="font-semibold text-accent">{name.slice(at, at + needle.length)}</span>{name.slice(at + needle.length)}
  {/if}
{/snippet}

<!-- One table row: the caret is optional because the PINNED group is a list of
     shortcuts, not a second place to expand the same table's columns. -->
{#snippet tableRow(profileName: string, database: string, table: TableNode, caret: boolean)}
  <div
    class="group flex items-center border-l-2 pr-2 transition-colors {caret
      ? 'pl-9'
      : 'pl-12'} {isSelected(profileName, database, table)
      ? 'border-accent bg-surface-overlay'
      : 'border-transparent hover:bg-surface-raised'}"
  >
    {#if caret}
      <button
        type="button"
        class="flex h-6 w-4 shrink-0 items-center justify-center text-text-subtle transition-colors hover:text-text"
        onclick={() => toggleTable(profileName, database, table)}
        aria-label={table.expanded
          ? `Collapse columns of ${table.name}`
          : `Show columns of ${table.name}`}
        aria-expanded={table.expanded}
      >
        <svg
          class="h-3 w-3 transition-transform {table.expanded ? 'rotate-90' : ''}"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M4.5 2.5 8 6l-3.5 3.5" />
        </svg>
      </button>
    {/if}
    <button
      type="button"
      class="flex h-6 min-w-0 flex-1 items-center gap-1.5 text-left"
      onclick={() => selectTable(profileName, database, table.name)}
      title="Browse {table.name}"
    >
      <svg
        class="h-3.5 w-3.5 shrink-0 text-text-subtle"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.3"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <rect x="2.25" y="3.25" width="11.5" height="9.5" rx="1.25" />
        <path d="M2.25 6.5h11.5M6.25 6.5v6.25" />
      </svg>
      <span class="truncate text-base text-text">
        {#if filtering}{@render highlighted(table.name)}{:else}{table.name}{/if}
      </span>
      {#if table.rowEstimate !== null}
        <span
          class="ml-auto shrink-0 text-xs tabular-nums text-text-subtle"
          title="Approximate row count"
        >
          {rowEstimateLabel(table.rowEstimate)}
        </span>
      {/if}
    </button>
    {@render star(profileName, database, table.name)}
  </div>
{/snippet}

<!-- One database and, when it is open, everything under it. -->
{#snippet databaseRow(profileName: string, database: string)}
  {@const node = databaseNode(profileName, database)}
  {@const nouns = nounsOf(profileName)}
  {@const structured = hasStructure(profileName)}
  {@const system = structured && isSystemDatabase(database)}
  {@const visibleTables = node.tables !== null ? tablesToShow(database, node.tables) : null}
  <div class="flex items-center border-l-2 border-transparent pr-2 pl-5 hover:bg-surface-raised">
    <button
      type="button"
      class="flex h-6 w-4 shrink-0 items-center justify-center text-text-subtle transition-colors hover:text-text"
      onclick={() => toggleDatabase(profileName, database)}
      aria-label={node.expanded ? `Collapse ${database}` : `Expand ${database}`}
      aria-expanded={node.expanded}
    >
      <svg
        class="h-3 w-3 transition-transform {node.expanded ? 'rotate-90' : ''}"
        viewBox="0 0 12 12"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path d="M4.5 2.5 8 6l-3.5 3.5" />
      </svg>
    </button>
    <button
      type="button"
      class="flex h-6 min-w-0 flex-1 items-center gap-1.5 text-left"
      onclick={() => toggleDatabase(profileName, database)}
      title={system
        ? `${database} — one of MySQL's own schemas`
        : `Show the ${nouns.many} in ${database}`}
    >
      <svg
        class="h-3.5 w-3.5 shrink-0 {system ? 'text-text-subtle' : 'text-text-muted'}"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.3"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path d="M2.75 4.5a1.75 1.75 0 0 1 1.75-1.75h1.9l1.2 1.5h4.15a1.75 1.75 0 0 1 1.75 1.75v5.5a1.75 1.75 0 0 1-1.75 1.75h-7.5A1.75 1.75 0 0 1 2.75 11.5z" />
      </svg>
      <span class="truncate text-base {system ? 'text-text-subtle' : 'text-text'}">
        {#if filtering}{@render highlighted(database)}{:else}{database}{/if}
      </span>
      {#if node.loading}
        <span class="ml-auto shrink-0 text-xs text-text-subtle">loading…</span>
      {:else if node.tables !== null}
        <span class="ml-auto shrink-0 text-xs tabular-nums text-text-subtle">
          {node.tables.length}
        </span>
      {/if}
    </button>
  </div>

  {#if node.expanded}
    {#if node.error !== null}
      <p class="py-1 pr-3 pl-12 text-sm text-danger">{node.error}</p>
    {:else if visibleTables !== null && visibleTables.length === 0}
      <p class="py-1 pr-3 pl-12 text-sm text-text-subtle">
        No {nouns.many} in this {nouns.container}.
      </p>
    {:else if visibleTables !== null}
      {@const pinned = pinnedAmong(profileName, database, visibleTables)}
      {#if pinned.length > 0}
        <p
          class="flex h-6 items-center pr-3 pl-12 text-xs font-medium tracking-wide text-text-subtle uppercase"
        >
          Pinned
        </p>
        {#each pinned as table (table.name)}
          {@render tableRow(profileName, database, table, false)}
        {/each}
        <div class="mx-4 my-1 border-t border-border"></div>
      {/if}

      {#each visibleTables as table (table.name)}
        {@render tableRow(profileName, database, table, structured)}

        {#if table.expanded}
          {#if table.loading}
            <p class="py-1 pr-3 pl-16 text-sm text-text-subtle">Loading columns…</p>
          {:else if table.error !== null}
            <p class="py-1 pr-3 pl-16 text-sm text-danger">{table.error}</p>
          {:else if table.columns !== null}
            {#each table.columns as column (column.name)}
              {@const chip = keyChip(column.key)}
              <div class="flex h-6 items-center gap-1.5 border-l-2 border-transparent pr-3 pl-16">
                <span class="truncate text-base text-text-muted">{column.name}</span>
                {#if chip !== null}
                  <span
                    class="shrink-0 rounded-full border px-1 text-xs leading-tight font-medium {chip.primary
                      ? 'border-accent/40 bg-accent/10 text-accent'
                      : 'border-border bg-surface-raised text-text-subtle'}"
                    title={column.key}
                  >
                    {chip.text}
                  </span>
                {/if}
                <span
                  class="ml-auto shrink-0 truncate font-mono text-xs text-text-subtle"
                  title={column.nullable ? `${column.data_type}, nullable` : column.data_type}
                >
                  {column.data_type}{column.nullable ? " ?" : ""}
                </span>
              </div>
            {/each}
          {/if}
        {/if}
      {/each}

      <!-- A list the backend cut says so where the list ends, in the same
           voice a truncated result set uses: what is here is not all there
           is. Only a Redis keyspace reaches the cap today. -->
      {#if node.truncated}
        <p class="py-1 pr-3 pl-12 text-xs text-text-subtle">
          Showing the first {node.tables?.length ?? 0}
          {nouns.many}; there are more.
        </p>
      {/if}
    {/if}
  {/if}
{/snippet}

<aside
  class="flex shrink-0 flex-col overflow-hidden bg-surface-panel"
  style="width: {width}px"
>
  <div class="flex h-11 shrink-0 items-center justify-between border-b border-border pr-2 pl-4">
    <h2 class="text-xs font-medium tracking-wide text-text-muted uppercase">Database</h2>
    <button
      type="button"
      class="flex h-7 w-7 items-center justify-center rounded-control border border-transparent text-text-muted transition-colors hover:border-border hover:bg-surface-overlay hover:text-text"
      onclick={refreshAll}
      title="Reload the schema"
      aria-label="Reload the schema"
    >
      <svg
        class="h-4 w-4"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.4"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path d="M13.25 8a5.25 5.25 0 1 1-1.6-3.78" />
        <path d="M13.4 2.4v2.85h-2.85" />
      </svg>
    </button>
  </div>

  <!-- The filter, right under the header where it can act on everything
       below it. Esc clears and hands focus back to the tree; the × does the
       same with the mouse, but leaves focus in the box for another query. -->
  <div class="flex h-10 shrink-0 items-center border-b border-border px-3">
    <div
      class="flex h-7 w-full items-center gap-1.5 rounded-control border border-border bg-surface px-2 transition-colors focus-within:border-accent hover:border-border-strong"
    >
      <svg
        class="h-3.5 w-3.5 shrink-0 text-text-subtle"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.4"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <circle cx="7.25" cy="7.25" r="4" />
        <path d="M13.25 13.25 10.4 10.4" />
      </svg>
      <input
        bind:this={filterInputEl}
        bind:value={filterQuery}
        onkeydown={handleFilterKeydown}
        class="min-w-0 flex-1 bg-transparent text-base text-text placeholder:text-text-subtle focus:outline-none"
        placeholder="Filter tables…"
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        aria-label="Filter the database tree"
      />
      {#if filterQuery !== ""}
        <button
          type="button"
          class="flex h-4 w-4 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
          onclick={clearFilter}
          title="Clear the filter"
          aria-label="Clear the filter"
        >
          <svg
            class="h-2.5 w-2.5"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            aria-hidden="true"
          >
            <path d="M3 3l6 6M9 3l-6 6" />
          </svg>
        </button>
      {/if}
    </div>
  </div>

  <div bind:this={treeBodyEl} tabindex="-1" class="min-h-0 flex-1 overflow-auto py-1">
    {#if connectedProfiles.length === 0}
      <p class="px-4 py-2 text-sm text-text-subtle">
        No profile connected —
        <button
          type="button"
          class="font-medium text-accent underline-offset-2 hover:underline"
          onclick={onGoToConnections}
        >
          connect one
        </button>
        to browse its tables.
      </p>
    {:else if filtering && !anyMatchVisible()}
      <p class="px-4 py-2 text-sm text-text-subtle">
        No tables match "{filterQuery.trim()}"
      </p>
    {:else}
      {#each connectedProfiles as profileName (profileName)}
        {@const node = profileNode(profileName)}
        <div class="flex items-center border-l-2 border-transparent pr-2 pl-1 hover:bg-surface-raised">
          <button
            type="button"
            class="flex h-6 w-4 shrink-0 items-center justify-center text-text-subtle transition-colors hover:text-text"
            onclick={() => toggleProfile(profileName)}
            aria-label={node.expanded ? `Collapse ${profileName}` : `Expand ${profileName}`}
            aria-expanded={node.expanded}
          >
            <svg
              class="h-3 w-3 transition-transform {node.expanded ? 'rotate-90' : ''}"
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M4.5 2.5 8 6l-3.5 3.5" />
            </svg>
          </button>
          <button
            type="button"
            class="flex h-6 min-w-0 flex-1 items-center gap-1.5 text-left"
            onclick={() => toggleProfile(profileName)}
          >
            <svg
              class="h-3.5 w-3.5 shrink-0 text-accent"
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <ellipse cx="8" cy="3.75" rx="5.25" ry="2.25" />
              <path d="M2.75 3.75v8.5c0 1.24 2.35 2.25 5.25 2.25s5.25-1.01 5.25-2.25v-8.5" />
              <path d="M2.75 8c0 1.24 2.35 2.25 5.25 2.25s5.25-1.01 5.25-2.25" />
            </svg>
            <span class="truncate text-base font-medium text-text">{profileName}</span>
            <!-- The Engine badge: reuses ProfileList's pill classes, so the
                 same rows read the same badge on both screens. Shown for
                 every Profile, MySQL included — the point is telling three
                 otherwise-identical rows apart at a glance, not flagging an
                 exception. -->
            <span
              class="shrink-0 rounded-full border border-border bg-surface-raised px-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
              title="Engine: {engineLabel(engineOf(profileName))}"
            >
              {engineLabel(engineOf(profileName))}
            </span>
            {#if node.loading}
              <span class="ml-auto shrink-0 text-xs text-text-subtle">loading…</span>
            {:else if node.databases !== null}
              <span class="ml-auto shrink-0 text-xs tabular-nums text-text-subtle">
                {node.databases.length}
              </span>
            {/if}
          </button>
        </div>

        {#if node.expanded}
          {#if node.error !== null}
            <p class="py-1 pr-3 pl-8 text-sm text-danger">{node.error}</p>
          {:else if node.databases !== null && node.databases.length === 0}
            <p class="py-1 pr-3 pl-8 text-sm text-text-subtle">No databases on this server.</p>
          {:else if node.databases !== null}
            {#each node.databases as database (database)}
              {#if databaseVisible(database, databaseNode(profileName, database))}
                {@render databaseRow(profileName, database)}
              {/if}
            {/each}
          {/if}
        {/if}
      {/each}
    {/if}
  </div>
</aside>
