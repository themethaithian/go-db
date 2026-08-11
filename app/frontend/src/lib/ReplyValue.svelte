<script lang="ts">
  // Renders one node of a Redis reply tree (db.Reply) — the Value arm of a
  // QueryResult (ADR-0006). A scalar is one inline value; an array is an
  // index + value list and a map a key + value list, each row matching
  // ResultsTable's own font-mono body and border-b divider. A container's
  // own values recurse through this component again — Svelte 5's way of
  // doing what <svelte:self> used to — and each level of nesting adds one
  // left border and one indent, so the tree reads the way MaxReplyDepth
  // already bounds it: the recursion cannot run away because the server
  // never hands over more than 16 levels.
  import type { db } from "../../wailsjs/go/models";
  // A component may import its own file to recurse — Svelte 5's
  // replacement for <svelte:self>.
  import ReplyValue from "./ReplyValue.svelte";

  let { reply, depth = 0 }: { reply: db.Reply; depth?: number } = $props();

  function isContainer(kind: string): boolean {
    return kind === "array" || kind === "map";
  }

  // The text a scalar shows. Kind decides which field is meaningful — see
  // db.Reply's own doc comment on why reading the wrong one is unsafe —
  // "error" and "nil" are handled by the markup below rather than here,
  // since both need their own styling, not just their own text.
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
        return node.kind;
    }
  }
</script>

{#snippet scalar(node: db.Reply)}
  {#if node.kind === "nil"}
    <span class="text-text-subtle italic">(nil)</span>
  {:else if node.kind === "error"}
    <span class="text-danger">{scalarText(node)}</span>
  {:else if node.kind === "integer" || node.kind === "double"}
    <span class="tabular-nums text-text">{scalarText(node)}</span>
  {:else}
    <span class="text-text">{scalarText(node)}</span>
  {/if}
{/snippet}

{#if isContainer(reply.kind)}
  <div class="flex flex-col {depth > 0 ? 'border-l border-border/60 pl-3' : ''}">
    {#if reply.kind === "array"}
      {#if (reply.items ?? []).length === 0}
        <p class="px-3 py-1.5 text-sm text-text-subtle italic">(empty)</p>
      {:else}
        {#each reply.items ?? [] as item, i (i)}
          <div class="flex items-start gap-3 border-b border-border/60 px-3 py-1.5 font-mono text-base">
            <span class="w-8 shrink-0 text-right tabular-nums text-text-subtle">{i}</span>
            <div class="min-w-0 flex-1">
              <ReplyValue reply={item} depth={depth + 1} />
            </div>
          </div>
        {/each}
      {/if}
    {:else if (reply.entries ?? []).length === 0}
      <p class="px-3 py-1.5 text-sm text-text-subtle italic">(empty)</p>
    {:else}
      {#each reply.entries ?? [] as entry, i (i)}
        <div class="flex items-start gap-3 border-b border-border/60 px-3 py-1.5 font-mono text-base">
          <div class="w-32 shrink-0 truncate text-text-muted">
            <ReplyValue reply={entry.key} depth={depth + 1} />
          </div>
          <div class="min-w-0 flex-1">
            <ReplyValue reply={entry.value} depth={depth + 1} />
          </div>
        </div>
      {/each}
    {/if}

    <!-- The container that was cut says so itself (db.Reply.Truncated), in
         the same pill ResultsTable's own truncation notice uses. -->
    {#if reply.truncated}
      <div class="flex items-center gap-1.5 px-3 py-1.5">
        <span
          class="shrink-0 rounded-full border border-warning/40 bg-warning/10 px-1.5 py-px font-medium text-warning"
        >
          truncated
        </span>
        <span class="text-sm text-text-subtle italic">and more…</span>
      </div>
    {/if}
  </div>
{:else}
  {@render scalar(reply)}
{/if}
