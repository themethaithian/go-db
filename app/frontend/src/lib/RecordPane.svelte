<script lang="ts">
  // The record pane: the rows picked in the Explorer's grid, read down the
  // page instead of across it.
  //
  // One picked row is the common case — a wide table whose columns ran off
  // the right edge, laid out as field/value so every column is on screen at
  // once. Picking more than one turns the same layout into a comparison: the
  // field names stay in the first column and each picked row gets a value
  // column of its own, in the order the rows were picked.
  //
  // Comparing is only worth the width if it answers "what is different about
  // these rows", so that is what the pane says out loud: a field the picked
  // rows disagree on has its values tinted and its name marked, and the
  // fields they agree on stay quiet. Reading two rows side by side and
  // diffing them by eye is the job this pane exists to take away.
  //
  // The pane renders what it is given and reports the close: which rows are
  // picked is the caller's state (Explorer or Editor), because the grid
  // highlights the same selection and the two must not be able to disagree.
  //
  // This same component is what the pop-out shows: onExpand is the docked
  // pane's own affordance for opening it, so it is only given to the docked
  // instance — the popup's copy of this pane has nothing further to expand
  // into. onClose means "close the row pane" docked (clearing the selection)
  // but "close the popup" in the pop-out (leaving the selection alone); which
  // one it does is entirely up to the caller that wires it.
  //
  // It is also where a value is edited, when the caller says the rows may be:
  // this is the surface that already shows one row's every column with room to
  // type in, and the grid's own click already means "pick this row", so putting
  // the caret in the grid would have meant dismantling a comparison to open an
  // editor. A field turns into an input on double-click (or the pencil), Enter
  // commits the edit locally and Esc drops it, and the value stays marked until
  // the save that takes it through the Approval Gate. Nothing here writes
  // anything: it hands the typed value to RowEdits and stops.
  import type { RowEdits } from "./edits.svelte";
  import type { CellValue } from "./mutate";

  let {
    columns,
    rows,
    indices,
    totalRows,
    edits = null,
    editable = false,
    readOnlyHint = null,
    onExpand,
    onClose,
  }: {
    columns: string[];
    /** The picked rows' cells, in the order they were picked. */
    rows: CellValue[][];
    /** Each picked row's index into the grid, aligned with `rows`. */
    indices: number[];
    /** How many rows the grid is showing, for the single-row header. */
    totalRows: number;
    /** The result set's pending edits, shown here and written to from here. */
    edits?: RowEdits | null;
    /** Whether these rows can be edited at all — the caller's verdict, not this pane's. */
    editable?: boolean;
    /** Why they cannot be, when they cannot and it is worth saying. */
    readOnlyHint?: string | null;
    /** Given, a header button opens the pop-out. Omitted in the pop-out itself. */
    onExpand?: () => void;
    onClose: () => void;
  } = $props();

  /** How long "Copied" stays up after a copy. */
  const COPIED_MS = 1200;

  let comparing = $derived(rows.length > 1);

  // What every cell shows: the value typed over it if there is one, otherwise
  // the value that was fetched. Everything below reads this rather than `rows`
  // — the pane shows the row as it would be after saving — while `rows` stays
  // the record of what the database said, which is what an edit is compared
  // against and what identifies the row when it is saved.
  let shown = $derived(
    rows.map((row, picked) =>
      columns.map((column, field) =>
        edits === null ? row[field] : edits.value(indices[picked], column, row[field]),
      ),
    ),
  );

  // The fields the picked rows disagree on. Compared as strings, with NULL (an
  // absence, not a value) unequal to everything but another NULL. One row
  // disagrees with nothing.
  let differing = $derived(
    columns.map((_, field) => comparing && shown.some((row) => row[field] !== shown[0][field])),
  );

  // The field open for typing, as the grid row it belongs to and its column,
  // and the text in the box. One at a time: this is a field editor, not a
  // form, and a commit is one keypress away.
  let editing = $state<{ row: number; column: string } | null>(null);
  let draft = $state("");

  // A fixed name column, then one value column per picked row. Comparing
  // gives the value columns a floor wide enough to read: four of them will
  // not fit a pane this narrow, so the pane scrolls sideways under a name
  // column that stays put.
  let template = $derived(
    comparing ? `6.5rem repeat(${rows.length}, minmax(10.5rem, 1fr))` : "6.5rem minmax(0, 1fr)",
  );

  // Which cell just went to the clipboard, as "field:column" — the feedback
  // is a moment on the button that was pressed, not a toast.
  let copied = $state<string | null>(null);
  let timer: ReturnType<typeof setTimeout> | null = null;

  $effect(() => () => {
    if (timer !== null) clearTimeout(timer);
  });

  // Copy puts the value on the clipboard exactly as the database gave it.
  // NULL copies as an empty string: the four letters the cell draws are this
  // pane's way of drawing an absence, and pasting the word NULL somewhere
  // else would be pasting a value that was never in the column.
  async function copy(key: string, value: string | null) {
    try {
      await navigator.clipboard.writeText(value ?? "");
    } catch {
      // A refused clipboard is worth no feedback at all — claiming "Copied"
      // for something that is not on the clipboard is the one bad outcome.
      return;
    }
    copied = key;
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => (copied = null), COPIED_MS);
  }

  // Opening a field for typing starts it on what the field shows — including
  // an empty box on a NULL, which is how NULL is cleared: type nothing, press
  // Enter, and the column holds the empty string rather than an absence.
  function startEdit(row: number, column: string, current: string | null) {
    if (!editable || edits === null) return;
    editing = { row, column };
    draft = current ?? "";
  }

  // Commits what is in the box as a pending edit. `fetched` is what the
  // database gave for this cell, so an edit that lands back on it is recorded
  // as no edit at all rather than as a change that changes nothing.
  function commit(fetched: string | null) {
    const cell = editing;
    if (cell === null || edits === null) return;
    editing = null;
    edits.set(cell.row, cell.column, draft, fetched);
  }

  function setNull(fetched: string | null) {
    const cell = editing;
    if (cell === null || edits === null) return;
    editing = null;
    edits.set(cell.row, cell.column, null, fetched);
  }

  // Enter commits, Escape drops the edit. Escape stops here rather than
  // bubbling: the same key closes the whole pane (RecordPaneDock listens on
  // the window), and leaving a field editor should not also clear the
  // selection behind it.
  function handleEditKey(event: KeyboardEvent, fetched: string | null) {
    if (event.key === "Enter") {
      event.preventDefault();
      commit(fetched);
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      editing = null;
    }
  }

  // Editing a value almost always means replacing it, so the box opens
  // selected — the same contract the Editor's tab rename box has.
  function openEditor(node: HTMLInputElement) {
    node.focus();
    node.select();
  }
