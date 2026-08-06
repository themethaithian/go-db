<script lang="ts">
  // Renders one QueryOK result: columns/rows from the backend, paginated
  // client-side. Holds no knowledge of how the query ran — App/EditorView
  // decide when to show it.
  let {
    columns,
    rows,
    truncated,
  }: {
    columns: string[];
    rows: (string | null)[][];
    truncated: boolean;
  } = $props();

  const pageSize = 50;
  let page = $state(0);

  // A new result set always starts on page 1, whatever page the previous
  // one was left on.
  $effect(() => {
    rows;
    page = 0;
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
        <tbody class="font-mono text-base">
          {#each pageRows as row, i (i)}
            <tr class="transition-colors hover:bg-surface-overlay">
              {#each row as cell, j (j)}
                <td class="border-b border-border/60 px-3 py-2 text-text tabular-nums whitespace-nowrap">
                  {#if cell === null}
                    <span class="text-text-subtle italic">NULL</span>
                  {:else}
                    {cell}
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
