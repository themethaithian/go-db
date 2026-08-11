<script lang="ts">
  // Renders a QueryResult's Value arm — one Redis reply tree (ADR-0006) — in
  // the results pane, in place of ResultsTable's grid. Holds no knowledge of
  // how the query ran, the same contract ResultsTable keeps.
  //
  // A scalar reply (string, integer, double, boolean) is shown as one
  // left-aligned row in the results' own visual language — mono, at
  // ResultsTable's size, wrapping when it is long — rather than as a large
  // centred line. Centring is the empty state's treatment ("0 rows", "(nil)"),
  // and borrowing it for a value that is *there* made a two-character answer
  // look like an announcement and a long one look like a wall. The two
  // treatments that stay centred are the two that are genuinely absences or
  // failures: nil and error. An array or map hands off to ReplyValue for the
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
  import { patchedAt, prunedAt, type JsonEditing } from "./jsonFields";
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

  // Per-field editing on the JSON side of the toggle.
  //
  // A Redis key holding JSON holds one string, and Redis has no command that
  // writes part of one (RedisJSON's JSON.SET does, and this app does not
  // assume the module is loaded). So a field edit here patches the parsed
  // value and writes the whole string back through the same SET the Edit
  // button builds — one statement, and one whose Inline Confirm shows the
  // string that will be stored. The caveat says so before it is confirmed, in
  // the pencil's own title, rather than leaving the human to notice it in the
  // preview.
  //
  // Everything else is the tree's: the box, the draft, the literal parsing
  // and the reading hint are JsonTree's and are shared with the Documents arm
  // exactly as they stand.
  let jsonEditing = $derived.by((): JsonEditing | null => {
    const parsed = jsonValue;
    if (!stringWritable || edits === null || parsed === undefined) return null;
    return {
      edits,
      scope: "json",
      onSet: (path, next) => onWrite?.(VALUE_SLOT, JSON.stringify(patchedAt(parsed, path, next))),
      onRemove: (path) => onWrite?.(VALUE_SLOT, JSON.stringify(prunedAt(parsed, path))),
      caveat: () =>
        "this key holds one JSON string, and Redis writes a string whole, so the SET carries the whole value",
    };
  });

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

{#snippet scalarRow(withEdit: boolean)}
  <!-- One value, read left to right at the size everything else in the
       results pane is read at. It wraps rather than scrolls: a Redis string
       is arbitrary text, and a value that ran off the right edge would need
       a horizontal scrollbar to read a sentence. The Edit affordance sits
       beside it — on the toggle's own bar when there is one, so it is not
       offered twice. -->
  <div class="flex items-start gap-2 px-3 py-2">
    <span
      class="min-w-0 flex-1 font-mono text-base leading-5 tabular-nums whitespace-pre-wrap break-all text-text select-text"
    >
      {scalarText(value)}
    </span>
    {#if withEdit && stringWritable}
      <div class="shrink-0">{@render editButton()}</div>
    {/if}
  </div>
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
      <div class="flex shrink-0 items-center gap-2 border-b border-border/60 px-3 py-2">
        <RawJsonToggle bind:mode />
        {#if stringWritable}{@render editButton()}{/if}
      </div>
      {#if mode === "json"}
        <div class="min-h-0 flex-1 overflow-auto">
          <JsonTree value={jsonValue} edit={jsonEditing} />
        </div>
      {:else}
        <div class="min-h-0 flex-1 overflow-auto">{@render scalarRow(false)}</div>
      {/if}
    </div>
  {:else if value.kind === "nil"}
    <!-- An absence, and centring is what this app says absence with. -->
    <div class="m-auto px-6 py-8 text-center">
      <p class="font-mono text-lg text-text-subtle italic">(nil)</p>
    </div>
  {:else if value.kind === "error"}
    <div class="m-auto px-6 py-8 text-center">
      <p
        class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
      >
        {scalarText(value)}
      </p>
    </div>
  {:else if isScalar}
    <div class="min-h-0 flex-1 overflow-auto">{@render scalarRow(true)}</div>
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