</script>

<div class="flex h-8 shrink-0 items-center gap-2 border-b border-border pr-2 pl-3">
  <span class="shrink-0 text-xs font-medium tracking-wide text-text-muted uppercase">
    {comparing ? "Compare" : `Row ${indices[0] + 1}`}
  </span>
  <span class="shrink-0 text-xs tabular-nums text-text-subtle">
    {comparing ? `· ${rows.length} rows` : `of ${totalRows}`}
  </span>

  <!-- Why there is no pencil on any of these fields. Only ever shown when
       there is something to say: while the answer is still being fetched the
       pane says nothing rather than a hint that disappears a moment later. -->
  {#if readOnlyHint !== null}
    <span class="min-w-0 truncate text-xs text-text-subtle italic" title={readOnlyHint}>
      {readOnlyHint}
    </span>
  {/if}

  <div class="ml-auto"></div>
  {#if onExpand !== undefined}
    <button
      type="button"
      class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
      onclick={onExpand}
      title="Expand into a large view"
      aria-label="Expand into a large view"
    >
      <svg
        class="h-3 w-3"
        viewBox="0 0 12 12"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        stroke-linejoin="round"
        aria-hidden="true"
      >
        <path d="M2.5 5.5v-3h3" />
        <path d="M9.5 6.5v3h-3" />
        <path d="M2.5 2.5l3 3M9.5 9.5l-3-3" />
      </svg>
    </button>
  {/if}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
    onclick={onClose}
    title="Close the row pane (Esc)"
    aria-label="Close the row pane"
  >
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      aria-hidden="true"
    >
      <path d="M3 3l6 6M9 3l-6 6" />
    </svg>
  </button>
</div>

<div class="min-h-0 flex-1 overflow-auto">
  <div class="grid min-w-full" style="grid-template-columns: {template}">
    {#if comparing}
      <!-- The corner: it holds the name column's ground while the value
           headers scroll under it, so it outranks both. -->
      <div class="sticky top-0 left-0 z-30 border-b border-border bg-surface"></div>
      {#each indices as index (index)}
        <div class="sticky top-0 z-20 border-b border-border bg-surface px-3 py-1.5">
          <span
            class="rounded-full border border-accent/40 bg-accent/10 px-1.5 py-0.5 font-mono text-xs font-medium text-accent tabular-nums"
          >
            #{index + 1}
          </span>
        </div>
      {/each}
    {/if}

    {#each columns as column, field (column)}
      <div
        class="sticky left-0 z-10 flex items-start justify-end gap-1.5 border-b border-border/60 bg-surface py-1.5 pr-2 pl-3"
      >
        {#if differing[field]}
          <span
            class="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-warning"
            title="These rows differ on this field"
          ></span>
        {/if}
        <span class="truncate text-right font-mono text-xs leading-5 text-text-muted" title={column}>
          {column}
        </span>
      </div>

      {#each rows as row, column_index (indices[column_index])}
        {@const key = `${field}:${column_index}`}
        {@const at = indices[column_index]}
        {@const fetched = row[field]}
        {@const value = shown[column_index][field]}
        {@const dirty = edits?.dirty(at, column) ?? false}
        {@const open = editing !== null && editing.row === at && editing.column === column}
        <!-- Double-click opens the field, which is the gesture people arrive
             with; the pencil in the corner is the discoverable one. The a11y
             route is the pencil, which is a real button. -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="group relative border-b border-border/60 px-3 py-1.5 {dirty
            ? 'bg-accent/10 ring-1 ring-accent/40 ring-inset'
            : differing[field]
              ? 'bg-warning/10'
              : ''}"
          ondblclick={() => startEdit(at, column, value)}
        >
          {#if open}
            <div class="flex items-center gap-1">
              <input
                class="min-w-0 flex-1 rounded-control border border-accent bg-surface px-1.5 py-px font-mono text-base leading-5 text-text focus:outline-none"
                bind:value={draft}
                use:openEditor
                onkeydown={(event) => handleEditKey(event, fetched)}
                onblur={() => commit(fetched)}
                spellcheck="false"
                autocapitalize="off"
                autocomplete="off"
                aria-label="Edit {column}"
              />
              <!-- Set NULL takes the focus away from the box, which would
                   otherwise commit what is in it first; holding the mousedown
                   keeps the caret where it is so the click means only this. -->
              <button
                type="button"
                class="flex h-5 shrink-0 items-center rounded-control border border-border bg-surface-raised px-1 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
                onmousedown={(event) => event.preventDefault()}
                onclick={() => setNull(fetched)}
                title="Set this field to NULL"
              >
                NULL
              </button>
            </div>
          {:else}
            <span class="block pr-6 font-mono text-base leading-5 text-text break-words">
              {#if value === null}
                <span class="text-text-subtle italic">NULL</span>
              {:else}
                {value}
              {/if}
            </span>

            <div
              class="absolute top-1 right-1 flex items-center gap-1 transition-opacity group-hover:opacity-100 focus-within:opacity-100 {copied ===
                key || dirty
                ? 'opacity-100'
                : 'opacity-0'}"
            >
              {#if dirty}
                <button
                  type="button"
                  class="flex h-5 items-center rounded-control border border-accent/40 bg-surface-raised px-1 text-accent transition-colors hover:bg-accent/15"
                  onclick={() => edits?.revertCell(at, column)}
                  title="Undo this edit"
                  aria-label="Undo this edit"
                >
                  <svg
                    class="h-3 w-3"
                    viewBox="0 0 12 12"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M9.75 8.5a3.5 3.5 0 0 0-3.5-3.5H2.5" />
                    <path d="M4.25 2.75 2 5l2.25 2.25" />
                  </svg>
                </button>
              {/if}
              {#if editable}
                <button
                  type="button"
                  class="flex h-5 items-center rounded-control border border-border bg-surface-raised px-1 text-text-muted transition-colors hover:text-text"
                  onclick={() => startEdit(at, column, value)}
                  title="Edit this value"
                  aria-label="Edit this value"
                >
                  <svg
                    class="h-3 w-3"
                    viewBox="0 0 12 12"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.2"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M8 2.25 9.75 4 4.5 9.25 2.25 9.75l.5-2.25z" />
                  </svg>
                </button>
              {/if}
              <button
                type="button"
                class="flex h-5 items-center gap-1 rounded-control border border-border bg-surface-raised px-1 text-text-muted transition-colors hover:text-text"
                onclick={() => copy(key, value)}
                title="Copy this value"
                aria-label="Copy this value"
              >
                {#if copied === key}
                  <span class="px-0.5 text-xs font-medium text-success">Copied</span>
                {:else}
                  <svg
                    class="h-3 w-3"
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
          {/if}
        </div>
      {/each}
    {/each}
  </div>
</div>
