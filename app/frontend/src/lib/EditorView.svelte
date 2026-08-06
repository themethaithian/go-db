<script lang="ts">
  // The DB Explorer workspace: the Database tool window on the left, and the
  // Query and Results panes stacked to its right, with a draggable splitter
  // at each seam. Editor-local state (sql text, classification, run result,
  // the selected Profile) lives here; connectedProfiles is owned and
  // refreshed by App.svelte and passed down, since it also drives the status
  // bar; the panel sizes and the tree's own state live at module scope so
  // they survive a trip to another view.
  import { tick, untrack } from "svelte";
  import SqlEditor from "./SqlEditor.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import InlineConfirm from "./InlineConfirm.svelte";
  import DatabaseTree from "./DatabaseTree.svelte";
  import Splitter from "./Splitter.svelte";
  import {
    clamp,
    layout,
    persistLayout,
    QUERY_MIN_PX,
    RESULTS_MIN_PX,
    TREE_MAX_PX,
    TREE_MIN_PX,
  } from "./layout.svelte";
  import { Classify, RunQuery, CancelPending } from "../../wailsjs/go/app/App";
  import type { guard, service } from "../../wailsjs/go/models";

  let {
    connectedProfiles,
    onGoToConnections,
  }: {
    connectedProfiles: string[];
    onGoToConnections: () => void;
  } = $props();

  // How many rows a table opened from the tree shows. Small enough to come
  // back instantly on any table, large enough to be a real look at the data.
  const BROWSE_LIMIT = 200;

  let sql = $state("");
  let profileName = $state<string | null>(null);
  let classification = $state<guard.Classification | null>(null);
  let running = $state(false);
  let result = $state<service.QueryResult | null>(null);

  // The horizontal splitter's own footprint (6px hit area + 4px of margin
  // either side), which is height the two panes do not get.
  const SPLITTER_PX = 14;

  // Measured height of the Query+Results stack, so the splitter between them
  // knows how far down it may go before Results is squeezed out of existence.
  let stackHeight = $state(0);
  let queryMaxHeight = $derived(
    Math.max(QUERY_MIN_PX, stackHeight - RESULTS_MIN_PX - SPLITTER_PX),
  );

  // A window that shrinks must shrink the Query pane with it, or Results
  // disappears below the fold with no way to drag it back.
  $effect(() => {
    const max = queryMaxHeight;
    if (stackHeight > 0 && layout.queryHeight > max) {
      layout.queryHeight = max;
    }
  });

  // If the selected Profile disconnects out from under the editor, fall back
  // to no selection rather than silently running against a stale one.
  $effect(() => {
    if (profileName !== null && !connectedProfiles.includes(profileName)) {
      profileName = null;
    }
  });

  // Live classification badge: debounced ~250ms behind typing, and cleared
  // for an empty editor.
  $effect(() => {
    const text = sql;
    if (text.trim() === "") {
      classification = null;
      return;
    }
    const timer = setTimeout(async () => {
      classification = await Classify(text);
    }, 250);
    return () => clearTimeout(timer);
  });

  // A confirm panel open counts as busy too: resolving it (confirm or cancel)
  // is the only way forward, so a second Run cannot spawn a second pending
  // behind the human's back.
  let confirmOpen = $derived(result?.status === "requires_confirmation");
  let canRun = $derived(profileName !== null && sql.trim() !== "" && !running && !confirmOpen);

  async function run() {
    if (profileName === null || sql.trim() === "" || running || confirmOpen) return;
    running = true;
    try {
      result = await RunQuery(profileName, sql);
    } finally {
      running = false;
    }
  }

  function handleSqlChange(next: string) {
    sql = next;
  }

  // Clicking a table in the tree writes the query it stands for into the
  // editor and runs it. Writing it out rather than fetching rows behind the
  // scenes is the point: the human sees exactly what ran, can edit it, and it
  // goes through the same Approval Gate as anything they typed themselves.
  //
  // The toolbar's Profile select stays the one answer to "which database am I
  // querying", so a table under a different Profile switches the select first
  // — visibly, before the query runs.
  async function handleOpenTable(profile: string, table: string) {
    if (profileName !== profile) profileName = profile;
    sql = `SELECT * FROM ${quoteIdentifier(table)} LIMIT ${BROWSE_LIMIT}`;
    // Let the effects settle before running: if an Inline Confirm was open,
    // changing the SQL is what cancels it, and run() would otherwise still
    // see the workspace as busy and quietly do nothing.
    await tick();
    run();
  }

  // MySQL's identifier quoting: wrap in backticks, and double any backtick
  // inside. A table named with one is pathological but legal, and a generated
  // query that breaks on it would be a generated query nobody could trust.
  function quoteIdentifier(name: string): string {
    return "`" + name.replaceAll("`", "``") + "`";
  }

  function resizeTree(next: number) {
    layout.treeWidth = clamp(next, TREE_MIN_PX, TREE_MAX_PX);
    persistLayout();
  }

  function resizeQuery(next: number) {
    layout.queryHeight = clamp(next, QUERY_MIN_PX, queryMaxHeight);
    persistLayout();
  }

  // A withheld mutation is the statement the gate previewed, by ID — not
  // whatever text is in the editor a moment later. If the human edits the SQL
  // or switches Profile while its Inline Confirm is open, that statement no
  // longer reflects their intent, so it is cancelled rather than left to be
  // confirmed stale. The pending's own ID is read with untrack so this effect
  // only fires on sql/profileName changing, not on result changing (which it
  // itself causes).
  $effect(() => {
    sql;
    profileName;
    const pending = untrack(() => result);
    if (pending?.status === "requires_confirmation" && pending.pending_id) {
      CancelPending(pending.pending_id);
      untrack(() => {
        if (result === pending) result = null;
      });
    }
  });

  function handlePendingResolved(next: service.QueryResult) {
    result = next;
  }
