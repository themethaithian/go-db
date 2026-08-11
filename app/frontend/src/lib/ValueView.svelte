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
  import ReplyValue from "./ReplyValue.svelte";
  import type { db } from "../../wailsjs/go/models";

  let { value, message }: { value: db.Reply; message: string } = $props();

  const SCALAR_KINDS = new Set(["string", "integer", "double", "boolean", "nil", "error"]);
  let isScalar = $derived(SCALAR_KINDS.has(value.kind));

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
  {#if isScalar}
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
