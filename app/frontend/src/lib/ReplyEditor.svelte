<script lang="ts">
  // The browse pane's editable view of one browsed Redis key — the hash
  // fields, list elements, set members or sorted set scores of the key
  // selected in the Database tree, each with the affordances that are one
  // honest statement.
  //
  // It stands beside ReplyValue rather than inside it. ReplyValue renders any
  // reply at any depth and knows nothing about where it came from, which is
  // exactly right for a reply tree and exactly wrong for editing: an element
  // can only be written to when the pane knows which key it belongs to and
  // which position within it, and that provenance stops at the top level. So
  // this component owns the top-level rows and their affordances, and hands
  // each element's *value* straight back to ReplyValue — which is why a hash
  // field holding JSON still grows its own Raw | JSON toggle here, unchanged.
  //
  // The row idiom is ReplyValue's own, deliberately: same font-mono body, same
  // border-b divider, same name column at text-muted and index column at
  // text-subtle. Editing enters the way the grid's own cells do (ResultsTable,
  // RecordPane): a pencil visible at rest at low emphasis, double-click as the
  // gesture people arrive with, Enter commits, Escape closes. An element that
  // cannot be edited or deleted in one statement says why in a muted title
  // where its button would be, rather than showing a button that refuses.
  //
  // Nothing is written from here. A commit hands the built statement to
  // `onWrite`, which is the Explorer's route to RunQuery and the Inline
  // Confirm; the draft itself lives in `edits` (valueEdits.svelte.ts), because
  // the confirm takes the panel and unmounts this component while it is up.
  import ReplyValue from "./ReplyValue.svelte";
  import type { ValueEdits } from "./valueEdits.svelte";
  import {
    removeRefusal,
    writeRefusal,
    type Element,
    type Slot,
  } from "./mutateValue";

  let {
    elements,
    edits,
    onWrite,
    onRemove,
  }: {
    /** The key's rows, as mutateValue's browsedElements read them. */
    elements: Element[];
    /** The open editor and its draft, lifted so a confirm cannot take it away. */
    edits: ValueEdits;
    /** Commits the box's text against this slot. */
    onWrite: (slot: Slot, text: string) => void;
    /** Removes this slot — one statement, or the button is not there. */
    onRemove: (slot: Slot) => void;
  } = $props();

  // A row's identity for the open-editor state. Its own name rather than its
  // position: a hash's rows are sorted by field name and a re-read can move a
  // row without changing which element it is.
  function idOf(slot: Slot): string {
    switch (slot.at) {
      case "value":
        return "value";
      case "field":
        return `field:${slot.field}`;
      case "index":
        return `index:${slot.index}`;
      case "member":
        return `member:${slot.member}`;
      case "score":
        return `score:${slot.member}`;
    }
  }

  function begin(element: Element) {
    if (writeRefusal(element.slot) !== null) return;
    edits.begin(idOf(element.slot), element.text);
  }

  function commit(element: Element) {
    onWrite(element.slot, edits.draft);
  }

  // Enter commits, as it does in the grid's own cells. Shift-Enter is the way
  // to put a newline in — the box has to be a textarea rather than an input
  // for the reason below, and a textarea whose Enter did nothing but commit
  // would be a box that cannot type the one character it exists to carry.
  function handleKeydown(event: KeyboardEvent, element: Element) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      commit(element);
    } else if (event.key === "Escape") {
      // Stopped as well as prevented: Escape also closes the record pane and
      // clears a row selection higher up, and leaving a box should not also
      // do that.
      event.preventDefault();
      event.stopPropagation();
      edits.cancel();
    }
  }

  // Editing a value almost always means replacing it, so the box opens
  // selected — the contract ResultsTable's and RecordPane's own editors keep.
  // It grows to what it holds, up to a few lines, because a Redis value is
  // arbitrary text and a one-line box would hide most of a long one.
  function openEditor(node: HTMLTextAreaElement) {
    node.focus();
    node.select();
    resize(node);
  }

  function resize(node: HTMLTextAreaElement) {
    node.style.height = "auto";
    node.style.height = `${Math.min(node.scrollHeight, 160)}px`;
  }
</script>

