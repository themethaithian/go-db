<script lang="ts">
  // The strip along the bottom of a results panel while edits are waiting to
  // be sent: how many there are, the way back, and the way on.
  //
  // Save is deliberately not the quiet option. Nothing here writes anything —
  // pressing it submits one UPDATE per dirty row to the Approval Gate, and
  // every one of them comes back as an Inline Confirm with its Impact Preview
  // before a row moves. The bar says so, so that the button is read as "show
  // me what this would do" rather than as a commit.
  let {
    changes,
    rows,
    saving,
    failure,
    onSave,
    onRevert,
  }: {
    /** How many cells hold an unsaved value. */
    changes: number;
    /** How many rows those cells are spread across — one statement each. */
    rows: number;
    saving: boolean;
    /** Why the last save stopped short, or null. */
    failure: string | null;
    onSave: () => void;
    onRevert: () => void;
  } = $props();
</script>

<div
  class="flex h-10 shrink-0 items-center gap-3 border-t border-accent/40 bg-accent/10 px-3"
  role="status"
>
  <span class="shrink-0 text-base font-medium text-text tabular-nums">
    {changes === 1 ? "1 change" : `${changes} changes`}
  </span>
  <span class="shrink-0 text-sm text-text-muted tabular-nums">
    {rows === 1 ? "in 1 row" : `in ${rows} rows`} · one confirm each
  </span>

  {#if failure !== null}
    <span class="min-w-0 truncate font-mono text-sm text-danger" title={failure}>{failure}</span>
  {/if}

  <div class="ml-auto flex shrink-0 items-center gap-2">
    <button
      type="button"
      class="inline-flex h-7 items-center rounded-control border border-border bg-surface-raised px-2.5 text-base font-medium text-text transition-colors hover:border-border-strong hover:bg-surface-overlay disabled:cursor-not-allowed disabled:opacity-50"
      disabled={saving}
      onclick={onRevert}
      title="Drop every unsaved edit"
    >
      Revert
    </button>
    <button
      type="button"
      class="inline-flex h-7 items-center rounded-control bg-accent px-3 text-base font-medium text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:bg-surface-raised disabled:text-text-subtle"
      disabled={saving}
      onclick={onSave}
      title="Submit these rows as UPDATEs, one confirm at a time"
    >
      {saving ? "Saving…" : "Save"}
    </button>
  </div>
</div>
