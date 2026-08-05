<script lang="ts">
  // App shell: view switching between the Connections manager (issues #5/#6)
  // and the Editor (issue #7), plus the status bar. Owns connectedProfiles —
  // the one piece of state both views need — and refreshes it on entry to
  // the Editor view so a Profile connected moments ago shows up there.
  import { onMount } from "svelte";
  import ConnectionsView from "./lib/ConnectionsView.svelte";
  import EditorView from "./lib/EditorView.svelte";
  import { ConnectedProfiles } from "../wailsjs/go/app/App";

  type View = "connections" | "editor";

  let view = $state<View>("connections");
  let connectedProfiles = $state<string[]>([]);

  let statusText = $derived(
    connectedProfiles.length === 0
      ? "go-db · not connected"
      : `go-db · connected: ${connectedProfiles.join(", ")}`,
  );

  async function refreshConnections() {
    connectedProfiles = await ConnectedProfiles();
  }

  onMount(() => {
    refreshConnections();
  });

  function switchView(next: View) {
    view = next;
    if (next === "editor") refreshConnections();
  }
</script>

<div class="flex h-screen w-screen flex-col bg-surface font-sans text-base text-text">
  <header class="flex h-10 shrink-0 items-center gap-4 border-b border-border bg-surface-raised px-3">
    <span class="text-sm font-semibold text-text">go-db</span>
    <div class="flex gap-0.5 rounded-control border border-border bg-surface p-0.5 text-xs">
      <button
        type="button"
        class="rounded-control px-3 py-1 transition-colors {view === 'connections'
          ? 'bg-accent text-white'
          : 'text-text-muted hover:text-text'}"
        onclick={() => switchView("connections")}
      >
        Connections
      </button>
      <button
        type="button"
        class="rounded-control px-3 py-1 transition-colors {view === 'editor'
          ? 'bg-accent text-white'
          : 'text-text-muted hover:text-text'}"
        onclick={() => switchView("editor")}
      >
        Editor
      </button>
    </div>
  </header>

  <div class="flex flex-1 overflow-hidden">
    {#if view === "connections"}
      <ConnectionsView onConnectionsChanged={refreshConnections} />
    {:else}
      <EditorView {connectedProfiles} onGoToConnections={() => switchView("connections")} />
    {/if}
  </div>

  <footer
    class="flex h-8 shrink-0 items-center justify-between border-t border-border bg-surface-raised px-3 text-xs text-text-muted"
  >
    <span>{statusText}</span>
    <span>— MB</span>
  </footer>
</div>