<div class="flex flex-col">
  {#if elements.length === 0}
    <p class="px-3 py-1.5 text-sm text-text-subtle italic">(empty)</p>
  {/if}

  {#each elements as element (idOf(element.slot))}
    {@const id = idOf(element.slot)}
    {@const open = edits.open === id}
    {@const cannotWrite = writeRefusal(element.slot)}
    {@const cannotRemove = removeRefusal(element.slot)}
    <!-- Double-click is the gesture people arrive with; the pencil beside the
         value is the discoverable one, and it is the a11y route since it is a
         real button. -->
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_static_element_interactions -->
    <div
      class="group flex items-start gap-3 border-b border-border/60 px-3 py-1.5 font-mono text-base"
      ondblclick={() => begin(element)}
    >
      <!-- A list's positions are numbers and read right-aligned like the
           reply tree's own index column; every other key's rows are named,
           and read left like its map keys. -->
      {#if element.slot.at === "index"}
        <span class="w-8 shrink-0 text-right tabular-nums text-text-subtle">{element.label}</span>
      {:else}
        <span class="w-32 shrink-0 truncate text-text-muted" title={element.label}>
          {element.label}
        </span>
      {/if}

      <div class="min-w-0 flex-1">
        {#if open}
          <!-- A textarea rather than an input, and not for room: a Redis value
               can hold a newline, and an <input> silently drops one out of the
               value it is given. Editing a value that had a newline in an
               input would quietly offer to save it without — the exact class
               of thing this whole path exists not to do. -->
          <textarea
            class="box-border block w-full resize-none rounded-control border border-accent bg-surface px-2 py-1 font-mono text-base leading-5 text-text select-text focus:outline-none"
            rows="1"
            value={edits.draft}
            oninput={(event) => {
              edits.draft = event.currentTarget.value;
              resize(event.currentTarget);
            }}
            use:openEditor
            onkeydown={(event) => handleKeydown(event, element)}
            onclick={(event) => event.stopPropagation()}
            ondblclick={(event) => event.stopPropagation()}
            spellcheck="false"
            autocapitalize="off"
            autocomplete="off"
            aria-label="Edit {element.label}"
          ></textarea>
        {:else}
          <ReplyValue reply={element.value} depth={1} />
        {/if}
      </div>

      <!-- Beside the value, never over it, and holding their place whether or
           not they are showing — the same layout RecordPane's own buttons
           keep, so a hover never reflows the row it is hovering. -->
      <div class="flex shrink-0 items-center gap-1">
        {#if open}
          <button
            type="button"
            class="flex h-6 items-center rounded-control bg-accent px-2 text-xs font-medium text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
            disabled={edits.saving}
            onmousedown={(event) => event.preventDefault()}
            onclick={() => commit(element)}
            title="Build the statement and take it to the Approval Gate (⏎ · shift-⏎ for a newline)"
          >
            Save
          </button>
          <button
            type="button"
            class="flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
            onmousedown={(event) => event.preventDefault()}
            onclick={() => edits.cancel()}
          >
            Cancel
          </button>
        {:else}
          {#if cannotWrite === null}
            <!-- Visible at rest, at low emphasis, so the edit gesture is
                 something a first-time visitor sees rather than something only
                 a hover discovers. -->
            <button
              type="button"
              class="flex h-6 w-6 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
              onclick={() => begin(element)}
              title={element.slot.at === "score"
                ? "Edit this score"
                : "Edit this value"}
              aria-label="Edit {element.label}"
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
          {:else}
            <!-- The refusal where the pencil would be: a reason a hover can
                 read, rather than a button that says no when pressed. -->
            <span
              class="flex h-6 w-6 items-center justify-center text-text-subtle"
              title={cannotWrite}
            >
              <svg
                class="h-3.5 w-3.5"
                viewBox="0 0 12 12"
                fill="none"
                stroke="currentColor"
                stroke-width="1.2"
                stroke-linecap="round"
                aria-hidden="true"
              >
                <circle cx="6" cy="6" r="4.25" />
                <path d="M3 9 9 3" />
              </svg>
            </span>
          {/if}

          {#if cannotRemove === null}
            <button
              type="button"
              class="flex h-6 w-6 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 hover:border-danger/50 hover:text-danger"
              onclick={() => onRemove(element.slot)}
              title={element.slot.at === "field"
                ? "Delete this field"
                : "Delete this member"}
              aria-label="Delete {element.label}"
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
                <path d="M2.5 3.5h7M5 3.5V2.25h2V3.5M3.5 3.5l.4 6h4.2l.4-6" />
              </svg>
            </button>
          {:else}
            <!-- The refusal where the delete would be, revealed on hover
                 exactly as the delete itself is — an invisible placeholder
                 would hold the layout but tell nobody why the button they
                 expected is missing. -->
            <span
              class="flex h-6 w-6 items-center justify-center text-text-subtle opacity-0 transition-opacity group-hover:opacity-100"
              title={cannotRemove}
            >
              <svg
                class="h-3.5 w-3.5"
                viewBox="0 0 12 12"
                fill="none"
                stroke="currentColor"
                stroke-width="1.2"
                stroke-linecap="round"
                aria-hidden="true"
              >
                <circle cx="6" cy="6" r="4.25" />
                <path d="M3 9 9 3" />
              </svg>
            </span>
          {/if}
        {/if}
      </div>
    </div>
  {/each}
</div>
