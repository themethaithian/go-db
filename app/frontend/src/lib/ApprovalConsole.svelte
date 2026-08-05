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

<main class="flex flex-1 flex-col overflow-y-auto p-6">
  {#if entries.length === 0}
    <div class="flex flex-1 items-center justify-center">
      <span class="text-sm text-text-muted">No queries waiting for approval</span>
    </div>
  {:else}
    <div class="mx-auto flex w-full max-w-3xl flex-col gap-4">
      {#each entries as entry (entry.id)}
        <ApprovalCard {entry} onDecided={onRefresh} />
      {/each}
    </div>
  {/if}
</main>
