<script lang="ts">
  // Renders one already-parsed JSON value as an interactive tree — the
  // MongoDB Documents arm's own view of a document (DocumentsView.svelte)
  // and, wherever a Redis string parses as JSON (jsonReply.ts), the JSON
  // side of ReplyValue's and ValueView's Raw | JSON toggle. Both callers
  // hand this component a plain JS value already run through JSON.parse —
  // it holds no opinion on where that value came from.
  //
  // The row and indent language is ReplyValue.svelte's own: one left border
  // and one indent per nesting level, entry rows divided by border-b, an
  // array's own index column at text-subtle, a map's own key column at
  // text-muted — so a Redis reply tree and a JSON tree read as the same
  // kind of thing when they sit side by side in the results pane.
  //
  // Colour is the one thing ReplyValue's plain text-on-text never needed:
  // a JSON document has keys, strings, numbers, booleans and null all in
  // view together, and app.css already has four syntax tokens (built for
  // SqlEditor's CodeMirror theme) plus the muted/subtle text tokens this
  // app already reads "de-emphasised" from elsewhere. No new colour is
  // introduced — the mapping below only routes to what SqlEditor and
  // ReplyValue already established:
  //   - keys      → text-muted        (ReplyValue's own map-key colour)
  //   - strings   → text-syntax-string
  //   - numbers   → text-syntax-number, tabular-nums (ReplyValue's own
  //                 treatment for integer/double)
  //   - booleans  → text-syntax-keyword (a literal, the same family
  //                 SqlEditor colours SQL's own keywords)
  //   - null      → text-subtle italic (ReplyValue's own "(nil)" treatment,
  //                 exactly, since both mean the same absence)
  //   - punctuation (braces/brackets/colons) → text-subtle, so it never
  //                 competes with the values it is only there to frame
  //
  // Expansion defaults to the top DEFAULT_EXPANDED_DEPTH levels open and
  // everything past that collapsed — enough to see a document's shape
  // without opening every node of a deep one. A collapsed object/array
  // never renders as nothing: it shows `{…} 5 keys` / `[…] 12 items`, so
  // collapsing hides a subtree's contents but never how big it was.
  //
  // A string past STRING_TRUNCATE_AT truncates with its own "show more"
  // toggle rather than relying on a title attribute — a title is invisible
  // until hovered and useless for anything long enough to need scrolling.
  //
  // Every object/array row grows a small copy-this-subtree button on
  // hover, the same reveal RecordPane's own copy buttons use; the root
  // call (DocumentsView's one per card, ValueView's one per JSON string)
  // is just this same button at depth 0, not a separate affordance.
  //
  // Recurses through its own import — Svelte 5's replacement for
  // <svelte:self>, the same trick ReplyValue.svelte already uses.
  import { untrack } from "svelte";
  import JsonTree from "./JsonTree.svelte";

  let { value, depth = 0 }: { value: unknown; depth?: number } = $props();

  const DEFAULT_EXPANDED_DEPTH = 2;
  const STRING_TRUNCATE_AT = 300;
  const COPIED_MS = 1200;

  type Kind = "object" | "array" | "string" | "number" | "boolean" | "null";

  function kindOf(v: unknown): Kind {
    if (v === null) return "null";
    if (Array.isArray(v)) return "array";
    const t = typeof v;
    if (t === "string" || t === "number" || t === "boolean") return t;
    // Anything else parsed JSON can hand back (a plain object) — the only
    // remaining case typeof reports for a JSON.parse result.
    return "object";
  }

  let kind = $derived(kindOf(value));
  let isContainer = $derived(kind === "object" || kind === "array");

  let objectEntries = $derived(
    kind === "object" ? Object.entries(value as Record<string, unknown>) : [],
  );
  let arrayItems = $derived(kind === "array" ? (value as unknown[]) : []);
  let count = $derived(kind === "object" ? objectEntries.length : arrayItems.length);

  // Collapsed past the default depth. A node's own depth never changes
  // after it mounts, so this is a plain $state seeded once from it — via
  // untrack, so the seed is read without also becoming a dependency this
  // state would otherwise (uselessly) re-derive from — rather than a
  // $derived that would fight a click's own toggle on every re-render.
  let expanded = $state(untrack(() => depth < DEFAULT_EXPANDED_DEPTH));

  let summary = $derived(`${count} ${count === 1 ? (kind === "object" ? "key" : "item") : kind === "object" ? "keys" : "items"}`);

  let isLongString = $derived(kind === "string" && (value as string).length > STRING_TRUNCATE_AT);
  let stringExpanded = $state(false);
  let stringText = $derived(kind === "string" ? (value as string) : "");
  let stringShown = $derived(
    isLongString && !stringExpanded ? stringText.slice(0, STRING_TRUNCATE_AT) : stringText,
  );

  // Copy feedback for this node only — the tick shows on the button that
  // was pressed, not as a toast, the same contract RecordPane's own copy
  // buttons keep.
  let copied = $state(false);
  let copyTimer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => () => {
    if (copyTimer !== null) clearTimeout(copyTimer);
  });

  async function copyValue(event: MouseEvent) {
    // A copy button living inside a row must not also fire whatever the
    // row itself does on click — there is nothing here yet, but a stray
    // click reaching an ancestor's own handler later is exactly the kind
    // of bug that shows up only after this component is reused somewhere
    // new. Stopping it here costs nothing today and saves that bug.
    event.stopPropagation();
    try {
      await navigator.clipboard.writeText(JSON.stringify(value, null, 2));
    } catch {
      // A refused clipboard is worth no feedback at all — claiming
      // "copied" for something that never reached the clipboard is the one
      // outcome worth avoiding.
      return;
    }
    copied = true;
    if (copyTimer !== null) clearTimeout(copyTimer);
    copyTimer = setTimeout(() => (copied = false), COPIED_MS);
  }
