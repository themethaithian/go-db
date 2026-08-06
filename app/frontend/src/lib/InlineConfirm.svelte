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

<section
  class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-warning/40 bg-surface-panel shadow-panel"
>
  <header class="flex shrink-0 items-start gap-2.5 border-b border-warning/25 bg-warning/10 px-3 py-2.5">
    <svg
      class="mt-px h-4 w-4 shrink-0 text-warning"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      stroke-width="1.4"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M8 2.75 14.25 13.5H1.75L8 2.75Z" />
      <path d="M8 6.75v3M8 11.75h.01" />
    </svg>
    <div class="min-w-0">
      <p class="text-base font-semibold text-warning">This query will modify data</p>
      <p class="mt-0.5 text-sm text-text-muted">{reason}</p>
    </div>
  </header>

  <div class="min-h-0 flex-1 overflow-auto p-3">
    <ImpactPreview {preview} />
  </div>

  <footer class="flex shrink-0 items-center justify-end gap-2 border-t border-border px-3 py-2.5">
    <span class="mr-auto text-xs text-text-subtle">Esc cancels · ⏎ confirms</span>
    <button
      type="button"
      class="inline-flex h-8 items-center rounded-control border border-border bg-surface-raised px-3 text-base font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-50"
      disabled={busy}
      onclick={cancel}
    >
      {inFlight === "cancel" ? "Cancelling…" : "Cancel"}
    </button>
    <button
      type="button"
      bind:this={confirmButton}
      class="inline-flex h-8 items-center rounded-control bg-danger px-3 text-base font-medium text-white transition-colors hover:bg-danger/85 disabled:cursor-not-allowed disabled:opacity-50"
      disabled={busy}
      onclick={confirm}
    >
      {inFlight === "confirm" ? "Running…" : "Confirm & run"}
    </button>
  </footer>
</section>
