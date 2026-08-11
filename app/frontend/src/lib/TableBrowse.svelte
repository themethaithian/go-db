<script lang="ts">
  // Browsing one MySQL table: the grid, the WHERE/LIMIT controls above it, the
  // Structure side beside it, and the record pane's row editing.
  //
  // One of the Explorer's three browse panes (KeyBrowse and CollectionBrowse
  // are the others), and the reason there are three rather than one component
  // with an Engine in every second condition: what "the contents" are is the
  // Engine's to say (ADR-0006), and so is nearly everything around them. A
  // table is rows, and rows come with a condition to filter them, a limit to
  // bound them, a primary key that decides whether they can be typed over, and
  // a schema underneath. None of that means anything to a Redis key.
  //
  // The rows are fetched by running a SELECT through the Approval Gate — the
  // `read` this pane is given — so browsing is classified and audited exactly
  // as anything typed by hand is, and the caption under the header says which
  // statement produced what is on screen. A data panel that fetched rows by
  // some private path would be asking to be trusted; this one would rather be
  // checked.
  //
  // The controls above the grid are edits to that one statement and nothing
  // more. The condition is dropped in verbatim, exactly as typed: it is the
  // human's SQL, and the gate that classifies it (and the READ ONLY transaction
  // behind it) is the same safety story as the Editor's. Sanitising it here
  // would only teach people that the panel writes queries they did not.
  //
  // Values in these rows can be typed over, in the record pane, and that is the
  // one thing this pane does that is not a read — but it is not a second write
  // path either. Saving builds an UPDATE per dirty row and sends it through the
  // same RowEdits the Editor's grid uses: the gate withholds it, the Inline
  // Confirm the Explorer renders in place of this pane shows exactly what would
  // run, and nothing moves until the human says so. A confirmed write is
  // followed by re-running the browse, so what the pane shows next is the
  // database's own answer rather than this app's idea of what it should hold.
  import { untrack, type Snippet } from "svelte";
  import BrowseHeader from "./BrowseHeader.svelte";
  import RecordPaneDock from "./RecordPaneDock.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import SaveBar from "./SaveBar.svelte";
  import StructureView from "./StructureView.svelte";
  import {
    clamp,
    COMPARE_MIN_PX,
    DETAIL_MIN_PX,
    GRID_MIN_PX,
    layout,
    persistLayout,
  } from "./layout.svelte";
  import {
    browse,
    BROWSE_LIMITS,
    ensureColumns,
    ensureIndexes,
    findTable,
    refreshTableStructure,
    type TableNode,
  } from "./schema.svelte";
  import { browseTableSql } from "./browse";
  import type { RowEdits } from "./edits.svelte";
  import { editability, qualify } from "./mutate";
  import type { service } from "../../wailsjs/go/models";

  /** Data shows the grid; Structure shows the table's columns and indexes. */
  type PanelMode = "data" | "structure";

  let {
    profileName,
    database,
    table,
    edits,
    read,
    confirm = null,
    onOpenInEditor,
  }: {
    profileName: string;
    database: string;
    table: string;
    /** The rows typed over and not yet saved — the Explorer's, so the Inline
     *  Confirm it raises and this grid are the same edit. */
    edits: RowEdits;
    /** One read through the Approval Gate, cancelled if the gate withholds it. */
    read: (profileName: string, statement: string) => Promise<service.QueryResult>;
    /** The Inline Confirm, when a save has raised one: it takes this pane. */
    confirm?: Snippet | null;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  // The vertical splitter's own footprint (a 6px hit area), which is width
  // neither the grid nor the record pane gets.
  const SPLITTER_PX = 6;

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The statement behind what is on screen right now — set from the fetch that
  // produced it, never from the current controls, so the caption is a record
  // rather than a promise.
  let shownSql = $state("");

  // The condition as it is being typed, which is not yet the condition in
  // force: it becomes browse.filter on Enter, and that is what re-runs the
  // query. Nothing is fetched while a half-written WHERE is on screen.
  let filterInput = $state(browse.filter);

  // The picked rows, as indices into the rows on screen and in the order they
  // were picked — that order is the order of the record pane's value columns.
  // Empty means the pane is closed. Every fetch clears it: an index means
  // nothing against a result set it was not taken from.
  let selectedRows = $state<number[]>([]);

  // Data shows the grid and its WHERE/LIMIT controls; Structure shows the
  // table's columns and indexes instead. It is not scoped by table the way
  // browse.filter is — there is nothing to preserve, so every table starts on
  // Data rather than remembering the last mode picked for it.
  let mode = $state<PanelMode>("data");

  // Width of the grid + Row pane strip, so the splitter between them knows how
  // far it may travel before one of the two is squeezed out.
  let bodyWidth = $state(0);

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later: clicking three tables quickly must leave the third one's rows on
  // screen, not whichever query the database happened to finish last. Filter
  // and limit changes take the same numbered path, for the same reason.
  let latestRequest = 0;

  // The statement that browses the selection. The table is qualified with the
  // database it was picked from — the tree can show two schemas with a `users`
  // in each, and an unqualified name would read whichever the connection
  // happens to default to.
  let browseSql = $derived(browseTableSql(database, table, browse.filter, browse.limit, qualify));

  let rowCount = $derived(result?.status === "ok" ? (result.rows?.length ?? 0) : null);
  let shownColumns = $derived(result?.status === "ok" ? (result.columns ?? []) : []);
  let shownRows = $derived(
    result?.status === "ok" ? ((result.rows ?? []) as (string | null)[][]) : [],
  );
  // The selection as the pane and the grid both see it: indices that still
  // point at a row, and those rows' cells in the same order. Filtering here
  // rather than trusting the state means a selection can never outlive the rows
  // it was taken from, however it was cleared.
  let pickedIndices = $derived(selectedRows.filter((index) => shownRows[index] !== undefined));
  let pickedRows = $derived(pickedIndices.map((index) => shownRows[index]));

  let filterDirty = $derived(filterInput.trim() !== browse.filter);
  // Comparing asks for more room than reading one row does, so the pane's floor
  // moves with what it is doing.
  let detailMin = $derived(pickedIndices.length > 1 ? COMPARE_MIN_PX : DETAIL_MIN_PX);
  let detailMax = $derived(Math.max(detailMin, bodyWidth - GRID_MIN_PX - SPLITTER_PX));

  // The tree's own node for the selected table — the schema cache the Structure
  // view reads and refreshes, shared with the Database tree so the two can never
  // disagree about a table's columns. Resolvable as soon as a table is selected:
  // selecting one requires clicking it in the tree, which requires its Profile's
  // table list — and so this table's node — to already be loaded.
  let tableNode: TableNode | null = $derived(findTable(profileName, database, table));
  let structureLoading = $derived(
    tableNode !== null && (tableNode.loading || tableNode.indexesLoading),
  );

  // Whether these rows can be typed over, which here comes down to one
  // question: does the table have a primary key? Browsing selects *, so a key
  // that exists is always in the result — there is no third case where the
  // table has one and the grid cannot see it.
  let editable = $derived(editability(table, shownColumns, tableNode?.indexes ?? null));
  let readOnlyHint = $derived(editable.editable ? null : editable.reason);

  let statusCount = $derived(
    mode === "data" && rowCount !== null
      ? rowCount === 1
        ? "1 row"
        : `${rowCount.toLocaleString()} rows`
      : null,
  );

  // Every table starts on Data — a table switch is a strong enough signal that a
  // mode picked for the last one should not carry over.
  $effect(() => {
    database;
    table;
    mode = "data";
  });

  // The schema this view needs. Indexes are wanted in either mode — the primary
  // key is what decides whether the grid's rows can be edited, so Data asks for
  // them too — while the columns are the Structure view's alone. ensureColumns
  // and ensureIndexes are the same no-op-if-cached calls the tree's toggleTable
  // makes, so flipping between Data and Structure, or between tables and back,
  // never re-asks the database.
  $effect(() => {
    const node = tableNode;
    if (node === null) return;
    ensureIndexes(profileName, database, node);
    if (mode !== "structure") return;
    ensureColumns(profileName, database, node);
  });

  // The statement is the whole input to this pane: whenever it changes — a table
  // picked in the tree, a condition applied, a limit chosen — the rows are
  // fetched again for exactly that statement.
  $effect(() => {
    const sql = browseSql;
    void fetchRows(profileName, sql);
  });

  // The box shows the condition in force: applying one normalises what is typed,
  // and switching tables (which drops the condition) empties the box rather than
  // leaving a stale one behind, applied or not.
  $effect(() => {
    database;
    table;
    filterInput = browse.filter;
  });

  // A window that shrinks must shrink the record pane with it, or the grid
  // disappears off the edge with no way to drag it back — and a pane that starts
  // comparing is widened to the comparison's floor the same way.
  $effect(() => {
    const min = detailMin;
    const max = detailMax;
    if (bodyWidth <= 0) return;
    const next = clamp(untrack(() => layout.explorerDetailWidth), min, max);
    if (next !== layout.explorerDetailWidth) layout.explorerDetailWidth = next;
  });

  async function fetchRows(profile: string, sql: string) {
    const request = (latestRequest += 1);
    // Rows picked out of the old result set cannot survive a new one — the
    // indices would land on some other rows, or on none.
    selectedRows = [];
    // Nor can edits made against it, for the same reason, and a save waiting on
    // a confirm is a save for rows that are about to be replaced.
    edits.abandon();
    edits.revert();
    // Rows from the table we were looking at a moment ago are worse than no
    // rows: they would sit under another table's name, or another condition.
    // They go the instant the statement changes — but re-running the same one
    // keeps them, so a refresh does not blink the grid out and back.
    // (untracked: this runs inside the statement effect, which must not re-fire
    // on the caption it is itself writing.)
    if (untrack(() => shownSql) !== sql) {
      result = null;
      failure = null;
      shownSql = "";
    }
    loading = true;
    try {
      // No database: the browse statement is qualified with the schema it
      // reads, and runs on the Profile's own connection as it always has.
      const next = await read(profile, sql);
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
    if (mode === "structure") {
      if (tableNode !== null) refreshTableStructure(profileName, database, tableNode);
      return;
    }
    void fetchRows(profileName, browseSql);
  }

  // Enter applies. A condition that has not changed still re-runs the query —
  // pressing Enter and having nothing at all happen is the kind of silence that
  // gets read as a broken box.
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

  // Clicking a row picks it and nothing else — including when it was already one
  // of several, which is the way back from a comparison to a single row. Cmd (or
  // Ctrl) adds a row to the comparison, or takes it back out, and the order rows
  // go in is the order their columns appear in the pane. Shift extends the
  // comparison instead of toggling one row: every row between the last-picked
  // one and the one just clicked joins it, in the order they sit on screen,
  // skipping whichever of them were already picked. There is no cap: the pane
  // scrolls sideways under the field-name column once the value columns outrun
  // the width, so comparing more rows than fit is a scroll away, not a wall.
  function pickRow(index: number, options: { additive: boolean; range: boolean }) {
    if (options.range && selectedRows.length > 0) {
      const anchor = selectedRows[selectedRows.length - 1];
      const [from, to] = anchor <= index ? [anchor, index] : [index, anchor];
      const already = new Set(selectedRows);
      const added: number[] = [];
      for (let i = from; i <= to; i += 1) {
        if (!already.has(i)) added.push(i);
      }
      selectedRows = [...selectedRows, ...added];
      return;
    }
    if (!options.additive) {
      selectedRows = [index];
      return;
    }
    if (selectedRows.includes(index)) {
      selectedRows = selectedRows.filter((picked) => picked !== index);
      return;
    }
    selectedRows = [...selectedRows, index];
  }

  // Up/down walk one row through the rows — but never while a control has the
  // focus, where those keys already mean something to it. Walking out of a
  // comparison collapses it: the keys move a single row, and the pane follows
  // what they moved. Escape is RecordPaneDock's own business (it also has to
  // arbitrate the pop-out), so this only ever sees the arrow keys.
  function handleWindowKey(event: KeyboardEvent) {
    if (selectedRows.length === 0) return;
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const tag = (event.target as HTMLElement | null)?.tagName;
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
    event.preventDefault();
    const step = event.key === "ArrowDown" ? 1 : -1;
    const from = selectedRows[selectedRows.length - 1];
    selectedRows = [clamp(from + step, 0, shownRows.length - 1)];
  }

  // The statement the Editor is handed is the one that ran, falling back to the
  // one that would.
  let editorStatement = $derived(browseSql !== "" ? browseSql : shownSql);

  function openInEditor() {
    if (editorStatement === "") return;
    onOpenInEditor(profileName, editorStatement);
  }

  // Sends the dirty rows to the gate, one UPDATE and one Inline Confirm at a
  // time. Only a run that got all the way through re-fetches: a cancel part way
  // leaves the rows it never reached dirty, and fetching over them would throw
  // away exactly the edits the human kept.
  async function saveEdits() {
    if (!editable.editable) return;
    const saved = await edits.save({
      profileName,
      database,
      table: editable.table,
      keyColumns: editable.keyColumns,
      columns: shownColumns,
      rows: shownRows,
    });
    if (saved > 0 && edits.changes === 0) void fetchRows(profileName, shownSql);
  }
</script>

<svelte:window onkeydown={handleWindowKey} />

{#snippet modeToggle()}
  <!-- The Data | Structure toggle, in the app's own pill/tab language
       (App.svelte's nav bar): a bordered strip, the active side filled with the
       accent, the other muted until hovered. It is MySQL's alone — a key has no
       columns and no indexes — which is why it lives in this pane. -->
  <div
    class="flex h-8 shrink-0 items-center gap-0.5 rounded-control border border-border bg-surface p-0.5 text-sm"
    role="group"
    aria-label="Data or Structure"
  >
    <button
      type="button"
      class="rounded-control px-2.5 py-1 font-medium transition-colors {mode === 'data'
        ? 'bg-accent text-white'
        : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
      aria-pressed={mode === "data"}
      onclick={() => (mode = "data")}
    >
      Data
    </button>
    <button
      type="button"
      class="rounded-control px-2.5 py-1 font-medium transition-colors {mode === 'structure'
        ? 'bg-accent text-white'
        : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
      aria-pressed={mode === "structure"}
      onclick={() => (mode = "structure")}
    >
      Structure
    </button>
  </div>
{/snippet}

<BrowseHeader
  name={table}
  qualifier={database}
  profile={profileName}
  {loading}
  count={statusCount}
  statement={editorStatement}
  onOpenInEditor={openInEditor}
  onRefresh={refresh}
  refreshDisabled={mode === "data" ? loading : structureLoading}
  actions={modeToggle}
/>

<div class="flex min-h-0 flex-1 flex-col p-3">
  <!-- A save in progress takes the whole pane, exactly as it does in the
       Editor: one withheld statement, its Impact Preview, and the two keys that
       decide it. The pane comes back the moment it is answered. -->
  {#if confirm !== null}
    {@render confirm()}
  {:else}
    <section
      class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
    >
      <!-- WHERE and LIMIT are edits to a SELECT, so they are shown where there
           is one, and only while the grid is what is on screen. -->
      {#if mode === "data"}
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

          <label
            class="flex shrink-0 items-center gap-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
          >
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
        {#if mode === "structure"}
          <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">Structure</span>
        {:else if shownSql === ""}
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

      {#if mode === "structure"}
        <StructureView node={tableNode} />
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
              selectedIndices={pickedIndices}
              {edits}
              editable={editable.editable}
              {readOnlyHint}
              onRowClick={(index, options) => pickRow(index, options)}
            />
          </div>

          <RecordPaneDock
            columns={shownColumns}
            rows={pickedRows}
            indices={pickedIndices}
            totalRows={shownRows.length}
            {edits}
            editable={editable.editable}
            {readOnlyHint}
            {bodyWidth}
            gridMinPx={GRID_MIN_PX}
            splitterPx={SPLITTER_PX}
            detailWidth={layout.explorerDetailWidth}
            {detailMin}
            {detailMax}
            onResizeDetail={(next) => {
              layout.explorerDetailWidth = next;
              persistLayout();
            }}
            onClose={() => (selectedRows = [])}
          />
        </div>
      {:else}
        <!-- Anything but "ok" is the connection, the database, or the gate
             talking (not_connected, failed, and — if a condition classifies as
             a mutation — requires_confirmation or blocked). Shown verbatim,
             above controls that still work, since the fix is almost always a
             word in the WHERE box. Nothing here offers to confirm a write: this
             pane reads. -->
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
              Nothing ran. This panel only reads — fix the condition, or take the statement to the
              Editor with “Open in editor” and decide on it there.
            </p>
          {/if}
        </div>
      {/if}

      {#if edits.changes > 0}
        <SaveBar
          changes={edits.changes}
          rows={edits.dirtyRows.length}
          saving={edits.saving}
          failure={edits.failure}
          onSave={saveEdits}
          onRevert={() => edits.revert()}
        />
      {/if}
    </section>
  {/if}
</div>
