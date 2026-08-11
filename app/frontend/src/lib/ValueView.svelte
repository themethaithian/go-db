<script lang="ts">
  // Renders a QueryResult's Value arm — one Redis reply tree (ADR-0006) — in
  // the results pane, in place of ResultsTable's grid. Holds no knowledge of
  // how the query ran, the same contract ResultsTable keeps.
  //
  // A scalar reply (string, integer, double, boolean, nil, error) is shown as
  // one prominent centred line, the same treatment ResultsTable gives its own
  // "0 rows" empty state. An array or map hands off to ReplyValue for the
  // index/key + value list.
  //
  // `message` is the backend's own summary line ("1 value: a list of 2
  // items.") — it is shown in the footer, the same position and style
  // ResultsTable's row-range line occupies, so a human's eye finds a result's
  // headline in the same place whichever Engine produced it.
  //
  // A string reply that parses as JSON (jsonReply.ts — object/array-shaped
  // text only, so a plain numeric string like "123" is never hijacked) gets
  // its own branch: a small Raw | JSON toggle above the value, defaulting to
  // JSON once the string runs past JSON_REPLY_THRESHOLD (RedisInsight's own
  // rule — short JSON-shaped strings read fine as text, long ones read
  // better as a tree) and to Raw below it. Either way the toggle stays, so a
  // human can always see the string exactly as the server sent it.
  //
  // Editing is the Explorer's alone, and only for a key it browsed. Given
  // `edits`, a `type` and the two callbacks, the values here can be typed
  // over: the string branch grows an Edit button, and a hash, list, set or
  // sorted set hands off to ReplyEditor for its per-element affordances.
  // Given none of them — the Editor, which shows the Value arm for whatever a
  // typed command returned — everything below is exactly as read-only as it
  // was before. That is not a limitation of this component but of what can be
  // known: an edit needs the key it belongs to, and the Explorer's selection
  // is the only place that provenance exists (see EditorView's own note).
  //
  // Nothing here writes. A commit hands a built statement to `onWrite`, whose
  // route is RunQuery and the Inline Confirm, exactly as a typed command's is.
  import ReplyValue from "./ReplyValue.svelte";
  import ReplyEditor from "./ReplyEditor.svelte";
  import JsonTree from "./JsonTree.svelte";
  import RawJsonToggle from "./RawJsonToggle.svelte";
  import { parseJsonReply, JSON_REPLY_THRESHOLD } from "./jsonReply";
  import { browsedElements, type Slot } from "./mutateValue";
  import type { ValueEdits } from "./valueEdits.svelte";
  import type { db } from "../../wailsjs/go/models";

  let {
    value,
    message,
    edits = null,
    type = null,
    onWrite = null,
    onRemove = null,
  }: {
    value: db.Reply;
    message: string;
    /** The open editor and its draft. Null — the Editor — means read-only. */
    edits?: ValueEdits | null;
    /** The browsed key's Redis type, which is what says how this reply is laid out. */
    type?: string | null;
    /** Commits an element's text as a statement. */
    onWrite?: ((slot: Slot, text: string) => void) | null;
    /** Removes one element, where that is one statement. */
    onRemove?: ((slot: Slot) => void) | null;
  } = $props();

  const SCALAR_KINDS = new Set(["string", "integer", "double", "boolean", "nil", "error"]);
  let isScalar = $derived(SCALAR_KINDS.has(value.kind));

  let jsonValue = $derived(value.kind === "string" ? parseJsonReply(value.text ?? "") : undefined);
  let showJsonToggle = $derived(jsonValue !== undefined);

  // Raw below the threshold, JSON at or above it — recomputed whenever the
  // value itself changes (a new query result must not inherit the previous
  // one's toggle state), but freely overridable afterwards by the toggle.
  let mode = $state<"raw" | "json">("raw");
  $effect(() => {
    const text = value.text ?? "";
    mode = jsonValue !== undefined && text.length >= JSON_REPLY_THRESHOLD ? "json" : "raw";
  });

  // Whether this pane was given everything an edit needs. All four or none:
  // a pencil with no route to the gate would be a button that does nothing.
  let writable = $derived(edits !== null && type !== null && onWrite !== null && onRemove !== null);

  // The key's rows, when its reply is the shape its type browses as. Null is
  // the honest "not a shape this pane can address" and falls through to the
  // read-only tree below — see browsedElements.
  let elements = $derived(
    writable && type !== null ? browsedElements(type, value) : null,
  );

  // The one editable scalar: a string key's whole value. Every other scalar
  // reply reaching this pane is an answer rather than a key's contents.
  const VALUE_SLOT: Slot = { at: "value" };
  let stringWritable = $derived(writable && type === "string" && value.kind === "string");
  let editingString = $derived(stringWritable && edits?.open === "value");

  function beginString() {
    edits?.begin("value", value.text ?? "");
  }

  function commitString() {
    if (edits !== null) onWrite?.(VALUE_SLOT, edits.draft);
  }

  function handleStringKeydown(event: KeyboardEvent) {
    if (event.key === "Enter" && !event.shiftKey) {
      event.preventDefault();
      commitString();
    } else if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      edits?.cancel();
    }
  }

  function openEditor(node: HTMLTextAreaElement) {
    node.focus();
    node.select();
  }

  function scalarText(node: db.Reply): string {
    switch (node.kind) {
      case "string":
        return node.text ?? "";
      case "integer":
        return String(node.integer ?? 0);
      case "double":
        return String(node.double ?? 0);
      case "boolean":
        return node.boolean ? "true" : "false";
      case "error":
        return node.text ?? "";
      default:
        return "";
    }
  }
