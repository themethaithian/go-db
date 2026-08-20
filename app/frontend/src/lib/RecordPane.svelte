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
  // editor. A field turns into an input on double-click (or the pencil), and
  // every keystroke is live in RowEdits from there — there is no separate
  // commit, so Enter and clicking away just close the box, and Esc undoes
  // this session's typing rather than the field's whole edit. The value stays
  // marked, old value visible alongside it, until the save that takes it
  // through the Approval Gate. Nothing here writes anything: it hands the
  // typed value to RowEdits and stops.
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
  // form.
  //
  // Unlike the grid's own in-cell editor (ResultsTable), there is no commit
  // gesture here — every keystroke writes straight to RowEdits, so the dirty
  // mark and the diff line below are live while you type, and Enter/blur only
  // close the box. That is a deliberate divergence, not a shortcut: the grid
  // is a spreadsheet mid-paste, where Enter-to-commit and Esc-to-cancel are
  // the whole point, while this pane is a single field with the width to show
  // its own state changing as you go. `opened` is what makes Escape still
  // mean something precise in a world with no commit step: it is the value
  // this field held the instant the editor opened — already-dirty or still
  // fetched — and Escape's job is to put exactly that back, undoing this
  // session's keystrokes without disturbing an edit made in an earlier one.
  let editing = $state<{ row: number; column: string } | null>(null);
  let draft = $state("");
  let opened = $state<string | null>(null);

  // The field name column, then one value column per picked row.
  //
  // The name column is deliberately the narrowest thing that still reads: a
  // field name is looked at once to find the row you want and then ignored,
  // while the value beside it is what the pane exists to show. Anything wider
  // is width taken from the values — the pane's whole failure mode, since a
  // value that does not fit wraps mid-word and stops being readable at all.
  // Names longer than the column truncate and carry their full text as a
  // tooltip, which is the cheap half of the trade.
  //
  // One row gets the rest of the pane for its value, and wraps inside it —
  // a single row is read down, so there is nothing to scroll sideways for.
  //
  // Comparing sizes each value column to its own content instead, between a
  // floor wide enough to be worth a column at all and whatever the widest
  // value in it needs (capped by max-w-value on the value itself, so one long
  // text field cannot push every other column off the edge). Content-sized is
  // the whole difference between a comparison you can read and one you
  // cannot: equal columns cut the long values up to pay for the short ones,
  // and an email broken across two lines in every column is exactly what
  // there is no room for. So the grid is laid out at its content's width and
  // the pane scrolls under a name column that stays put — with a last track
  // taking up whatever slack a wide pop-out leaves, so the rows still rule a
  // line all the way across.
  let template = $derived(
    comparing
      ? `7rem repeat(${rows.length}, minmax(9rem, max-content)) minmax(0, 1fr)`
      : "7rem minmax(0, 1fr)",
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

  // Opening a field for typing starts it on what the field shows — an empty
  // box on a NULL. Opening and closing without typing records nothing: only
  // an input event reaches RowEdits, so looking inside a field can never
  // quietly edit it. (Turning a NULL into the empty string therefore takes an
  // actual keystroke — type and delete — which is the rarer intent by far.)
  // `current` is also the session's own baseline: it is whatever the field
  // held a moment ago, dirty or not, and Escape below puts it straight back.
  function startEdit(row: number, column: string, current: string | null) {
    if (!editable || edits === null) return;
    editing = { row, column };
    draft = current ?? "";
    opened = current;
  }

  // Every keystroke lands here, live — this is the box's only path to
  // RowEdits, and it runs on every `input` event rather than waiting for a
  // commit that no longer exists. `edits.set` already treats typing back the
  // fetched value as taking the edit away rather than as an edit that changes
  // nothing, so a field that ends up matching the database again simply stops
  // being dirty as you type, with no revert gesture needed.
  function typed(event: Event, fetched: string | null) {
    const cell = editing;
    if (cell === null || edits === null) return;
    draft = (event.currentTarget as HTMLInputElement).value;
    edits.set(cell.row, cell.column, draft, fetched);
  }

  function setNull(fetched: string | null) {
    const cell = editing;
    if (cell === null || edits === null) return;
    editing = null;
    edits.set(cell.row, cell.column, null, fetched);
  }

  // Nothing left to commit by the time either of these fires — typed() above
  // already wrote the live value — so both just close the box.
  function closeEdit() {
    editing = null;
  }

  // Escape's job is narrower than the grid's: it undoes what was typed in
  // this one session, not the field's whole edit. Setting the cell back to
  // `opened` does that in one call — landing on the fetched value clears the
  // edit through the same path typing does, and landing on an earlier
  // session's edit quietly restores it — either way the box then closes on a
  // field left exactly as it was found.
  function cancelEdit(fetched: string | null) {
    const cell = editing;
    if (cell === null || edits === null) {
      editing = null;
      return;
    }
    editing = null;
    edits.set(cell.row, cell.column, opened, fetched);
  }

  // Enter and Escape both just close the box now — the difference is what,
  // if anything, gets put back first. Escape stops here rather than bubbling:
  // the same key closes the whole pane (RecordPaneDock listens on the
  // window), and leaving a field editor should not also clear the selection
  // behind it.
  function handleEditKey(event: KeyboardEvent, fetched: string | null) {
    if (event.key === "Enter") {
      event.preventDefault();
      closeEdit();
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      cancelEdit(fetched);
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

<!-- bg-surface here, not just on the aside that docks this pane: the aside's
     own background is enough in the ordinary case, but this is the region
     that actually scrolls, and the one a stray stacking-context escape
     elsewhere (the grid's own sticky, z-indexed header, chiefly) would paint
     through first if this component ever ends up beside something that isn't
     as carefully contained as it should be. Owning its own opaque backdrop is
     cheap insurance that does not depend on that staying true. -->
<div class="min-h-0 flex-1 overflow-auto bg-surface">
  <!-- Comparing lays the grid out at its content's width (and never less than
       the pane's); one row wraps inside the pane instead, which is why only
       the comparison asks to be as wide as it needs. -->
  <div
    class="grid {comparing ? 'w-max min-w-full' : 'min-w-full'}"
    style="grid-template-columns: {template}"
  >
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
      <div class="sticky top-0 z-20 border-b border-border bg-surface"></div>
    {/if}

    {#each columns as column, field (column)}
      <!-- The name reads left, where a column of names is scanned from, and
           the difference dot sits at the far edge instead — hard against the
           values it is talking about, so the dots line up as a rail down the
           seam rather than ragged against names of every length. -->
      <div
        class="sticky left-0 z-10 flex items-start gap-1.5 border-b border-border/60 bg-surface py-1.5 pr-2 pl-3"
      >
        <span
          class="min-w-0 flex-1 truncate font-mono text-xs leading-6 text-text-muted"
          title={column}
        >
          {column}
        </span>
        {#if differing[field]}
          <span
            class="mt-2.5 h-1.5 w-1.5 shrink-0 rounded-full bg-warning"
            title="These rows differ on this field"
          ></span>
        {/if}
      </div>

      {#each rows as row, column_index (indices[column_index])}
        {@const key = `${field}:${column_index}`}
        {@const at = indices[column_index]}
        {@const fetched = row[field]}
        {@const value = shown[column_index][field]}
        {@const dirty = edits?.dirty(at, column) ?? false}
        {@const open = editing !== null && editing.row === at && editing.column === column}
        <!-- Double-click opens the field, which is the gesture people arrive
             with; the pencil beside the value is the discoverable one. The
             a11y route is the pencil, which is a real button. -->
        <!-- svelte-ignore a11y_click_events_have_key_events -->
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
          class="group flex items-start gap-2 border-b border-border/60 px-3 py-1.5 {dirty
            ? 'bg-accent/10 ring-1 ring-accent/40 ring-inset'
            : differing[field]
              ? 'bg-warning/10'
              : ''}"
          ondblclick={() => startEdit(at, column, value)}
        >
          {#if open}
            <!-- The box takes the whole value column, which is the reason a
                 value is edited here rather than in the grid: the width the
                 pane spent on the value at rest is the width there is to type
                 in. NULL keeps its own place at the end of the same line. -->
            <input
              class="h-6 min-w-0 flex-1 rounded-control border border-accent bg-surface-panel px-2 font-mono text-base leading-6 text-text focus:outline-none"
              value={draft}
              use:openEditor
              oninput={(event) => typed(event, fetched)}
              onkeydown={(event) => handleEditKey(event, fetched)}
              onblur={closeEdit}
              spellcheck="false"
              autocapitalize="off"
              autocomplete="off"
              aria-label="Edit {column}"
            />
            <!-- Set NULL takes the focus away from the box, and a native blur
                 fires first and closes the editor — which would make `editing`
                 null before this button's own click ever runs, and the click
                 would find no field to act on. Holding the mousedown keeps
                 the caret (and the editor) where it is so the click still
                 lands on something. -->
            <button
              type="button"
              class="flex h-6 shrink-0 items-center rounded-control border border-border bg-surface-raised px-1.5 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
              onmousedown={(event) => event.preventDefault()}
              onclick={() => setNull(fetched)}
              title="Set this field to NULL"
            >
              NULL
            </button>
          {:else}
            <!-- Wrapping is the browser's `break-word`, which is the rule this
                 pane wants stated exactly: break inside a word only when the
                 word cannot fit a line on its own. Prose still wraps at its
                 spaces, and an email or a URL — one unbroken word — breaks
                 rather than running off the edge, but only once the column is
                 genuinely too narrow for it, which at the pane's own widths it
                 no longer is. -->
            <!-- overflow-hidden is the containment fix, not the wrapping one:
                 break-words already lets a long value wrap and grow this row
                 as many lines as it needs, and that growth is untouched here.
                 What it stops is the case wrapping does not cover — a tall
                 script's fallback glyphs (Thai's, chiefly) rendering taller
                 than this line's own box, which nothing clips by default, so
                 the excess ink used to paint straight through into whichever
                 field row sits underneath. Clipped at this span's own edge
                 instead, it stays a property of this one value. -->
            <span
              class="min-w-0 max-w-value flex-1 overflow-hidden font-mono text-base leading-6 text-text break-words"
            >
              {#if dirty}
                <!-- What did I change, answered without the row growing a
                     second line: the old value sits inline, ahead of the
                     new one, struck through and capped at a fixed width of
                     its own so a long fetched value truncates rather than
                     pushing the new one around. It only appears here, on
                     the at-rest row — the editor branch above is this same
                     field's other rendering of "what it currently holds",
                     and showing the old value twice (once beside the input,
                     once beside this) would just be the same fact stated
                     two ways in the one row. The input already answers "old
                     vs new" well enough on its own: it opens pre-selected
                     over the current value, which is the fetched value the
                     first time and is this old-value line's twin the rest
                     of the time. -->
                {#if fetched === null}
                  <span
                    class="mr-1.5 inline-flex h-3.5 items-center rounded-control border border-border/60 bg-surface-raised px-1 align-middle font-sans text-[10px] font-medium tracking-wide text-text-subtle/70 line-through"
                  >
                    NULL
                  </span>
                {:else}
                  <span
                    class="mr-1.5 inline-block max-w-[6rem] truncate align-middle text-xs text-text-subtle/70 line-through"
                    title={fetched}
                  >
                    {fetched}
                  </span>
                {/if}
              {/if}
              {#if value === null}
                <!-- An absence, drawn as a thing rather than as the four
                     letters — a column whose value is the string "NULL" must
                     not be able to look like this one. -->
                <span
                  class="inline-flex h-4 items-center rounded-control border border-border bg-surface-raised px-1 align-middle font-sans text-xs font-medium tracking-wide text-text-subtle"
                >
                  NULL
                </span>
              {:else}
                {value}
              {/if}
            </span>

            <!-- Beside the value, never over it: the buttons are laid out in
                 the same line, so the value's text ends where they begin
                 instead of running underneath them. They hold their place
                 whether or not they are showing, which is what keeps a hover
                 from reflowing the field it is hovering. They follow the
                 value rather than the pane's right edge — in a pane dragged
                 wide, or a pop-out, an edit button half a window away from
                 the thing it edits belongs to nothing. -->
            <div class="flex shrink-0 items-center gap-1">
              {#if editable}
                <!-- Visible at rest, at low emphasis, so the edit gesture is
                     something a first-time visitor sees rather than something
                     only a hover discovers. Copy and undo stay hover-reveal
                     beside it — the field is already carrying enough chrome. -->
                <button
                  type="button"
                  class="flex h-6 w-6 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
                  onclick={() => startEdit(at, column, value)}
                  title="Edit this value"
                  aria-label="Edit this value"
                >
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
                    <path d="M8 2.25 9.75 4 4.5 9.25 2.25 9.75l.5-2.25z" />
                  </svg>
                </button>
              {/if}
              {#if dirty}
                <!-- Ghost like the pencil and the copy button beside it, on
                     purpose: a revert drawn in the accent color read as the
                     row's "confirm" button, which is the opposite of what it
                     does. Undo earns its own glyph — a plain counterclockwise
                     arrow, nothing that could be mistaken for a checkmark —
                     and nothing else about it, so the loudest thing in a
                     dirty row is the dirty tint, not this button. -->
                <button
                  type="button"
                  class="flex h-6 w-6 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
                  onclick={() => edits?.revertCell(at, column)}
                  title="Revert this field"
                  aria-label="Revert this field"
                >
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
                    <path d="M0.5 2v3h3" />
                    <path d="M1.755 7.5a4.5 4.5 0 1 0 1.065-4.68L0.5 5" />
                  </svg>
                </button>
              {/if}
              <!-- Copied says so by becoming a tick in the same 24px box,
                   rather than by widening into a word: feedback that reflows
                   the field it is reporting on is feedback that moves the
                   thing you were reading. -->
              <button
                type="button"
                class="flex h-6 w-6 items-center justify-center rounded-control border transition-opacity group-hover:opacity-100 focus-visible:opacity-100 {copied ===
                key
                  ? 'border-success/40 bg-surface-raised text-success opacity-100'
                  : 'border-border bg-surface-raised text-text-subtle opacity-0 hover:border-border-strong hover:text-text'}"
                onclick={() => copy(key, value)}
                title="Copy this value"
                aria-label="Copy this value"
              >
                {#if copied === key}
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
          {/if}
        </div>
      {/each}
      {#if comparing}
        <div class="border-b border-border/60"></div>
      {/if}
    {/each}
  </div>
</div>

{#if !comparing}
  <!-- The two gestures this pane has and nothing on screen otherwise says:
       cmd/ctrl-click (or shift-click, or ctrl-click) another row to start a
       comparison, and — when the row can be written to — double-click a
       value to edit it. Dropped once there is more than one row picked: the
       comparison it is teaching has, at that point, already happened. -->
  <div class="shrink-0 border-t border-border/60 px-3 py-1.5 text-xs text-text-subtle">
    ⌘ click more rows to compare{editable ? " · double-click a value to edit" : ""}
  </div>
{/if}
