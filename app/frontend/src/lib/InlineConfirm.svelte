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
  import ImpactPreview from "./ImpactPreview.svelte";

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

</script>

<div class="flex h-full flex-col gap-3 overflow-auto rounded-panel border border-warning/40 bg-warning/10 p-4">
  <div>
    <p class="text-sm font-semibold text-warning">This query will modify data</p>
    <p class="mt-0.5 text-xs text-warning/80">{reason}</p>
  </div>

  <ImpactPreview {preview} />

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
