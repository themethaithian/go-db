<script module lang="ts">
  /**
   * The most rows this table will hold in memory at once. Scroll-to-load has
   * no natural stopping point of its own — DBeaver's own grids do not either
   * — so without a ceiling a big enough table would grow the grid, and the
   * DOM under it, without bound. 5,000 rows is comfortably past what anyone
   * reads in one sitting and comfortably inside this app's own performance
   * budget (CLAUDE.md: idle RAM under 150 MB). Exported so the callers that
   * do the fetching (TableBrowse, EditorView) never even see a batch past it
   * — this table simply stops asking once `rows` reaches it, so there is
   * only one place this number lives.
   */
  export const MAX_LOADED_ROWS = 5000;
</script>

<script lang="ts">
  // Renders one QueryOK result: columns/rows from the backend, scrolled
  // through DBeaver-style rather than paged. Every row handed to it is
  // rendered at once; scrolling near the bottom asks the caller for the next
  // batch and the caller appends it — this table never discards rows once
  // shown, and never asks for anything before what it already has. It holds
  // no knowledge of how the query ran or how the batch it is scrolling
  // through was assembled: App/EditorView/TableBrowse decide that, and hand
  // over one growing `rows` array plus a couple of flags.
  //
  // Rows can also be picked, for views that show the picked rows on their own
  // (the record pane). The selection is not this table's state: it is passed
  // in as indices into `rows` and reported back on click, so the one
  // component that renders the picked rows and the one that highlights them
  // cannot disagree. A click's modifiers decide what it does to that
  // selection — plain replaces it, cmd/ctrl toggles one row into or out of
  // it, shift extends it from the last-picked row to the clicked one — but
  // this table only reports which modifiers were held; the caller owns what
  // the selection becomes. Leaving both props off — as the Editor does for a
  // result it never lets be picked — leaves rows unclickable and the markup
  // unchanged.
  //
  // A cell can be edited here too, DBeaver-style: double-click opens an
  // in-place input, Enter commits, Esc cancels, Tab commits and opens the
  // next cell in the row. Nothing is written from here — a commit is handed
  // straight to `edits`, the same RowEdits the record pane already edits
  // into, so a value typed in the grid and a value typed in the pane land in
  // the identical dirty set. `editable` and `readOnlyHint` are the caller's
  // own verdict (mutate.ts's editability), passed through unchanged; a
  // double-click while read-only flashes the reason in the footer instead of
  // opening anything. Leaving `edits` off leaves cells inert on double-click,
  // the same way leaving `onRowClick` off leaves rows inert on click.
  import type { RowEdits } from "./edits.svelte";
  import type { CellValue } from "./mutate";
  import { toCsv, toJson, toTsv } from "./copy";

  let {
    columns,
    rows,
    truncated,
    selectedIndices = [],
    edits = null,
    editable = false,
    readOnlyHint = null,
    onRowClick,
    hasMoreAfter = false,
    onFetchAfter,
    loadingMore = false,
    resultKey = 0,
  }: {
    columns: string[];
    rows: CellValue[][];
    truncated: boolean;
    /** Indices into `rows` of the picked rows, newest last. Ignored without onRowClick. */
    selectedIndices?: number[];
    /** The result's pending edits, shown here and written to from here. */
    edits?: RowEdits | null;
    /** Whether these rows can be edited at all — the caller's verdict, not this table's. */
    editable?: boolean;
    /** Why they cannot be, when they cannot and it is worth saying — flashed on a double-click attempt. */
    readOnlyHint?: string | null;
    /** Given, rows become clickable and report their index into `rows` and which modifiers were held. */
    onRowClick?: (index: number, options: { additive: boolean; range: boolean }) => void;
    /** Whether there is a next batch beyond `rows` for onFetchAfter to fetch —
     *  scrolling near the bottom, or the Load more button, both read this.
     *  false once MAX_LOADED_ROWS is reached even if the caller still thinks
     *  there is more, since this table will not ask for it either way. */
    hasMoreAfter?: boolean;
    /** Appends the next batch onto `rows` — called at most once per scroll
     *  arrival at the bottom (see `armed` below), and from the footer's Load
     *  more button, which exists for exactly the case a scroll gesture
     *  cannot reach: a batch short enough that this table never grows a
     *  scrollbar in the first place. */
    onFetchAfter?: () => void;
    /** True while the caller's own onFetchAfter is in flight — disables the
     *  Load more button (and its label reads "Loading…") and blocks a second
     *  scroll-triggered fetch from stacking behind the first. */
    loadingMore?: boolean;
    /** A generation counter the caller bumps only when `rows` is about to be
     *  a brand new result — a fresh run, a different browse target — never
     *  when it is the next batch of the one already on screen. Array
     *  identity cannot tell those two apart: both a replace and an append
     *  hand this table a new array reference (append is `[...old, ...more]`),
     *  and a "did it grow" heuristic cannot either, since a replace can
     *  easily grow too (a wider filter, a bigger page size). An explicit
     *  counter is the only unambiguous signal, so that is what this reads:
     *  scroll position, any open cell editor, and the read-only flash are all
     *  reset when it changes, and left alone otherwise — an append must never
     *  yank the human back to the top of rows they were already scrolled
     *  past. Defaults to 0 for a caller with nothing to page. */
    resultKey?: number;
  } = $props();

  let selectable = $derived(onRowClick !== undefined);
  let picked = $derived(new Set(selectedIndices));

  // The cell open for typing, as its row (an index into `rows`) and column
  // index, and the text in the box. One at a time, like the record pane's own
  // field editor.
  let editingCell = $state<{ row: number; col: number } | null>(null);
  let cellDraft = $state("");

  // The read-only reason, shown in the footer for a few seconds after a
  // double-click attempt finds nothing to open. A moment is enough to read it
  // without it becoming a second, permanent status line.
  const FLASH_MS = 3000;
  let flash = $state<string | null>(null);
  let flashTimer: ReturnType<typeof setTimeout> | null = null;

  $effect(() => () => {
    if (flashTimer !== null) clearTimeout(flashTimer);
  });

  function showFlash(message: string) {
    flash = message;
    if (flashTimer !== null) clearTimeout(flashTimer);
    flashTimer = setTimeout(() => (flash = null), FLASH_MS);
  }

  // The quiet footer line for a result nobody has picked from yet — shown
  // only until the first click teaches the gesture. The edit half only
  // applies when there is something to edit.
  let hint = $derived(
    selectable && picked.size === 0
      ? `click a row for details${editable ? " · double-click a cell to edit" : ""}`
      : null,
  );

  // What a cell shows: a pending edit if there is one, else the value the
  // query fetched. The grid's own render, Tab's advance to the next cell, and
  // a copy all read a cell through this one function, so a value typed over
  // a fetched one shows the same everywhere it is read from — including on
  // the clipboard.
  function resolvedCell(row: number, column: string, fetched: CellValue): CellValue {
    return edits ? edits.value(row, column, fetched) : fetched;
  }

  // Opens a field for typing — or, off a read-only result, flashes why not.
  // The box opens holding what the cell shows right now: a pending edit if
  // there is one, else the fetched value.
  function startCellEdit(row: number, col: number) {
    if (edits === null || edits === undefined) return;
    if (!editable) {
      if (readOnlyHint !== null) showFlash(readOnlyHint);
      return;
    }
    const column = columns[col];
    const fetched = rows[row]?.[col] ?? null;
    const current = resolvedCell(row, column, fetched);
    editingCell = { row, col };
    cellDraft = current ?? "";
  }

  // Double-click detection of our own, in place of the browser's: picking a
  // row opens or resizes the record pane beside this table, which reflows
  // the grid under the cursor between a double-click's two physical clicks
  // far more often than a mouse moves between them. A native `dblclick` on
  // the second click would then land on whatever the pane now covers, not
  // the cell the human was looking at — this table saw exactly that in
  // practice. Tracking by screen position and timing instead of by which
  // element each click happened to hit survives that reflow: the first click
  // records where and when, on which cell; a second click close to both is
  // read as a double-click on that cell regardless of what element it lands
  // on. `editingCell` being open already takes clicks out of this — an
  // editor open on one cell must not treat a double-click meant for its own
  // text (selecting a word to retype) as a request to reopen it elsewhere.
  const DBLCLICK_MS = 500;
  const DBLCLICK_PX = 6;
  let pendingDouble = $state<{ row: number; col: number; x: number; y: number; time: number } | null>(
    null,
  );

  function handleWindowClick(event: MouseEvent) {
    if (editingCell !== null) return;
    const target = event.target as Element | null;
    const cell = target?.closest<HTMLElement>("td[data-row]") ?? null;
    const now = performance.now();

    if (
      pendingDouble !== null &&
      now - pendingDouble.time <= DBLCLICK_MS &&
      Math.abs(event.clientX - pendingDouble.x) <= DBLCLICK_PX &&
      Math.abs(event.clientY - pendingDouble.y) <= DBLCLICK_PX
    ) {
      const { row, col } = pendingDouble;
      pendingDouble = null;
      startCellEdit(row, col);
      return;
    }

    if (cell !== null && cell.dataset.row !== undefined && cell.dataset.col !== undefined) {
      pendingDouble = {
        row: Number(cell.dataset.row),
        col: Number(cell.dataset.col),
        x: event.clientX,
        y: event.clientY,
        time: now,
      };
    } else {
      pendingDouble = null;
    }
  }

  // Commits the box's text as a pending edit against the column it belongs
  // to, and closes the box. `fetched` is what the database gave for this
  // cell, so an edit that lands back on it is recorded as no edit at all.
  function commitCell(row: number, column: string, fetched: CellValue) {
    if (edits === null || edits === undefined) return;
    edits.set(row, column, cellDraft, fetched);
    editingCell = null;
  }

  // Tab's own half of a commit: closes the current box and, if there is
  // another column in the row, opens it holding whatever it already shows.
  // Running off the end of the row simply stops — there is no next row to
  // wrap into, and Tab is not Enter.
  function advanceCell(row: number, col: number) {
    const nextCol = col + 1;
    if (nextCol >= columns.length) return;
    const column = columns[nextCol];
    const fetched = rows[row]?.[nextCol] ?? null;
    const shown = resolvedCell(row, column, fetched);
    editingCell = { row, col: nextCol };
    cellDraft = shown ?? "";
  }

  // Enter commits, Escape drops the edit and stops there — the same key
  // closes the record pane (or the row selection), and leaving a cell editor
  // should not also do that. Tab commits and moves on, the way a spreadsheet
  // does, rather than leaving the field's editor open and losing the keypress
  // to the browser's own focus order.
  function handleCellKeydown(event: KeyboardEvent, row: number, col: number, fetched: CellValue) {
    if (event.key === "Enter") {
      event.preventDefault();
      commitCell(row, columns[col], fetched);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      editingCell = null;
    } else if (event.key === "Tab") {
      event.preventDefault();
      commitCell(row, columns[col], fetched);
      advanceCell(row, col);
    }
  }

  // A box removed from under a focused caret (Tab moving to the next one, an
  // Escape unmounting it) fires a native blur on its way out — after
  // `editingCell` already points somewhere else, or nowhere. Committing only
  // when this box is still the one on record is what keeps that blur from
  // re-committing a cell Tab or Escape has already settled.
  function blurCell(row: number, col: number, column: string, fetched: CellValue) {
    if (editingCell?.row === row && editingCell?.col === col) commitCell(row, column, fetched);
  }

  // Editing a value almost always means replacing it, so the box opens
  // selected — the same contract the record pane's own field editor has.
  function openCellEditor(node: HTMLInputElement) {
    node.focus();
    node.select();
  }

  // The row the selection last moved to — the one the scroll follows.
  // Earlier picks stay highlighted where they are: adding a row to a
  // comparison should not yank the grid back to the first one.
  let focused = $derived(
    selectedIndices.length === 0 ? null : selectedIndices[selectedIndices.length - 1],
  );

  // The scroll container this table owns — both for the reveal action below
  // and for the near-bottom check that drives scroll-to-load. Owning it here
  // rather than in TableBrowse/EditorView is what lets both wire the same
  // props and get the same behaviour: this table is the one thing that knows
  // where its own fold is.
  let scrollContainer = $state<HTMLDivElement | null>(null);

  // A resultKey change is the caller's word that `rows` is a brand new
  // result — see the prop doc for why this, and not `rows` itself, is what
  // this reacts to. Everything a stale result would leave behind goes with
  // it: an open cell editor, a read-only flash, and the scroll position,
  // which would otherwise show whatever row used to be at this pixel offset
  // in a grid that no longer holds it.
  $effect(() => {
    resultKey;
    editingCell = null;
    pendingDouble = null;
    flash = null;
    if (scrollContainer !== null) scrollContainer.scrollTop = 0;
  });

  let totalRows = $derived(rows.length);
  let rangeStart = $derived(totalRows === 0 ? 0 : 1);
  let rangeEnd = $derived(totalRows);

  // Whether MAX_LOADED_ROWS has been reached — the one condition under which
  // this table refuses to ask for more regardless of what the caller's
  // hasMoreAfter says, so a runaway scroll (or a caller that forgot to check)
  // cannot grow the grid past its own budget.
  let atCap = $derived(totalRows >= MAX_LOADED_ROWS);

  // "of N" is only honest when there is nothing past this batch — a caller
  // with more rows behind onFetchAfter (or a batch withheld only because the
  // cap was hit) has not told this table how many rows the table it is
  // browsing actually holds. The "+" says so rather than implying rows.length
  // is the whole of it.
  let totalLabel = $derived(
    hasMoreAfter || atCap ? `${totalRows}+` : `${totalRows}`,
  );

  // Re-armed whenever the loaded rows grow — a batch actually landing is
  // what makes it meaningful to ask for the next one. Set false the instant
  // a scroll-triggered fetch fires, so holding the scroll position at the
  // bottom while the caller's fetch is in flight cannot ask twice for the
  // same batch; `loadingMore` guards the same window from the caller's side.
  let armed = $state(true);
  $effect(() => {
    rows.length;
    armed = true;
  });

  // Roughly two rows tall: close enough to the fold that reaching it reads as
  // "the human wants more" rather than requiring them to scroll to the exact
  // last pixel, which a fast scroll gesture can overshoot entirely.
  const NEAR_BOTTOM_PX = 96;

  function handleScroll() {
    if (!armed || loadingMore || atCap || !hasMoreAfter || scrollContainer === null) return;
    const { scrollTop, scrollHeight, clientHeight } = scrollContainer;
    if (scrollHeight - scrollTop - clientHeight > NEAR_BOTTOM_PX) return;
    armed = false;
    onFetchAfter?.();
  }

  // Placeholder rows for the batch that is on its way while `loadingMore` is
  // true — enough to fill roughly what the fold reveals, so the eye lands on
  // something already there instead of a blank scrollbar-jump when the real
  // rows arrive. Bar widths cycle rather than run full-width in every cell:
  // a uniform block of identical bars reads as decoration, while widths that
  // vary the way real values do is what makes it read as "rows, still
  // loading" rather than a generic loading banner.
  const SKELETON_ROWS = 3;
  const SKELETON_WIDTHS = ["w-3/4", "w-1/2", "w-5/6", "w-2/3"];

  // What a copy reaches for: the selection, in on-screen order rather than
  // pick order — selectedIndices is newest-last, and a paste that reordered
  // itself around which row was clicked last would be a stranger grid than
  // the one on screen. With nothing picked, a copy takes every loaded row —
  // the footer already says "of 5,000+" when there are more, and a copy that
  // quietly gave back less would contradict its own count.
  let copyIndices = $derived(
    selectedIndices.length > 0
      ? [...selectedIndices].filter((index) => rows[index] !== undefined).sort((a, b) => a - b)
      : rows.map((_, index) => index),
  );

  // The words each copy button's title reads before the click — "3 selected
  // rows" or "all 200 rows" — so what a copy is about to do is legible
  // without having to try it first.
  let copyScope = $derived(
    selectedIndices.length > 0
      ? `${copyIndices.length} selected row${copyIndices.length === 1 ? "" : "s"}`
      : `all ${totalRows} row${totalRows === 1 ? "" : "s"}`,
  );

  const COPIED_MS = 1200;
  let copiedFormat = $state<"tsv" | "csv" | "json" | null>(null);
  let copyTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => () => {
    if (copyTimer !== null) clearTimeout(copyTimer);
  });

  // Builds the grid to copy — copyIndices' rows, each cell read through
  // resolvedCell so a value mid-edit copies what the cell shows rather than
  // what the query fetched under it — and writes one of copy.ts's three
  // shapes to the clipboard. The tick on the button pressed follows
  // CopyJson's own contract: it shows only once the write has actually
  // landed, never for a clipboard that refused.
  async function copyResults(format: "tsv" | "csv" | "json") {
    const grid = copyIndices.map((index) =>
      rows[index].map((cell, col) => resolvedCell(index, columns[col], cell)),
    );
    const text =
      format === "tsv" ? toTsv(columns, grid) : format === "csv" ? toCsv(columns, grid) : toJson(columns, grid);
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      return;
    }
    copiedFormat = format;
    if (copyTimer !== null) clearTimeout(copyTimer);
    copyTimer = setTimeout(() => (copiedFormat = null), COPIED_MS);
  }

  // Keeps the picked row in view — the selection can be moved by keyboard,
  // and a highlight below the fold is a highlight nobody sees. Clicking a row
  // that is already on screen scrolls nothing ("nearest" is a no-op then).
  function reveal(node: HTMLElement, picked: boolean) {
    const sync = (on: boolean) => {
      if (on) node.scrollIntoView({ block: "nearest" });
    };
    sync(picked);
    return { update: sync };
  }
