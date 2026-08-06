<script lang="ts">
  // The Explorer: the Database tool window on the left, and the rows of the
  // table selected in it on the right.
  //
  // This view has one job and no ambiguity about it. There is no SQL to edit
  // here, so the grid cannot drift away from the highlighted table the way it
  // could when a tree and an editor shared a screen: the selection lives in
  // schema.svelte.ts, and everything on the right — the name in the header,
  // the statement in the caption, the rows in the grid — is derived from it.
  //
  // The rows are still fetched by running a SELECT through RunQuery, so this
  // browsing goes through the same Approval Gate as anything typed by hand,
  // is classified by it, and lands in the same audit log. The caption under
  // the header says exactly which statement that was: a data panel that
  // fetched rows by some private path would be asking to be trusted, and this
  // one would rather be checked.
  import { untrack } from "svelte";
  import DatabaseTree from "./DatabaseTree.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import Splitter from "./Splitter.svelte";
  import { clamp, layout, persistLayout, TREE_MAX_PX, TREE_MIN_PX } from "./layout.svelte";
  import { selected } from "./schema.svelte";
  import { RunQuery } from "../../wailsjs/go/app/App";
  import type { service } from "../../wailsjs/go/models";

  let {
    connectedProfiles,
    onGoToConnections,
    onOpenInEditor,
  }: {
    connectedProfiles: string[];
    onGoToConnections: () => void;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  // How many rows browsing a table shows. Small enough to come back instantly
  // on any table, large enough to be a real look at the data.
  const BROWSE_LIMIT = 200;

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The statement behind what is on screen right now — set from the fetch
  // that produced it, never from the current selection, so the caption is a
  // record rather than a promise.
  let shownSql = $state("");

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later: clicking three tables quickly must leave the third one's rows on
  // screen, not whichever query the database happened to finish last.
  let latestRequest = 0;

  let browseSql = $derived(
    selected.table === null ? "" : `SELECT * FROM ${quoteIdentifier(selected.table)} LIMIT ${BROWSE_LIMIT}`,
  );

  let rowCount = $derived(result?.status === "ok" ? (result.rows?.length ?? 0) : null);

  // The selection is the whole input to this view: whenever it changes — from
  // the tree, from a pin, or from a Profile disconnecting out from under it —
  // the rows are fetched again for exactly that table.
  $effect(() => {
    const profileName = selected.profile;
    const table = selected.table;
    if (profileName === null || table === null) {
      latestRequest += 1;
      result = null;
      failure = null;
      loading = false;
      shownSql = "";
      return;
    }
    void browse(profileName, table);
  });

  async function browse(profileName: string, table: string) {
    const request = (latestRequest += 1);
    const sql = `SELECT * FROM ${quoteIdentifier(table)} LIMIT ${BROWSE_LIMIT}`;
    // Rows from the table we were looking at a moment ago are worse than no
    // rows: they would sit under another table's name. They go the instant
    // the selection moves — but a refresh of the same table keeps them, so
    // re-running does not blink the grid out and back.
    // (untracked: this runs inside the selection effect, which must not
    // re-fire on the caption it is itself writing.)
    if (untrack(() => shownSql) !== sql) {
      result = null;
      failure = null;
      shownSql = "";
    }
    loading = true;
    try {
      const next = await RunQuery(profileName, sql);
      if (request !== latestRequest) return;
      result = next;
      failure = null;
      shownSql = sql;
    } catch (err) {
      if (request !== latestRequest) return;
      result = null;
      failure = String(err);
      shownSql = sql;
    } finally {
      if (request === latestRequest) loading = false;
    }
  }

  function refresh() {
    if (selected.profile === null || selected.table === null) return;
    void browse(selected.profile, selected.table);
  }

  function openInEditor() {
    if (selected.profile === null || browseSql === "") return;
    onOpenInEditor(selected.profile, browseSql);
  }

  // MySQL's identifier quoting: wrap in backticks, and double any backtick
  // inside. A table named with one is pathological but legal, and a generated
  // query that breaks on it would be a generated query nobody could trust.
  function quoteIdentifier(name: string): string {
    return "`" + name.replaceAll("`", "``") + "`";
  }

  function resizeTree(next: number) {
    layout.explorerTreeWidth = clamp(next, TREE_MIN_PX, TREE_MAX_PX);
    persistLayout();
  }
</script>

<div class="flex flex-1 overflow-hidden bg-surface">
  <DatabaseTree {connectedProfiles} width={layout.explorerTreeWidth} {onGoToConnections} />

  <Splitter
    orientation="vertical"
    value={layout.explorerTreeWidth}
    min={TREE_MIN_PX}
    max={TREE_MAX_PX}
    label="Resize the database tool window"
    onChange={resizeTree}
  />

  <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
    <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-surface-panel px-3">
      {#if selected.table === null || selected.profile === null}
        <span class="text-base text-text-muted">No table selected</span>
      {:else}
        <span class="truncate font-mono text-md font-medium text-text" title={selected.table}>
          {selected.table}
        </span>
        <span
          class="shrink-0 rounded-full border border-accent/40 bg-accent/10 px-2 py-0.5 text-xs font-medium text-accent"
          title="Profile"
        >
          {selected.profile}
        </span>
        {#if loading}
          <span class="shrink-0 text-sm text-text-subtle">loading…</span>
        {:else if rowCount !== null}
          <span class="shrink-0 text-sm tabular-nums text-text-muted">
            {rowCount === 1 ? "1 row" : `${rowCount.toLocaleString()} rows`}
          </span>
        {/if}

        <div class="flex-1"></div>

        <button
          type="button"
          class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-base text-text-muted transition-colors hover:border-border-strong hover:text-text"
          onclick={openInEditor}
          title="Open this SELECT in the Editor"
        >
          <svg
            class="h-3.5 w-3.5"
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="M9.25 2.75h4v4M13.25 2.75 7.5 8.5" />
            <path d="M12.25 9.75v2.5a1.5 1.5 0 0 1-1.5 1.5h-7a1.5 1.5 0 0 1-1.5-1.5v-7a1.5 1.5 0 0 1 1.5-1.5h2.5" />
          </svg>
          Open in editor
        </button>
        <button
          type="button"
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-muted transition-colors hover:border-border-strong hover:text-text disabled:cursor-not-allowed disabled:text-text-subtle"
          onclick={refresh}
          disabled={loading}
          title="Run the query again"
          aria-label="Run the query again"
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
      {/if}
    </div>

    <div class="flex min-h-0 flex-1 flex-col p-3">
      <section
        class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
      >
        <div class="flex h-8 shrink-0 items-center justify-between gap-3 border-b border-border px-3">
          {#if shownSql === ""}
            <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">Data</span>
          {:else}
            <span class="truncate font-mono text-xs text-text-subtle" title={shownSql}>
              {shownSql}
            </span>
            {#if result?.classification.kind === "read"}
              <span
                class="shrink-0 rounded-full border border-success/40 bg-success/10 px-2 py-0.5 text-xs font-semibold tracking-wide text-success"
                title="Classified by the Approval Gate"
              >
                READ
              </span>
            {/if}
          {/if}
        </div>

        {#if selected.table === null}
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
            <p class="text-base font-medium text-text">Select a table to browse its data</p>
            {#if connectedProfiles.length === 0}
              <p class="text-sm text-text-muted">
                No profile connected —
                <button
                  type="button"
                  class="font-medium text-accent underline-offset-2 hover:underline"
                  onclick={onGoToConnections}
                >
                  connect one
                </button>
                to see its tables.
              </p>
            {:else}
              <p class="text-sm text-text-muted">
                Pick one in the Database tree, and star the ones you keep coming back to.
              </p>
            {/if}
          </div>
        {:else if failure !== null}
          <div class="p-3">
            <p
              class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
            >
              {failure}
            </p>
          </div>
        {:else if result === null}
          <div class="m-auto px-6 py-8 text-center">
            <p class="text-base text-text-muted">Loading rows…</p>
          </div>
        {:else if result.status === "ok"}
          <ResultsTable
            columns={result.columns ?? []}
            rows={(result.rows ?? []) as (string | null)[][]}
            truncated={result.truncated}
          />
        {:else}
          <!-- Anything but "ok" from a plain SELECT is the connection or the
               database talking (not_connected, failed) — shown verbatim,
               since guessing at what it meant helps nobody. -->
          <div class="p-3">
            <p
              class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
            >
              {result.message}
            </p>
          </div>
        {/if}
      </section>
    </div>
  </div>
</div>
