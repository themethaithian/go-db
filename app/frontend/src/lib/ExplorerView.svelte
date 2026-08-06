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
  //
  // The controls above the grid — a WHERE condition and a row limit — are
  // edits to that one statement and nothing more. The condition is dropped in
  // verbatim, exactly as typed: it is the human's SQL, and the gate that
  // classifies it (and the READ ONLY transaction behind it) is the same
  // safety story as the Editor's. Sanitising it here would only teach people
  // that the panel writes queries they did not.
  import { untrack } from "svelte";
  import DatabaseTree from "./DatabaseTree.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import Splitter from "./Splitter.svelte";
  import {
    clamp,
    DETAIL_MIN_PX,
    GRID_MIN_PX,
    layout,
    persistLayout,
    TREE_MAX_PX,
    TREE_MIN_PX,
  } from "./layout.svelte";
  import { browse, BROWSE_LIMITS, selected } from "./schema.svelte";
  import { CancelPending, RunQuery } from "../../wailsjs/go/app/App";
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

  // The vertical splitter's own footprint (a 6px hit area), which is width
  // neither the grid nor the Row pane gets.
  const SPLITTER_PX = 6;

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The statement behind what is on screen right now — set from the fetch
  // that produced it, never from the current controls, so the caption is a
  // record rather than a promise.
  let shownSql = $state("");

  // The condition as it is being typed, which is not yet the condition in
  // force: it becomes browse.filter on Enter, and that is what re-runs the
  // query. Nothing is fetched while a half-written WHERE is on screen.
  let filterInput = $state(browse.filter);

  // The picked row, as an index into the rows on screen — null when the Row
  // pane is closed. Every fetch clears it: an index means nothing against a
  // result set it was not taken from.
  let selectedRow = $state<number | null>(null);

  // Width of the grid + Row pane strip, so the splitter between them knows
  // how far it may travel before one of the two is squeezed out.
  let bodyWidth = $state(0);

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later: clicking three tables quickly must leave the third one's rows on
  // screen, not whichever query the database happened to finish last. Filter
  // and limit changes take the same numbered path, for the same reason.
  let latestRequest = 0;

  let browseSql = $derived(buildSql(selected.table, browse.filter, browse.limit));

  let rowCount = $derived(result?.status === "ok" ? (result.rows?.length ?? 0) : null);
  let shownColumns = $derived(result?.status === "ok" ? (result.columns ?? []) : []);
  let shownRows = $derived(
    result?.status === "ok" ? ((result.rows ?? []) as (string | null)[][]) : [],
  );
  let detailRow = $derived(selectedRow === null ? null : (shownRows[selectedRow] ?? null));

  let filterDirty = $derived(filterInput.trim() !== browse.filter);
  let detailMax = $derived(
    Math.max(DETAIL_MIN_PX, bodyWidth - GRID_MIN_PX - SPLITTER_PX),
  );

  // The statement is the whole input to this view: whenever it changes — a
  // table picked in the tree, a condition applied, a limit chosen, or a
  // Profile disconnecting out from under it — the rows are fetched again for
  // exactly that statement.
  $effect(() => {
    const profileName = selected.profile;
    const sql = browseSql;
    if (profileName === null || sql === "") {
      latestRequest += 1;
      result = null;
      failure = null;
      loading = false;
      shownSql = "";
      selectedRow = null;
      return;
    }
    void fetchRows(profileName, sql);
  });

  // The box shows the condition in force: applying one normalises what is
  // typed, and switching tables (which drops the condition) empties the box
  // rather than leaving a stale one behind, applied or not.
  $effect(() => {
    selected.table;
    filterInput = browse.filter;
  });

  // A window that shrinks must shrink the Row pane with it, or the grid
  // disappears off the edge with no way to drag it back.
  $effect(() => {
    const max = detailMax;
    if (bodyWidth > 0 && layout.explorerDetailWidth > max) {
      layout.explorerDetailWidth = max;
    }
  });

  async function fetchRows(profileName: string, sql: string) {
    const request = (latestRequest += 1);
    // A row picked out of the old result set cannot survive a new one — the
    // index would land on some other row, or on none.
    selectedRow = null;
    // Rows from the table we were looking at a moment ago are worse than no
    // rows: they would sit under another table's name, or another condition.
    // They go the instant the statement changes — but re-running the same one
    // keeps them, so a refresh does not blink the grid out and back.
    // (untracked: this runs inside the statement effect, which must not
    // re-fire on the caption it is itself writing.)
    if (untrack(() => shownSql) !== sql) {
      result = null;
      failure = null;
      shownSql = "";
    }
    loading = true;
    try {
      const next = await RunQuery(profileName, sql);
      // This panel never confirms a write, so a withheld statement's pending
      // must not linger in the gate's queue (Inline Confirms have no expiry).
      // Cancelling records the honest outcome: the surface declined to run it.
      if (next.status === "requires_confirmation" && next.pending_id) {
        void CancelPending(next.pending_id);
      }
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

  // The statement, and the only place it is built. Empty condition means no
  // WHERE clause at all, rather than a WHERE that is quietly always true.
  function buildSql(table: string | null, filter: string, limit: number): string {
    if (table === null) return "";
    const condition = filter.trim();
    const where = condition === "" ? "" : ` WHERE ${condition}`;
    return `SELECT * FROM ${quoteIdentifier(table)}${where} LIMIT ${limit}`;
  }

  function refresh() {
    if (selected.profile === null || browseSql === "") return;
    void fetchRows(selected.profile, browseSql);
  }

  // Enter applies. A condition that has not changed still re-runs the query —
  // pressing Enter and having nothing at all happen is the kind of silence
  // that gets read as a broken box.
  function applyFilter() {
    const next = filterInput.trim();
    if (next === browse.filter) refresh();
    else browse.filter = next;
  }

  function clearFilter() {
    filterInput = "";
    applyFilter();
  }

  function handleFilterKey(event: KeyboardEvent) {
    if (event.key !== "Enter") return;
    event.preventDefault();
    applyFilter();
  }

  // Escape closes the Row pane from anywhere, and up/down walk the selection
  // through the rows — but never while a control has the focus, where those
  // keys already mean something to it.
  function handleWindowKey(event: KeyboardEvent) {
    if (selectedRow === null) return;
    if (event.key === "Escape") {
      selectedRow = null;
      return;
    }
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const tag = (event.target as HTMLElement | null)?.tagName;
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
    event.preventDefault();
    const step = event.key === "ArrowDown" ? 1 : -1;
    selectedRow = clamp(selectedRow + step, 0, shownRows.length - 1);
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

  // The splitter reports the width of the pane before it — the grid — so the
  // Row pane is whatever the strip has left over.
  function resizeDetail(gridWidth: number) {
    layout.explorerDetailWidth = clamp(
      bodyWidth - SPLITTER_PX - gridWidth,
      DETAIL_MIN_PX,
      detailMax,
    );
    persistLayout();
  }
</script>

<svelte:window onkeydown={handleWindowKey} />

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
        {#if selected.table !== null}
          <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border px-3">
            <div
              class="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-control border border-border bg-surface px-2.5 transition-colors focus-within:border-accent hover:border-border-strong"
            >
              <span class="shrink-0 font-mono text-xs font-medium tracking-wide text-text-subtle">
                WHERE
              </span>
              <input
                class="min-w-0 flex-1 bg-transparent font-mono text-base text-text placeholder:text-text-subtle focus:outline-none"
                placeholder="city = 'Bangkok' — filter condition"
                bind:value={filterInput}
                onkeydown={handleFilterKey}
                spellcheck="false"
                autocapitalize="off"
                autocomplete="off"
                aria-label="WHERE condition"
              />
              {#if filterInput !== ""}
                <button
                  type="button"
                  class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
                  onclick={clearFilter}
                  title="Clear the condition"
                  aria-label="Clear the condition"
                >
                  <svg
                    class="h-3 w-3"
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
              <!-- The apply affordance, and only when there is something to
                   apply: a condition on screen that is not the one that ran. -->
              {#if filterDirty}
                <button
                  type="button"
                  class="flex h-5 shrink-0 items-center gap-1 rounded-control border border-accent/40 bg-accent/10 px-1.5 text-xs font-medium text-accent transition-colors hover:bg-accent/20"
                  onclick={applyFilter}
                  title="Apply the condition (Enter)"
                >
                  <svg
                    class="h-3 w-3"
                    viewBox="0 0 12 12"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.4"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M9.5 2.5v3.25a1.5 1.5 0 0 1-1.5 1.5H2.75" />
                    <path d="M4.75 5.25 2.5 7.25l2.25 2" />
                  </svg>
                  Apply
                </button>
              {/if}
            </div>

            <label class="flex shrink-0 items-center gap-1.5 text-xs font-medium tracking-wide text-text-muted uppercase">
              Limit
              <select
                class="h-8 rounded-control border border-border bg-surface-raised px-2 text-base font-normal text-text tabular-nums transition-colors hover:border-border-strong"
                bind:value={browse.limit}
              >
                {#each BROWSE_LIMITS as option (option)}
                  <option value={option}>{option}</option>
                {/each}
              </select>
            </label>
          </div>
        {/if}

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
          <div class="flex min-h-0 min-w-0 flex-1" bind:clientWidth={bodyWidth}>
            <div class="flex min-w-0 flex-1">
              <ResultsTable
                columns={shownColumns}
                rows={shownRows}
                truncated={result.truncated}
                selectedIndex={selectedRow}
                onRowClick={(index) => (selectedRow = index)}
              />
            </div>

            {#if detailRow !== null && selectedRow !== null}
              <Splitter
                orientation="vertical"
                value={bodyWidth - SPLITTER_PX - layout.explorerDetailWidth}
                min={GRID_MIN_PX}
                max={Math.max(GRID_MIN_PX, bodyWidth - SPLITTER_PX - DETAIL_MIN_PX)}
                label="Resize the row pane"
                onChange={resizeDetail}
              />

              <!-- One row, read down instead of across: the shape you want the
                   moment a table has more columns than the window has width. -->
              <aside
                class="flex shrink-0 flex-col overflow-hidden border-l border-border bg-surface"
                style="width: {layout.explorerDetailWidth}px"
              >
                <div class="flex h-8 shrink-0 items-center gap-2 border-b border-border pr-2 pl-3">
                  <span class="text-xs font-medium tracking-wide text-text-muted uppercase">Row</span>
                  <span class="ml-auto shrink-0 text-xs tabular-nums text-text-subtle">
                    {selectedRow + 1} of {shownRows.length}
                  </span>
                  <button
                    type="button"
                    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
                    onclick={() => (selectedRow = null)}
                    title="Close the row pane (Esc)"
                    aria-label="Close the row pane"
                  >
                    <svg
                      class="h-3 w-3"
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
                </div>

                <div class="min-h-0 flex-1 overflow-auto">
                  {#each shownColumns as column, i (column)}
                    <div class="flex gap-3 border-b border-border/60 px-3 py-1.5">
                      <span
                        class="w-24 shrink-0 truncate text-right font-mono text-xs leading-5 text-text-muted"
                        title={column}
                      >
                        {column}
                      </span>
                      <span class="min-w-0 flex-1 font-mono text-base leading-5 text-text break-words">
                        {#if detailRow[i] === null}
                          <span class="text-text-subtle italic">NULL</span>
                        {:else}
                          {detailRow[i]}
                        {/if}
                      </span>
                    </div>
                  {/each}
                </div>
              </aside>
            {/if}
          </div>
        {:else}
          <!-- Anything but "ok" is the connection, the database, or the gate
               talking (not_connected, failed, and — if a condition classifies
               as a mutation — requires_confirmation or blocked). Shown
               verbatim, above controls that still work, since the fix is
               almost always a word in the WHERE box. Nothing here offers to
               confirm a write: this panel reads. -->
          <div class="p-3">
            <p
              class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
            >
              {result.message}
            </p>
            {#if result.status === "requires_confirmation"}
              <!-- The gate's message ends "Confirm to run it", and there is
                   nothing here to confirm with — on purpose. Saying where that
                   decision does live beats leaving a dead end on screen. -->
              <p class="px-3 py-2 text-sm text-text-muted">
                Nothing ran. This panel only reads — fix the condition, or take the statement to
                the Editor with “Open in editor” and decide on it there.
              </p>
            {/if}
          </div>
        {/if}
      </section>
    </div>
  </div>
</div>
