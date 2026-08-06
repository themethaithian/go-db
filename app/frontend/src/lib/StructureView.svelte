<script lang="ts">
  // The Explorer's Structure view: a table's columns and indexes, read from
  // the same schema cache the Database tree populates (schema.svelte.ts) —
  // so a column expanded in the tree and the same column shown here can never
  // disagree. This component only renders what it is given; fetching and
  // refetching is ExplorerView's job, the same split ResultsTable has with
  // the query it renders.
  import type { TableNode } from "./schema.svelte";

  let { node }: { node: TableNode | null } = $props();

  // PRIMARY first, then everything else in the order ListIndexes returned it
  // (alphabetical by index_name, which does not put PRIMARY first under a
  // case-insensitive collation) — a stable partition floats it to the top
  // without disturbing the rest.
  function sortedIndexes(indexes: NonNullable<TableNode["indexes"]>) {
    return [...indexes.filter((i) => i.primary), ...indexes.filter((i) => !i.primary)];
  }

  function indexChip(index: { primary: boolean; unique: boolean }): {
    text: string;
    className: string;
  } {
    if (index.primary) {
      return { text: "PRIMARY", className: "border-accent/40 bg-accent/10 text-accent" };
    }
    if (index.unique) {
      return { text: "UNIQUE", className: "border-success/40 bg-success/10 text-success" };
    }
    return { text: "INDEX", className: "border-border bg-surface-raised text-text-subtle" };
  }
</script>

<div class="min-h-0 flex-1 overflow-auto p-4">
  {#if node === null}
    <p class="text-base text-text-muted">Loading structure…</p>
  {:else}
    <section class="mb-6">
      <h3 class="mb-2 text-xs font-medium tracking-wide text-text-subtle uppercase">Columns</h3>
      {#if node.error !== null}
        <p
          class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
        >
          {node.error}
        </p>
      {:else if node.columns === null}
        <p class="text-base text-text-muted">Loading columns…</p>
      {:else if node.columns.length === 0}
        <p class="text-base text-text-muted">No columns.</p>
      {:else}
        <div class="overflow-hidden rounded-control border border-border">
          <table class="w-full min-w-max border-collapse text-left">
            <thead>
              <tr>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Name
                </th>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Type
                </th>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Nullable
                </th>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Key
                </th>
              </tr>
            </thead>
            <tbody class="text-base">
              {#each node.columns as column (column.name)}
                <tr class="border-b border-border/60 last:border-b-0">
                  <td class="px-3 py-1.5 font-mono text-text">{column.name}</td>
                  <td class="px-3 py-1.5 font-mono text-text-muted">{column.data_type}</td>
                  <td class="px-3 py-1.5 text-text-muted">{column.nullable ? "yes" : "—"}</td>
                  <td class="px-3 py-1.5">
                    {#if column.key === ""}
                      <span class="text-text-subtle">—</span>
                    {:else}
                      <span
                        class="inline-flex rounded-full border px-1.5 py-px font-mono text-xs font-medium {column.key ===
                        'PRI'
                          ? 'border-accent/40 bg-accent/10 text-accent'
                          : 'border-border bg-surface-raised text-text-subtle'}"
                      >
                        {column.key}
                      </span>
                    {/if}
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <section>
      <h3 class="mb-2 text-xs font-medium tracking-wide text-text-subtle uppercase">Indexes</h3>
      {#if node.indexesError !== null}
        <p
          class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
        >
          {node.indexesError}
        </p>
      {:else if node.indexes === null}
        <p class="text-base text-text-muted">Loading indexes…</p>
      {:else if node.indexes.length === 0}
        <p class="text-base text-text-muted">No indexes.</p>
      {:else if node.indexes.length === 1 && node.indexes[0].primary}
        <p class="text-base text-text-muted">
          No indexes beyond the primary key ({node.indexes[0].columns.join(", ")}).
        </p>
      {:else}
        <div class="overflow-hidden rounded-control border border-border">
          <table class="w-full min-w-max border-collapse text-left">
            <thead>
              <tr>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Name
                </th>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Type
                </th>
                <th
                  class="border-b border-border bg-surface-raised px-3 py-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
                >
                  Columns
                </th>
              </tr>
            </thead>
            <tbody class="text-base">
              {#each sortedIndexes(node.indexes) as index (index.name)}
                {@const chip = indexChip(index)}
                <tr class="border-b border-border/60 last:border-b-0">
                  <td class="px-3 py-1.5 font-mono text-text">{index.name}</td>
                  <td class="px-3 py-1.5">
                    <span
                      class="inline-flex rounded-full border px-1.5 py-px font-mono text-xs font-medium {chip.className}"
                    >
                      {chip.text}
                    </span>
                  </td>
                  <td class="px-3 py-1.5 font-mono text-text-muted">({index.columns.join(", ")})</td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>
  {/if}
</div>
