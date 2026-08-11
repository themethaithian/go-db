<script lang="ts">
  // Browsing one Redis key: what TYPE says it holds, the command that reads
  // that type, and the value pane those two produce.
  //
  // One of the Explorer's three browse panes (TableBrowse and CollectionBrowse
  // are the others). A key is not a table with fewer columns: it has no
  // condition to filter and no limit to choose, its statement is not known
  // until the server has been asked what type it is, and the affordances on
  // what comes back — a hash field, a list element, a sorted set's score, the
  // whole string — are Redis's own. That is what this pane owns.
  //
  // Two reads rather than one guess. Redis has no command that reads a key of
  // any type, and trying GET first would answer WRONGTYPE for four of the six
  // — an error where there is data. Both reads go through the Approval Gate, so
  // both are classified and both are in the audit log, which is the same
  // bargain the rest of the Explorer makes: the reads are visible because they
  // are real. The caption shows the second command, which is the one that
  // produced what is on screen.
  //
  // Editing is one element at a time (mutateValue.ts), and each edit is exactly
  // one command taken to the gate through `onWrite`. This pane is where those
  // affordances live because this is where the provenance is: the key is the
  // selection in the tree, unambiguously. A value that arrived in the Editor
  // has no such provenance, which is why the Editor's Value answers stay
  // read-only.
  import { untrack, type Snippet } from "svelte";
  import BrowseHeader from "./BrowseHeader.svelte";
  import ValueView from "./ValueView.svelte";
  import {
    MISSING_KEY,
    noReadForType,
    redisReadCommand,
    redisTypeCommand,
    UNQUOTABLE_KEY,
  } from "./browse";
  import { deleteKeyCommand, removeCommand, writeCommand, type Slot } from "./mutateValue";
  import type { ValueEdits } from "./valueEdits.svelte";
  import type { service } from "../../wailsjs/go/models";

  let {
    profileName,
    keyName,
    edits,
    read,
    onWrite,
    confirm = null,
    onOpenInEditor,
  }: {
    profileName: string;
    /** The key selected in the Database tree. */
    keyName: string;
    /** The open edit and its draft — the Explorer's, so the Inline Confirm it
     *  raises and the box here are the same edit. */
    edits: ValueEdits;
    /** One read through the Approval Gate, cancelled if the gate withholds it. */
    read: (profileName: string, statement: string) => Promise<service.QueryResult>;
    /**
     * One built statement through the Approval Gate. Answers true when it
     * actually ran and this pane is still the one on screen, which is when the
     * key is worth reading again.
     */
    onWrite: (build: () => string) => Promise<boolean>;
    /** The Inline Confirm, when a write has raised one: it takes this pane. */
    confirm?: Snippet | null;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The command behind what is on screen right now — the read, not the TYPE —
  // set from the fetch that produced it, so the caption is a record rather than
  // a promise.
  let shownSql = $state("");
  // The key behind what is on screen, for the one thing shownSql cannot answer
  // for a key: whether a fetch is a re-read of what is already there. A key's
  // statement is not known until TYPE has been asked.
  let shownKey = $state<string | null>(null);
  // What TYPE said that key holds, which is what says how to read the reply
  // that came back — a flat array is a list's elements or a sorted set's
  // members and scores depending only on this, and an edit addresses a
  // different thing in each. Recorded from the fetch, like shownSql, so it
  // describes what is on screen rather than what is selected.
  let shownType = $state<string | null>(null);

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later: clicking three keys quickly must leave the third one's value on
  // screen, not whichever read the server happened to finish last.
  let latestRequest = 0;

  // Whether the pane is showing a browsed key's value — which is the one thing
  // "Delete key" can honestly mean. A value the gate or the server answered
  // with instead is not a key's contents.
  let browsingKey = $derived(
    shownKey !== null && result?.status === "ok" && result.value !== undefined,
  );

  // The key selected is the whole input to this pane.
  $effect(() => {
    void fetchKey(profileName, keyName);
  });

  async function fetchKey(profile: string, key: string) {
    const typeCommand = redisTypeCommand(key);
    if (typeCommand === null) {
      clearPane(UNQUOTABLE_KEY);
      return;
    }

    const request = (latestRequest += 1);
    edits.abandon();
    edits.reset();
    // What is on screen belongs to the key that was selected a moment ago, and
    // leaving it under another key's name would be worse than showing nothing.
    // Re-reading the same key keeps it, so refresh does not blink — the same
    // bargain the grid makes with its statement, made with the key because the
    // statement is not known until TYPE has answered.
    // (untracked: this runs inside the selection effect, which must not re-fire
    // on the record it is itself writing.)
    if (untrack(() => shownKey) !== key) {
      result = null;
      failure = null;
      shownSql = "";
    }
    shownKey = key;
    loading = true;
    try {
      const typed = await read(profile, typeCommand);
      if (request !== latestRequest) return;
      if (typed.status !== "ok" || typed.value === undefined) {
        // The gate or the server answered instead of Redis: shown as it came,
        // the same way a failed SELECT is.
        result = typed;
        failure = null;
        shownSql = typeCommand;
        shownType = null;
        return;
      }

      const type = typed.value.kind === "string" ? (typed.value.text ?? "") : "";
      if (type === "none" || type === "") {
        clearPane(MISSING_KEY);
        return;
      }
      const readCommand = redisReadCommand(key, type);
      if (readCommand === null) {
        clearPane(noReadForType(type));
        return;
      }

      const next = await read(profile, readCommand);
      if (request !== latestRequest) return;
      result = next;
      failure = null;
      shownSql = readCommand;
      // Recorded beside the statement it belongs to, and only once the read it
      // describes has landed: a type set before the value arrived would tell the
      // pane how to lay out a reply that is not there yet.
      shownType = type;
    } catch (err) {
      if (request !== latestRequest) return;
      result = null;
      failure = String(err);
      shownSql = "";
      shownType = null;
    } finally {
      if (request === latestRequest) loading = false;
    }
  }

  // Empties the pane, leaving one line in place of the value. Bumping the
  // request number is what makes it stick: a fetch already in flight lands on a
  // number that is no longer the latest and drops itself.
  function clearPane(reason: string) {
    latestRequest += 1;
    result = null;
    failure = reason;
    loading = false;
    shownSql = "";
    shownKey = null;
    shownType = null;
    edits.abandon();
    edits.reset();
  }

  // A key is refreshed from the top, TYPE included: what a key holds can change
  // between two looks at it, and re-running only the second command would answer
  // WRONGTYPE where the honest answer is the new value.
  function refresh() {
    void fetchKey(profileName, keyName);
  }

  function openInEditor() {
    if (shownSql === "") return;
    onOpenInEditor(profileName, shownSql);
  }

  // The affordances. The key they name is shownKey — the key whose value is on
  // screen — rather than the selection, and the distinction is the same one
  // shownSql makes for the caption: a write belongs to what is being looked at,
  // not to what has just been clicked. The two agree except for the moment a
  // fetch is in flight, and in that moment shownKey is the honest one, since it
  // is the key shownType was recorded for and the elements were read with.
  //
  // A write that ran is followed by re-reading the key, so what the pane shows
  // next is Redis's own answer rather than this app's idea of what it now holds.
  async function runWrite(build: () => string) {
    if (await onWrite(build)) refresh();
  }

  function writeElement(slot: Slot, text: string) {
    const key = shownKey;
    if (key === null) return;
    void runWrite(() => writeCommand(key, slot, text));
  }

  function removeElement(slot: Slot) {
    const key = shownKey;
    if (key === null) return;
    void runWrite(() => removeCommand(key, slot));
  }

  function deleteKey() {
    const key = shownKey;
    if (key === null) return;
    void runWrite(() => deleteKeyCommand(key));
  }
</script>

{#snippet deleteKeyButton()}
  <!-- Deleting the whole key is a pane-level action, not an element's, so it
       sits with the pane's other pane-level buttons — and only while a key's
       value is what the pane is showing. It is one statement like every other
       affordance here, and goes the same way through the gate. -->
  {#if browsingKey}
    <button
      type="button"
      class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-base text-text-muted transition-colors hover:border-danger/50 hover:text-danger disabled:cursor-not-allowed disabled:text-text-subtle"
      onclick={deleteKey}
      disabled={edits.saving}
      title="DEL this key — the Approval Gate will ask first"
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
        <path d="M2.5 3.5h7M5 3.5V2.25h2V3.5M3.5 3.5l.4 6h4.2l.4-6" />
      </svg>
      Delete key
    </button>
  {/if}
{/snippet}

<BrowseHeader
  name={keyName}
  profile={profileName}
  {loading}
  statement={shownSql}
  onOpenInEditor={openInEditor}
  onRefresh={refresh}
  refreshDisabled={loading}
  actions={deleteKeyButton}
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
      {:else if result.status === "ok" && result.value !== undefined}
        <!-- The Value arm (ADR-0006) — what browsing a Redis key answers with.
             The elements are editable here because this pane knows which key
             they came from, and the type TYPE reported is what says how to read
             the reply that came back. -->
        <ValueView
          value={result.value}
          message={result.message}
          {edits}
          type={shownType}
          onWrite={writeElement}
          onRemove={removeElement}
        />
      {:else if result.status === "ok"}
        <!-- An "ok" that is not a value: the read answered with something this
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
