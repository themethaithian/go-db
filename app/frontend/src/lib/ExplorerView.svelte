<script lang="ts">
  // The Explorer: the Database tool window on the left, and the contents of
  // whatever is selected in it on the right.
  //
  // This view has one job and no ambiguity about it. There is no SQL to edit
  // here, so the pane cannot drift away from the highlighted row in the tree
  // the way it could when a tree and an editor shared a screen: the selection
  // lives in schema.svelte.ts, and everything on the right is derived from it.
  //
  // What "the contents" are is the Engine's to say (ADR-0006), and so is nearly
  // everything around them — which is why this file is a shell and the browse
  // itself belongs to one of three siblings:
  //
  //   - TableBrowse      a MySQL table: the grid, WHERE and LIMIT, Structure,
  //                      row editing in the record pane.
  //   - KeyBrowse        a Redis key: TYPE, then the one command that reads
  //                      that type, then the value pane and its per-element
  //                      affordances.
  //   - CollectionBrowse a MongoDB collection: document cards, per-field
  //                      editing, and the first N documents with a Load more.
  //
  // They were one component with an Engine in every second condition, and it
  // read as three programs interleaved. Each of them now owns its Engine's
  // browse from the statement to the footer, and what is left here is what is
  // genuinely shared:
  //
  //   - the tree, the splitter and the selection;
  //   - the two edit stores and the Approval Gate runs behind them, because the
  //     Inline Confirm is raised from here whichever pane built the statement;
  //   - the one read helper, so "a browse read that the gate withholds is
  //     cancelled" is decided once rather than three times;
  //   - the empty state, which is the one screen with no Engine at all.
  //
  // Every browse — the SELECT, the two Redis commands, the aggregate — is run
  // through RunQuery like anything typed by hand, so it is classified by the
  // Approval Gate and lands in the same audit log, and each pane's caption says
  // exactly which statement produced what is on screen. A data panel that
  // fetched rows by some private path would be asking to be trusted; these
  // would rather be checked.
  //
  // Writing works the same way, one statement at a time. A pane builds exactly
  // one statement (mutate.ts, mutateValue.ts), hands it here, and this view
  // takes it to the gate: withheld, shown in the Inline Confirm below, and run
  // only when the human says so. The panes are where those affordances live
  // because that is where the provenance is — the Profile, the database and the
  // table, key or collection are the selection in the tree, unambiguously. A
  // value that arrived in the Editor has no such provenance, so the Editor's
  // Value and Documents answers stay read-only and say so where they render.
  import DatabaseTree from "./DatabaseTree.svelte";
  import InlineConfirm from "./InlineConfirm.svelte";
  import Splitter from "./Splitter.svelte";
  import TableBrowse from "./TableBrowse.svelte";
  import KeyBrowse from "./KeyBrowse.svelte";
  import CollectionBrowse from "./CollectionBrowse.svelte";
  import { clamp, layout, persistLayout, TREE_MAX_PX, TREE_MIN_PX } from "./layout.svelte";
  import { engineOf, selected } from "./schema.svelte";
  import { MONGODB, REDIS } from "./browse";
  import { RowEdits } from "./edits.svelte";
  import { ValueEdits } from "./valueEdits.svelte";
  import { CancelPending, RunQuery } from "../../wailsjs/go/app/App";
  import type { service } from "../../wailsjs/go/models";

  let {
    connectedProfiles,
    onGoToConnections,
    onOpenInEditor,
  }: {
    connectedProfiles: string[];
    onGoToConnections: () => void;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  // The values typed over browsed rows and not yet saved. They belong to this
  // view, not to the module: the Editor's grid has its own, and a row index
  // means nothing between two different result sets.
  const edits = new RowEdits();

  // The edit open on one browsed value or document, which is the same idea one
  // element at a time (valueEdits.svelte.ts). Two objects rather than one
  // because they hold different things — a patch set against rows, an open box
  // against an element — but they can never both be busy: a browse answer is
  // rows, or a value, or documents, and never two of those at once.
  const valueEdits = new ValueEdits();

  // The Engine of the Profile the selection is on, which is the whole of what
  // this shell decides: which pane browses it.
  let engine = $derived(engineOf(selected.profile));

  let browsing = $derived(
    selected.profile === null || selected.table === null
      ? null
      : { profile: selected.profile, database: selected.database ?? "", table: selected.table },
  );

  // The statement a save is currently holding out for, if any — from either
  // editor, since only one of them can ever have raised one. Read into a single
  // local so the panel that renders it and the props it is given are the same
  // withheld statement, not two reads of a moving one, and so the Inline Confirm
  // below is written once rather than three times.
  let confirming = $derived(
    edits.pending !== null
      ? {
          held: edits.pending,
          sql: edits.pendingSql,
          settle: (next: service.QueryResult) => edits.resolved(next),
        }
      : valueEdits.pending !== null
        ? {
            held: valueEdits.pending,
            sql: valueEdits.pendingSql,
            settle: (next: service.QueryResult) => valueEdits.resolved(next),
          }
        : null,
  );
  let confirmable = $derived(
    confirming !== null && !!confirming.held.pending_id && !!confirming.held.preview,
  );

  // A new selection invalidates whatever the last one had open. Each pane drops
  // its own edits when it fetches, but a pane that is being unmounted — clicking
  // a Redis key while a MySQL save waits on a confirm — never gets the chance,
  // and a confirm nobody will answer must not be left in the gate's queue: an
  // Inline Confirm has no expiry.
  $effect(() => {
    selected.profile;
    selected.database;
    selected.table;
    edits.abandon();
    edits.revert();
    valueEdits.abandon();
    valueEdits.reset();
  });

  // Leaving the Explorer unmounts this view, and the same rule applies.
  $effect(() => () => {
    edits.abandon();
    valueEdits.abandon();
  });

  // One browse read through the gate, with the one thing this view always does
  // to a withheld read: cancel it. Nothing a pane reads confirms a write, and an
  // Inline Confirm nobody answers has no expiry.
  //
  // It is here rather than in each pane because it is the same bargain for all
  // three, and it is the kind of thing that rots when it is copied.
  async function runRead(profileName: string, statement: string) {
    const answer = await RunQuery(profileName, "", statement);
    if (answer.status === "requires_confirmation" && answer.pending_id) {
      void CancelPending(answer.pending_id);
    }
    return answer;
  }

  // One generated statement, through the gate, and the answer to the only
  // question the pane that built it has: did it run?
  //
  // Everything the browse panes write comes through here, whichever Engine and
  // whichever affordance built it, because the sequence is the same for all of
  // them and it is the sequence that matters: build (a refusal stops here and
  // never reaches RunQuery), submit, wait for the Inline Confirm, and report.
  // The pane then re-reads, so what it shows next is the database's own answer
  // rather than this app's idea of what it should now hold.
  //
  // A run that did not happen leaves the pane exactly as it was, box and all.
  // That is what makes Cancel a way back: the draft lives in valueEdits, which
  // outlives the confirm panel that unmounted the box while it was up.
  //
  // The selection can move while a confirm is on screen, and a re-read then
  // would fetch the new selection's contents in the old one's pane, so that
  // counts as "it did not run" as far as the pane is concerned.
  async function runWrite(build: () => string): Promise<boolean> {
    const profileName = selected.profile;
    if (profileName === null) return false;
    const ran = await valueEdits.submit(profileName, build);
    return ran && selected.profile === profileName;
  }

  function resizeTree(next: number) {
    layout.explorerTreeWidth = clamp(next, TREE_MIN_PX, TREE_MAX_PX);
    persistLayout();
  }
</script>

{#snippet confirmPanel()}
  <!-- A save in progress takes the whole browse pane, exactly as it does in the
       Editor: one withheld statement, its Impact Preview, and the two keys that
       decide it. The pane comes back the moment it is answered — holding
       whatever was typed into it, since the draft outlives this panel
       (valueEdits.svelte.ts). Whichever editor raised it, it is rendered once:
       the two can never both be withholding one. -->
  {#if confirming !== null && confirming.held.pending_id && confirming.held.preview}
    <InlineConfirm
      reason={confirming.held.classification.reason}
      preview={confirming.held.preview}
      pendingId={confirming.held.pending_id}
      sql={confirming.sql}
      onResolved={confirming.settle}
    />
  {/if}
{/snippet}

<div class="flex flex-1 overflow-hidden bg-surface">
  <DatabaseTree {connectedProfiles} width={layout.explorerTreeWidth} {onGoToConnections} />

  <Splitter
    orientation="vertical"
    value={layout.explorerTreeWidth}
    min={TREE_MIN_PX}
    max={TREE_MAX_PX}
    label="Resize the database tool window"
    onChange={resizeTree}
  />

  <div class="flex min-w-0 flex-1 flex-col overflow-hidden">
    {#if browsing === null}
      <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-surface-panel px-3">
        <span class="text-base text-text-muted">No table selected</span>
      </div>
      <div class="flex min-h-0 flex-1 flex-col p-3">
        <section
          class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
        >
          <div class="flex h-8 shrink-0 items-center border-b border-border px-3">
            <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">Data</span>
          </div>
          <div class="m-auto flex max-w-xs flex-col items-center gap-2 px-6 py-8 text-center">
            <svg
              class="h-5 w-5 text-text-subtle"
              viewBox="0 0 20 20"
              fill="none"
              stroke="currentColor"
              stroke-width="1.4"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <rect x="2.75" y="3.75" width="14.5" height="12.5" rx="1.75" />
              <path d="M2.75 8h14.5M7.75 8v8.25" />
            </svg>
            <p class="text-base font-medium text-text">Select a table to browse its data</p>
            {#if connectedProfiles.length === 0}
              <p class="text-sm text-text-muted">
                No profile connected —
                <button
                  type="button"
                  class="font-medium text-accent underline-offset-2 hover:underline"
                  onclick={onGoToConnections}
                >
                  connect one
                </button>
                to see its tables.
              </p>
            {:else}
              <p class="text-sm text-text-muted">
                Pick one in the Database tree, and star the ones you keep coming back to.
              </p>
            {/if}
          </div>
        </section>
      </div>
    {:else if engine === REDIS}
      <KeyBrowse
        profileName={browsing.profile}
        keyName={browsing.table}
        edits={valueEdits}
        read={runRead}
        onWrite={runWrite}
        confirm={confirmable ? confirmPanel : null}
        {onOpenInEditor}
      />
    {:else if engine === MONGODB}
      <CollectionBrowse
        profileName={browsing.profile}
        database={browsing.database}
        collection={browsing.table}
        edits={valueEdits}
        read={runRead}
        onWrite={runWrite}
        confirm={confirmable ? confirmPanel : null}
        {onOpenInEditor}
      />
    {:else}
      <TableBrowse
        profileName={browsing.profile}
        database={browsing.database}
        table={browsing.table}
        {edits}
        read={runRead}
        confirm={confirmable ? confirmPanel : null}
        {onOpenInEditor}
      />
    {/if}
  </div>
</div>
