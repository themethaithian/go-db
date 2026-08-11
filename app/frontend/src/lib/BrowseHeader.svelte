<script lang="ts">
  // The line above the browse pane: what is being looked at, and what can be
  // done to it.
  //
  // It is one component because it is one piece of chrome. The three browse
  // panes below it (TableBrowse, KeyBrowse, CollectionBrowse) disagree about
  // almost everything — a table has a WHERE box and a Structure side, a key has
  // a type and a Delete, a collection has cards and a Load more — but they
  // agree about this: the name of the thing, the Profile it is on, whether a
  // read is in flight, and the two buttons every pane offers (take the
  // statement to the Editor, run it again). Written once here, those cannot
  // drift apart between Engines.
  //
  // What an Engine has that the others do not goes in `actions`, which is
  // rendered where the pane's own buttons belong: after the spacer and before
  // the two shared ones, which is exactly where the Data | Structure toggle and
  // the Delete key button sat when this header was part of the Explorer.
  import type { Snippet } from "svelte";

  let {
    name,
    qualifier = null,
    profile,
    loading = false,
    count = null,
    statement,
    onOpenInEditor,
    onRefresh,
    refreshDisabled = false,
    actions = null,
  }: {
    /** The thing being browsed: a table, a collection, a key. */
    name: string;
    /** What qualifies that name, dimmed before it — a database. Keys have none. */
    qualifier?: string | null;
    /** The Profile it is on. */
    profile: string;
    /** Whether a read is in flight right now. */
    loading?: boolean;
    /** The count line beside the name, when the answer has one to give. */
    count?: string | null;
    /** The statement "Open in editor" would hand over; empty disables it. */
    statement: string;
    onOpenInEditor: () => void;
    onRefresh: () => void;
    refreshDisabled?: boolean;
    /** This Engine's own header buttons, if it has any. */
    actions?: Snippet | null;
  } = $props();
</script>

<div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-surface-panel px-3">
  <!-- The name as the statement writes it: the qualifier dimmed, the name it
       qualifies in full — two schemas can each have a `users`. A Redis key is
       shown alone, because the index it is in is not part of any command that
       names it. -->
  <span
    class="truncate font-mono text-md font-medium text-text"
    title={qualifier === null ? name : `${qualifier}.${name}`}
  >
    {#if qualifier !== null}<span class="text-text-subtle">{qualifier}.</span>{/if}{name}
  </span>
  <span
    class="shrink-0 rounded-full border border-accent/40 bg-accent/10 px-2 py-0.5 text-xs font-medium text-accent"
    title="Profile"
  >
    {profile}
  </span>
  {#if loading}
    <span class="shrink-0 text-sm text-text-subtle">loading…</span>
  {:else if count !== null}
    <span class="shrink-0 text-sm tabular-nums text-text-muted">{count}</span>
  {/if}

  <div class="flex-1"></div>

  {@render actions?.()}

  <button
    type="button"
    class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-base text-text-muted transition-colors hover:border-border-strong hover:text-text disabled:cursor-not-allowed disabled:text-text-subtle"
    onclick={onOpenInEditor}
    disabled={statement === ""}
    title="Open this statement in the Editor"
  >
    <svg
      class="h-3.5 w-3.5"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      stroke-width="1.4"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M9.25 2.75h4v4M13.25 2.75 7.5 8.5" />
      <path d="M12.25 9.75v2.5a1.5 1.5 0 0 1-1.5 1.5h-7a1.5 1.5 0 0 1-1.5-1.5v-7a1.5 1.5 0 0 1 1.5-1.5h2.5" />
    </svg>
    Open in editor
  </button>
  <button
    type="button"
    class="flex h-8 w-8 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-muted transition-colors hover:border-border-strong hover:text-text disabled:cursor-not-allowed disabled:text-text-subtle"
    onclick={onRefresh}
    disabled={refreshDisabled}
    title="Run the query again"
    aria-label="Run the query again"
  >
    <svg
      class="h-4 w-4"
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      stroke-width="1.4"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M13.25 8a5.25 5.25 0 1 1-1.6-3.78" />
      <path d="M13.4 2.4v2.85h-2.85" />
    </svg>
  </button>
</div>
