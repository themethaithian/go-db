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

<div class="flex h-full flex-col gap-2">
  {#if totalRows === 0}
    <p class="text-sm text-text-muted">0 rows.</p>
  {:else}
    <div class="flex-1 overflow-auto rounded-panel border border-border">
      <table class="w-full min-w-max border-collapse font-mono text-sm">
        <thead class="sticky top-0 z-10 bg-surface-raised">
          <tr>
            {#each columns as column (column)}
              <th class="border-b border-border px-3 py-2 text-left font-medium text-text-muted whitespace-nowrap">
                {column}
              </th>
            {/each}
          </tr>
        </thead>
        <tbody>
          {#each pageRows as row, i (i)}
            <tr class="odd:bg-surface even:bg-surface-raised/40 hover:bg-surface-overlay">
              {#each row as cell, j (j)}
                <td class="border-b border-border px-3 py-1.5 whitespace-nowrap text-text">
                  {#if cell === null}
                    <span class="italic text-text-muted">NULL</span>
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

    <div class="flex shrink-0 items-center justify-between text-xs text-text-muted">
      <span>
        rows {rangeStart}–{rangeEnd} of {totalRows}
        {#if truncated}
          · showing first {totalRows} rows (truncated)
        {/if}
      </span>
      <div class="flex gap-2">
        <button
          type="button"
          class="rounded-control border border-border px-2 py-1 transition-colors hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-40"
          onclick={prev}
          disabled={page === 0}
        >
          Prev
        </button>
        <button
          type="button"
          class="rounded-control border border-border px-2 py-1 transition-colors hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-40"
          onclick={next}
          disabled={page >= totalPages - 1}
        >
          Next
        </button>
      </div>
    </div>
  {/if}
</div>
