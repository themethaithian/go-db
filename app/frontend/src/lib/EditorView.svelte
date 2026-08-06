<script lang="ts">
  // The Editor: query tabs over a SQL editor, its results below, and a
  // draggable splitter at the seam. Nothing else — browsing a schema is the
  // Explorer's job, and the two were only ever confusing on one screen: a tree
  // highlighting `orders` beside an editor now selecting from `users` is a
  // screen that lies.
  //
  // The SQL itself is not held here. It belongs to the open documents
  // (documents.svelte.ts), which outlive this component — a view is unmounted
  // on every switch, and a query that vanished because someone glanced at the
  // Approval Console was the old behaviour, not a design. What is held here is
  // everything about the current run: classification, results, a pending
  // Inline Confirm and the selected Profile. connectedProfiles is owned and
  // refreshed by App.svelte and passed down, since it also drives the status
  // bar; the pane sizes live at module scope so they survive a trip elsewhere.
  //
  // One statement runs at a time. The buffer may hold a dozen; Run sends the
  // one under the cursor (or the selection, when there is one), so a
  // half-finished DELETE two statements down is never submitted alongside the
  // SELECT the human meant, and each statement meets the Approval Gate on its
  // own terms.
  import { onMount, untrack } from "svelte";
  import SqlEditor from "./SqlEditor.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import RecordPaneDock from "./RecordPaneDock.svelte";
  import InlineConfirm from "./InlineConfirm.svelte";
  import SaveBar from "./SaveBar.svelte";
  import Splitter from "./Splitter.svelte";
  import {
    clamp,
    layout,
    persistLayout,
    COMPARE_MIN_PX,
    DETAIL_MIN_PX,
    GRID_MIN_PX,
    QUERY_MIN_PX,
    RESULTS_MIN_PX,
  } from "./layout.svelte";
  import {
    activateDocument,
    closeDocument,
    documents,
    openDocument,
    renameDocument,
    writeSql,
  } from "./documents.svelte";
  import { deleteSaved, openSaved, saved, saveQuery } from "./saved.svelte";
  import { editorRanges, statementAt, subjectTable, type StatementRange } from "./statements";
  import { ensureIndexes, ensureTables, findTable } from "./schema.svelte";
  import { RowEdits } from "./edits.svelte";
  import { editability, selectTarget } from "./mutate";
  import { Classify, RunQuery, CancelPending, SplitStatements } from "../../wailsjs/go/app/App";
  import type { guard, service } from "../../wailsjs/go/models";

  let {
    connectedProfiles,
    seed,
    onSeedConsumed,
    onGoToConnections,
  }: {
    connectedProfiles: string[];
    /** A statement handed over from another view (Explorer's "Open in editor"), or null. */
    seed: { profile: string; sql: string } | null;
    onSeedConsumed: () => void;
    onGoToConnections: () => void;
  } = $props();

  // The seed is read once, and opens a tab of its own named after the table it
  // reads — deliberately not watched, and never written over an existing
  // document. Whatever the human has in their tabs is theirs; a handover is a
  // new piece of work beside it, not a replacement for it. (This view is
  // remounted on every entry, so "once" means "once per visit".)
  const handover = untrack(() => seed);
  if (handover !== null) {
    openDocument(handover.sql, subjectTable(handover.sql) ?? undefined);
  }

  let profileName = $state<string | null>(handover?.profile ?? null);
  let classification = $state<guard.Classification | null>(null);
  let running = $state(false);
  let result = $state<service.QueryResult | null>(null);

  // The statement behind what the Results pane is showing — recorded from the
  // run that produced it, never from the cursor's current whereabouts, so the
  // caption is a record rather than a promise.
  let ranSql = $state("");

  // The picked rows, as indices into the rows on screen and in the order they
  // were picked — that order is the order of the record pane's value
  // columns. Empty means the pane is closed. Every run clears it, the same
  // way a fresh fetch does in the Explorer: an index means nothing against a
  // result set it was not taken from.
  let selectedRows = $state<number[]>([]);

  let shownColumns = $derived(result?.status === "ok" ? (result.columns ?? []) : []);
  let shownRows = $derived(
    result?.status === "ok" ? ((result.rows ?? []) as (string | null)[][]) : [],
  );
  // The selection as the pane and the grid both see it: indices that still
  // point at a row, and those rows' cells in the same order.
  let pickedIndices = $derived(selectedRows.filter((index) => shownRows[index] !== undefined));
  let pickedRows = $derived(pickedIndices.map((index) => shownRows[index]));

  // The values typed over these rows and not yet saved, and the statement a
  // save is currently holding out for. Per view, like the selection: the
  // Explorer's grid has its own, and a row index means nothing between two
  // different result sets.
  const edits = new RowEdits();
  let savePending = $derived(edits.pending);

  // Which table the rows on screen can be written back to — the statement that
  // produced them has to be a plain single-table SELECT for there to be one at
  // all (see selectTarget), and that table's primary key has to be in the
  // result. The schema cache answers the second half; it is the same cache the
  // Database tree fills, asked for without expanding anything in it.
  let editTable = $derived(result?.status === "ok" ? selectTarget(ranSql) : null);
  let editNode = $derived(
    profileName === null || editTable === null ? null : findTable(profileName, editTable),
  );
  let editable = $derived(editability(editTable, shownColumns, editNode?.indexes ?? null));
  let readOnlyHint = $derived(editable.editable ? null : editable.reason);

  $effect(() => {
    if (profileName === null || editTable === null) return;
    ensureTables(profileName);
    const node = editNode;
    if (node !== null) ensureIndexes(profileName, node);
  });

  // A confirm nobody will answer must not be left in the gate's queue: leaving
  // the Editor unmounts this view, and an Inline Confirm has no expiry.
  $effect(() => () => edits.abandon());

  onMount(() => {
    if (seed !== null) onSeedConsumed();
  });

  // The document in front, and its text. Both are read-through: typing goes
  // back to the store, which is what makes it survive a view switch.
  let doc = $derived(documents.active);
  let sql = $derived(doc.sql);

  // Where the caret is and what is selected under it, as CodeMirror last
  // reported. A fresh document starts at 0, which is where CodeMirror puts the
  // caret too.
  let cursor = $state(0);
  let selection = $state("");

  // Which tab's name is being edited, if any.
  let renaming = $state<string | null>(null);

  // How long the splitter waits behind typing before asking the gate to split
  // the buffer again. Short enough that the badge keeps up with a cursor being
  // walked through a statement, long enough that a parse does not run on every
  // keystroke.
  const SPLIT_DEBOUNCE_MS = 150;

  // The horizontal splitter's own footprint (6px hit area + 4px of margin
  // either side), which is height the two panes do not get.
  const SPLITTER_PX = 14;

  // The vertical splitter's own footprint, between the results grid and the
  // record pane beside it — the same 6px hit area the Explorer's uses.
  const ROW_SPLITTER_PX = 6;

  // Measured height of the Query+Results stack, so the splitter between them
  // knows how far down it may go before Results is squeezed out of existence.
  let stackHeight = $state(0);
  let queryMaxHeight = $derived(
    Math.max(QUERY_MIN_PX, stackHeight - RESULTS_MIN_PX - SPLITTER_PX),
  );

  // Width of the results grid + record pane strip, so the vertical splitter
  // between them knows how far it may travel before one of the two is
  // squeezed out.
  let resultsWidth = $state(0);
  // Comparing asks for more room than reading one row does, so the pane's
  // floor moves with what it is doing.
  let detailMin = $derived(pickedIndices.length > 1 ? COMPARE_MIN_PX : DETAIL_MIN_PX);
  let detailMax = $derived(Math.max(detailMin, resultsWidth - GRID_MIN_PX - ROW_SPLITTER_PX));

  // A window that shrinks must shrink the Query pane with it, or Results
  // disappears below the fold with no way to drag it back.
  $effect(() => {
    const max = queryMaxHeight;
    if (stackHeight > 0 && layout.editorQueryHeight > max) {
      layout.editorQueryHeight = max;
    }
  });

  // The same shrink protection for the record pane beside the results grid —
  // a pane wider than the window has room for would push the grid off the
  // edge with no way to drag it back.
  $effect(() => {
    const min = detailMin;
    const max = detailMax;
    if (resultsWidth <= 0) return;
    const next = clamp(untrack(() => layout.editorDetailWidth), min, max);
    if (next !== layout.editorDetailWidth) layout.editorDetailWidth = next;
  });

  // If the selected Profile disconnects out from under the editor, fall back
  // to no selection rather than silently running against a stale one.
  $effect(() => {
    if (profileName !== null && !connectedProfiles.includes(profileName)) {
      profileName = null;
    }
  });

  // Where the statements in the buffer are, in the editor's own coordinates.
  // The gate does the splitting — it owns the MySQL grammar — and this is
  // debounced behind typing because a half-written buffer is re-parsed on
  // every keystroke otherwise.
  let statements = $state<StatementRange[]>([]);

  $effect(() => {
    const text = sql;
    if (text.trim() === "") {
      statements = [];
      return;
    }
    const timer = setTimeout(async () => {
      let ranges: StatementRange[];
      try {
        ranges = editorRanges(text, await SplitStatements(text));
      } catch {
        // The gate is unreachable — rather than leaving Run dead with text on
        // screen, treat the buffer as the one statement it usually is and let
        // the classifier have the last word on it, as it would anyway.
        ranges = [{ from: 0, to: text.length, text }];
      }
      // A split that came back after the human typed again describes a buffer
      // that no longer exists; the newer split is already on its way.
      if (untrack(() => sql) === text) statements = ranges;
    }, SPLIT_DEBOUNCE_MS);
    return () => clearTimeout(timer);
  });

  let activeStatement = $derived(statementAt(statements, cursor));

  // What Run would send. A selection wins over the cursor: selecting three
  // lines of a statement and running them is a deliberate act, and the human
  // who did it does not want the whole statement back.
  let selected = $derived(selection.trim());
  let target = $derived(
    selected !== "" ? selected : (statements[activeStatement]?.text ?? ""),
  );

  // The tint only appears once there is something to disambiguate. On a
  // single-statement buffer it would say nothing the buffer does not already
  // say, and a selection says it better than a tint can.
  let highlight = $derived(
    statements.length > 1 && selected === "" && activeStatement !== -1
      ? { from: statements[activeStatement].from, to: statements[activeStatement].to }
      : null,
  );

  // Which of the statements Run is aimed at, in words, beside the badge — the
  // badge alone would leave a buffer of five statements saying MUTATION with
  // no clue as to which one.
  let targetLabel = $derived(
    selected !== ""
      ? "selection"
      : statements.length > 1
        ? `statement ${activeStatement + 1} of ${statements.length}`
        : null,
  );

  // Live classification badge for the statement Run would send — not for the
  // buffer, which would read as a multi-statement Mutation the moment a second
  // statement was typed. Debounced ~250ms behind the target changing, and
  // cleared when there is nothing to run.
  $effect(() => {
    const text = target;
    if (text === "") {
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
  let canRun = $derived(
    profileName !== null && target !== "" && !running && !confirmOpen && !edits.saving,
  );

  async function run() {
    const statement = target;
    if (!canRun || profileName === null) return;
    await execute(profileName, statement);
  }

  // Runs the statement the Results pane is already showing again — what a
  // finished save does, so the grid shows what was persisted rather than what
  // was typed. It is the same read that produced the rows in the first place.
  async function rerun() {
    if (profileName === null || ranSql === "" || running) return;
    await execute(profileName, ranSql);
  }

  async function execute(profile: string, statement: string) {
    running = true;
    // Rows picked out of the old result set cannot survive a new one — the
    // indices would land on some other rows, or on none — and neither can
    // values typed over them.
    selectedRows = [];
    edits.abandon();
    edits.revert();
    try {
      result = await RunQuery(profile, statement);
      ranSql = statement;
    } finally {
      running = false;
    }
  }

  // Sends the dirty rows to the gate, one UPDATE and one Inline Confirm at a
  // time. Only a run that got all the way through re-runs the query: a cancel
  // part way leaves the rows it never reached dirty, and running over them
  // would throw away exactly the edits the human kept.
  async function saveEdits() {
    const profile = profileName;
    if (profile === null || !editable.editable) return;
    const saved = await edits.save({
      profileName: profile,
      table: editable.table,
      keyColumns: editable.keyColumns,
      columns: shownColumns,
      rows: shownRows,
    });
    if (saved > 0 && edits.changes === 0 && profileName === profile) void rerun();
  }

  // Clicking a row picks it and nothing else — including when it was already
  // one of several, which is the way back from a comparison to a single row.
  // Cmd (or Ctrl) adds a row to the comparison, or takes it back out. There is
  // no cap: the pane scrolls sideways under the field-name column once the
  // value columns outrun the width, so comparing more rows than fit is a
  // scroll away, not a wall.
  function pickRow(index: number, additive: boolean) {
    if (!additive) {
      selectedRows = [index];
      return;
    }
    if (selectedRows.includes(index)) {
      selectedRows = selectedRows.filter((picked) => picked !== index);
      return;
    }
    selectedRows = [...selectedRows, index];
  }

  function handleSqlChange(next: string) {
    writeSql(next);
  }

  function handleCursorChange(head: number, selectionText: string) {
    cursor = head;
    selection = selectionText;
  }

  function resizeQuery(next: number) {
    layout.editorQueryHeight = clamp(next, QUERY_MIN_PX, queryMaxHeight);
    persistLayout();
  }

  // A withheld mutation is the statement the gate previewed, by ID — not
  // whatever text is in the editor a moment later. If the human edits the SQL,
  // switches Profile, or switches to another tab while its Inline Confirm is
  // open, that statement no longer reflects their intent, so it is cancelled
  // rather than left to be confirmed stale. The pending's own ID is read with
  // untrack so this effect only fires on those three changing, not on result
  // changing (which it itself causes).
  $effect(() => {
    sql;
    profileName;
    doc.id;
    const pending = untrack(() => result);
    if (pending?.status === "requires_confirmation" && pending.pending_id) {
      CancelPending(pending.pending_id);
      untrack(() => {
        if (result === pending) result = null;
      });
    }
  });

  // Results belong to the run that produced them, and that run was made in one
  // tab. Coming back to a tab shows its SQL, not rows fetched before the last
  // time it was left — which nothing has kept true since. Declared after the
  // cancel above so a withheld statement is retracted before its result is
  // dropped on the floor.
  $effect(() => {
    doc.id;
    untrack(() => {
      result = null;
      ranSql = "";
      selectedRows = [];
      edits.abandon();
      edits.revert();
    });
  });

  function handlePendingResolved(next: service.QueryResult) {
    result = next;
  }

  function startRename(id: string) {
    renaming = id;
  }

  function commitRename(id: string, name: string) {
    renameDocument(id, name);
    if (renaming === id) renaming = null;
  }

  // The rename box is the only place in this view that takes the keyboard away
  // from the editor, so it hands it back on both exits: Enter commits, Escape
  // restores the name it opened with and leaves it committing nothing.
  function renameKeydown(event: KeyboardEvent & { currentTarget: HTMLInputElement }, id: string) {
    if (event.key === "Enter") {
      commitRename(id, event.currentTarget.value);
    } else if (event.key === "Escape") {
      event.currentTarget.value = documents.open.find((tab) => tab.id === id)?.name ?? "";
      renaming = null;
    }
  }

  // Opening the box on the name means editing it, which almost always means
  // replacing it — so it opens selected.
  function editName(node: HTMLInputElement) {
    node.focus();
    node.select();
  }

  // The Saved popover and the "Saved" transient beside the save button. Both
  // live here rather than in saved.svelte.ts: they are about this screen
  // reacting to a save, not about the library itself.
  let savedPopoverOpen = $state(false);
  let savedFeedback = $state(false);
  let savedFeedbackTimer: ReturnType<typeof setTimeout> | undefined;
  let savedPanel: HTMLDivElement | undefined;

  let canSave = $derived(sql.trim() !== "");

  // Saves the active tab under its own name — silently overwriting a saved
  // entry of the same name, per saveQuery's contract — and shows a brief
  // confirmation rather than nothing, since a save with no feedback reads as
  // a save that failed.
  function saveActive() {
    if (!canSave) return;
    saveQuery(doc.name, sql);
    savedFeedback = true;
    clearTimeout(savedFeedbackTimer);
    savedFeedbackTimer = setTimeout(() => {
      savedFeedback = false;
    }, 1400);
  }

  function toggleSavedPopover() {
    savedPopoverOpen = !savedPopoverOpen;
  }

  // Opening a saved query closes the popover — the human asked to see their
  // query, not to keep browsing the list beside it.
  function selectSaved(id: string) {
    openSaved(id);
    savedPopoverOpen = false;
  }

  // No live ticking: a saved query's date is a fact about the past, not a
  // countdown, so the exact wording only needs to be right when it renders.
  function formatSavedAt(iso: string): string {
    const date = new Date(iso);
    if (Number.isNaN(date.getTime())) return "";
    return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
  }

  // Scoped listeners, alive only while the popover is open: a click outside
  // it or an Escape closes it, and both are torn down the moment it does so
  // nothing keeps listening to the whole document once there is nothing to
  // close.
  $effect(() => {
    if (!savedPopoverOpen) return;
    function onPointerDown(event: MouseEvent) {
      if (savedPanel && !savedPanel.contains(event.target as Node)) {
        savedPopoverOpen = false;
      }
    }
    function onKeydown(event: KeyboardEvent) {
      if (event.key === "Escape") savedPopoverOpen = false;
    }
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeydown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeydown);
    };
  });
</script>

<div class="flex min-w-0 flex-1 flex-col overflow-hidden bg-surface">
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

    <span class="flex min-w-0 items-center gap-2">
      {#if classification}
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
        {/if}
      {/if}

      <!-- Which statement the badge and Run are talking about. Muted, and
           absent entirely while there is only one — a buffer of one statement
           has nothing to disambiguate. -->
      {#if targetLabel !== null}
        <span class="shrink-0 text-sm text-text-subtle">{targetLabel}</span>
      {/if}

      {#if classification && classification.kind !== "read"}
        <span class="truncate text-sm text-text-subtle" title={classification.reason}>
          {classification.reason}
        </span>
      {/if}
    </span>

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
        style="height: {layout.editorQueryHeight}px"
      >
        <!-- The tab strip is the Query pane's header: the pane is the document,
             so naming it twice (a "QUERY" caption above a row of names) would
             be one label too many in 32 pixels. -->
        <div
          class="flex h-8 shrink-0 items-stretch border-b border-border"
          role="tablist"
          aria-label="Query tabs"
        >
          <div class="flex min-w-0 flex-1 items-stretch overflow-x-auto pr-1">
          {#each documents.open as tab (tab.id)}
            <div
              class="group flex shrink-0 items-center border-b-2 pr-1 pl-2.5 transition-colors {tab.id ===
              doc.id
                ? 'border-accent bg-surface-raised'
                : 'border-transparent hover:bg-surface-raised/50'}"
            >
              {#if renaming === tab.id}
                <input
                  class="my-1 w-28 rounded-control border border-accent bg-surface px-1 text-sm text-text focus-visible:outline-none"
                  value={tab.name}
                  use:editName
                  onkeydown={(event) => renameKeydown(event, tab.id)}
                  onblur={(event) => commitRename(tab.id, event.currentTarget.value)}
                  aria-label="Rename tab"
                />
              {:else}
                <button
                  type="button"
                  role="tab"
                  aria-selected={tab.id === doc.id}
                  class="max-w-44 truncate text-sm transition-colors {tab.id === doc.id
                    ? 'font-medium text-text'
                    : 'text-text-muted hover:text-text'}"
                  title="{tab.name} — double-click to rename"
                  onclick={() => activateDocument(tab.id)}
                  ondblclick={() => startRename(tab.id)}
                >
                  {tab.name}
                </button>
              {/if}

              <button
                type="button"
                class="ml-1 flex h-4 w-4 shrink-0 items-center justify-center rounded-control text-text-subtle transition-opacity hover:bg-surface-overlay hover:text-text group-hover:opacity-100 focus-visible:opacity-100 {tab.id ===
                doc.id
                  ? 'opacity-60'
                  : 'opacity-0'}"
                title="Close tab"
                aria-label="Close {tab.name}"
                onclick={() => closeDocument(tab.id)}
              >
                <svg
                  class="h-2.5 w-2.5"
                  viewBox="0 0 10 10"
                  fill="none"
                  stroke="currentColor"
                  stroke-width="1.5"
                  stroke-linecap="round"
                  aria-hidden="true"
                >
                  <path d="M2 2l6 6M8 2l-6 6" />
                </svg>
              </button>
            </div>
          {/each}

          <button
            type="button"
            class="my-1 ml-1 flex w-6 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-raised hover:text-text"
            title="New query tab"
            aria-label="New query tab"
            onclick={() => openDocument()}
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
              <path d="M6 2v8M2 6h8" />
            </svg>
          </button>
        </div>

        <div
          class="relative flex shrink-0 items-center gap-1 border-l border-border px-1.5"
          bind:this={savedPanel}
        >
          {#if savedFeedback}
            <span class="text-xs font-medium text-success">Saved</span>
          {/if}

          <button
            type="button"
            class="flex h-6 w-6 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-raised hover:text-text disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:bg-transparent disabled:hover:text-text-subtle"
            title="Save &quot;{doc.name}&quot; to Saved queries"
            aria-label="Save query"
            disabled={!canSave}
            onclick={saveActive}
          >
            <svg
              class="h-3 w-3"
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              stroke-width="1.3"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M3 1.75h6a1 1 0 0 1 1 1v7.5l-4-2.4-4 2.4v-7.5a1 1 0 0 1 1-1z" />
            </svg>
          </button>

          <button
            type="button"
            class="flex h-6 shrink-0 items-center gap-1 rounded-control px-1.5 text-sm font-medium text-text-muted transition-colors hover:bg-surface-raised hover:text-text"
            aria-haspopup="true"
            aria-expanded={savedPopoverOpen}
            onclick={toggleSavedPopover}
          >
            Saved
            {#if saved.count > 0}
              <span class="rounded-full bg-accent/20 px-1.5 text-xs font-semibold text-accent">
                {saved.count}
              </span>
            {/if}
            <svg
              class="h-2.5 w-2.5"
              viewBox="0 0 10 10"
              fill="none"
              stroke="currentColor"
              stroke-width="1.5"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M2.5 4l2.5 2.5L7.5 4" />
            </svg>
          </button>

          {#if savedPopoverOpen}
            <div
              class="absolute top-full right-0 z-10 mt-1 w-64 rounded-panel border border-border bg-surface-panel p-1 shadow-panel"
              role="menu"
              aria-label="Saved queries"
            >
              {#if saved.all.length === 0}
                <p class="px-2 py-3 text-center text-sm text-text-subtle">No saved queries yet.</p>
              {:else}
                {#each saved.all as entry (entry.id)}
                  <div
                    class="group flex items-start gap-1 rounded-control px-1.5 py-1.5 hover:bg-surface-raised"
                  >
                    <button
                      type="button"
                      class="min-w-0 flex-1 text-left"
                      onclick={() => selectSaved(entry.id)}
                    >
                      <div class="truncate text-sm font-semibold text-text">{entry.name}</div>
                      <div class="truncate font-mono text-xs text-text-subtle">{entry.sql}</div>
                      <div class="text-xs text-text-subtle">{formatSavedAt(entry.savedAt)}</div>
                    </button>
                    <button
                      type="button"
                      class="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-control text-text-subtle opacity-0 transition-opacity hover:bg-surface-overlay hover:text-text group-hover:opacity-100 focus-visible:opacity-100"
                      title="Delete {entry.name}"
                      aria-label="Delete {entry.name}"
                      onclick={() => deleteSaved(entry.id)}
                    >
                      <svg
                        class="h-2.5 w-2.5"
                        viewBox="0 0 10 10"
                        fill="none"
                        stroke="currentColor"
                        stroke-width="1.5"
                        stroke-linecap="round"
                        aria-hidden="true"
                      >
                        <path d="M2 2l6 6M8 2l-6 6" />
                      </svg>
                    </button>
                  </div>
                {/each}
              {/if}
            </div>
          {/if}
        </div>
        </div>
        <div class="min-h-0 flex-1">
          <SqlEditor
            value={sql}
            {highlight}
            onChange={handleSqlChange}
            onCursorChange={handleCursorChange}
            onRun={run}
          />
        </div>
      </section>

      <Splitter
        orientation="horizontal"
        value={layout.editorQueryHeight}
        min={QUERY_MIN_PX}
        max={queryMaxHeight}
        label="Resize the query pane"
        onChange={resizeQuery}
      />

      <!-- A save's own withheld UPDATE comes first: it is a statement the human
           asked for a moment ago, and it is answered the same way a typed one
           is — same panel, same preview, same two keys. -->
      {#if savePending !== null && savePending.pending_id && savePending.preview}
        <InlineConfirm
          reason={savePending.classification.reason}
          preview={savePending.preview}
          pendingId={savePending.pending_id}
          sql={edits.pendingSql}
          onResolved={(next) => edits.resolved(next)}
        />
      {:else if result !== null && result.status === "requires_confirmation" && result.pending_id && result.preview}
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
          <!-- The caption is the statement these rows came from, which on a
               multi-statement buffer is the only thing that says which one ran.
               Before the first run there is nothing to record, so the pane
               simply names itself. -->
          <div class="flex h-8 shrink-0 items-center justify-between gap-3 border-b border-border px-3">
            {#if ranSql === ""}
              <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">
                Results
              </span>
            {:else}
              <span class="truncate font-mono text-xs text-text-subtle" title={ranSql}>
                {ranSql}
              </span>
            {/if}
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
                Write a query above and press ⌘⏎ — or browse a table in the Explorer.
              </p>
            </div>
          {:else if result.status === "ok"}
            <div class="flex min-h-0 min-w-0 flex-1" bind:clientWidth={resultsWidth}>
              <div class="flex min-w-0 flex-1">
                <ResultsTable
                  columns={shownColumns}
                  rows={shownRows}
                  truncated={result.truncated}
                  selectedIndices={pickedIndices}
                  pending={edits.cells(shownColumns)}
                  onRowClick={(index, options) => pickRow(index, options.additive)}
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
                bodyWidth={resultsWidth}
                gridMinPx={GRID_MIN_PX}
                splitterPx={ROW_SPLITTER_PX}
                detailWidth={layout.editorDetailWidth}
                {detailMin}
                {detailMax}
                onResizeDetail={(next) => {
                  layout.editorDetailWidth = next;
                  persistLayout();
                }}
                onClose={() => (selectedRows = [])}
              />
            </div>
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
  </div>
</div>
