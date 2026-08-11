<script lang="ts">
  // Renders one already-parsed JSON value as an interactive tree — the
  // MongoDB Documents arm's own view of a document (DocumentsView.svelte)
  // and, wherever a Redis string parses as JSON (jsonReply.ts), the JSON
  // side of ReplyValue's and ValueView's Raw | JSON toggle. Both callers
  // hand this component a plain JS value already run through JSON.parse —
  // it holds no opinion on where that value came from.
  //
  // The row and indent language is ReplyValue.svelte's own: one left border
  // and one indent per nesting level, an array's own index column at
  // text-subtle, a map's own key column at text-muted — so a Redis reply
  // tree and a JSON tree read as the same kind of thing when they sit side
  // by side in the results pane. Row height and padding are ResultsTable's
  // density rather than looser: a document is a grid of fields, and a tree
  // that breathes twice as hard as the grid beside it reads as a different
  // app.
  //
  // Three things carry the readability of a JSON document, and all three are
  // deliberate here:
  //
  //   - Keys are unquoted. They are identifiers to the eye, and a screen of
  //     "key": pairs is a screen where half the marks are punctuation.
  //
  //   - Punctuation — braces, brackets, the colon after a key — is
  //     text-subtle, one step below the muted keys and two below the values,
  //     so the value carries the row and the syntax frames it.
  //
  //   - Extended JSON is inlined. MongoDB writes an ObjectId as
  //     {"$oid": "…"} and a date as {"$date": "…"}; expanded as objects they
  //     turn a five-field document into a page of braces. recogniseExtended
  //     (jsonFields.ts) names the ones go-db knows, and each renders as the
  //     one compact leaf a mongosh user reads — ObjectId("…"), ISODate("…"),
  //     the digits of a $numberLong with a muted "long" beside them — in
  //     text-syntax-type, which is the app's existing fourth syntax colour
  //     and is exactly "a type, not a value" everywhere else it appears.
  //
  // Colour otherwise routes only to what SqlEditor and ReplyValue already
  // established — keys → text-muted, strings → text-syntax-string, numbers →
  // text-syntax-number, booleans → text-syntax-keyword, null → text-subtle
  // italic, punctuation → text-subtle. No new colour is introduced.
  //
  // Expansion defaults to the top `expandDepth` levels open and everything past
  // that collapsed. A collapsed object/array never renders as nothing: it shows
  // `{…} 5 keys` / `[…] 12 items`. The default is two levels; the Documents
  // pane asks for one, because it stacks fifty of these at a time and the
  // second level of fifty documents is a screenful nobody asked for.
  //
  // A string past STRING_TRUNCATE_AT truncates with its own "show more"
  // toggle rather than relying on a title attribute.
  //
  // ---------------------------------------------------------------------
  // What this costs to mount
  //
  // Two rules keep a tree cheap, and both matter at fifty documents:
  //
  //   - A collapsed container renders its summary row and nothing else. Its
  //     entries are behind the same {#if} as the braces, so a subtree nobody
  //     has opened has never existed — not hidden with a class, not built and
  //     then skipped.
  //
  //   - A leaf is drawn inline, in its parent's row, rather than as a JsonTree
  //     of its own. A component per field is the cost that does not show up in
  //     any one document and dominates fifty of them; the recursion is for the
  //     values that genuinely need their own state — containers, which collapse,
  //     and long strings, which carry their own "show more".
  //
  // So a document card costs one component for its root plus one per container
  // field, rather than one per field of every level.
  //
  // ---------------------------------------------------------------------
  // Editing
  //
  // Given an `edit` context (jsonFields.ts) the leaves become editable in
  // place: hover a row, press the pencil, and the value swaps to an inline
  // input in the same row — the grid's own idiom, at the grain of one field.
  // Enter commits, Escape cancels, and the draft lives in the caller's
  // ValueEdits rather than here, because an Inline Confirm takes the panel
  // and unmounts this tree while it is up; held there, a declined confirm
  // leaves the box exactly as it was. This tree also seeds itself expanded
  // when the open box is somewhere underneath it, so that draft comes back
  // visible rather than inside a subtree that collapsed on the way.
  //
  // What a commit *means* is not decided here. The text is read as a JSON
  // literal when it parses and as a plain string when it does not
  // (parseLiteral), with the reading shown beside the box as it is typed;
  // the value and its path then go to the caller, which builds the statement
  // and takes it to the Approval Gate. This component never writes and never
  // names a collection.
  //
  // Given no `edit` — the Editor's read-only results, and every nested Redis
  // reply — nothing above exists and the tree is exactly what it was.
  //
  // Recurses through its own import — Svelte 5's replacement for
  // <svelte:self>, the same trick ReplyValue.svelte already uses.
  import { untrack } from "svelte";
  import JsonTree from "./JsonTree.svelte";
  import CopyJson from "./CopyJson.svelte";
  import {
    fieldId,
    literalText,
    openFieldPath,
    parseLiteral,
    recogniseExtended,
    underPath,
    type FieldPath,
    type JsonEditing,
  } from "./jsonFields";

  let {
    value,
    depth = 0,
    path = [],
    edit = null,
    bare = false,
    expandDepth = 2,
  }: {
    value: unknown;
    depth?: number;
    /** Where this node sits in the value the root was given. */
    path?: FieldPath;
    /** The edit context, or null for a read-only tree. */
    edit?: JsonEditing | null;
    /**
     * Renders a container's entries without its own brace rows or collapse
     * toggle — for a caller whose card chrome already frames the document
     * (DocumentsView). Only meaningful at the root.
     */
    bare?: boolean;
    /**
     * How many levels start expanded. Two for a reply the human asked one
     * question to see; one for the Documents pane, which shows fifty answers at
     * once. Passed down the recursion so one tree has one rule.
     */
    expandDepth?: number;
  } = $props();

  const STRING_TRUNCATE_AT = 300;

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

  function isLeaf(v: unknown): boolean {
    return !(v !== null && typeof v === "object");
  }

  /**
   * Whether a child needs a JsonTree of its own, or can be drawn inline in the
   * row that names it.
   *
   * Only two kinds of value need one: a container, which has a collapsed state
   * and entries of its own, and a long string, which carries its own "show
   * more". Everything else — a short string, a number, a boolean, a null, and
   * an extended-JSON wrapper, which renders as one compact leaf whatever it is
   * technically made of — is a span, and a span does not need a component
   * around it. See the mounting note at the top of this file.
   */
  function needsTree(v: unknown): boolean {
    if (typeof v === "string") return v.length > STRING_TRUNCATE_AT;
    if (v === null || typeof v !== "object") return false;
    return recogniseExtended(v) === null;
  }

  // An extended-JSON wrapper is one value spelled across two levels, so it is
  // read before anything else: it renders as a leaf and never as the object
  // it technically is.
  let extended = $derived(recogniseExtended(value));

  let kind = $derived(kindOf(value));
  let isContainer = $derived(extended === null && (kind === "object" || kind === "array"));

  let objectEntries = $derived(
    isContainer && kind === "object" ? Object.entries(value as Record<string, unknown>) : [],
  );
  let arrayItems = $derived(isContainer && kind === "array" ? (value as unknown[]) : []);
  let count = $derived(kind === "object" ? objectEntries.length : arrayItems.length);

  // Collapsed past the default depth, unless the open edit box is somewhere
  // under this node — a draft that survived a declined Inline Confirm is no
  // use inside a subtree that came back collapsed. A node's own depth and the
  // open box at mount never change after it mounts, so this is a plain $state
  // seeded once via untrack, so the seed is read without also becoming a
  // dependency this state would (uselessly) re-derive from and that would
  // fight a click's own toggle.
  let expanded = $state(
    untrack(() => {
      if (depth < expandDepth) return true;
      if (edit === null) return false;
      const open = openFieldPath(edit.scope, edit.edits.open);
      return open !== null && underPath(path, open);
    }),
  );

  let summary = $derived(
    `${count} ${count === 1 ? (kind === "object" ? "key" : "item") : kind === "object" ? "keys" : "items"}`,
  );

  let isLongString = $derived(kind === "string" && (value as string).length > STRING_TRUNCATE_AT);
  let stringExpanded = $state(false);
  let stringText = $derived(kind === "string" ? (value as string) : "");
  let stringShown = $derived(
    isLongString && !stringExpanded ? stringText.slice(0, STRING_TRUNCATE_AT) : stringText,
  );

  // ---------------------------------------------------------------------
  // Editing
  //
  // Every function below is a no-op without an edit context, so the markup
  // can call them without repeating the null check the affordances already
  // made before they rendered.

  /** The id of the box on one child row, and of this node's add-a-field row. */
  function rowId(childPath: FieldPath): string {
    return edit === null ? "" : fieldId(edit.scope, childPath);
  }

  let addId = $derived(edit === null ? "" : fieldId(edit.scope, path, true));
  let adding = $derived(edit !== null && edit.edits.open === addId);

  function beginEdit(childPath: FieldPath, current: unknown) {
    edit?.edits.begin(rowId(childPath), literalText(current));
  }

  function beginAdd() {
    edit?.edits.begin(addId, "", "");
  }

  function setDraft(text: string) {
    if (edit !== null) edit.edits.draft = text;
  }

  function setName(text: string) {
    if (edit !== null) edit.edits.name = text;
  }

  function commitEdit(childPath: FieldPath) {
    if (edit === null) return;
    edit.onSet(childPath, parseLiteral(edit.edits.draft).value);
  }

  // The add row's own commit. An object needs a name and refuses without one;
  // an array appends, which is a $set on the position after the last — the
  // one place a path is built from the value's own length rather than read
  // off it.
  function commitAdd() {
    if (edit === null) return;
    if (kind === "array") {
      edit.onSet([...path, arrayItems.length], parseLiteral(edit.edits.draft).value);
      return;
    }
    const name = edit.edits.name.trim();
    if (name === "") {
      edit.edits.refuse("A new field needs a name.");
      return;
    }
    edit.onSet([...path, name], parseLiteral(edit.edits.draft).value);
  }

  function keydown(event: KeyboardEvent, commit: () => void) {
    if (event.key === "Enter") {
      event.preventDefault();
      commit();
    } else if (event.key === "Escape") {
      // Stopped as well as prevented: Escape also closes panes and clears
      // selections higher up, and leaving a box should not also do that.
      event.preventDefault();
      event.stopPropagation();
      edit?.edits.cancel();
    }
  }

  // Editing a value almost always means replacing it, so the box opens
  // selected — the contract ResultsTable's and RecordPane's own editors keep.
  // `active` is false for the one box that is not the first field in its row:
  // the add-a-field row focuses its name box, and two boxes both grabbing
  // focus on mount would be a race the later one wins.
  function openBox(node: HTMLInputElement, active: boolean) {
    if (!active) return;
    node.focus();
    node.select();
  }

  function focusBox(node: HTMLInputElement) {
    node.focus();
  }

  function withCaveat(action: "set" | "remove", childPath: FieldPath, plain: string): string {
    const caveat = edit?.caveat?.(childPath, action) ?? null;
    return caveat === null ? plain : `${plain} — ${caveat}`;
  }
