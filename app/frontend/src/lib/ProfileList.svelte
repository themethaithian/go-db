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

<aside class="flex w-60 shrink-0 flex-col border-r border-border bg-surface-panel">
  <div class="flex h-11 shrink-0 items-center justify-between border-b border-border pr-2 pl-4">
    <h2 class="text-xs font-medium tracking-wide text-text-muted uppercase">Profiles</h2>
    <button
      type="button"
      class="flex h-7 w-7 items-center justify-center rounded-control border border-transparent text-text-muted transition-colors hover:border-border hover:bg-surface-overlay hover:text-text"
      onclick={onCreate}
      title="New profile"
      aria-label="New profile"
    >
      <svg
        class="h-4 w-4"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        aria-hidden="true"
      >
        <path d="M8 3.5v9M3.5 8h9" />
      </svg>
    </button>
  </div>

  <nav class="flex-1 overflow-y-auto py-2">
    {#if profiles.length === 0}
      <p class="px-4 py-2 text-sm text-text-subtle">No profiles yet</p>
    {:else}
      {#each profiles as profile (profile.Name)}
        <button
          type="button"
          class="flex w-full flex-col items-start gap-1 border-l-2 py-2.5 pr-3 pl-3.5 text-left transition-colors {profile.Name ===
          selectedName
            ? 'border-accent bg-surface-overlay'
            : 'border-transparent hover:bg-surface-raised'}"
          onclick={() => onSelect(profile.Name)}
        >
          <span class="flex w-full items-center gap-2">
            <span
              class="h-1.5 w-1.5 shrink-0 rounded-full {isConnected(profile)
                ? 'bg-success'
                : 'bg-text-subtle'}"
              aria-label={isConnected(profile) ? "Connected" : "Not connected"}
            ></span>
            <span class="truncate text-base font-medium text-text">{profile.Name}</span>
            {#if profile.SSH}
              <span
                class="ml-auto shrink-0 rounded-full border border-border bg-surface-raised px-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                title="Reached through an SSH tunnel"
              >
                ssh
              </span>
            {/if}
          </span>
          <span class="truncate pl-3.5 font-mono text-xs text-text-subtle">
            {subtitle(profile)}
          </span>
        </button>
      {/each}
    {/if}
  </nav>
</aside>
