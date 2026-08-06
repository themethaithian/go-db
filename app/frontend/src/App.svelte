<script lang="ts">
  // App shell: view switching between the Connections manager (issues #5/#6)
  // and the Editor (issue #7), plus the status bar. Owns connectedProfiles —
  // the one piece of state both views need — and refreshes it on entry to
  // the Editor view so a Profile connected moments ago shows up there.
  import { onMount } from "svelte";
  import ConnectionsView from "./lib/ConnectionsView.svelte";
  import EditorView from "./lib/EditorView.svelte";
  import ApprovalConsole from "./lib/ApprovalConsole.svelte";
  import { ConnectedProfiles, ListPendingApprovals, MemoryUsageMB, Ready } from "../wailsjs/go/app/App";
  import type { guard } from "../wailsjs/go/models";

  type View = "connections" | "editor" | "approvals";

  // Poll interval for the Approval Console: how often App.svelte refreshes
  // pendingApprovals so the nav badge stays live from every view.
  const APPROVALS_POLL_MS = 2000;

  // Poll interval for the status bar's live RAM gauge (CLAUDE.md budget:
  // idle RAM < 150 MB). Same cadence as the approvals poll — cheap enough
  // to run continuously, proving the budget rather than just asserting it.
  const MEMORY_POLL_MS = 2000;

  // Idle RAM budget from CLAUDE.md / README's "Performance budget as a
  // feature" section. The gauge renders in the warning token color once it
  // crosses this line — the budget keeping itself honest.
  const MEMORY_BUDGET_MB = 150;

  let view = $state<View>("connections");
  let connectedProfiles = $state<string[]>([]);
  let pendingApprovals = $state<guard.Waiting[]>([]);
  let memoryMB = $state<number | null>(null);

  let statusText = $derived(
    connectedProfiles.length === 0
      ? "Not connected"
      : `Connected · ${connectedProfiles.join(", ")}`,
  );

  async function refreshConnections() {
    connectedProfiles = await ConnectedProfiles();
  }

  async function refreshApprovals() {
    pendingApprovals = await ListPendingApprovals();
  }

  async function refreshMemory() {
    memoryMB = await MemoryUsageMB();
  }

  onMount(() => {
    // Tells the shell the frontend has mounted, so it can log
    // launch-to-usable once to stderr (CLAUDE.md budget: < 2 s).
    Ready();
    refreshConnections();
    refreshApprovals();
    refreshMemory();
    const approvalsInterval = setInterval(refreshApprovals, APPROVALS_POLL_MS);
    const memoryInterval = setInterval(refreshMemory, MEMORY_POLL_MS);
    return () => {
      clearInterval(approvalsInterval);
      clearInterval(memoryInterval);
    };
  });

  function switchView(next: View) {
    view = next;
    if (next === "editor") refreshConnections();
    if (next === "approvals") refreshApprovals();
  }
</script>

<div class="flex h-screen w-screen flex-col bg-surface font-sans text-base text-text">
  <header class="flex h-11 shrink-0 items-center gap-5 border-b border-border bg-surface-panel px-3">
    <span class="flex items-center gap-2 pl-1">
      <svg
        class="h-4 w-4 text-accent"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.4"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <ellipse cx="8" cy="3.75" rx="5.25" ry="2.25" />
        <path d="M2.75 3.75v8.5c0 1.24 2.35 2.25 5.25 2.25s5.25-1.01 5.25-2.25v-8.5" />
        <path d="M2.75 8c0 1.24 2.35 2.25 5.25 2.25s5.25-1.01 5.25-2.25" />
      </svg>
      <span class="text-md font-semibold tracking-tight text-text">go-db</span>
    </span>

    <nav class="flex items-center gap-0.5 rounded-control border border-border bg-surface p-0.5 text-sm">
      <button
        type="button"
        class="rounded-control px-3 py-1 font-medium transition-colors {view === 'connections'
          ? 'bg-accent text-white'
          : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
        onclick={() => switchView("connections")}
      >
        Connections
      </button>
      <button
        type="button"
        class="rounded-control px-3 py-1 font-medium transition-colors {view === 'editor'
          ? 'bg-accent text-white'
          : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
        onclick={() => switchView("editor")}
      >
        Editor
      </button>
      <button
        type="button"
        class="flex items-center gap-1.5 rounded-control px-3 py-1 font-medium transition-colors {view ===
        'approvals'
          ? 'bg-accent text-white'
          : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
        onclick={() => switchView("approvals")}
      >
        Approvals
        {#if pendingApprovals.length > 0}
          <span
            class="flex h-4 min-w-4 items-center justify-center rounded-full bg-danger px-1 text-xs leading-none font-semibold tabular-nums text-white"
          >
            {pendingApprovals.length}
          </span>
        {/if}
      </button>
    </nav>
  </header>

  <div class="flex flex-1 overflow-hidden">
    {#if view === "connections"}
      <ConnectionsView onConnectionsChanged={refreshConnections} />
    {:else if view === "editor"}
      <EditorView {connectedProfiles} onGoToConnections={() => switchView("connections")} />
    {:else}
      <ApprovalConsole entries={pendingApprovals} onRefresh={refreshApprovals} />
    {/if}
  </div>

  <footer
    class="flex h-7 shrink-0 items-center justify-between border-t border-border bg-surface-panel px-3 text-xs text-text-muted"
  >
    <span class="flex items-center gap-2">
      <span
        class="h-1.5 w-1.5 shrink-0 rounded-full {connectedProfiles.length > 0
          ? 'bg-success'
          : 'bg-text-subtle'}"
      ></span>
      <span>{statusText}</span>
      {#if pendingApprovals.length > 0}
        <span class="text-warning">
          · {pendingApprovals.length} awaiting approval
        </span>
      {/if}
    </span>
    <span
      class="tabular-nums {memoryMB !== null && memoryMB > MEMORY_BUDGET_MB ? 'text-warning' : ''}"
      title={memoryMB !== null && memoryMB > MEMORY_BUDGET_MB
        ? `over the ${MEMORY_BUDGET_MB} MB idle budget`
        : `idle budget ${MEMORY_BUDGET_MB} MB`}
    >
      {memoryMB === null ? "— MB" : `${Math.round(memoryMB)} MB`}
    </span>
  </footer>
</div>
