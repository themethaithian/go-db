<script lang="ts">
  // Renders one QueryOK result: columns/rows from the backend, paginated
  // client-side. Holds no knowledge of how the query ran — App/EditorView
  // decide when to show it.
  //
  // Rows can also be picked, for views that show the picked rows on their own
  // (the Explorer's record pane). The selection is not this table's state: it
  // is passed in as indices into `rows` and reported back on click, so the one
  // component that renders the picked rows and the one that highlights them
  // cannot disagree. Whether a click adds to the selection or replaces it is
  // that same caller's business too — this table only says which modifier the
  // mouse was holding. Leaving both props off — as the Editor does — leaves
  // rows unclickable and the markup unchanged.
  //
  // Unsaved edits show here too, though they are never made here: `pending`
  // maps a cell to the value typed over it in the record pane, and the grid
  // draws that value in the accent until it is saved or reverted. The grid
  // stays a reading surface — its click already means "pick this row", and an
  // editor opened on a double-click would have to dismantle a comparison to
  // get at the cell under it.
  let {
    columns,
    rows,
    truncated,
    selectedIndices = [],
    pending,
    onRowClick,
  }: {
    columns: string[];
    rows: (string | null)[][];
    truncated: boolean;
    /** Indices into `rows` of the picked rows, newest last. Ignored without onRowClick. */
    selectedIndices?: number[];
    /** Unsaved values by `row:column` index, from the pane that edited them. */
    pending?: Map<string, string | null>;
    /** Given, rows become clickable and report their index into `rows`. */
    onRowClick?: (index: number, options: { additive: boolean }) => void;
  } = $props();

  const pageSize = 50;
  let page = $state(0);

  let selectable = $derived(onRowClick !== undefined);
  let picked = $derived(new Set(selectedIndices));

  // The row the selection last moved to — the one the page and the scroll
  // follow. Earlier picks stay highlighted where they are: adding a row to a
  // comparison should not yank the grid back to the first one.
  let focused = $derived(
    selectedIndices.length === 0 ? null : selectedIndices[selectedIndices.length - 1],
  );

  // A new result set always starts on page 1, whatever page the previous
  // one was left on.
  $effect(() => {
    rows;
    page = 0;
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
          and Escape, handled where that pane lives.
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
                : (event) => onRowClick(index, { additive: event.metaKey || event.ctrlKey })}
              use:reveal={index === focused}
            >
              {#each row as cell, j (j)}
                {@const edited = pending?.has(`${index}:${j}`) ?? false}
                {@const value = edited ? (pending?.get(`${index}:${j}`) ?? null) : cell}
                <td
                  class="border-b border-border/60 px-3 py-2 tabular-nums whitespace-nowrap {edited
                    ? 'bg-accent/10 text-accent'
                    : 'text-text'}"
                  title={edited ? "Edited — not saved yet" : undefined}
                >
                  {#if value === null}
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
      <span class="flex items-center gap-2">
        <span class="tabular-nums">
          Rows {rangeStart}–{rangeEnd} of {totalRows}
        </span>
        {#if truncated}
          <span
            class="rounded-full border border-warning/40 bg-warning/10 px-1.5 py-px font-medium text-warning"
            title="The backend capped this result set"
          >
            truncated
          </span>
        {/if}
      </span>

      <div class="flex items-center gap-2">
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
