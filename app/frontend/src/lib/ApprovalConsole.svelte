<script lang="ts">
  // Approval Console (CONTEXT.md): the gate's policy for AI-originated
  // mutating queries. App.svelte polls ListPendingApprovals every 2s and
  // passes the list down; this view only renders it and asks for an
  // immediate refresh after a decision, so the console feels responsive
  // instead of waiting out the next poll tick. The list is already
  // AI-origin-only and oldest-first — the backend filters and sorts it.
  import ApprovalCard from "./ApprovalCard.svelte";
  import type { guard } from "../../wailsjs/go/models";

  let { entries, onRefresh }: { entries: guard.Waiting[]; onRefresh: () => void } = $props();
</script>

<main class="flex flex-1 flex-col overflow-y-auto bg-surface">
  {#if entries.length === 0}
    <div class="m-auto flex max-w-sm flex-col items-center gap-3 px-6 py-10 text-center">
      <span
        class="flex h-11 w-11 items-center justify-center rounded-panel border border-border bg-surface-panel text-text-subtle"
      >
        <svg
          class="h-5 w-5"
          viewBox="0 0 20 20"
          fill="none"
          stroke="currentColor"
          stroke-width="1.4"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M10 2.25 16.25 4.5v5c0 3.5-2.5 6.6-6.25 8.25C6.25 16.1 3.75 13 3.75 9.5v-5L10 2.25Z" />
          <path d="m7.5 9.75 1.75 1.75 3.25-3.5" />
        </svg>
      </span>
      <p class="text-lg font-medium text-text">Nothing waiting for approval</p>
      <p class="text-base leading-relaxed text-text-muted">
        Mutating queries an AI agent sends through the MCP server pause here until you approve or reject them.
      </p>
    </div>
  {:else}
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4 px-6 py-6">
      <div class="flex items-baseline justify-between gap-3">
        <h2 class="text-lg font-semibold text-text">Waiting for approval</h2>
        <span class="text-sm text-text-muted tabular-nums">
          {entries.length === 1 ? "1 query" : `${entries.length} queries`}
        </span>
      </div>
      {#each entries as entry (entry.id)}
        <ApprovalCard {entry} onDecided={onRefresh} />
      {/each}
    </div>
  {/if}
</main>
