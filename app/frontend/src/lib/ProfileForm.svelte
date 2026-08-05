<script lang="ts">
  import { db } from "../../wailsjs/go/models";
  import type { ConnectionOutcome } from "./connection";

  // Create/edit form for a single Profile. `profile` is null when creating;
  // Name is immutable once set, since it is the Profile's key. The password
  // field is always blank on entry — a stored password is never echoed back
  // — and is sent to the backend only when the user types a new one.
  //
  // Connection state (isConnected, testing, outcome) is owned by App.svelte —
  // this component only renders it and reports user intent through callbacks.
  let {
    profile,
    error,
    isConnected,
    testing,
    outcome,
    onSave,
    onDelete,
    onTest,
    onConnect,
    onDisconnect,
  }: {
    profile: db.Profile | null;
    error: string | null;
    isConnected: boolean;
    testing: boolean;
    outcome: ConnectionOutcome | null;
    onSave: (profile: db.Profile, password: string) => void;
    onDelete: (name: string) => void;
    onTest: () => void;
    onConnect: () => void;
    onDisconnect: () => void;
  } = $props();

  let isEditing = $derived(profile !== null);

  let name = $state("");
  let host = $state("");
  let port = $state(3306);
  let user = $state("");
  let database = $state("");
  let password = $state("");
  let confirmingDelete = $state(false);

  // SSH tunnel section. sshPortInput is kept as a plain string (not a bound
  // number) so an emptied field reads back as "" rather than NaN; it is
  // parsed on submit, with empty treated as 0 so the backend's own default
  // to port 22 applies (SSHTunnel.Address).
  let sshEnabled = $state(false);
  let sshHost = $state("");
  let sshPortInput = $state("");
  let sshUser = $state("");
  let sshKeyFile = $state("");

  $effect(() => {
    name = profile?.Name ?? "";
    host = profile?.Host ?? "";
    port = profile?.Port ?? 3306;
    user = profile?.User ?? "";
    database = profile?.Database ?? "";
    password = "";
    confirmingDelete = false;

    sshEnabled = profile?.SSH != null;
    sshHost = profile?.SSH?.Host ?? "";
    sshPortInput = profile?.SSH?.Port ? String(profile.SSH.Port) : "";
    sshUser = profile?.SSH?.User ?? "";
    sshKeyFile = profile?.SSH?.KeyFile ?? "";
  });

  function parsedSshPort(): number {
    const trimmed = sshPortInput.trim();
    if (trimmed === "") return 0;
    const n = parseInt(trimmed, 10);
    return Number.isFinite(n) ? n : 0;
  }

  function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    const submitted = new db.Profile({
      Name: name,
      Host: host,
      Port: port,
      User: user,
      Database: database,
      SSH: sshEnabled
        ? new db.SSHTunnel({
            Host: sshHost,
            Port: parsedSshPort(),
            User: sshUser,
            KeyFile: sshKeyFile,
          })
        : undefined,
    });
    onSave(submitted, password);
  }

  function handleDeleteClick() {
    if (!isEditing) return;
    if (!confirmingDelete) {
      confirmingDelete = true;
      return;
    }
    onDelete(name);
  }
</script>