</script>

<svelte:window onclick={handleWindowClick} />

<!-- min-w-0 is not decoration: without it, a flex item defaults to never
     shrinking below its content's own natural width, and a wide result (the
     `<table>` below is `min-w-max`, i.e. never narrower than every column
     unwrapped) demands that width regardless of what the caller's layout
     actually gives this component. TableBrowse and EditorView both shrink
     the space this table gets to make room for the record pane beside it —
     without min-w-0 this root refuses to follow, and the table (and the
     overflow-auto scroll region below, which stretches to match this root's
     width) silently renders past its own right edge, wide enough to paint
     straight under the pane docked next to it. isolate is the same
     containment for stacking: the sticky, z-indexed thead a few lines down
     would otherwise be free to compete for paint order with whatever sibling
     happens to share its nearest ancestor stacking context — the record pane
     among them — since nothing here currently draws that boundary. Both
     together are what keep this table's own machinery (however it scrolls,
     whatever it pins) from ever being visible outside the box it was given. -->
<div class="isolate flex min-h-0 min-w-0 flex-1 flex-col">
  {#if totalRows === 0}
    <div class="m-auto flex flex-col items-center gap-1 px-6 py-8 text-center">
      <p class="text-base font-medium text-text">0 rows</p>
      <p class="text-sm text-text-muted">The query ran fine — it just matched nothing.</p>
    </div>
  {:else}
    <!-- overscroll-contain keeps a scroll gesture that reaches the bottom of
         this grid inside this grid: without it, the last pixel of scroll
         chains into whatever scrolls behind this table (the page, a dialog),
         which is the rubber-band bounce scroll-to-load was getting blamed
         for even though the fetch itself was working fine. -->
    <div
      class="min-h-0 flex-1 overflow-auto overscroll-contain"
      bind:this={scrollContainer}
      onscroll={handleScroll}
    >
      <!-- The grid is not text. A click here means "pick this row" and a
           shift-click means "pick these rows", so the browser's own reading of
           the same gestures — put a caret here, drag a highlight to there —
           has nothing to contribute and everything to take away: a black
           smear across the rows you just selected. Every DB tool turns it off
           for the same reason, and hands the copying back through affordances
           that copy a *value* rather than whatever the pointer swept over —
           here, the record pane's per-field copy button. The one place text is
           still text is inside an open cell editor, which says so itself.

           This is half the fix; the other half is on the row, where a
           shift-click has to be stopped from extending a selection that was
           anchored somewhere outside this table. -->
      <table class="w-full min-w-max border-collapse text-left select-none">
        <thead class="sticky top-0 z-10">
          <tr>
            <!-- Keyed by position, not name: two joined tables can each bring
                 a `name`, and a keyed each over duplicate keys is a crash,
                 not a warning. Position is what a result column really is. -->
            {#each columns as column, i (i)}
              <th
                class="border-b border-border bg-surface-raised px-3 py-2 text-xs font-medium tracking-wide text-text-muted uppercase whitespace-nowrap"
              >
                {column}
              </th>
            {/each}
          </tr>
        </thead>
        <!--
          A picked row is a row you clicked, so it carries a click handler and
          says which one it is. Svelte's a11y rules see a bare <tr>; the
          keyboard route to the same thing is the record pane's own up/down
          and Escape, handled where that pane lives. A cell's own dblclick
          handler is a step further down the same row — Svelte flags that the
          same way, and the a11y route to it is the record pane's pencil,
          which is a real button.
        -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
        <tbody class="font-mono text-base">
          {#each rows as row, index (index)}
            {@const chosen = selectable && picked.has(index)}
            <tr
              class="transition-colors {chosen
                ? 'bg-accent/15'
                : 'hover:bg-surface-overlay'} {selectable ? 'cursor-pointer' : ''}"
              aria-selected={selectable ? chosen : undefined}
              onmousedown={onRowClick === undefined
                ? undefined
                : (event) => {
                    // A shift-click is the browser's "extend the selection to
                    // here" as much as it is this table's "extend the picked
                    // range to here", and an anchor left in the record pane
                    // beside the grid is enough for it to sweep a highlight
                    // across everything in between. Refusing the default on
                    // the mousedown that carries the modifier heads that off
                    // without touching the plain click, which still focuses
                    // and clicks the way a row should.
                    if (event.shiftKey) event.preventDefault();
                  }}
              onclick={onRowClick === undefined
                ? undefined
                : (event) =>
                    onRowClick(index, {
                      additive: event.metaKey || event.ctrlKey,
                      range: event.shiftKey,
                    })}
              use:reveal={index === focused}
            >
              {#each row as cell, j (j)}
                {@const column = columns[j]}
                {@const dirty = edits?.dirty(index, column) ?? false}
                {@const value = resolvedCell(index, column, cell)}
                {@const open = editingCell !== null && editingCell.row === index && editingCell.col === j}
                <!-- Opening the editor is handleWindowClick's job, not a
                     native dblclick here — see the comment on it for why.
                     data-row/data-col are how that handler finds its way
                     back to this cell from a click that lands anywhere.
                     preventDefault only heads off the browser's own
                     select-the-word reflex on a fast second click. -->
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <td
                  class="overflow-hidden border-b border-border/60 {open
                    ? 'p-0'
                    : 'px-3 py-2'} tabular-nums whitespace-nowrap {dirty
                    ? 'bg-accent/10 text-accent'
                    : 'text-text'}"
                  title={dirty ? "Edited — not saved yet" : undefined}
                  data-row={index}
                  data-col={j}
                  ondblclick={(event) => event.preventDefault()}
                >
                  {#if open}
                    <input
                      class="box-border block w-full border-0 bg-surface px-3 py-2 font-mono text-base leading-5 text-text ring-2 ring-accent select-text ring-inset focus:outline-none"
                      bind:value={cellDraft}
                      use:openCellEditor
                      onkeydown={(event) => handleCellKeydown(event, index, j, cell)}
                      onblur={() => blurCell(index, j, column, cell)}
                      onclick={(event) => event.stopPropagation()}
                      onmousedown={(event) => event.stopPropagation()}
                      spellcheck="false"
                      autocapitalize="off"
                      autocomplete="off"
                      aria-label="Edit {column}"
                    />
                  {:else if value === null}
                    <span class="text-text-subtle italic">NULL</span>
                  {:else}
                    {value}
                  {/if}
                </td>
              {/each}
            </tr>
          {/each}
          {#if loadingMore}
            <!-- The next batch's rows, before they exist: same column count
                 and row rhythm as the real ones, laid down at the bottom
                 inside the same scroll container so they sit exactly where
                 the fetched rows are about to land. `aria-hidden` because
                 there is nothing here to read — a screen reader already has
                 the "Loading…" button label for that. Gone the instant
                 `loadingMore` drops, so the append reads as these resolving
                 into real rows rather than content popping in beside them. -->
            {#each Array(SKELETON_ROWS) as _, skeletonRow (skeletonRow)}
              <tr aria-hidden="true">
                {#each columns as column, col (col)}
                  <td class="border-b border-border/60 px-3 py-2">
                    <div
                      class="h-3.5 {SKELETON_WIDTHS[
                        (skeletonRow + col) % SKELETON_WIDTHS.length
                      ]} animate-pulse rounded-control bg-surface-raised"
                    ></div>
                  </td>
                {/each}
              </tr>
            {/each}
          {/if}
        </tbody>
      </table>
    </div>

    <div
      class="flex shrink-0 items-center justify-between gap-3 border-t border-border px-3 py-2 text-xs text-text-muted"
    >
      <span class="flex min-w-0 items-center gap-2">
        <span class="shrink-0 tabular-nums">
          Rows {rangeStart}–{rangeEnd} of {totalLabel}
        </span>
        {#if truncated}
          <span
            class="shrink-0 rounded-full border border-warning/40 bg-warning/10 px-1.5 py-px font-medium text-warning"
            title="The backend capped this result set"
          >
            truncated
          </span>
        {/if}
        <!-- A double-click attempt on a read-only result flashes here for a
             moment; absent that, the quiet hint teaches the two gestures this
             table has and no docs mention. Never both at once — the flash is
             a direct answer to something just clicked, and would be lost
             under a permanent line. -->
        {#if flash !== null}
          <span class="min-w-0 truncate text-warning" title={flash}>{flash}</span>
        {:else if hint !== null}
          <span class="min-w-0 truncate text-text-subtle italic" title={hint}>{hint}</span>
        {/if}
      </span>

      <!-- Copy reaches through the same resolvedCell every rendered cell
           does, so what lands on the clipboard is what is on screen — a
           dirty edit included — not a second read of the query's own
           answer. Three small buttons rather than one button with a menu:
           the whole point is a human choosing a format without a click just
           to see what the choices are. -->
      <div class="flex shrink-0 items-center gap-1" role="group" aria-label="Copy results">
        <button
          type="button"
          class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
          onclick={() => copyResults("tsv")}
          disabled={totalRows === 0}
          title={`Copy ${copyScope} as TSV`}
        >
          {copiedFormat === "tsv" ? "Copied" : "TSV"}
        </button>
        <button
          type="button"
          class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
          onclick={() => copyResults("csv")}
          disabled={totalRows === 0}
          title={`Copy ${copyScope} as CSV`}
        >
          {copiedFormat === "csv" ? "Copied" : "CSV"}
        </button>
        <button
          type="button"
          class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
          onclick={() => copyResults("json")}
          disabled={totalRows === 0}
          title={`Copy ${copyScope} as JSON`}
        >
          {copiedFormat === "json" ? "Copied" : "JSON"}
        </button>
      </div>

      <!-- The scroll gesture is the primary way to load more, but a batch
           short enough to fit without a scrollbar leaves nothing to scroll —
           this button is the fallback for exactly that case, and a visible
           affordance so the feature is discoverable even for someone who
           never scrolls to the fold. atCap wins over hasMoreAfter: once the
           ceiling is reached there is nothing this button could still ask
           for, so it says why instead of offering to try. -->
      <div class="flex shrink-0 items-center">
        {#if atCap}
          <span class="text-text-subtle italic">
            {MAX_LOADED_ROWS.toLocaleString()} rows loaded — narrow the query to see beyond
          </span>
        {:else if hasMoreAfter}
          <button
            type="button"
            class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
            onclick={() => onFetchAfter?.()}
            disabled={loadingMore}
          >
            {loadingMore ? "Loading…" : "Load more"}
          </button>
        {/if}
      </div>
    </div>
  {/if}
</div>
