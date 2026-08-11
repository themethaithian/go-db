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
  // misleading) one; documentValue still copes if a raw JSON string ever
  // arrives instead, as a defensive fallback rather than the expected path.
  //
  // Each document gets one card: its JSON as an interactive JsonTree
  // (syntax colour, collapsible nesting, per-document copy) rather than a
  // flat pretty-printed block — the same tree ReplyValue's own Raw | JSON
  // toggle renders for a Redis string that turns out to hold JSON, so
  // "readable JSON" means one thing across both Engines.
  //
  // Editing is the Explorer's alone, and only for a collection it browsed.
  // Given `edits`, a `collection` and the callbacks, a card is editable two
  // ways, and the first of them is the one people reach for:
  //
  //   - Per field, in the tree itself. Hover a value, press the pencil, type,
  //     press Enter — one field, one statement, and the statement the human
  //     confirms names exactly that field:
  //     updateOne({"_id": …}, {"$set": {"a.b": …}}). Nothing swaps to raw to
  //     change one value. JsonTree draws it; mutateValue.ts writes it; the
  //     path rules and the fallbacks live in jsonFields.ts.
  //
  //   - Whole document, as before: Edit swaps the tree for a textarea holding
  //     the document pretty-printed, and Save parses it here before anything
  //     is sent — invalid JSON never becomes a call. It is still the right
  //     affordance for a restructuring, and it is the honest one for a field
  //     whose name MongoDB's dotted language cannot address.
  //
  // What is edited either way is the relaxed extended JSON the backend
  // emitted ({"$oid": "…"} and friends), and it re-enters through mongoql's
  // grammar as the plain JSON it is. Given none of the edit props — the
  // Editor — the cards are exactly as read-only as they were before, because
  // an edit needs the collection a document came from and only the Explorer's
  // selection knows it (see EditorView's own note).
  //
  // A document with no _id is not editable at all, by either route: every
  // call built here addresses one document by its own _id, and there is no
  // honest filter without one. _id itself is not editable either, for the
  // neighbouring reason — it is the address, not a field (ID_NOT_EDITABLE).
  //
  // The card chrome is deliberately light. The index sits small in the
  // corner, the document's own _id is surfaced compactly on the header line
  // so a stack of cards is scannable without opening any of them, and the
  // body starts with the fields rather than with a brace — which is why
  // JsonTree is asked for its `bare` rendering here.
  //
  // `message` is the backend's own summary line ("3 documents.", "1
  // document.") — shown in the footer, the same position and style
  // ValueView's own footer occupies. The truncated pill beside it is
  // ResultsTable's own notice, unchanged.
  import CopyJson from "./CopyJson.svelte";
  import JsonTree from "./JsonTree.svelte";
  import {
    ARRAY_ELEMENT_REWRITE,
    documentId,
    DOCUMENT_WITHOUT_ID,
    ID_NOT_EDITABLE,
    WHOLE_DOCUMENT_FALLBACK,
  } from "./mutateValue";
  import {
    dottedPath,
    isFieldBox,
    recogniseExtended,
    type FieldPath,
    type JsonEditing,
  } from "./jsonFields";
  import type { ValueEdits } from "./valueEdits.svelte";

  type Documents = { documents: unknown[]; truncated: boolean };

  let {
    documents,
    message,
    edits = null,
    collection = null,
    onReplace = null,
    onDelete = null,
    onSetField = null,
    onRemoveField = null,
  }: {
    documents: Documents;
    message: string;
    /** The open editor and its draft. Null — the Editor — means read-only. */
    edits?: ValueEdits | null;
    /** The collection these documents were browsed from. */
    collection?: string | null;
    /** Saves the edited document, already parsed, against the _id it came with. */
    onReplace?: ((id: unknown, document: unknown) => void) | null;
    /** Deletes one document by the _id it came with. */
    onDelete?: ((id: unknown) => void) | null;
    /**
     * Writes one field of one document. The document goes with it because the
     * statement is not always a $set — an unaddressable path is written as a
     * patched replaceOne, and patching needs what is being patched.
     */
    onSetField?:
      | ((id: unknown, document: unknown, path: FieldPath, value: unknown) => void)
      | null;
    /** Removes one field or array element of one document. */
    onRemoveField?: ((id: unknown, document: unknown, path: FieldPath) => void) | null;
  } = $props();

  const INDENT = 2;
  const ID_SHOWN = 44;

  let writable = $derived(
    edits !== null && collection !== null && onReplace !== null && onDelete !== null,
  );

  // The per-field affordances need their own two callbacks as well, and all or
  // none: a pencil with no route to the gate would be a button that does
  // nothing.
  let fieldWritable = $derived(writable && onSetField !== null && onRemoveField !== null);

  // The value JsonTree renders for one document. `doc` is normally already
  // the parsed object — see the module comment above for why
  // json.RawMessage's own MarshalJSON hands this component an
  // already-parsed value rather than a string to parse itself. The string
  // branch below is the defensive fallback for a raw JSON string arriving
  // instead: parsed if it holds valid JSON, handed through as-is (JsonTree
  // then renders it as the string it is) if it does not.
  function documentValue(doc: unknown): unknown {
    if (typeof doc !== "string") return doc;
    try {
      return JSON.parse(doc);
    } catch {
      return doc;
    }
  }

  // A card's identity for the open-editor state. Its own _id rather than its
  // position: the position is an index into a result set a save is about to
  // replace, and a re-read can reorder it.
  function idOf(id: unknown): string {
    return `doc:${JSON.stringify(id)}`;
  }

  function begin(value: unknown, id: unknown) {
    edits?.begin(idOf(id), JSON.stringify(value, null, INDENT));
  }

  // Save's whole client-side job: parse. Anything the parser refuses stops
  // here with the parser's own words, and nothing is sent — a call carrying
  // text that is not JSON would be refused by mongoql anyway, one round trip
  // later and in a sentence about a character position rather than about the
  // brace the human forgot.
  function commit(id: unknown) {
    if (edits === null) return;
    let parsed: unknown;
    try {
      parsed = JSON.parse(edits.draft);
    } catch (err) {
      edits.refuse(`That is not JSON go-db can read: ${err instanceof Error ? err.message : String(err)}`);
      return;
    }
    onReplace?.(id, parsed);
  }

  function handleKeydown(event: KeyboardEvent) {
    // No Enter-commits here: a document is many lines and Enter is how they
    // are made. Escape still closes, as it does in every other box.
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    edits?.cancel();
  }

  function openEditor(node: HTMLTextAreaElement) {
    node.focus();
    node.setSelectionRange(0, 0);
  }

  // The document's own _id, compactly, for the card's header line. An
  // ObjectId reads as ObjectId("…") here exactly as it does in the tree —
  // recogniseExtended is the one place that mapping lives.
  function idLabel(id: unknown): string {
    const extended = recogniseExtended(id);
    if (extended !== null) return extended.text;
    if (typeof id === "string") return id;
    return JSON.stringify(id) ?? String(id);
  }

  function clipped(text: string): string {
    return text.length > ID_SHOWN ? `${text.slice(0, ID_SHOWN)}…` : text;
  }

  // What JsonTree needs to make this card's leaves editable. One per card, so
  // the open-box ids of two cards showing the same shapes never collide — the
  // scope is the card's own identity, which is its _id.
  function editing(value: unknown, id: unknown): JsonEditing | null {
    if (!fieldWritable || edits === null) return null;
    return {
      edits,
      scope: idOf(id),
      onSet: (path, next) => onSetField?.(id, value, path, next),
      onRemove: (path) => onRemoveField?.(id, value, path),
      refusal,
      caveat,
    };
  }

  // _id is the one field with no pencil at all. Every write here is addressed
  // by it, so typing over it would be asking for a different document.
  function refusal(path: FieldPath): string | null {
    return path.length === 1 && path[0] === "_id" ? ID_NOT_EDITABLE : null;
  }

  // What the human is told before the confirm, when the statement about to be
  // built is bigger than the change they made. Both cases are honest endings
  // rather than refusals — see mutateValue's writeFieldCommand and
  // removeFieldCommand — and both are worth reading before, not after.
  function caveat(path: FieldPath, action: "set" | "remove"): string | null {
    if (action === "remove" && typeof path[path.length - 1] === "number") {
      const array = path.slice(0, -1);
      return dottedPath(array) === null ? WHOLE_DOCUMENT_FALLBACK : ARRAY_ELEMENT_REWRITE;
    }
    return dottedPath(path) === null ? WHOLE_DOCUMENT_FALLBACK : null;
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
          {@const value = documentValue(doc)}
          {@const identity = documentId(value)}
          {@const open = identity !== null && edits?.open === idOf(identity.id)}
          <div class="rounded-control border border-border bg-surface">
            <!-- The card's header line: what this card *is*, then what can be
                 done to it. The index sits small in the corner and the _id is
                 surfaced beside it, so a stack of cards can be scanned without
                 opening any of them. It sits above the document rather than
                 floating over it, so a long document scrolls under nothing,
                 and it holds its place whether or not it is hovered — the same
                 bargain RecordPane's buttons make. -->
            <div class="flex items-center gap-2 border-b border-border/60 px-2 py-1">
              <span class="shrink-0 font-mono text-xs tabular-nums text-text-subtle">#{i + 1}</span>
              {#if identity === null}
                <span
                  class="min-w-0 flex-1 truncate text-xs text-text-subtle italic"
                  title={DOCUMENT_WITHOUT_ID}
                >
                  no _id
                </span>
              {:else}
                {@const label = idLabel(identity.id)}
                <span class="flex min-w-0 flex-1 items-baseline gap-1.5 font-mono text-xs" title={label}>
                  <span class="shrink-0 text-text-subtle">_id</span>
                  <span class="min-w-0 truncate text-syntax-type">{clipped(label)}</span>
                </span>
              {/if}
              <CopyJson {value} title="Copy this document" />
              {#if writable}
                {#if identity === null}
                  <span class="shrink-0 text-xs text-text-subtle italic" title={DOCUMENT_WITHOUT_ID}>
                    read-only
                  </span>
                {:else if open}
                  <button
                    type="button"
                    class="inline-flex h-6 items-center rounded-control border border-border bg-surface-raised px-2 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
                    onclick={() => edits?.cancel()}
                  >
                    Cancel
                  </button>
                  <button
                    type="button"
                    class="inline-flex h-6 items-center rounded-control bg-accent px-2 text-xs font-medium text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
                    disabled={edits?.saving ?? false}
                    onclick={() => commit(identity.id)}
                    title="Build the replaceOne and take it to the Approval Gate"
                  >
                    Save
                  </button>
                {:else}
                  <button
                    type="button"
                    class="inline-flex h-6 items-center gap-1 rounded-control border border-border bg-surface-raised px-2 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text"
                    onclick={() => begin(value, identity.id)}
                    title="Edit this document"
                  >
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
                      <path d="M8 2.25 9.75 4 4.5 9.25 2.25 9.75l.5-2.25z" />
                    </svg>
                    Edit
                  </button>
                  <button
                    type="button"
                    class="inline-flex h-6 items-center gap-1 rounded-control border border-border bg-surface-raised px-2 text-xs font-medium text-text-muted transition-colors hover:border-danger/50 hover:text-danger"
                    onclick={() => onDelete?.(identity.id)}
                    title="Delete this document"
                  >
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
                      <path d="M2.5 3.5h7M5 3.5V2.25h2V3.5M3.5 3.5l.4 6h4.2l.4-6" />
                    </svg>
                    Delete
                  </button>
                {/if}
              {/if}
            </div>

            {#if open}
              <div class="flex flex-col gap-2 p-2">
                <textarea
                  class="min-h-48 w-full resize-y rounded-control border border-accent bg-surface px-3 py-2 font-mono text-base leading-6 text-text select-text focus:outline-none"
                  value={edits?.draft ?? ""}
                  oninput={(event) => {
                    if (edits !== null) edits.draft = event.currentTarget.value;
                  }}
                  use:openEditor
                  onkeydown={handleKeydown}
                  spellcheck="false"
                  autocapitalize="off"
                  autocomplete="off"
                  aria-label="Edit this document"
                ></textarea>
                <!-- The parse refusal belongs against the text that caused it,
                     not in the pane footer three hundred pixels below. -->
                {#if edits?.failure}
                  <p
                    class="rounded-control border border-danger/40 bg-danger/10 px-2.5 py-1.5 font-mono text-sm text-danger"
                  >
                    {edits.failure}
                  </p>
                {:else}
                  <p class="text-xs text-text-subtle">
                    Esc cancels · Save parses this as JSON and takes the replaceOne to the Approval
                    Gate
                  </p>
                {/if}
              </div>
            {:else}
              <!-- The body starts with the fields: the card is already the
                   frame, so JsonTree's own outer braces would be a second one.
                   Editing enters here, per field, wherever this pane was given
                   the route to the gate. -->
              <div class="overflow-x-auto py-1">
                <JsonTree
                  {value}
                  bare
                  edit={identity === null ? null : editing(value, identity.id)}
                />
              </div>
            {/if}
          </div>
        {/each}
      </div>
    </div>
  {/if}

  <div
    class="flex shrink-0 items-center gap-2 border-t border-border px-3 py-2 text-xs text-text-muted"
  >
    <!-- A refusal with the whole-document box open is already shown against
         that box; this line is for the ones with nowhere else to go — a
         delete the gate refused, a collection the grammar cannot name, an
         inline field edit the gate would not run (an inline row is one line
         high and has nowhere to put a sentence). -->
    {#if edits?.failure && (edits.open === null || isFieldBox(edits.open))}
      <span class="min-w-0 truncate text-danger" title={edits.failure}>{edits.failure}</span>
    {:else}
      <span class="min-w-0 truncate">{message}</span>
    {/if}
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