<form class="flex w-full max-w-md flex-col gap-4" onsubmit={handleSubmit}>
  <h2 class="text-lg text-text">{isEditing ? "Edit profile" : "New profile"}</h2>

  {#if error}
    <p class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 text-sm text-danger">
      {error}
    </p>
  {/if}

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    Name
    <input
      class="rounded-control border border-border bg-surface px-3 py-2 text-text disabled:opacity-50"
      bind:value={name}
      disabled={isEditing}
      required
    />
  </label>

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    Host
    <input
      class="rounded-control border border-border bg-surface px-3 py-2 text-text"
      bind:value={host}
      required
    />
  </label>

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    Port
    <input
      type="number"
      class="rounded-control border border-border bg-surface px-3 py-2 text-text"
      bind:value={port}
      required
    />
  </label>

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    User
    <input
      class="rounded-control border border-border bg-surface px-3 py-2 text-text"
      bind:value={user}
      required
    />
  </label>

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    Database
    <input class="rounded-control border border-border bg-surface px-3 py-2 text-text" bind:value={database} />
  </label>

  <label class="flex flex-col gap-1 text-sm text-text-muted">
    Password
    <input
      type="password"
      class="rounded-control border border-border bg-surface px-3 py-2 text-text"
      bind:value={password}
      placeholder={isEditing ? "leave blank to keep current" : "optional"}
    />
  </label>

  <div class="flex flex-col gap-3 rounded-control border border-border bg-surface-raised p-3">
    <label class="flex items-center gap-2 text-sm text-text">
      <input
        type="checkbox"
        class="h-4 w-4 rounded-sm border-border bg-surface accent-accent"
        bind:checked={sshEnabled}
      />
      Connect through an SSH tunnel
    </label>

    {#if sshEnabled}
      <div class="flex flex-col gap-3 border-l border-border pl-4">
        <label class="flex flex-col gap-1 text-sm text-text-muted">
          Bastion host
          <input
            class="rounded-control border border-border bg-surface px-3 py-2 text-text"
            bind:value={sshHost}
            required
          />
        </label>

        <label class="flex flex-col gap-1 text-sm text-text-muted">
          Port
          <input
            type="number"
            class="rounded-control border border-border bg-surface px-3 py-2 text-text"
            value={sshPortInput}
            oninput={(e) => (sshPortInput = e.currentTarget.value)}
            placeholder="22"
          />
        </label>

        <label class="flex flex-col gap-1 text-sm text-text-muted">
          User
          <input
            class="rounded-control border border-border bg-surface px-3 py-2 text-text"
            bind:value={sshUser}
            required
          />
        </label>

        <label class="flex flex-col gap-1 text-sm text-text-muted">
          Key file
          <input
            class="rounded-control border border-border bg-surface px-3 py-2 text-text"
            bind:value={sshKeyFile}
            placeholder="optional"
          />
          <span class="text-xs text-text-muted">
            Optional — leave empty to use your SSH agent or default keys (~/.ssh). Passphrase-protected keys must be
            held by the agent.
          </span>
        </label>
      </div>
    {/if}
  </div>

  <div class="flex flex-wrap items-center gap-3 pt-2">
    <button
      type="submit"
      class="rounded-control bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent/90"
    >
      Save
    </button>

    {#if isEditing}
      <button
        type="button"
        class="rounded-control border border-border px-4 py-2 text-sm font-medium text-text transition-colors hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-50"
        onclick={onTest}
        disabled={testing}
      >
        {testing ? "Testing…" : "Test connection"}
      </button>

      <button
        type="button"
        class="rounded-control border px-4 py-2 text-sm font-medium transition-colors {isConnected
          ? 'border-border text-text hover:bg-surface-overlay'
          : 'border-accent text-accent hover:bg-accent/10'}"
        onclick={isConnected ? onDisconnect : onConnect}
      >
        {isConnected ? "Disconnect" : "Connect"}
      </button>

      <button
        type="button"
        class="rounded-control border border-danger px-4 py-2 text-sm font-medium text-danger transition-colors hover:bg-danger/10"
        onclick={handleDeleteClick}
      >
        {confirmingDelete ? "Really delete?" : "Delete"}
      </button>
    {:else}
      <span class="text-xs text-text-muted">Save first to test</span>
    {/if}
  </div>

  {#if outcome}
    <p
      class="rounded-control border px-3 py-2 text-sm {outcome.kind === 'success'
        ? 'border-success/40 bg-success/10 text-success'
        : 'border-danger/40 bg-danger/10 text-danger'}"
    >
      {outcome.message}
    </p>
  {/if}
</form>