</script>

{#snippet editButton()}
  <button
    type="button"
    class="inline-flex h-7 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-sm font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
    onclick={beginString}
    title="Edit this value"
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
    Edit
  </button>
{/snippet}

<div class="flex min-h-0 flex-1 flex-col">
  {#if editingString}
    <!-- The string branch's own box. It takes the pane rather than sitting in
         a row, because a string key's value *is* the pane's whole contents —
         the same reason the value it replaces is centred and prominent. -->
    <div class="flex min-h-0 flex-1 flex-col gap-2 p-3">
      <textarea
        class="min-h-0 w-full flex-1 resize-none rounded-control border border-accent bg-surface px-3 py-2 font-mono text-base leading-6 text-text select-text focus:outline-none"
        value={edits?.draft ?? ""}
        oninput={(event) => {
          if (edits !== null) edits.draft = event.currentTarget.value;
        }}
        use:openEditor
        onkeydown={handleStringKeydown}
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        aria-label="Edit this value"
      ></textarea>
      <div class="flex shrink-0 items-center gap-2">
        <span class="mr-auto text-xs text-text-subtle">Esc cancels · ⏎ saves · shift-⏎ for a newline</span>
        <button
          type="button"
          class="inline-flex h-7 items-center rounded-control border border-border bg-surface-raised px-2.5 text-sm font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
          onclick={() => edits?.cancel()}
        >
          Cancel
        </button>
        <button
          type="button"
          class="inline-flex h-7 items-center rounded-control bg-accent px-2.5 text-sm font-medium text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
          disabled={edits?.saving ?? false}
          onclick={commitString}
        >
          Save
        </button>
      </div>
    </div>
  {:else if showJsonToggle}
    <div class="flex min-h-0 flex-1 flex-col">
      <div class="flex shrink-0 items-center justify-center gap-2 border-b border-border/60 px-3 py-2">
        <RawJsonToggle bind:mode />
        {#if stringWritable}{@render editButton()}{/if}
      </div>
      {#if mode === "json"}
        <div class="min-h-0 flex-1 overflow-auto">
          <JsonTree value={jsonValue} />
        </div>
      {:else}
        <div class="m-auto flex flex-col items-center gap-1 px-6 py-8 text-center">
          <p class="max-w-full font-mono text-lg font-medium break-all text-text tabular-nums">
            {scalarText(value)}
          </p>
        </div>
      {/if}
    </div>
  {:else if isScalar}
    <div class="m-auto flex flex-col items-center gap-2 px-6 py-8 text-center">
      {#if value.kind === "nil"}
        <p class="font-mono text-lg text-text-subtle italic">(nil)</p>
      {:else if value.kind === "error"}
        <p
          class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
        >
          {scalarText(value)}
        </p>
      {:else}
        <p class="max-w-full font-mono text-lg font-medium break-all text-text tabular-nums">
          {scalarText(value)}
        </p>
      {/if}
      {#if stringWritable}{@render editButton()}{/if}
    </div>
  {:else if elements !== null && edits !== null && onWrite !== null && onRemove !== null}
    <div class="min-h-0 flex-1 overflow-auto">
      <ReplyEditor {elements} {edits} {onWrite} {onRemove} />
    </div>
  {:else}
    <div class="min-h-0 flex-1 overflow-auto">
      <ReplyValue reply={value} />
    </div>
  {/if}

  <div
    class="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2 text-xs text-text-muted"
  >
    <!-- One line, and it says whichever is the more urgent thing to know: why
         the last edit did not happen, or what the read returned. A refusal
         shown beside the summary it displaces would be a refusal competing
         with a sentence about something else. -->
    {#if edits?.failure}
      <span class="min-w-0 truncate text-danger" title={edits.failure}>{edits.failure}</span>
    {:else}
      <span class="min-w-0 truncate">{message}</span>
    {/if}
  </div>
</div>
