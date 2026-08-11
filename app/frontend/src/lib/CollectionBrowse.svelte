<script lang="ts">
  // Browsing one MongoDB collection: its first documents, as cards, with
  // per-field and whole-document editing.
  //
  // One of the Explorer's three browse panes (TableBrowse and KeyBrowse are the
  // others). A collection is not a table with looser columns: there is no WHERE
  // and no schema, the answer is documents rather than rows, and how many of
  // them to ask for is this pane's own question rather than a shared row limit.
  //
  // How many, and why it is asked at all
  // ------------------------------------
  //
  // This used to browse with db.<collection>.find({}) and let the adapter cap
  // the answer at MaxRows. A thousand documents then crossed the wire and a
  // thousand cards were built before the first one could be read, which is what
  // made this pane stutter where the grid beside it did not. So it browses with
  //
  //   db.<collection>.aggregate([{"$limit": 50}])
  //
  // — the bound written into the statement, where the human can read it in the
  // caption and the classifier can see it. $limit is on the Approval Gate's
  // list of aggregation stages proven to only read (internal/guard/mongo.go),
  // so this is the same guarded read path the find took, with a smaller answer.
  // "Load more" walks the limit up the same ladder the grid's row limit offers
  // — 50, 200, 500, 1000 — and stops at 1000 because that is the adapter's own
  // MaxRows and a step past it would be a promise the backend does not keep.
  //
  // The footer line is this pane's own rather than the backend's. A find that
  // hits the cap comes back marked truncated, and its message says so; an
  // aggregate that asked for fifty and got fifty is not truncated by anything —
  // the pipeline did exactly what it said. "50 documents." would then read as
  // "that is the whole collection", which is the one thing nobody can know from
  // here. So the pane says what it actually knows: these are the first fifty,
  // and there may be more.
  //
  // Editing is unchanged by any of that, because editing was never addressed by
  // position: a card carries its document's own _id, and every call built from
  // it — replaceOne, deleteOne, the per-field updateOne — matches on that _id.
  // Reading fifty documents instead of a thousand changes which documents are on
  // screen and nothing about what an edit to one of them means.
  import { untrack, type Snippet } from "svelte";
  import BrowseHeader from "./BrowseHeader.svelte";
  import DocumentsView from "./DocumentsView.svelte";
  import {
    browseMongoCollection,
    MONGO_BROWSE_CAP,
    MONGO_BROWSE_FIRST,
    nextMongoBrowseLimit,
    UNNAMEABLE_COLLECTION,
  } from "./browse";
  import {
    deleteOneCommand,
    removeFieldCommand,
    replaceOneCommand,
    writeFieldCommand,
  } from "./mutateValue";
  import type { FieldPath } from "./jsonFields";
  import type { ValueEdits } from "./valueEdits.svelte";
  import type { service } from "../../wailsjs/go/models";

  let {
    profileName,
    database,
    collection,
    edits,
    read,
    onWrite,
    confirm = null,
    onOpenInEditor,
  }: {
    profileName: string;
    database: string;
    collection: string;
    /** The open edit and its draft — the Explorer's, so the Inline Confirm it
     *  raises and the box here are the same edit. */
    edits: ValueEdits;
    /** One read through the Approval Gate, cancelled if the gate withholds it. */
    read: (profileName: string, statement: string) => Promise<service.QueryResult>;
    /**
     * One built statement through the Approval Gate. Answers true when it
     * actually ran and this pane is still the one on screen, which is when the
     * collection is worth reading again.
     */
    onWrite: (build: () => string) => Promise<boolean>;
    /** The Inline Confirm, when a write has raised one: it takes this pane. */
    confirm?: Snippet | null;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The call behind what is on screen right now, set from the fetch that
  // produced it, so the caption is a record rather than a promise.
  let shownSql = $state("");
  // The limit that produced what is on screen — read by the footer line, which
  // has to compare "how many came back" against "how many were asked for" and
  // must not do that against a limit the human has since raised.
  let shownLimit = $state<number>(MONGO_BROWSE_FIRST);

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later.
  let latestRequest = 0;

  // How many documents to ask for, and the collection that was chosen for.
  // Kept as one piece of state rather than a limit plus an effect that resets
  // it: picking another collection starts the ladder again, and it has to start
  // again in the same tick the collection changes, or the statement is briefly
  // the last collection's limit against the new name and fetches twice. Null is
  // "nobody has pressed Load more yet", which is also where every fresh
  // collection starts.
  let ladder = $state<{ collection: string | null; limit: number }>({
    collection: null,
    limit: MONGO_BROWSE_FIRST,
  });
  let limit = $derived(ladder.collection === collection ? ladder.limit : MONGO_BROWSE_FIRST);

  // The call that browses this collection, or null for a name go-db's MongoDB
  // grammar cannot write.
  let browseCall = $derived(browseMongoCollection(collection, limit));

  let documents = $derived(
    result?.status === "ok" && result.documents !== undefined ? result.documents : null,
  );
  let shownCount = $derived(documents?.documents.length ?? 0);

  // Whether there may be more where these came from. An aggregate that returned
  // exactly what its $limit asked for has told us nothing about what came after
  // it; one that returned fewer has reached the end of the collection.
  let maybeMore = $derived(documents !== null && shownCount >= shownLimit);
  let nextLimit = $derived(maybeMore ? nextMongoBrowseLimit(shownLimit) : null);

  // The footer line, in place of the backend's own summary — see the note at
  // the top of this file for why this pane writes its own.
  let summary = $derived.by(() => {
    if (documents === null) return "";
    const count = shownCount.toLocaleString();
    if (!maybeMore) return shownCount === 1 ? "1 document." : `${count} documents.`;
    if (shownLimit >= MONGO_BROWSE_CAP) {
      return `Showing the first ${count} documents — go-db reads at most ${MONGO_BROWSE_CAP.toLocaleString()} at a time.`;
    }
    return `Showing the first ${count} documents — there may be more.`;
  });

  // The call is the whole input to this pane: a collection picked in the tree
  // or a limit raised by Load more fetches again for exactly that call.
  $effect(() => {
    const call = browseCall;
    const asked = limit;
    if (call === null) {
      // Something is selected and this Engine cannot write a statement that
      // names it — a collection whose name is outside the grammar. Saying so
      // beats a pane that sits on "Loading…" for ever.
      clearPane(UNNAMEABLE_COLLECTION);
      return;
    }
    void fetchDocuments(profileName, call, asked);
  });

  async function fetchDocuments(profile: string, call: string, asked: number) {
    const request = (latestRequest += 1);
    // An open card editor belongs to a result set that is about to be replaced.
    edits.abandon();
    edits.reset();
    // Documents from the collection we were looking at a moment ago are worse
    // than none: they would sit under another collection's name. They go the
    // instant the call changes — but re-running the same one keeps them, so a
    // refresh does not blink the cards out and back.
    // (untracked: this runs inside the call effect, which must not re-fire on
    // the caption it is itself writing.)
    if (untrack(() => shownSql) !== call) {
      result = null;
      failure = null;
      shownSql = "";
    }
    loading = true;
    try {
      const next = await read(profile, call);
      if (request !== latestRequest) return;
      result = next;
      failure = null;
      shownSql = call;
      shownLimit = asked;
    } catch (err) {
      if (request !== latestRequest) return;
      result = null;
      failure = String(err);
      shownSql = call;
      shownLimit = asked;
    } finally {
      if (request === latestRequest) loading = false;
    }
  }

  // Empties the pane, leaving one line in place of the cards. Bumping the
  // request number is what makes it stick: a fetch already in flight lands on a
  // number that is no longer the latest and drops itself.
  function clearPane(reason: string) {
    latestRequest += 1;
    result = null;
    failure = reason;
    loading = false;
    shownSql = "";
    edits.abandon();
    edits.reset();
  }

  function refresh() {
    if (browseCall === null) return;
    void fetchDocuments(profileName, browseCall, limit);
  }

  // The next rung: the same read again, asking for more. It is a fresh browse
  // rather than a page appended to this one — the cards on screen are then all
  // one answer from one statement, and the caption names the statement that
  // produced every one of them.
  function loadMore() {
    const next = nextLimit;
    if (next === null) return;
    ladder = { collection, limit: next };
  }

  function openInEditor() {
    if (shownSql === "") return;
    onOpenInEditor(profileName, shownSql);
  }

  // The affordances. The collection is the selection's own name, and the _id is
  // the document's own — carried back from the card exactly as it arrived, so an
  // ObjectId's {"$oid": "…"} round-trips rather than being rebuilt from
  // something that looks like it.
  //
  // A write that ran is followed by re-reading the collection, so what the pane
  // shows next is the database's own answer rather than this app's idea of what
  // it now holds. The limit in force is kept: a human who pressed Load more
  // twice does not want an edit to take them back to the first fifty.
  async function runWrite(build: () => string) {
    if (await onWrite(build)) refresh();
  }

  function replaceDocument(id: unknown, document: unknown) {
    void runWrite(() => replaceOneCommand(collection, id, document));
  }

  function deleteDocument(id: unknown) {
    void runWrite(() => deleteOneCommand(collection, id));
  }

  // The per-field affordances, the same route with a smaller statement: an
  // updateOne naming exactly the field that changes, so the human confirming it
  // reads the change rather than the document around it. The document goes along
  // because the statement is not always a $set — see writeFieldCommand for the
  // two paths that cannot be named, and what is written instead.
  function setDocumentField(id: unknown, document: unknown, path: FieldPath, value: unknown) {
    void runWrite(() => writeFieldCommand(collection, id, document, path, value));
  }

  function removeDocumentField(id: unknown, document: unknown, path: FieldPath) {
    void runWrite(() => removeFieldCommand(collection, id, document, path));
  }
</script>

{#snippet loadMoreButton()}
  <button
    type="button"
    class="inline-flex h-6 shrink-0 items-center gap-1 rounded-control border border-border bg-surface-raised px-2 text-xs font-medium text-text-muted transition-colors hover:border-border-strong hover:text-text disabled:cursor-not-allowed disabled:text-text-subtle"
    onclick={loadMore}
    disabled={loading}
    title="Read the first {nextLimit?.toLocaleString()} documents instead"
  >
    Load more
    <span class="tabular-nums text-text-subtle">({nextLimit?.toLocaleString()})</span>
  </button>
{/snippet}

<BrowseHeader
  name={collection}
  qualifier={database}
  profile={profileName}
  {loading}
  statement={shownSql}
  onOpenInEditor={openInEditor}
  onRefresh={refresh}
  refreshDisabled={loading}
/>

<div class="flex min-h-0 flex-1 flex-col p-3">
  {#if confirm !== null}
    {@render confirm()}
  {:else}
    <section
      class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
    >
      <div class="flex h-8 shrink-0 items-center justify-between gap-3 border-b border-border px-3">
        {#if shownSql === ""}
          <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">Data</span>
        {:else}
          <span class="truncate font-mono text-xs text-text-subtle" title={shownSql}>
            {shownSql}
          </span>
          {#if result?.classification.kind === "read"}
            <span
              class="shrink-0 rounded-full border border-success/40 bg-success/10 px-2 py-0.5 text-xs font-semibold tracking-wide text-success"
              title="Classified by the Approval Gate"
            >
              READ
            </span>
          {/if}
        {/if}
      </div>

      {#if failure !== null}
        <div class="p-3">
          <p
            class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
          >
            {failure}
          </p>
        </div>
      {:else if result === null}
        <div class="m-auto px-6 py-8 text-center">
          <p class="text-base text-text-muted">Loading…</p>
        </div>
      {:else if documents !== null}
        <!-- The Documents arm (ADR-0006) — what browsing a collection answers
             with. The cards are editable here because this pane knows which
             collection they came from: it is the selection in the tree, and
             every call built from it addresses one document by its own _id. -->
        <DocumentsView
          {documents}
          message={summary}
          more={nextLimit === null ? null : loadMoreButton}
          {edits}
          {collection}
          onReplace={replaceDocument}
          onDelete={deleteDocument}
          onSetField={setDocumentField}
          onRemoveField={removeDocumentField}
        />
      {:else if result.status === "ok"}
        <!-- An "ok" that is not documents: the read answered with something this
             pane has no layout for. Its own message is the honest thing to
             show. -->
        <div class="m-auto px-6 py-8 text-center">
          <p class="text-base text-text-muted">{result.message}</p>
        </div>
      {:else}
        <!-- Anything but "ok" is the connection, the server, or the gate
             talking. Shown verbatim; nothing here offers to confirm a write,
             because this pane reads. -->
        <div class="p-3">
          <p
            class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
          >
            {result.message}
          </p>
          {#if result.status === "requires_confirmation"}
            <p class="px-3 py-2 text-sm text-text-muted">
              Nothing ran. This panel only reads — take the statement to the Editor with “Open in
              editor” and decide on it there.
            </p>
          {/if}
        </div>
      {/if}
    </section>
  {/if}
</div>
