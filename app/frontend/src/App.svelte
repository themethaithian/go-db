<script lang="ts">
  // App shell: connection manager UI (issue #5) plus Connect/Test connection
  // (issue #6) over the design tokens and status bar skeleton from issue #4.
  // Live RAM reporting lands separately.
  import { onMount } from "svelte";
  import ProfileList from "./lib/ProfileList.svelte";
  import ProfileForm from "./lib/ProfileForm.svelte";
  import type { ConnectionOutcome } from "./lib/connection";
  import {
    Connect,
    ConnectedProfiles,
    DeleteProfile,
    Disconnect,
    ListProfiles,
    SaveProfile,
    TestConnection,
  } from "../wailsjs/go/app/App";
  import type { db } from "../wailsjs/go/models";

  type Mode = "empty" | "create" | "edit";

  let profiles = $state<db.Profile[]>([]);
  let selectedName = $state<string | null>(null);
  let mode = $state<Mode>("empty");
  let formError = $state<string | null>(null);

  let connectedProfiles = $state<string[]>([]);
  let testing = $state(false);
  let outcome = $state<ConnectionOutcome | null>(null);

  let selectedProfile = $derived(
    mode === "edit" ? (profiles.find((p) => p.Name === selectedName) ?? null) : null,
  );
  let isSelectedConnected = $derived(
    selectedName !== null && connectedProfiles.includes(selectedName),
  );
  let statusText = $derived(
    connectedProfiles.length === 0
      ? "go-db · not connected"
      : `go-db · connected: ${connectedProfiles.join(", ")}`,
  );

  async function refresh() {
    profiles = await ListProfiles();
  }

  async function refreshConnections() {
    connectedProfiles = await ConnectedProfiles();
  }

  onMount(() => {
    refresh();
    refreshConnections();
  });

  function handleSelect(name: string) {
    selectedName = name;
    mode = "edit";
    formError = null;
    outcome = null;
  }

  function handleCreate() {
    selectedName = null;
    mode = "create";
    formError = null;
    outcome = null;
  }

  async function handleSave(profile: db.Profile, password: string) {
    try {
      await SaveProfile(profile, password);
      await refresh();
      formError = null;
      selectedName = profile.Name;
      mode = "edit";
    } catch (err) {
      formError = String(err);
    }
  }

  async function handleDelete(name: string) {
    try {
      await DeleteProfile(name);
      await refresh();
      formError = null;
      selectedName = null;
      mode = "empty";
    } catch (err) {
      formError = String(err);
    }
  }

  async function handleTest() {
    if (selectedName === null) return;
    testing = true;
    outcome = null;
    try {
      const result = await TestConnection(selectedName);
      outcome = {
        kind: result.status === "ok" ? "success" : "danger",
        message: result.message,
      };
    } finally {
      testing = false;
    }
  }

  async function handleConnect() {
    if (selectedName === null) return;
    try {
      await Connect(selectedName);
      outcome = null;
      await refreshConnections();
    } catch (err) {
      outcome = { kind: "danger", message: String(err) };
    }
  }

  async function handleDisconnect() {
    if (selectedName === null) return;
    try {
      await Disconnect(selectedName);
      outcome = null;
      await refreshConnections();
    } catch (err) {
      outcome = { kind: "danger", message: String(err) };
    }
  }
</script>

<div class="flex h-screen w-screen flex-col bg-surface font-sans text-base text-text">
  <div class="flex flex-1 overflow-hidden">
    <ProfileList
      {profiles}
      {selectedName}
      {connectedProfiles}
      onSelect={handleSelect}
      onCreate={handleCreate}
    />

    <main class="flex flex-1 items-center justify-center overflow-y-auto p-6">
      {#if mode === "empty"}
        <span class="text-lg text-text-muted">
          {profiles.length === 0 ? "No profiles yet — create one" : "Select a profile, or create one"}
        </span>
      {:else}
        <ProfileForm
          profile={selectedProfile}
          error={formError}
          isConnected={isSelectedConnected}
          {testing}
          {outcome}
          onSave={handleSave}
          onDelete={handleDelete}
          onTest={handleTest}
          onConnect={handleConnect}
          onDisconnect={handleDisconnect}
        />
      {/if}
    </main>
  </div>

  <footer
    class="flex h-8 shrink-0 items-center justify-between border-t border-border bg-surface-raised px-3 text-xs text-text-muted"
  >
    <span>{statusText}</span>
    <span>— MB</span>
  </footer>
</div>
