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
  import ReplyValue from "./ReplyValue.svelte";
  import JsonTree from "./JsonTree.svelte";
  import RawJsonToggle from "./RawJsonToggle.svelte";
  import { parseJsonReply, JSON_REPLY_THRESHOLD } from "./jsonReply";
  import type { db } from "../../wailsjs/go/models";

  let { value, message }: { value: db.Reply; message: string } = $props();

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

<div class="flex min-h-0 flex-1 flex-col">
  {#if showJsonToggle}
    <div class="flex min-h-0 flex-1 flex-col">
      <div class="flex shrink-0 items-center justify-center gap-2 border-b border-border/60 px-3 py-2">
        <RawJsonToggle bind:mode />
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
    <div class="m-auto flex flex-col items-center gap-1 px-6 py-8 text-center">
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
    </div>
  {:else}
    <div class="min-h-0 flex-1 overflow-auto">
      <ReplyValue reply={value} />
    </div>
  {/if}

  <div
    class="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2 text-xs text-text-muted"
  >
    <span class="min-w-0 truncate">{message}</span>
  </div>
</div>
