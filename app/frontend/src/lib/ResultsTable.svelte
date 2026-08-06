<script lang="ts">
  // Renders one QueryOK result: columns/rows from the backend, paginated
  // client-side. Holds no knowledge of how the query ran — App/EditorView
  // decide when to show it.
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

  let {
    columns,
    rows,
    truncated,
    selectedIndices = [],
    edits = null,
    editable = false,
    readOnlyHint = null,
    onRowClick,
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
  } = $props();

  const pageSize = 50;
  let page = $state(0);

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
    const current = edits.value(row, column, fetched);
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
    const shown = edits ? edits.value(row, column, fetched) : fetched;
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

  // The row the selection last moved to — the one the page and the scroll
  // follow. Earlier picks stay highlighted where they are: adding a row to a
  // comparison should not yank the grid back to the first one.
  let focused = $derived(
    selectedIndices.length === 0 ? null : selectedIndices[selectedIndices.length - 1],
  );

  // A new result set always starts on page 1, whatever page the previous
  // one was left on — and takes any open cell editor and flash with it, since
  // a row index means nothing against rows that just replaced the ones it was
  // opened over.
  $effect(() => {
    rows;
    page = 0;
    editingCell = null;
    pendingDouble = null;
    flash = null;
  });

  // A selection made from outside the visible page (the record pane's up/down
  // keys walking off the end of one) brings its page with it, so the
  // highlighted row is always a row you can see.
  $effect(() => {
    if (focused === null) return;
    const target = Math.floor(focused / pageSize);
    if (page !== target) page = target;
  });

  let totalRows = $derived(rows.length);
  let totalPages = $derived(Math.max(1, Math.ceil(totalRows / pageSize)));
  let pageRows = $derived(rows.slice(page * pageSize, page * pageSize + pageSize));
  let rangeStart = $derived(totalRows === 0 ? 0 : page * pageSize + 1);
  let rangeEnd = $derived(Math.min(totalRows, (page + 1) * pageSize));

  function prev() {
    if (page > 0) page -= 1;
  }
  function next() {
    if (page < totalPages - 1) page += 1;
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

<div class="flex min-h-0 flex-1 flex-col">
  {#if totalRows === 0}
    <div class="m-auto flex flex-col items-center gap-1 px-6 py-8 text-center">
      <p class="text-base font-medium text-text">0 rows</p>
      <p class="text-sm text-text-muted">The query ran fine — it just matched nothing.</p>
    </div>
  {:else}
    <div class="min-h-0 flex-1 overflow-auto">
      <table class="w-full min-w-max border-collapse text-left">
        <thead class="sticky top-0 z-10">
          <tr>
            {#each columns as column (column)}
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
          {#each pageRows as row, i (i)}
            {@const index = page * pageSize + i}
            {@const chosen = selectable && picked.has(index)}
            <tr
              class="transition-colors {chosen
                ? 'bg-accent/15'
                : 'hover:bg-surface-overlay'} {selectable ? 'cursor-pointer' : ''}"
              aria-selected={selectable ? chosen : undefined}
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
                {@const value = edits ? edits.value(index, column, cell) : cell}
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
                  class="border-b border-border/60 {open
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
                      class="box-border block w-full border-0 bg-surface px-3 py-2 font-mono text-base leading-5 text-text ring-2 ring-inset ring-accent focus:outline-none"
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
        </tbody>
      </table>
    </div>

    <div
      class="flex shrink-0 items-center justify-between gap-3 border-t border-border px-3 py-2 text-xs text-text-muted"
    >
      <span class="flex min-w-0 items-center gap-2">
        <span class="shrink-0 tabular-nums">
          Rows {rangeStart}–{rangeEnd} of {totalRows}
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

      <div class="flex shrink-0 items-center gap-2">
        <span class="tabular-nums">Page {page + 1} of {totalPages}</span>
        <button
          type="button"
          class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
          onclick={prev}
          disabled={page === 0}
        >
          Prev
        </button>
        <button
          type="button"
          class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:border-border disabled:bg-transparent disabled:text-text-subtle"
          onclick={next}
          disabled={page >= totalPages - 1}
        >
          Next
        </button>
      </div>
    </div>
  {/if}
</div>
