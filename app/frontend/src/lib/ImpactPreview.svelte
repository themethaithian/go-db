<script lang="ts">
  // Impact Preview rendering (CONTEXT.md): the advisory affected-row count and
  // sample table, or the "No preview available" callout when the statement
  // has none. Shared between Inline Confirm and the Approval Console so both
  // policies show the same preview the same way.
  import type { guard } from "../../wailsjs/go/models";

  let { preview }: { preview: guard.Preview } = $props();

  function cellText(cell: string | null): string {
    return cell ?? "";
  }

  // guard.Preview's generated TS type says Rows is string[][]; the wire
  // format is really [][]* string, so a cell can be null (NULL). Cast once
  // here rather than at every use, matching ResultsTable's same workaround.
  let sampleRows = $derived((preview.rows ?? []) as (string | null)[][]);
</script>

{#if preview.available}
  <div class="flex flex-col gap-2">
    <p class="text-base">
      <span class="font-semibold text-text">
        ≈ {preview.count === 1 ? "1 row" : `${preview.count} rows`} affected
      </span>
      <span class="text-text-muted"> — advisory estimate; data may change before this runs.</span>
    </p>

    {#if preview.columns && preview.columns.length > 0 && sampleRows.length > 0}
      <div class="overflow-auto rounded-control border border-border">
        <table class="w-full min-w-max border-collapse text-left">
          <thead>
            <tr>
              {#each preview.columns as column (column)}
                <th
                  class="border-b border-border bg-surface-raised px-2.5 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase whitespace-nowrap"
                >
                  {column}
                </th>
              {/each}
            </tr>
          </thead>
          <tbody class="font-mono text-sm">
            {#each sampleRows as row, i (i)}
              <tr class="border-b border-border/60 last:border-b-0">
                {#each row as cell, j (j)}
                  <td class="px-2.5 py-1.5 text-text tabular-nums whitespace-nowrap">
                    {#if cell === null}
                      <span class="text-text-subtle italic">NULL</span>
                    {:else}
                      {cellText(cell)}
                    {/if}
                  </td>
                {/each}
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="text-xs text-text-subtle">A sample of the rows this would touch.</p>
    {/if}
  </div>
{:else}
  <div class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 text-base text-danger">
    <span class="font-semibold">No preview available</span> — {preview.reason}
  </div>
{/if}
