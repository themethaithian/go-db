<script lang="ts">
  // Renders a QueryResult's Documents arm — a list of JSON documents
  // (ADR-0006), for MongoDB — in the results pane, in place of ResultsTable's
  // grid and ValueView's reply tree. Holds no knowledge of how the query ran,
  // the same contract those two keep.
  //
  // db.DocumentSet, Wails' generated binding for the wire shape, types
  // `documents` as `number[][]`. That is the generator reading
  // json.RawMessage's underlying Go type ([]byte, whose element is a numeric
  // byte) rather than its custom MarshalJSON — and RawMessage's MarshalJSON
  // does something different: it splices the document's own JSON bytes
  // straight into the response tree rather than wrapping or encoding them
  // (see internal/db/result.go's DocumentSet doc comment for why that was
  // chosen). So by the time the Wails runtime's own JSON.parse has run over
  // the response, each element of `documents` here is already the parsed
  // value that document's JSON held — a plain object, for every document
  // this app renders. The local Documents type below says what actually
  // crosses the boundary rather than repeating the generated (and
  // misleading) one; renderDocument still copes if a raw JSON string ever
  // arrives instead, as a defensive fallback rather than the expected path.
  //
  // Each document gets one card: its JSON, pretty-printed at 2-space indent
  // in the results grid's own mono font (ResultsTable, ReplyValue). No
  // syntax colouring — a document is not code, and plain mono reads as
  // clearly as anything trivial to add here would (muted keys vs. values is
  // not worth the CSS for a JSON.stringify block with no key/value markup of
  // its own to hang a class on).
  //
  // `message` is the backend's own summary line ("3 documents.", "1
  // document.") — shown in the footer, the same position and style
  // ValueView's own footer occupies. The truncated pill beside it is
  // ResultsTable's own notice, unchanged.
  type Documents = { documents: unknown[]; truncated: boolean };

  let { documents, message }: { documents: Documents; message: string } = $props();

  function renderDocument(doc: unknown): string {
    if (typeof doc === "string") {
      try {
        return JSON.stringify(JSON.parse(doc), null, 2);
      } catch {
        return doc;
      }
    }
    return JSON.stringify(doc, null, 2);
  }
</script>

<div class="flex min-h-0 flex-1 flex-col">
  {#if documents.documents.length === 0}
    <div class="m-auto flex flex-col items-center gap-1 px-6 py-8 text-center">
      <p class="text-base font-medium text-text">0 documents</p>
      <p class="text-sm text-text-muted">The query ran fine — it just matched nothing.</p>
    </div>
  {:else}
    <div class="min-h-0 flex-1 overflow-auto">
      <div class="flex flex-col gap-2 p-3">
        {#each documents.documents as doc, i (i)}
          <pre
            class="overflow-x-auto rounded-control border border-border bg-surface px-3 py-2 font-mono text-sm text-text">{renderDocument(
              doc,
            )}</pre>
        {/each}
      </div>
    </div>
  {/if}

  <div
    class="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2 text-xs text-text-muted"
  >
    <span class="min-w-0 truncate">{message}</span>
    {#if documents.truncated}
      <span
        class="shrink-0 rounded-full border border-warning/40 bg-warning/10 px-1.5 py-px font-medium text-warning"
        title="The backend capped this result set"
      >
        truncated
      </span>
    {/if}
  </div>
</div>
