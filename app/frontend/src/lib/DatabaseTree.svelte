<script lang="ts">
  // The Database tool window: every connected Profile as a root, its tables
  // beneath it, and a table's columns beneath that. Disconnected Profiles are
  // simply absent — this window answers "what can I browse right now", and
  // the Connections view is where a Profile becomes browsable.
  //
  // Clicking a table's name is the point of the whole thing: it hands the
  // workspace a SELECT to run, so browsing data costs one click and no typing.
  // The caret is a separate control from the name for exactly that reason —
  // expanding a table to read its columns must not run a query.
  //
  // All schema state (what is loaded, what is expanded, what is selected)
  // lives in schema.svelte.ts at module scope; this component only renders it.
  import {
    profileNode,
    refreshAll,
    selected,
    syncProfiles,
    toggleProfile,
    toggleTable,
    type TableNode,
  } from "./schema.svelte";

  let {
    connectedProfiles,
    width,
    onOpenTable,
    onGoToConnections,
  }: {
    connectedProfiles: string[];
    width: number;
    onOpenTable: (profileName: string, table: string) => void;
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

  function isSelected(profileName: string, table: TableNode): boolean {
    return selected.profile === profileName && selected.table === table.name;
  }

  function openTable(profileName: string, table: TableNode) {
    selected.profile = profileName;
    selected.table = table.name;
    onOpenTable(profileName, table.name);
  }
</script>

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

  <div class="min-h-0 flex-1 overflow-auto py-1">
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
            <p class="py-1 pr-3 pl-8 text-sm text-danger">{node.error}</p>
          {:else if node.tables !== null && node.tables.length === 0}
            <p class="py-1 pr-3 pl-8 text-sm text-text-subtle">No tables in this schema.</p>
          {:else if node.tables !== null}
            {#each node.tables as table (table.name)}
              <div
                class="flex items-center border-l-2 pr-2 pl-5 transition-colors {isSelected(
                  profileName,
                  table,
                )
                  ? 'border-accent bg-surface-overlay'
                  : 'border-transparent hover:bg-surface-raised'}"
              >
                <button
                  type="button"
                  class="flex h-6 w-4 shrink-0 items-center justify-center text-text-subtle transition-colors hover:text-text"
                  onclick={() => toggleTable(profileName, table)}
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
                <button
                  type="button"
                  class="flex h-6 min-w-0 flex-1 items-center gap-1.5 text-left"
                  onclick={() => openTable(profileName, table)}
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
                  <span class="truncate text-base text-text">{table.name}</span>
                  {#if table.rowEstimate !== null}
                    <span
                      class="ml-auto shrink-0 text-xs tabular-nums text-text-subtle"
                      title="Approximate row count"
                    >
                      {rowEstimateLabel(table.rowEstimate)}
                    </span>
                  {/if}
                </button>
              </div>

              {#if table.expanded}
                {#if table.loading}
                  <p class="py-1 pr-3 pl-12 text-sm text-text-subtle">Loading columns…</p>
                {:else if table.error !== null}
                  <p class="py-1 pr-3 pl-12 text-sm text-danger">{table.error}</p>
                {:else if table.columns !== null}
                  {#each table.columns as column (column.name)}
                    {@const chip = keyChip(column.key)}
                    <div class="flex h-6 items-center gap-1.5 border-l-2 border-transparent pr-3 pl-12">
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
          {/if}
        {/if}
      {/each}
    {/if}
  </div>
</aside>
