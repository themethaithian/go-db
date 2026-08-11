<script lang="ts">
  // Connection manager: the Profile list plus the create/edit form, extracted
  // from App.svelte so the top-level shell only owns view switching. Owns its
  // own Profile/selection state; connection state is reported upward via
  // onConnectionsChanged so App.svelte's status bar and the Editor view's
  // Profile selector stay in step.
  import { onMount } from "svelte";
  import ProfileList from "./ProfileList.svelte";
  import ProfileForm from "./ProfileForm.svelte";
  import type { ConnectionOutcome } from "./connection";
  import {
    Connect,
    ConnectedProfiles,
    DeleteProfile,
    Disconnect,
    ListProfiles,
    ReorderProfiles,
    SaveProfile,
    TestConnection,
  } from "../../wailsjs/go/app/App";
  import { db } from "../../wailsjs/go/models";
  import { reloadConfigured } from "./schema.svelte";

  let { onConnectionsChanged }: { onConnectionsChanged: () => void } = $props();

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

  async function refresh() {
    profiles = await ListProfiles();
  }

  async function refreshConnections() {
    connectedProfiles = await ConnectedProfiles();
    onConnectionsChanged();
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
      // The schema store's own copy of this Profile's Database and Engine —
      // read by engineOf/introspectable and so by the Explorer and the
      // Editor's completion — is only ever loaded lazily; without this it
      // would keep answering for whatever this Profile used to be (or MySQL,
      // for one that is new) until something else happened to trigger a
      // re-read.
      void reloadConfigured();
      formError = null;
      selectedName = profile.Name;
      mode = "edit";
    } catch (err) {
      formError = String(err);
    }
  }

  // Handles a drop from ProfileList's drag-to-reorder: change is already the
  // fully computed new name order, plus the dragged Profile's Group if the
  // drop moved it between groups. The Group write goes through SaveProfile —
  // the same password-preserving path the form uses (an empty password
  // leaves the keychain untouched) — before the order write, since Save
  // replaces a Profile in place rather than moving it, so it cannot disturb
  // the order Reorder is about to set.
  async function handleReorder(change: { name: string; group: string; order: string[] }) {
    try {
      const dragged = profiles.find((p) => p.Name === change.name);
      if (dragged !== undefined && (dragged.Group ?? "") !== change.group) {
        await SaveProfile(new db.Profile({ ...dragged, Group: change.group }), "");
      }
      await ReorderProfiles(change.order);
      await refresh();
      if (dragged !== undefined && (dragged.Group ?? "") !== change.group) {
        // Only a Group change touches what reloadConfigured cares about
        // (Database, Engine) — by going through SaveProfile at all, not by
        // anything about Group itself — but it costs nothing to keep this
        // exactly as careful as handleSave.
        void reloadConfigured();
      }
    } catch (err) {
      formError = String(err);
    }
  }

  async function handleDelete(name: string) {
    try {
      await DeleteProfile(name);
      await refresh();
      // Same reasoning as handleSave: a deleted Profile's Engine must stop
      // being answerable at all, not linger as whatever it last was.
      void reloadConfigured();
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

<ProfileList
  {profiles}
  {selectedName}
  {connectedProfiles}
  onSelect={handleSelect}
  onCreate={handleCreate}
  onReorder={handleReorder}
/>

<main class="flex flex-1 overflow-y-auto bg-surface">
  {#if mode === "empty"}
    <div class="m-auto flex max-w-xs flex-col items-center gap-3 px-6 py-10 text-center">
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
          <ellipse cx="10" cy="4.75" rx="6.25" ry="2.5" />
          <path d="M3.75 4.75v10.5c0 1.38 2.8 2.5 6.25 2.5s6.25-1.12 6.25-2.5V4.75" />
          <path d="M3.75 10c0 1.38 2.8 2.5 6.25 2.5s6.25-1.12 6.25-2.5" />
        </svg>
      </span>
      <p class="text-lg font-medium text-text">
        {profiles.length === 0 ? "No profiles yet" : "No profile selected"}
      </p>
      <p class="text-base text-text-muted">
        {profiles.length === 0
          ? "Create a profile to describe how to reach a database."
          : "Pick a profile on the left to edit or connect it."}
      </p>
    </div>
  {:else}
    <div class="w-full max-w-xl px-8 py-7">
      <ProfileForm
        profile={selectedProfile}
        {profiles}
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
    </div>
  {/if}
</main>
