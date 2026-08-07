<script lang="ts">
  import { db } from "../../wailsjs/go/models";
  import { ExecutablePath } from "../../wailsjs/go/app/App";
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

  // The running app's own executable path, for the MCP setup popover below.
  // Fetched once on mount rather than on first open — the popover must never
  // block on it (CLAUDE.md: no ceremony, no spinner-gated affordances), so
  // it starts resolving immediately and the popover just renders whatever
  // is known yet. null means "still resolving"; the copy buttons stay
  // disabled and the code blocks show a placeholder until it lands.
  let exePath = $state<string | null>(null);
  $effect(() => {
    ExecutablePath()
      .then((path) => (exePath = path))
      .catch(() => {
        // Left null: the popover's placeholder already reads fine as "not
        // available yet", and there's nothing actionable to tell the human.
      });
  });

  // The MCP setup popover: a small panel of copyable setup snippets for the
  // saved profile, opened from the footer's MCP button. Closed by an outside
  // click or Esc, same as the Editor's Saved-queries popover.
  let mcpPopoverOpen = $state(false);
  let mcpPanel = $state<HTMLDivElement | undefined>(undefined);

  function toggleMcpPopover() {
    mcpPopoverOpen = !mcpPopoverOpen;
  }

  $effect(() => {
    if (!mcpPopoverOpen) return;
    function onPointerDown(event: MouseEvent) {
      if (mcpPanel && !mcpPanel.contains(event.target as Node)) {
        mcpPopoverOpen = false;
      }
    }
    function onKeydown(event: KeyboardEvent) {
      if (event.key === "Escape") mcpPopoverOpen = false;
    }
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeydown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeydown);
    };
  });

  // The two setup snippets, built from the saved Profile's name (locked
  // while editing, so `name` is always the saved value) and the executable
  // path once it has resolved. Server name mirrors the command's own
  // convention: "godb-<profile>".
  let mcpServerName = $derived(`godb-${name}`);
  let claudeCodeCommand = $derived(
    exePath !== null ? `claude mcp add ${mcpServerName} -- ${exePath} mcp ${name}` : null,
  );
  let claudeDesktopJson = $derived(
    exePath !== null
      ? JSON.stringify(
          { mcpServers: { [mcpServerName]: { command: exePath, args: ["mcp", name] } } },
          null,
          2,
        )
      : null,
  );

  /** How long "Copied" stays up after a copy — matches RecordPane. */
  const MCP_COPIED_MS = 1200;
  let mcpCopied = $state<"code" | "desktop" | null>(null);
  let mcpCopyTimer: ReturnType<typeof setTimeout> | null = null;

  async function copyMcp(key: "code" | "desktop", value: string | null) {
    if (value === null) return;
    try {
      await navigator.clipboard.writeText(value);
    } catch {
      return;
    }
    mcpCopied = key;
    if (mcpCopyTimer !== null) clearTimeout(mcpCopyTimer);
    mcpCopyTimer = setTimeout(() => (mcpCopied = null), MCP_COPIED_MS);
  }

  $effect(() => () => {
    if (mcpCopyTimer !== null) clearTimeout(mcpCopyTimer);
  });

  $effect(() => {
    name = profile?.Name ?? "";
    host = profile?.Host ?? "";
    port = profile?.Port ?? 3306;
    user = profile?.User ?? "";
    database = profile?.Database ?? "";
    password = "";
    confirmingDelete = false;
    mcpPopoverOpen = false;

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

  // Shared control recipes, so every field and button in the form is the
  // same object. Token-backed utilities only — see app.css.
  const fieldClass =
    "h-9 w-full rounded-control border border-border bg-surface px-2.5 text-base text-text transition-colors placeholder:text-text-subtle hover:border-border-strong focus:border-accent";
  const lockedFieldClass =
    "disabled:cursor-not-allowed disabled:border-border disabled:bg-surface-raised disabled:text-text-muted disabled:hover:border-border";
  const labelClass = "flex flex-col gap-1.5 text-xs font-medium text-text-muted";
  const buttonBase =
    "inline-flex h-8 shrink-0 items-center justify-center rounded-control px-3 text-base font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-50";
  const primaryButton = `${buttonBase} bg-accent text-white hover:bg-accent-hover`;
  const secondaryButton = `${buttonBase} border border-border bg-surface-raised text-text hover:border-border-strong hover:bg-surface-overlay`;
</script>

<!--
  One copyable setup block for the MCP popover: a label, a copy button using
  the same copied-tick pattern as the record pane, and a mono block that
  wraps rather than scrolling — the whole point is a command or JSON snippet
  a human can read and paste without fighting a scrollbar. content is null
  while the executable path is still resolving, which the placeholder text
  says outright and the copy button reflects by staying disabled.
-->
{#snippet mcpBlock(label: string, key: "code" | "desktop", content: string | null)}
  <div class="flex flex-col gap-1.5">
    <div class="flex items-center justify-between">
      <span class="text-xs font-medium text-text-muted">{label}</span>
      <button
        type="button"
        class="flex h-6 w-6 shrink-0 items-center justify-center rounded-control border transition-colors {mcpCopied ===
        key
          ? 'border-success/40 bg-surface-raised text-success'
          : 'border-border bg-surface-raised text-text-subtle hover:border-border-strong hover:text-text'}"
        onclick={() => copyMcp(key, content)}
        disabled={content === null}
        title="Copy to clipboard"
        aria-label="Copy {label} snippet"
      >
        {#if mcpCopied === key}
          <svg
            class="h-3.5 w-3.5"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            stroke-width="1.5"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="M2.5 6.25 5 8.75l4.5-5.5" />
          </svg>
        {:else}
          <svg
            class="h-3.5 w-3.5"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            stroke-width="1.2"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <rect x="4.25" y="4.25" width="5.5" height="5.5" rx="1.25" />
            <path d="M7.75 2.25h-5.5v5.5" />
          </svg>
        {/if}
      </button>
    </div>
    <pre
      class="max-h-32 overflow-auto rounded-control border border-border bg-surface px-2.5 py-2 font-mono text-xs whitespace-pre-wrap break-all text-text">{content ??
        "Resolving go-db's executable path…"}</pre>
  </div>
{/snippet}

<form class="rounded-panel border border-border bg-surface-panel shadow-panel" onsubmit={handleSubmit}>
  <header class="flex items-center justify-between gap-3 border-b border-border px-5 py-3.5">
    <div class="min-w-0">
      <h2 class="text-lg font-semibold text-text">{isEditing ? "Edit profile" : "New profile"}</h2>
      <p class="mt-0.5 truncate text-sm text-text-muted">
        {isEditing ? "How go-db reaches this database." : "Describe how to reach a database."}
      </p>
    </div>
    {#if isEditing && isConnected}
      <span
        class="flex shrink-0 items-center gap-1.5 rounded-full border border-success/40 bg-success/10 px-2 py-0.5 text-xs font-medium text-success"
      >
        <span class="h-1.5 w-1.5 rounded-full bg-success"></span>
        Connected
      </span>
    {/if}
  </header>

  <div class="flex flex-col gap-5 px-5 py-5">
    {#if error}
      <p class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 text-base text-danger">
        {error}
      </p>
    {/if}

    <div class="grid grid-cols-4 gap-x-3 gap-y-4">
      <label class="{labelClass} col-span-4">
        <span class="flex items-center justify-between">
          <span>Name</span>
          {#if isEditing}
            <span class="font-normal text-text-subtle">locked</span>
          {/if}
        </span>
        <input class="{fieldClass} {lockedFieldClass}" bind:value={name} disabled={isEditing} required />
      </label>

      <label class="{labelClass} col-span-3">
        Host
        <input class={fieldClass} bind:value={host} required />
      </label>

      <label class="{labelClass} col-span-1">
        Port
        <input type="number" class={fieldClass} bind:value={port} required />
      </label>

      <label class="{labelClass} col-span-2">
        User
        <input class={fieldClass} bind:value={user} required />
      </label>

      <label class="{labelClass} col-span-2">
        Database
        <input class={fieldClass} bind:value={database} placeholder="optional" />
      </label>

      <label class="{labelClass} col-span-4">
        Password
        <input
          type="password"
          class={fieldClass}
          bind:value={password}
          placeholder={isEditing ? "leave blank to keep the stored password" : "optional"}
        />
      </label>
    </div>

    <div class="rounded-control border border-border bg-surface">
      <label class="flex cursor-pointer items-center gap-2.5 px-3 py-2.5 text-base text-text">
        <span class="relative flex h-4 w-4 shrink-0 items-center justify-center">
          <input
            type="checkbox"
            class="h-4 w-4 cursor-pointer appearance-none rounded-sm border border-border bg-surface-raised transition-colors checked:border-accent checked:bg-accent hover:border-border-strong"
            bind:checked={sshEnabled}
          />
          {#if sshEnabled}
            <svg
              class="pointer-events-none absolute h-2.5 w-2.5 text-white"
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M2 6.25 4.75 9 10 3.25" />
            </svg>
          {/if}
        </span>
        <span>Connect through an SSH tunnel</span>
      </label>

      {#if sshEnabled}
        <div class="grid grid-cols-4 gap-x-3 gap-y-4 border-t border-border px-3 py-4">
          <label class="{labelClass} col-span-3">
            Bastion host
            <input class={fieldClass} bind:value={sshHost} required />
          </label>

          <label class="{labelClass} col-span-1">
            Port
            <input
              type="number"
              class={fieldClass}
              value={sshPortInput}
              oninput={(e) => (sshPortInput = e.currentTarget.value)}
              placeholder="22"
            />
          </label>

          <label class="{labelClass} col-span-2">
            User
            <input class={fieldClass} bind:value={sshUser} required />
          </label>

          <label class="{labelClass} col-span-2">
            Key file
            <input class={fieldClass} bind:value={sshKeyFile} placeholder="optional" />
          </label>

          <p class="col-span-4 -mt-1 text-xs leading-relaxed text-text-subtle">
            Leave the key file empty to use your SSH agent or default keys (~/.ssh). Passphrase-protected keys must
            be held by the agent.
          </p>
        </div>
      {/if}
    </div>
  </div>

  <footer class="flex items-center gap-2 border-t border-border px-5 py-3.5">
    <button type="submit" class={primaryButton}>Save</button>

    {#if isEditing}
      <button type="button" class={secondaryButton} onclick={onTest} disabled={testing}>
        {testing ? "Testing…" : "Test connection"}
      </button>

      <span class="mx-1 h-5 w-px shrink-0 bg-border"></span>

      <button
        type="button"
        class="{buttonBase} border {isConnected
          ? 'border-border bg-surface-raised text-text hover:border-border-strong hover:bg-surface-overlay'
          : 'border-accent/40 bg-accent/10 text-accent hover:border-accent/60 hover:bg-accent/15'}"
        onclick={isConnected ? onDisconnect : onConnect}
      >
        {isConnected ? "Disconnect" : "Connect"}
      </button>

      <span class="mx-1 h-5 w-px shrink-0 bg-border"></span>

      <div class="relative shrink-0" bind:this={mcpPanel}>
        <button
          type="button"
          class={secondaryButton}
          aria-haspopup="true"
          aria-expanded={mcpPopoverOpen}
          onclick={toggleMcpPopover}
        >
          MCP
        </button>

        {#if mcpPopoverOpen}
          <!-- Opens upward: the footer sits at the bottom of the panel, and a
               panel opening below it would clip against the viewport far
               more often than one opening over the form it belongs to. -->
          <div
            class="absolute bottom-full left-0 z-10 mb-1.5 w-96 max-w-[90vw] rounded-panel border border-border bg-surface-panel p-3 shadow-panel"
            role="dialog"
            aria-label="MCP setup for {name}"
          >
            <div class="flex flex-col gap-3">
              {@render mcpBlock("Claude Code", "code", claudeCodeCommand)}
              {@render mcpBlock("Claude Desktop", "desktop", claudeDesktopJson)}
              <p class="text-xs text-text-subtle">go-db must be running for the MCP server to work.</p>
            </div>
          </div>
        {/if}
      </div>

      <span class="flex-1"></span>

      <button
        type="button"
        class="{buttonBase} border {confirmingDelete
          ? 'border-danger bg-danger/15 text-danger'
          : 'border-border text-text-muted hover:border-danger/50 hover:bg-danger/10 hover:text-danger'}"
        onclick={handleDeleteClick}
      >
        {confirmingDelete ? "Really delete?" : "Delete"}
      </button>
    {:else}
      <span class="text-sm text-text-subtle">Save the profile before testing or connecting.</span>
    {/if}
  </footer>

  {#if outcome}
    <p
      class="flex items-start gap-2 rounded-b-panel border-t px-5 py-3 text-base {outcome.kind === 'success'
        ? 'border-success/30 bg-success/10 text-success'
        : 'border-danger/30 bg-danger/10 text-danger'}"
    >
      <svg
        class="mt-0.5 h-3.5 w-3.5 shrink-0"
        viewBox="0 0 14 14"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <circle cx="7" cy="7" r="5.5" />
        {#if outcome.kind === "success"}
          <path d="M4.75 7.25 6.25 8.75 9.25 5.5" />
        {:else}
          <path d="M7 4.25v3.5M7 9.75h.01" />
        {/if}
      </svg>
      <span>{outcome.message}</span>
    </p>
  {/if}
</form>
