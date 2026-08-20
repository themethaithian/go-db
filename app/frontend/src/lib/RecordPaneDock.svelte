<script lang="ts">
  // The seam between a results grid and the record pane it feeds: the
  // splitter that divides the two, the docked pane itself, and the pop-out
  // that shows the same content again at dialog size. Explorer and Editor
  // each pick their own rows their own way — Explorer also walks them with
  // arrow keys, browsing does not need to — but once a row is picked, what
  // happens next is identical in both, which is what lives here.
  //
  // Popping out does not fork the state: the modal renders the same
  // RecordPane with the same props, so the columns, the diff tinting, and the
  // copy buttons all work exactly as they do docked. Only the close button's
  // meaning changes between the two instances — docked, it clears the whole
  // selection; in the popup, it closes the popup and leaves the selection
  // (and the docked pane) exactly as it was. That difference is wired here,
  // not in RecordPane, which only ever knows "close" as a callback.
  //
  // Nothing is rendered — and no Esc key is claimed — unless there are picked
  // rows: a caller with an empty selection can mount this unconditionally and
  // get a no-op.
  import RecordPane from "./RecordPane.svelte";
  import Splitter from "./Splitter.svelte";
  import { clamp } from "./layout.svelte";
  import type { RowEdits } from "./edits.svelte";

  let {
    columns,
    rows,
    indices,
    totalRows,
    edits = null,
    editable = false,
    readOnlyHint = null,
    bodyWidth,
    gridMinPx,
    splitterPx,
    detailWidth,
    detailMin,
    detailMax,
    onResizeDetail,
    onClose,
  }: {
    columns: string[];
    /** The picked rows' cells, in the order they were picked. */
    rows: (string | null)[][];
    /** Each picked row's index into the grid, aligned with `rows`. */
    indices: number[];
    /** How many rows the grid is showing, for the single-row header. */
    totalRows: number;
    /** The result set's pending edits — passed through to both panes untouched. */
    edits?: RowEdits | null;
    /** Whether these rows can be edited at all — the caller's verdict. */
    editable?: boolean;
    /** Why they cannot be, when they cannot and it is worth saying. */
    readOnlyHint?: string | null;
    /** Width of the grid + pane strip, so the splitter knows its travel. */
    bodyWidth: number;
    /** Floor under the grid, so dragging the splitter cannot squeeze it out. */
    gridMinPx: number;
    /** The splitter's own footprint, subtracted from bodyWidth. */
    splitterPx: number;
    /** Current width of the docked pane, in pixels. */
    detailWidth: number;
    detailMin: number;
    detailMax: number;
    /** Reports the pane's new width, already clamped to [detailMin, detailMax]. */
    onResizeDetail: (next: number) => void;
    /** Clears the whole selection — the docked pane's own close button and Esc. */
    onClose: () => void;
  } = $props();

  // The pop-out: its own state, not the selection's — closing it must leave
  // the docked pane exactly as it was, and a selection change (a new fetch,
  // an Esc) unmounts this component entirely, which is what closes the popup
  // along with everything else.
  let expanded = $state(false);

  // Esc means two different things depending on what is open, and only one
  // of them at a time: with the popup open, Esc closes the popup and leaves
  // the selection untouched; otherwise it is the selection's own Esc, taken
  // here so both callers get it for free.
  function handleWindowKey(event: KeyboardEvent) {
    if (event.key !== "Escape") return;
    if (expanded) {
      expanded = false;
      return;
    }
    if (indices.length === 0) return;
    onClose();
  }

  // The splitter reports the width of the pane before it — the grid — so the
  // record pane is whatever the strip has left over.
  function resizeDetail(gridWidth: number) {
    onResizeDetail(clamp(bodyWidth - splitterPx - gridWidth, detailMin, detailMax));
  }
</script>

<svelte:window onkeydown={handleWindowKey} />

{#if indices.length > 0}
  <Splitter
    orientation="vertical"
    value={bodyWidth - splitterPx - detailWidth}
    min={gridMinPx}
    max={Math.max(gridMinPx, bodyWidth - splitterPx - detailMin)}
    label="Resize the row pane"
    onChange={resizeDetail}
  />

  <!-- The picked rows, read down instead of across: the shape you want the
       moment a table has more columns than the window has width — and, with
       more than one row picked, side by side.

       isolate draws the same stacking-context boundary here as ResultsTable
       draws around itself: the comparison header's own sticky, z-indexed
       cells (RecordPane's corner and per-row index badges) stay contained to
       this pane instead of being free to out-rank whatever sits beside it —
       the grid included — in some shared ancestor stacking context. Neither
       side should have to stay bug-free for the other to render correctly;
       each contains its own. -->
  <aside
    class="isolate flex shrink-0 flex-col overflow-hidden border-l border-border bg-surface"
    style="width: {detailWidth}px"
  >
    <RecordPane
      {columns}
      {rows}
      {indices}
      {totalRows}
      {edits}
      {editable}
      {readOnlyHint}
      onExpand={() => (expanded = true)}
      {onClose}
    />
  </aside>
{/if}

{#if expanded}
  <!-- A DOM overlay, not an OS window — Wails v2 is single-window. Centered
       and generously sized so comparing several rows reads as comfortably as
       it can, though the pane's own horizontal scroll (the same one the
       docked strip uses) is still there once the columns outrun even this. -->
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div
    class="fixed inset-0 z-40 flex items-center justify-center bg-surface/80 p-8"
    role="presentation"
    onclick={() => (expanded = false)}
  >
    <div
      class="flex h-4/5 w-4/5 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
      role="dialog"
      tabindex="-1"
      aria-modal="true"
      aria-label={indices.length > 1 ? "Compare rows" : "Row detail"}
      onclick={(event) => event.stopPropagation()}
    >
      <RecordPane
        {columns}
        {rows}
        {indices}
        {totalRows}
        {edits}
        {editable}
        {readOnlyHint}
        onClose={() => (expanded = false)}
      />
    </div>
  </div>
{/if}