</script>

<div class="flex flex-1 overflow-hidden bg-surface">
  <DatabaseTree
    {connectedProfiles}
    width={layout.treeWidth}
    onOpenTable={handleOpenTable}
    {onGoToConnections}
  />

  <Splitter
    orientation="vertical"
    value={layout.treeWidth}
    min={TREE_MIN_PX}
    max={TREE_MAX_PX}
    label="Resize the database tool window"
    onChange={resizeTree}
  />

  <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
    <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-surface-panel px-3">
      {#if connectedProfiles.length === 0}
        <span class="text-base text-text-muted">
          No profile connected —
          <button
            type="button"
            class="font-medium text-accent underline-offset-2 hover:underline"
            onclick={onGoToConnections}
          >
            connect one
          </button>
          to run queries.
        </span>
      {:else}
        <label class="flex items-center gap-2 text-xs font-medium text-text-muted">
          Profile
          <select
            class="h-8 rounded-control border border-border bg-surface-raised px-2 text-base font-normal text-text transition-colors hover:border-border-strong"
            bind:value={profileName}
          >
            <option value={null} disabled selected={profileName === null}>Select profile…</option>
            {#each connectedProfiles as name (name)}
              <option value={name}>{name}</option>
            {/each}
          </select>
        </label>
      {/if}

      {#if classification}
        <span class="flex min-w-0 items-center gap-2">
          {#if classification.kind === "read"}
            <span
              class="shrink-0 rounded-full border border-success/40 bg-success/10 px-2 py-0.5 text-xs font-semibold tracking-wide text-success"
            >
              READ
            </span>
          {:else}
            <span
              title={classification.reason}
              class="shrink-0 rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-xs font-semibold tracking-wide text-warning"
            >
              MUTATION
            </span>
            <span class="truncate text-sm text-text-subtle" title={classification.reason}>
              {classification.reason}
            </span>
          {/if}
        </span>
      {/if}

      <div class="flex-1"></div>

      <button
        type="button"
        class="inline-flex h-8 shrink-0 items-center gap-2 rounded-control bg-accent pr-2.5 pl-3.5 text-base font-medium text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-surface-raised disabled:text-text-subtle"
        disabled={!canRun}
        onclick={run}
      >
        {running ? "Running…" : "Run"}
        <kbd class="rounded-sm bg-white/15 px-1 py-px font-sans text-xs text-white/80">⌘⏎</kbd>
      </button>
    </div>

    <div class="flex min-h-0 flex-1 flex-col p-3">
      <!-- Measured inside the padding, so the splitter's limits are in the
           same coordinates as the panes it is dividing. -->
      <div class="flex min-h-0 flex-1 flex-col" bind:clientHeight={stackHeight}>
        <section
          class="flex shrink-0 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
          style="height: {layout.queryHeight}px"
        >
          <div
            class="flex h-8 shrink-0 items-center justify-between border-b border-border px-3 text-xs font-medium tracking-wide text-text-subtle uppercase"
          >
            <span>Query</span>
          </div>
          <div class="min-h-0 flex-1">
            <SqlEditor value={sql} onChange={handleSqlChange} onRun={run} />
          </div>
        </section>

        <Splitter
          orientation="horizontal"
          value={layout.queryHeight}
          min={QUERY_MIN_PX}
          max={queryMaxHeight}
          label="Resize the query pane"
          onChange={resizeQuery}
        />

        {#if result !== null && result.status === "requires_confirmation" && result.pending_id && result.preview}
          <InlineConfirm
            reason={result.classification.reason}
            preview={result.preview}
            pendingId={result.pending_id}
            onResolved={handlePendingResolved}
          />
        {:else}
          <section
            class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
          >
            <div
              class="flex h-8 shrink-0 items-center justify-between border-b border-border px-3 text-xs font-medium tracking-wide text-text-subtle uppercase"
            >
              <span>Results</span>
            </div>

            {#if result === null}
              <div class="m-auto flex max-w-xs flex-col items-center gap-2 px-6 py-8 text-center">
                <svg
                  class="h-5 w-5 text-text-subtle"
                  viewBox="0 0 20 20"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.4"
                  stroke-linecap="round"
                  stroke-linejoin="round"
                  aria-hidden="true"
                >
                  <rect x="2.75" y="3.75" width="14.5" height="12.5" rx="1.75" />
                  <path d="M2.75 8h14.5M7.75 8v8.25" />
                </svg>
                <p class="text-base font-medium text-text">No results yet</p>
                <p class="text-sm text-text-muted">
                  Click a table on the left, or write a query above and press ⌘⏎.
                </p>
              </div>
            {:else if result.status === "ok"}
              <ResultsTable
                columns={result.columns ?? []}
                rows={(result.rows ?? []) as (string | null)[][]}
                truncated={result.truncated}
              />
            {:else if result.status === "executed"}
              <div class="p-3">
                <p
                  class="rounded-control border border-success/40 bg-success/10 px-3 py-2 text-base text-success"
                >
                  Executed — {result.affected_rows === 1
                    ? "1 row"
                    : `${result.affected_rows} rows`} affected.
                </p>
              </div>
            {:else if result.status === "cancelled"}
              <div class="p-3">
                <p
                  class="rounded-control border border-border bg-surface px-3 py-2 text-base text-text-muted"
                >
                  Cancelled — nothing was executed.
                </p>
              </div>
            {:else}
              <!-- Covers failed, not_connected, unknown_pending, and rejected/timed_out
                   (Approval Console outcomes) — the last two can't reach a human-origin
                   editor session in practice, since only the editor's own Inline
                   Confirm ever runs here, but rendering them as danger notices costs
                   nothing and keeps this branch honest if that ever changes. -->
              <div class="p-3">
                <p
                  class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
                >
                  {result.message}
                </p>
              </div>
            {/if}
          </section>
        {/if}
      </div>
    </div>
  </div>
</div>
