<script lang="ts">
  // The tracer-bullet Editor view: a Profile selector, a live read/mutation
  // badge, a CodeMirror SQL editor, and a results panel. Editor-local state
  // (sql text, classification, run result, the selected Profile) lives here;
  // connectedProfiles is owned and refreshed by App.svelte and passed down,
  // since it also drives the status bar.
  import { untrack } from "svelte";
  import SqlEditor from "./SqlEditor.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import InlineConfirm from "./InlineConfirm.svelte";
  import { Classify, RunQuery, CancelPending } from "../../wailsjs/go/app/App";
  import type { guard, service } from "../../wailsjs/go/models";

  let {
    connectedProfiles,
    onGoToConnections,
  }: {
    connectedProfiles: string[];
    onGoToConnections: () => void;
  } = $props();

  let sql = $state("");
  let profileName = $state<string | null>(null);
  let classification = $state<guard.Classification | null>(null);
  let running = $state(false);
  let result = $state<service.QueryResult | null>(null);

  // If the selected Profile disconnects out from under the editor, fall back
  // to no selection rather than silently running against a stale one.
  $effect(() => {
    if (profileName !== null && !connectedProfiles.includes(profileName)) {
      profileName = null;
    }
  });

  // Live classification badge: debounced ~250ms behind typing, and cleared
  // for an empty editor.
  $effect(() => {
    const text = sql;
    if (text.trim() === "") {
      classification = null;
      return;
    }
    const timer = setTimeout(async () => {
      classification = await Classify(text);
    }, 250);
    return () => clearTimeout(timer);
  });

  // A confirm panel open counts as busy too: resolving it (confirm or cancel)
  // is the only way forward, so a second Run cannot spawn a second pending
  // behind the human's back.
  let confirmOpen = $derived(result?.status === "requires_confirmation");
  let canRun = $derived(profileName !== null && sql.trim() !== "" && !running && !confirmOpen);

  async function run() {
    if (profileName === null || sql.trim() === "" || running || confirmOpen) return;
    running = true;
    try {
      result = await RunQuery(profileName, sql);
    } finally {
      running = false;
    }
  }

  function handleSqlChange(next: string) {
    sql = next;
  }

  // A withheld mutation is the statement the gate previewed, by ID — not
  // whatever text is in the editor a moment later. If the human edits the SQL
  // or switches Profile while its Inline Confirm is open, that statement no
  // longer reflects their intent, so it is cancelled rather than left to be
  // confirmed stale. The pending's own ID is read with untrack so this effect
  // only fires on sql/profileName changing, not on result changing (which it
  // itself causes).
  $effect(() => {
    sql;
    profileName;
    const pending = untrack(() => result);
    if (pending?.status === "requires_confirmation" && pending.pending_id) {
      CancelPending(pending.pending_id);
      untrack(() => {
        if (result === pending) result = null;
      });
    }
  });

  function handlePendingResolved(next: service.QueryResult) {
    result = next;
  }
</script>

<div class="flex flex-1 flex-col overflow-hidden">
  <div class="flex shrink-0 items-center gap-3 border-b border-border bg-surface-raised px-4 py-2">
    {#if connectedProfiles.length === 0}
      <span class="text-sm text-text-muted">
        No profile connected —
        <button type="button" class="text-accent underline hover:no-underline" onclick={onGoToConnections}>
          connect one
        </button>
        to run queries.
      </span>
    {:else}
      <select
        class="rounded-control border border-border bg-surface px-2 py-1.5 text-sm text-text"
        bind:value={profileName}
      >
        <option value={null} disabled selected={profileName === null}>Select profile…</option>
        {#each connectedProfiles as name (name)}
          <option value={name}>{name}</option>
        {/each}
      </select>
    {/if}

    {#if classification}
      {#if classification.kind === "read"}
        <span
          class="rounded-full border border-success/40 bg-success/10 px-2 py-0.5 text-xs font-medium text-success"
        >
          READ
        </span>
      {:else}
        <span class="flex items-center gap-1.5">
          <span
            title={classification.reason}
            class="rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-xs font-medium text-warning"
          >
            MUTATION
          </span>
          <span class="text-xs text-text-muted" title={classification.reason}>
            {classification.reason}
          </span>
        </span>
      {/if}
    {/if}

    <div class="flex-1"></div>

    <span class="text-xs text-text-muted">⌘⏎</span>
    <button
      type="button"
      class="rounded-control bg-accent px-4 py-1.5 text-sm font-medium text-white transition-colors hover:bg-accent/90 disabled:cursor-not-allowed disabled:opacity-50"
      disabled={!canRun}
      onclick={run}
    >
      {running ? "Running…" : "Run"}
    </button>
  </div>

  <div class="h-56 shrink-0 border-b border-border">
    <SqlEditor value={sql} onChange={handleSqlChange} onRun={run} />
  </div>

  <div class="flex-1 overflow-hidden p-4">
    {#if result === null}
      <p class="text-sm text-text-muted">Run a query to see results here.</p>
    {:else if result.status === "ok"}
      <ResultsTable
        columns={result.columns ?? []}
        rows={(result.rows ?? []) as (string | null)[][]}
        truncated={result.truncated}
      />
    {:else if result.status === "requires_confirmation" && result.pending_id && result.preview}
      <InlineConfirm
        reason={result.classification.reason}
        preview={result.preview}
        pendingId={result.pending_id}
        onResolved={handlePendingResolved}
      />
    {:else if result.status === "executed"}
      <div class="rounded-panel border border-success/40 bg-success/10 p-4 text-sm text-success">
        <p>Executed — {result.affected_rows === 1 ? "1 row" : `${result.affected_rows} rows`} affected.</p>
      </div>
    {:else if result.status === "cancelled"}
      <p class="text-sm text-text-muted">Cancelled — nothing was executed.</p>
    {:else if result.status === "requires_approval"}
      <div class="rounded-panel border border-warning/40 bg-warning/10 p-4 text-sm text-warning">
        <p>{result.message}</p>
        <p class="mt-1 text-xs text-warning/80">Reason: {result.classification.reason}</p>
      </div>
    {:else}
      <div class="rounded-panel border border-danger/40 bg-danger/10 p-4 text-sm text-danger">
        <p>{result.message}</p>
      </div>
    {/if}
  </div>
</div>
