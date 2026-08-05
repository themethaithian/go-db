<script lang="ts">
  import type { db } from "../../wailsjs/go/models";

  // Sidebar: lists saved Profiles and lets the user pick one to edit or
  // start a new one. Holds no state of its own — App.svelte owns the list,
  // the current selection, and which Profiles are connected.
  let {
    profiles,
    selectedName,
    connectedProfiles,
    onSelect,
    onCreate,
  }: {
    profiles: db.Profile[];
    selectedName: string | null;
    connectedProfiles: string[];
    onSelect: (name: string) => void;
    onCreate: () => void;
  } = $props();

  function isConnected(profile: db.Profile): boolean {
    return connectedProfiles.includes(profile.Name);
  }

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
          <span class="flex items-center gap-1.5 text-sm text-text">
            {#if isConnected(profile)}
              <span class="h-1.5 w-1.5 shrink-0 rounded-full bg-success" aria-label="Connected"></span>
            {/if}
            {profile.Name}
          </span>
          <span class="flex items-center gap-1.5 text-xs text-text-muted">
            {subtitle(profile)}
            {#if profile.SSH}
              <span
                class="rounded-full border border-border bg-surface-overlay px-1.5 py-0 text-xs font-medium tracking-wide text-text-muted uppercase"
                title="Reached through an SSH tunnel"
              >
                ssh
              </span>
            {/if}
          </span>
        </button>
      {/each}
    {/if}
  </nav>
</aside>
