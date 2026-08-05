<script lang="ts">
  // Inline Confirm: the gate's policy for a human's mutating query (CONTEXT.md).
  // Renders in place of the results panel while one mutation is withheld,
  // carries its Impact Preview, and is the whole of "one extra keypress" —
  // the Confirm button is focused on mount so a bare Enter runs it, and Esc
  // cancels. Both keys are wired for exactly this component's lifetime: the
  // listener is added in onMount and removed in onDestroy, so it only ever
  // exists while the panel is on screen.
  import { onMount, onDestroy } from "svelte";
  import { ConfirmPending, CancelPending } from "../../wailsjs/go/app/App";
  import type { guard, service } from "../../wailsjs/go/models";

  let {
    reason,
    preview,
    pendingId,
    onResolved,
  }: {
    reason: string;
    preview: guard.Preview;
    pendingId: string;
    onResolved: (result: service.QueryResult) => void;
  } = $props();

  // Which action is in flight, if any. Tracked separately from a plain
  // boolean so the two buttons can each show their own busy label without
  // guessing which one was pressed.
  let inFlight = $state<"confirm" | "cancel" | null>(null);
  let busy = $derived(inFlight !== null);

  let confirmButton: HTMLButtonElement | undefined;

  onMount(() => {
    confirmButton?.focus();
    window.addEventListener("keydown", handleKeydown);
  });

  onDestroy(() => {
    window.removeEventListener("keydown", handleKeydown);
  });

  function handleKeydown(event: KeyboardEvent) {
    if (event.key === "Escape") {
      event.preventDefault();
      cancel();
    }
  }

  async function confirm() {
    if (busy) return;
    inFlight = "confirm";
    try {
      onResolved(await ConfirmPending(pendingId));
    } finally {
      inFlight = null;
    }
  }

  async function cancel() {
    if (busy) return;
    inFlight = "cancel";
    try {
      onResolved(await CancelPending(pendingId));
    } finally {
      inFlight = null;
    }
  }

  function cellText(cell: string | null): string {
    return cell ?? "";
  }

  // guard.Preview's generated TS type says Rows is string[][]; the wire
  // format is really [][]* string, so a cell can be null (NULL). Cast once
  // here rather than at every use, matching ResultsTable's same workaround.
  let sampleRows = $derived((preview.rows ?? []) as (string | null)[][]);
</script>

<div class="flex h-full flex-col gap-3 overflow-auto rounded-panel border border-warning/40 bg-warning/10 p-4">
  <div>
    <p class="text-sm font-semibold text-warning">This query will modify data</p>
    <p class="mt-0.5 text-xs text-warning/80">{reason}</p>
  </div>

  {#if preview.available}
    <div class="flex flex-col gap-2">
      <p class="text-sm text-text">
        <span class="font-medium">≈ {preview.count === 1 ? "1 row" : `${preview.count} rows`} affected</span>
        <span class="text-text-muted"> — advisory estimate; data may change before you confirm.</span>
      </p>

      {#if preview.columns && preview.columns.length > 0 && sampleRows.length > 0}
        <div class="overflow-auto rounded-control border border-border">
          <table class="w-full min-w-max border-collapse font-mono text-xs">
            <thead class="bg-surface-raised">
              <tr>
                {#each preview.columns as column (column)}
                  <th class="border-b border-border px-2 py-1 text-left font-medium text-text-muted whitespace-nowrap">
                    {column}
                  </th>
                {/each}
              </tr>
            </thead>
            <tbody>
              {#each sampleRows as row, i (i)}
                <tr class="odd:bg-surface even:bg-surface-raised/40">
                  {#each row as cell, j (j)}
                    <td class="border-b border-border px-2 py-1 whitespace-nowrap text-text">
                      {#if cell === null}
                        <span class="italic text-text-muted">NULL</span>
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
      {/if}
    </div>
  {:else}
    <div class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">
      <span class="font-semibold">No preview available</span> — {preview.reason}
    </div>
  {/if}

  <div class="mt-auto flex shrink-0 items-center justify-end gap-2 pt-1">
    <button
      type="button"
      class="rounded-control border border-border px-4 py-1.5 text-sm font-medium text-text transition-colors hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-50"
      disabled={busy}
      onclick={cancel}
    >
      {inFlight === "cancel" ? "Running…" : "Cancel"}
    </button>
    <button
      type="button"
      bind:this={confirmButton}
      class="rounded-control bg-danger px-4 py-1.5 text-sm font-medium text-white transition-colors hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-50"
      disabled={busy}
      onclick={confirm}
    >
      {inFlight === "confirm" ? "Running…" : "Confirm & run"}
    </button>
  </div>
</div>
