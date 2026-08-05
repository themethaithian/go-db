<script lang="ts">
  import type { db } from "../../wailsjs/go/models";

  // Sidebar: lists saved Profiles and lets the user pick one to edit or
  // start a new one. Holds no state of its own — App.svelte owns the list
  // and the current selection.
  let {
    profiles,
    selectedName,
    onSelect,
    onCreate,
  }: {
    profiles: db.Profile[];
    selectedName: string | null;
    onSelect: (name: string) => void;
    onCreate: () => void;
  } = $props();

  function subtitle(profile: db.Profile): string {
    const location = `${profile.Host}:${profile.Port}`;
    return profile.Database ? `${location} / ${profile.Database}` : location;
  }
</script>

<aside class="flex w-64 shrink-0 flex-col border-r border-border bg-surface-raised">
  <div class="flex items-center justify-between border-b border-border px-4 py-3">
    <h2 class="text-sm font-medium text-text">Profiles</h2>
    <button
      type="button"
      class="flex h-6 w-6 items-center justify-center rounded-control text-text-muted transition-colors hover:bg-surface-overlay hover:text-text"
      onclick={onCreate}
      aria-label="New profile"
    >
      +
    </button>
  </div>

  <nav class="flex-1 overflow-y-auto">
    {#if profiles.length === 0}
      <p class="px-4 py-3 text-xs text-text-muted">No profiles yet</p>
    {:else}
      {#each profiles as profile (profile.Name)}
        <button
          type="button"
          class="flex w-full flex-col items-start gap-0.5 border-l-2 px-4 py-2 text-left transition-colors {profile.Name ===
          selectedName
            ? 'border-accent bg-surface-overlay'
            : 'border-transparent hover:bg-surface-overlay/50'}"
          onclick={() => onSelect(profile.Name)}
        >
          <span class="text-sm text-text">{profile.Name}</span>
          <span class="text-xs text-text-muted">{subtitle(profile)}</span>
        </button>
      {/each}
    {/if}
  </nav>
</aside>