</script>

{#snippet pencil(childPath: FieldPath, current: unknown)}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
    onclick={() => beginEdit(childPath, current)}
    title={withCaveat("set", childPath, "Edit this value")}
    aria-label="Edit this value"
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
  </button>
{/snippet}

{#snippet blocked(reason: string)}
  <!-- The reason where the affordances would be: something a hover can read,
       rather than buttons that say no when they are pressed. ReplyEditor's
       own idiom, at the grain of one field. -->
  <span class="flex h-5 w-5 shrink-0 items-center justify-center text-text-subtle" title={reason}>
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.2"
      stroke-linecap="round"
      aria-hidden="true"
    >
      <circle cx="6" cy="6" r="4.25" />
      <path d="M3 9 9 3" />
    </svg>
  </span>
{/snippet}

{#snippet trash(childPath: FieldPath, indexed: boolean)}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-danger/50 hover:text-danger"
    onclick={() => edit?.onRemove(childPath)}
    title={withCaveat("remove", childPath, indexed ? "Remove this element" : "Delete this field")}
    aria-label={indexed ? "Remove this element" : "Delete this field"}
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
  </button>
{/snippet}

{#snippet commitButtons(commit: () => void)}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control bg-accent text-white transition-colors hover:bg-accent-hover disabled:cursor-not-allowed disabled:opacity-50"
    disabled={edit?.edits.saving ?? false}
    onmousedown={(event) => event.preventDefault()}
    onclick={commit}
    title="Take this change to the Approval Gate (⏎)"
    aria-label="Save"
  >
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.6"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M2.5 6.25 5 8.75l4.5-5.5" />
    </svg>
  </button>
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
    onmousedown={(event) => event.preventDefault()}
    onclick={() => edit?.edits.cancel()}
    title="Leave it as it was (Esc)"
    aria-label="Cancel"
  >
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.4"
      stroke-linecap="round"
      aria-hidden="true"
    >
      <path d="M3 3l6 6M9 3l-6 6" />
    </svg>
  </button>
{/snippet}

{#snippet valueBox(commit: () => void, focus: boolean)}
  {@const literal = parseLiteral(edit?.edits.draft ?? "")}
  <input
    class="min-w-0 flex-1 rounded-control border border-accent bg-surface px-1.5 font-mono text-base leading-5 text-text select-text focus:outline-none"
    value={edit?.edits.draft ?? ""}
    oninput={(event) => setDraft(event.currentTarget.value)}
    onkeydown={(event) => keydown(event, commit)}
    use:openBox={focus}
    spellcheck="false"
    autocapitalize="off"
    autocomplete="off"
    aria-label="Edit this value"
  />
  <!-- Which way the text will be read, while it is being typed. The parse is
       forgiving on purpose (see parseLiteral); this tag is what keeps the
       forgiveness from being a surprise. -->
  <span class="shrink-0 text-xs text-text-subtle">as {literal.as}</span>
  {@render commitButtons(commit)}
{/snippet}

{#snippet inlineValue(child: unknown)}
  <!-- One leaf, drawn where it sits. The markup is the same markup the
       bottom of this file renders for a value of its own — same colours, same
       break-all — because it is the same leaf; what it saves is the component
       that used to be built around it, once per field per document. -->
  {@const wrapper = recogniseExtended(child)}
  {#if wrapper !== null}
    <span
      class="text-syntax-type break-all"
      title="An extended JSON value — go-db does not edit one yet"
    >
      {wrapper.text}
    </span>
    {#if wrapper.tag !== null}
      <span class="ml-1 text-xs text-text-subtle">{wrapper.tag}</span>
    {/if}
  {:else if typeof child === "string"}
    <span class="text-syntax-string break-all">"{child}"</span>
  {:else if typeof child === "number"}
    <span class="tabular-nums text-syntax-number">{child}</span>
  {:else if typeof child === "boolean"}
    <span class="text-syntax-keyword">{String(child)}</span>
  {:else}
    <span class="text-text-subtle italic">null</span>
  {/if}
{/snippet}

{#snippet entry(label: string, indexed: boolean, child: unknown, childPath: FieldPath)}
  {@const open = edit !== null && edit.edits.open === rowId(childPath)}
  {@const editable = edit !== null && isLeaf(child)}
  <div class="json-row flex items-start gap-2 px-3 py-1 font-mono text-base leading-5">
    {#if indexed}
      <span class="w-8 shrink-0 text-right tabular-nums text-text-subtle">{label}</span>
    {:else}
      <span class="w-32 shrink-0 truncate text-text-muted" title={label}>{label}</span>
      <span class="shrink-0 text-text-subtle">:</span>
    {/if}

    <div class="flex min-w-0 flex-1 items-center gap-1.5">
      {#if open}
        {@render valueBox(() => commitEdit(childPath), true)}
      {:else if needsTree(child)}
        <div class="min-w-0 flex-1">
          <JsonTree value={child} depth={depth + 1} path={childPath} {edit} {expandDepth} />
        </div>
      {:else}
        <div class="min-w-0 flex-1">{@render inlineValue(child)}</div>
      {/if}
    </div>

    {#if edit !== null && !open}
      {@const refused = edit.refusal?.(childPath) ?? null}
      <div class="row-actions flex shrink-0 items-center gap-1">
        {#if refused !== null}
          {@render blocked(refused)}
        {:else}
          {#if editable}{@render pencil(childPath, child)}{/if}
          {@render trash(childPath, indexed)}
        {/if}
      </div>
    {/if}
  </div>
{/snippet}

{#snippet addRow()}
  <div class="json-row flex items-center gap-2 px-3 py-1 font-mono text-base leading-5">
    {#if kind === "array"}
      <span class="w-8 shrink-0 text-right tabular-nums text-text-subtle">{count}</span>
    {:else}
      <input
        class="w-32 shrink-0 rounded-control border border-accent bg-surface px-1.5 font-mono text-base leading-5 text-text select-text focus:outline-none"
        placeholder="field"
        value={edit?.edits.name ?? ""}
        oninput={(event) => setName(event.currentTarget.value)}
        onkeydown={(event) => keydown(event, commitAdd)}
        use:focusBox
        spellcheck="false"
        autocapitalize="off"
        autocomplete="off"
        aria-label="New field name"
      />
      <span class="shrink-0 text-text-subtle">:</span>
    {/if}
    <div class="flex min-w-0 flex-1 items-center gap-1.5">
      {@render valueBox(commitAdd, kind === "array")}
    </div>
  </div>
{/snippet}

{#snippet addButton()}
  <button
    type="button"
    class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-subtle transition-colors hover:border-border-strong hover:text-text"
    onclick={beginAdd}
    title={kind === "array" ? "Append an element" : "Add a field"}
    aria-label={kind === "array" ? "Append an element" : "Add a field"}
  >
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.4"
      stroke-linecap="round"
      aria-hidden="true"
    >
      <path d="M6 2.5v7M2.5 6h7" />
    </svg>
  </button>
{/snippet}

{#snippet entries()}
  {#if kind === "object"}
    {#each objectEntries as [k, v] (k)}
      {@render entry(k, false, v, [...path, k])}
    {/each}
  {:else}
    {#each arrayItems as v, i (i)}
      {@render entry(String(i), true, v, [...path, i])}
    {/each}
  {/if}
  {#if adding}{@render addRow()}{/if}
{/snippet}

{#if extended !== null}
  <!-- One compact leaf for the wrapper, in the app's own "this is a type"
       colour. Not editable in v1: it edits as a unit or not at all. -->
  <span class="text-syntax-type break-all" title="An extended JSON value — go-db does not edit one yet">
    {extended.text}
  </span>
  {#if extended.tag !== null}
    <span class="ml-1 text-xs text-text-subtle">{extended.tag}</span>
  {/if}
{:else if bare && isContainer}
  <!-- The caller's card chrome is already the frame, so the braces and the
       root collapse toggle would only be a second one. -->
  <div class="flex flex-col">
    {#if count === 0 && !adding}
      <p class="px-3 py-1 font-mono text-base text-text-subtle italic">(no fields)</p>
    {/if}
    {@render entries()}
    {#if edit !== null && !adding}
      <div class="px-3 py-1">
        <button
          type="button"
          class="inline-flex items-center gap-1 rounded-control px-1 text-xs font-medium text-text-subtle transition-colors hover:text-text"
          onclick={beginAdd}
        >
          <svg
            class="h-3 w-3"
            viewBox="0 0 12 12"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linecap="round"
            aria-hidden="true"
          >
            <path d="M6 2.5v7M2.5 6h7" />
          </svg>
          {kind === "array" ? "element" : "field"}
        </button>
      </div>
    {/if}
  </div>
{:else if isContainer && count === 0 && !adding}
  <div
    class="json-row flex items-center gap-1.5 {depth > 0 ? '' : 'px-3'} py-1 font-mono text-base leading-5"
  >
    <span class="text-text-subtle italic">{kind === "object" ? "{}" : "[]"}</span>
    <div class="row-actions flex items-center gap-1">
      <CopyJson {value} />
      {#if edit !== null}{@render addButton()}{/if}
    </div>
  </div>
{:else if isContainer}
  <div class="flex flex-col {depth > 0 ? 'border-l border-border/60 pl-3' : ''}">
    <div class="json-row flex items-center gap-1.5 {depth > 0 ? '' : 'px-3'} py-1 font-mono text-base leading-5">
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
      <div class="row-actions flex items-center gap-1">
        <CopyJson {value} />
        {#if edit !== null && expanded}{@render addButton()}{/if}
      </div>
    </div>
    {#if expanded}
      {@render entries()}
      <div class="{depth > 0 ? '' : 'px-3'} py-0.5 font-mono text-base leading-5 text-text-subtle">
        {kind === "object" ? "}" : "]"}
      </div>
    {/if}
  </div>
{:else if kind === "string"}
  <span class="text-syntax-string break-all"
    >"{stringShown}{#if isLongString && !stringExpanded}…{/if}"</span
  >
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

<style>
  /*
   * The hover idiom, as a rule rather than a utility, for the one thing
   * Tailwind's group- variants cannot express here: a row's affordances must
   * answer to *that* row, and this tree nests rows inside rows. `group-hover`
   * matches any ancestor carrying the group class, so hovering a top-level
   * field would light up every button in its subtree. The direct-child
   * combinator is exactly the containment the tree needs.
   *
   * :focus-within keeps the keyboard route open — a button at opacity 0 is
   * still focusable, and one that stayed invisible while focused would be a
   * tab stop nobody can see.
   */
  .json-row {
    transition: background-color 120ms ease;
  }

  .json-row:hover {
    background-color: color-mix(in oklab, var(--color-surface-overlay) 55%, transparent);
  }

  .row-actions {
    opacity: 0;
    transition: opacity 120ms ease;
  }

  .json-row:hover > .row-actions,
  .json-row:focus-within > .row-actions {
    opacity: 1;
  }
</style>
