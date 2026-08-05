<script lang="ts">
  import { db } from "../../wailsjs/go/models";

  // Create/edit form for a single Profile. `profile` is null when creating;
  // Name is immutable once set, since it is the Profile's key. The password
  // field is always blank on entry — a stored password is never echoed back
  // — and is sent to the backend only when the user types a new one.
  let {
    profile,
    error,
    onSave,
    onDelete,
  }: {
    profile: db.Profile | null;
    error: string | null;
    onSave: (profile: db.Profile, password: string) => void;
    onDelete: (name: string) => void;
  } = $props();

  let isEditing = $derived(profile !== null);

  let name = $state("");
  let host = $state("");
  let port = $state(3306);
  let user = $state("");
  let database = $state("");
  let password = $state("");
  let confirmingDelete = $state(false);

  $effect(() => {
    name = profile?.Name ?? "";
    host = profile?.Host ?? "";
    port = profile?.Port ?? 3306;
    user = profile?.User ?? "";
    database = profile?.Database ?? "";
    password = "";
    confirmingDelete = false;
  });

  function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    // Preserve any existing SSH tunnel config on edit — the form has no SSH
    // fields yet (issue #12), and omitting it here would wipe it on save.
    const submitted = new db.Profile({
      Name: name,
      Host: host,
      Port: port,
      User: user,
      Database: database,
      SSH: profile?.SSH,
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

  <div class="flex items-center gap-3 pt-2">
    <button
      type="submit"
      class="rounded-control bg-accent px-4 py-2 text-sm font-medium text-white transition-colors hover:bg-accent/90"
    >
      Save
    </button>
    {#if isEditing}
      <button
        type="button"
        class="rounded-control border border-danger px-4 py-2 text-sm font-medium text-danger transition-colors hover:bg-danger/10"
        onclick={handleDeleteClick}
      >
        {confirmingDelete ? "Really delete?" : "Delete"}
      </button>
    {/if}
  </div>
</form>
