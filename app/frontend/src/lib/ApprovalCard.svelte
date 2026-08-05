<script lang="ts">
  // One Approval Console entry: an AI-originated mutation waiting for a human
  // (CONTEXT.md). Owns its own busy/ack-message state — the list re-fetches
  // every 2s and passes a fresh `entry` down, but this component instance
  // persists across polls (App.svelte keys the each-block by entry.id), so
  // in-flight state survives a poll landing mid-click.
  import { ApprovePending, RejectPending } from "../../wailsjs/go/app/App";
  import type { guard, service } from "../../wailsjs/go/models";
  import ImpactPreview from "./ImpactPreview.svelte";

  let { entry, onDecided }: { entry: guard.Waiting; onDecided: () => void } = $props();

  let inFlight = $state<"approve" | "reject" | null>(null);
  let busy = $derived(inFlight !== null);
  let ackMessage = $state<string | null>(null);

  // The countdown is computed client-side from RemainingMillis captured at
  // fetch, ticking locally rather than re-deriving from a server clock every
  // second. Re-baselined whenever a fresh entry arrives (every poll), so it
  // never drifts far from what the gate is actually measuring.
  let baselineRemaining = $derived(entry.remaining_ms);
  let baselineAt = $derived.by(() => {
    entry.remaining_ms; // dependency: re-stamp the baseline whenever a fresh entry lands
    return Date.now();
  });

  let now = $state(Date.now());
  $effect(() => {
    const interval = setInterval(() => {
      now = Date.now();
    }, 1000);
    return () => clearInterval(interval);
  });

  let remainingMs = $derived(Math.max(baselineRemaining - (now - baselineAt), 0));
  let countdownText = $derived(formatCountdown(remainingMs));

  function formatCountdown(ms: number): string {
    if (ms <= 0) return "expiring…";
    const totalSeconds = Math.ceil(ms / 1000);
    const minutes = Math.floor(totalSeconds / 60);
    const seconds = totalSeconds % 60;
    return `auto-rejects in ${minutes}:${seconds.toString().padStart(2, "0")}`;
  }

  function requestedAtText(): string {
    const parsed = new Date(entry.requested_at as unknown as string);
    if (Number.isNaN(parsed.getTime())) return "";
    return parsed.toLocaleTimeString();
  }

  async function decide(action: "approve" | "reject") {
    if (busy) return;
    inFlight = action;
    ackMessage = null;
    try {
      const ack: service.ApprovalAck =
        action === "approve" ? await ApprovePending(entry.id) : await RejectPending(entry.id);
      if (!ack.ok) {
        ackMessage = ack.message;
      }
    } finally {
      inFlight = null;
      onDecided();
    }
  }
</script>

<div class="flex flex-col gap-3 rounded-panel border border-warning/40 bg-surface-raised p-4">
  <div class="flex flex-wrap items-center gap-2">
    <span class="rounded-full border border-accent/40 bg-accent/10 px-2 py-0.5 text-xs font-medium text-accent">
      {entry.profile}
    </span>
    <span
      class="rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-xs font-medium text-warning"
      title={entry.classification.reason}
    >
      MUTATION
    </span>
    <span class="text-xs text-text-muted">{entry.classification.reason}</span>
    <div class="flex-1"></div>
    <span class="text-xs font-medium text-warning">{countdownText}</span>
  </div>

  <pre
    class="max-h-40 overflow-auto rounded-control border border-border bg-surface p-2 font-mono text-xs whitespace-pre-wrap break-words text-text">{entry.sql}</pre>

  <ImpactPreview preview={entry.preview} />

  <div class="flex items-center justify-between gap-3">
    <span class="text-xs text-text-muted">Requested {requestedAtText()}</span>

    <div class="flex items-center gap-2">
      {#if ackMessage}
        <span class="text-xs text-danger">{ackMessage}</span>
      {/if}
      <button
        type="button"
        class="rounded-control border border-border px-4 py-1.5 text-sm font-medium text-text transition-colors hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-50"
        disabled={busy}
        onclick={() => decide("reject")}
      >
        {inFlight === "reject" ? "Rejecting…" : "Reject"}
      </button>
      <button
        type="button"
        class="rounded-control bg-danger px-4 py-1.5 text-sm font-medium text-white transition-colors hover:bg-danger/90 disabled:cursor-not-allowed disabled:opacity-50"
        disabled={busy}
        onclick={() => decide("approve")}
      >
        {inFlight === "approve" ? "Approving…" : "Approve"}
      </button>
    </div>
  </div>
</div>