</script>

{#snippet copyButton()}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border opacity-0 transition-opacity group-hover:opacity-100 focus-visible:opacity-100 {copied
      ? 'border-success/40 bg-surface-raised text-success opacity-100'
      : 'border-border bg-surface-raised text-text-subtle hover:border-border-strong hover:text-text'}"
    onclick={copyValue}
    title="Copy this JSON"
    aria-label="Copy this JSON"
  >
    {#if copied}
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
        <path d="M2.5 6.25 5 8.75l4.5-5.5" />
      </svg>
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
{/snippet}

{#if isContainer && count === 0}
  <div class="group flex items-center gap-1.5 px-3 py-1.5 font-mono text-base">
    <span class="text-text-subtle italic">{kind === "object" ? "{}" : "[]"}</span>
    {@render copyButton()}
  </div>
{:else if isContainer}
  <div class="flex flex-col {depth > 0 ? 'border-l border-border/60 pl-3' : ''}">
    <div class="group flex items-center gap-1.5 px-3 py-1.5 font-mono text-base">
      <button
        type="button"
        class="flex h-5 w-4 shrink-0 items-center justify-center text-text-subtle transition-colors hover:text-text"
        onclick={() => (expanded = !expanded)}
        aria-label={expanded ? "Collapse" : "Expand"}
        aria-expanded={expanded}
      >
        <svg
          class="h-3 w-3 transition-transform {expanded ? 'rotate-90' : ''}"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          stroke-width="1.5"
          stroke-linecap="round"
          stroke-linejoin="round"
          aria-hidden="true"
        >
          <path d="M4.5 2.5 8 6l-3.5 3.5" />
        </svg>
      </button>
      {#if expanded}
        <span class="text-text-subtle">{kind === "object" ? "{" : "["}</span>
      {:else}
        <span class="text-text-subtle">{kind === "object" ? "{…}" : "[…]"}</span>
        <span class="text-text-subtle italic">{summary}</span>
      {/if}
      {@render copyButton()}
    </div>
    {#if expanded}
      {#if kind === "object"}
        {#each objectEntries as [k, v], i (i)}
          <div class="flex items-start gap-3 border-b border-border/60 px-3 py-1.5 font-mono text-base">
            <span class="w-32 shrink-0 truncate text-text-muted" title={k}>{k}</span>
            <div class="min-w-0 flex-1"><JsonTree value={v} depth={depth + 1} /></div>
          </div>
        {/each}
      {:else}
        {#each arrayItems as v, i (i)}
          <div class="flex items-start gap-3 border-b border-border/60 px-3 py-1.5 font-mono text-base">
            <span class="w-8 shrink-0 text-right tabular-nums text-text-subtle">{i}</span>
            <div class="min-w-0 flex-1"><JsonTree value={v} depth={depth + 1} /></div>
          </div>
        {/each}
      {/if}
      <div class="px-3 py-1 font-mono text-base text-text-subtle">{kind === "object" ? "}" : "]"}</div>
    {/if}
  </div>
{:else if kind === "string"}
  <span class="text-syntax-string break-all">"{stringShown}{#if isLongString && !stringExpanded}…{/if}"</span>
  {#if isLongString}
    <button
      type="button"
      class="ml-1 align-baseline text-xs font-medium text-accent hover:text-accent-hover"
      onclick={() => (stringExpanded = !stringExpanded)}
    >
      {stringExpanded ? "show less" : "show more"}
    </button>
  {/if}
{:else if kind === "number"}
  <span class="tabular-nums text-syntax-number">{value}</span>
{:else if kind === "boolean"}
  <span class="text-syntax-keyword">{String(value)}</span>
{:else}
  <span class="text-text-subtle italic">null</span>
{/if}
