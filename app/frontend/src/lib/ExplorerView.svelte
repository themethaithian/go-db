<script lang="ts">
  // The Explorer: the Database tool window on the left, and the contents of
  // whatever is selected in it on the right.
  //
  // This view has one job and no ambiguity about it. There is no SQL to edit
  // here, so the grid cannot drift away from the highlighted table the way it
  // could when a tree and an editor shared a screen: the selection lives in
  // schema.svelte.ts, and everything on the right — the name in the header,
  // the statement in the caption, the rows in the grid — is derived from it.
  //
  // The rows are still fetched by running a SELECT through RunQuery, so this
  // browsing goes through the same Approval Gate as anything typed by hand,
  // is classified by it, and lands in the same audit log. The caption under
  // the header says exactly which statement that was: a data panel that
  // fetched rows by some private path would be asking to be trusted, and this
  // one would rather be checked.
  //
  // What "the contents" are is the Engine's to say (ADR-0006), and browse.ts
  // writes the statement for each: a MySQL table is a SELECT, a MongoDB
  // collection is a find, and a Redis key takes two reads — TYPE to learn what
  // it holds, then the one command that reads that type. Both of those go the
  // same way through RunQuery and are shown by the same panel; the caption
  // still says exactly what ran, which for a key is the second command.
  //
  // The controls above the grid — a WHERE condition and a row limit — are
  // edits to that one statement and nothing more. The condition is dropped in
  // verbatim, exactly as typed: it is the human's SQL, and the gate that
  // classifies it (and the READ ONLY transaction behind it) is the same
  // safety story as the Editor's. Sanitising it here would only teach people
  // that the panel writes queries they did not. They are MySQL's controls and
  // are shown for MySQL only: there is no WHERE to put on a GET.
  //
  // Values in these rows can also be typed over, in the record pane, and that
  // is the one thing this panel does that is not a read — but it is not a
  // second write path either. Saving builds an UPDATE per dirty row and sends
  // it through RunQuery like any other statement: the gate withholds it, the
  // Inline Confirm below shows exactly what would run with its Impact Preview,
  // and nothing moves until the human says so.
  //
  // The same holds for the other two Engines' answers, one element at a time
  // (mutateValue.ts). A browsed Redis key's hash field, list element, sorted
  // set score or whole string value can be typed over, and a browsed MongoDB
  // document can be edited or deleted whole — each producing exactly one
  // statement, and each going the same way through RunQuery. This panel is
  // where those affordances live because this is where the provenance is: the
  // Profile, the database and the key or collection are the selection in the
  // tree, unambiguously. A value that arrived in the Editor has no such
  // provenance — its statement would have to be read backwards to find out
  // which key it named — so the Editor's Value and Documents answers stay
  // read-only, and say so where they render them.
  //
  // A confirmed write is followed by re-running the browse, so what the pane
  // shows next is the database's own answer rather than this app's idea of
  // what it should now hold.
  import { untrack } from "svelte";
  import DatabaseTree from "./DatabaseTree.svelte";
  import InlineConfirm from "./InlineConfirm.svelte";
  import RecordPaneDock from "./RecordPaneDock.svelte";
  import ResultsTable from "./ResultsTable.svelte";
  import ValueView from "./ValueView.svelte";
  import DocumentsView from "./DocumentsView.svelte";
  import SaveBar from "./SaveBar.svelte";
  import Splitter from "./Splitter.svelte";
  import {
    clamp,
    COMPARE_MIN_PX,
    DETAIL_MIN_PX,
    GRID_MIN_PX,
    layout,
    persistLayout,
    TREE_MAX_PX,
    TREE_MIN_PX,
  } from "./layout.svelte";
  import StructureView from "./StructureView.svelte";
  import {
    browse,
    BROWSE_LIMITS,
    engineOf,
    ensureColumns,
    ensureIndexes,
    findTable,
    hasStructure,
    refreshTableStructure,
    selected,
    type TableNode,
  } from "./schema.svelte";
  import {
    browseMongoCollection,
    browseTableSql,
    MISSING_KEY,
    MONGODB,
    MYSQL,
    noReadForType,
    REDIS,
    redisReadCommand,
    redisTypeCommand,
    UNNAMEABLE_COLLECTION,
    UNQUOTABLE_KEY,
  } from "./browse";
  import { RowEdits } from "./edits.svelte";
  import { ValueEdits } from "./valueEdits.svelte";
  import { editability, qualify } from "./mutate";
  import {
    deleteKeyCommand,
    deleteOneCommand,
    removeCommand,
    replaceOneCommand,
    writeCommand,
    type Slot,
  } from "./mutateValue";
  import { CancelPending, RunQuery } from "../../wailsjs/go/app/App";
  import type { service } from "../../wailsjs/go/models";

  /** Data shows the grid; Structure shows the table's columns and indexes. */
  type PanelMode = "data" | "structure";

  let {
    connectedProfiles,
    onGoToConnections,
    onOpenInEditor,
  }: {
    connectedProfiles: string[];
    onGoToConnections: () => void;
    onOpenInEditor: (profileName: string, sql: string) => void;
  } = $props();

  // The vertical splitter's own footprint (a 6px hit area), which is width
  // neither the grid nor the record pane gets.
  const SPLITTER_PX = 6;

  let result = $state<service.QueryResult | null>(null);
  let failure = $state<string | null>(null);
  let loading = $state(false);
  // The statement behind what is on screen right now — set from the fetch
  // that produced it, never from the current controls, so the caption is a
  // record rather than a promise.
  let shownSql = $state("");
  // The Redis key behind what is on screen, for the one thing shownSql cannot
  // answer for a key: whether a fetch is a re-read of what is already there.
  // A key's statement is not known until TYPE has been asked.
  let shownKey = $state<string | null>(null);
  // What TYPE said that key holds, which is what says how to read the reply
  // that came back — a flat array is a list's elements or a sorted set's
  // members and scores depending only on this, and an edit addresses a
  // different thing in each. Recorded from the fetch, like shownSql, so it
  // describes what is on screen rather than what is selected.
  let shownType = $state<string | null>(null);

  // The condition as it is being typed, which is not yet the condition in
  // force: it becomes browse.filter on Enter, and that is what re-runs the
  // query. Nothing is fetched while a half-written WHERE is on screen.
  let filterInput = $state(browse.filter);

  // The picked rows, as indices into the rows on screen and in the order they
  // were picked — that order is the order of the record pane's value columns.
  // Empty means the pane is closed. Every fetch clears it: an index means
  // nothing against a result set it was not taken from.
  let selectedRows = $state<number[]>([]);

  // Data shows the grid and its WHERE/LIMIT controls; Structure shows the
  // table's columns and indexes instead. It is not scoped by table the way
  // browse.filter is — there is nothing to preserve, so every table starts
  // on Data rather than remembering the last mode picked for it.
  let mode = $state<PanelMode>("data");

  // Width of the grid + Row pane strip, so the splitter between them knows
  // how far it may travel before one of the two is squeezed out.
  let bodyWidth = $state(0);

  // The values typed over these rows and not yet saved. It belongs to this
  // view, not to the module: the Editor's grid has its own, and a row index
  // means nothing between two different result sets.
  const edits = new RowEdits();

  // The edit open on one browsed value or document, which is the same idea one
  // element at a time (valueEdits.svelte.ts). Two objects rather than one
  // because they hold different things — a patch set against rows, an open box
  // against an element — but they can never both be busy: a browse answer is
  // rows, or a value, or documents, and never two of those at once.
  const valueEdits = new ValueEdits();

  // Fetches are numbered so a slow one cannot overwrite a fast one started
  // later: clicking three tables quickly must leave the third one's rows on
  // screen, not whichever query the database happened to finish last. Filter
  // and limit changes take the same numbered path, for the same reason.
  let latestRequest = 0;

  // The Engine of the Profile the selection is on: which statement browses it,
  // and whether it has a Structure side at all.
  let engine = $derived(engineOf(selected.profile));
  let structured = $derived(hasStructure(selected.profile));

  // The statement that browses the selection, for the Engines whose browse is
  // one statement. A Redis key's is not — its command depends on a TYPE that
  // has to be asked for first — so this is empty there and fetchRedisKey
  // builds the real one once it knows. Everything that reads a statement out
  // of this view (the caption, "Open in editor", the refresh) reads shownSql
  // instead, which is a record of what actually ran.
  let browseSql = $derived(
    buildStatement(engine, selected.database, selected.table, browse.filter, browse.limit),
  );

  // The row count in the header, for the answers that have rows. A value or a
  // documents answer has none, and "0 rows" over a Redis string would be a
  // count of the wrong thing — while a table answer with no rows genuinely is
  // 0 rows, and its `rows` field is absent from the JSON rather than empty.
  let rowCount = $derived(
    result?.status === "ok" && result.value === undefined && result.documents === undefined
      ? (result.rows?.length ?? 0)
      : null,
  );
  let shownColumns = $derived(result?.status === "ok" ? (result.columns ?? []) : []);
  let shownRows = $derived(
    result?.status === "ok" ? ((result.rows ?? []) as (string | null)[][]) : [],
  );
  // The selection as the pane and the grid both see it: indices that still
  // point at a row, and those rows' cells in the same order. Filtering here
  // rather than trusting the state means a selection can never outlive the
  // rows it was taken from, however it was cleared.
  let pickedIndices = $derived(selectedRows.filter((index) => shownRows[index] !== undefined));
  let pickedRows = $derived(pickedIndices.map((index) => shownRows[index]));

  // The statement a save is currently holding out for, if any — from either
  // editor, since only one of them can ever have raised one. Read into a
  // single local so the panel that renders it and the props it is given are
  // the same withheld statement, not two reads of a moving one, and so the
  // Inline Confirm below is written once rather than twice.
  let confirming = $derived(
    edits.pending !== null
      ? { held: edits.pending, sql: edits.pendingSql, settle: (next: service.QueryResult) => edits.resolved(next) }
      : valueEdits.pending !== null
        ? {
            held: valueEdits.pending,
            sql: valueEdits.pendingSql,
            settle: (next: service.QueryResult) => valueEdits.resolved(next),
          }
        : null,
  );

  let filterDirty = $derived(filterInput.trim() !== browse.filter);
  // Comparing asks for more room than reading one row does, so the pane's
  // floor moves with what it is doing.
  let detailMin = $derived(pickedIndices.length > 1 ? COMPARE_MIN_PX : DETAIL_MIN_PX);
  let detailMax = $derived(Math.max(detailMin, bodyWidth - GRID_MIN_PX - SPLITTER_PX));

  // The tree's own node for the selected table — the schema cache the
  // Structure view reads and refreshes, shared with the Database tree so the
  // two can never disagree about a table's columns. Resolvable as soon as a
  // table is selected: selecting one requires clicking it in the tree, which
  // requires its Profile's table list — and so this table's node — to
  // already be loaded.
  let tableNode: TableNode | null = $derived(
    selected.profile === null || selected.database === null || selected.table === null
      ? null
      : findTable(selected.profile, selected.database, selected.table),
  );
  let structureLoading = $derived(
    tableNode !== null && (tableNode.loading || tableNode.indexesLoading),
  );

  // Whether these rows can be typed over, which here comes down to one
  // question: does the table have a primary key? Browsing selects *, so a key
  // that exists is always in the result — there is no third case where the
  // table has one and the grid cannot see it.
  let editable = $derived(editability(selected.table, shownColumns, tableNode?.indexes ?? null));
  let readOnlyHint = $derived(editable.editable ? null : editable.reason);

  // Every table starts on Data — a table switch is a strong enough signal
  // that a mode picked for the last one should not carry over.
  $effect(() => {
    selected.database;
    selected.table;
    mode = "data";
  });

  // The schema this view needs. Indexes are wanted in either mode — the
  // primary key is what decides whether the grid's rows can be edited, so Data
  // asks for them too — while the columns are the Structure view's alone.
  // ensureColumns and ensureIndexes are the same no-op-if-cached calls the
  // tree's toggleTable makes, so flipping between Data and Structure, or
  // between tables and back, never re-asks the database.
  //
  // Neither is asked off MySQL: a key has no columns and a collection has no
  // fixed ones, and there is no Structure side for them to fill. Both calls
  // hold that line themselves too, so this is the early exit rather than the
  // safeguard.
  $effect(() => {
    const profileName = selected.profile;
    const database = selected.database;
    const node = tableNode;
    if (!structured || profileName === null || database === null || node === null) return;
    ensureIndexes(profileName, database, node);
    if (mode !== "structure") return;
    ensureColumns(profileName, database, node);
  });

  // A confirm nobody will answer must not be left in the gate's queue: leaving
  // the Explorer unmounts this view, and an Inline Confirm has no expiry.
  $effect(() => () => {
    edits.abandon();
    valueEdits.abandon();
  });

  // The statement is the whole input to this view: whenever it changes — a
  // table picked in the tree, a condition applied, a limit chosen, or a
  // Profile disconnecting out from under it — the rows are fetched again for
  // exactly that statement.
  //
  // A Redis key is the one selection with no statement yet, so it is keyed on
  // the key itself and fetchRedisKey works out the rest. Both paths take the
  // same numbered route, so a slow one cannot land on top of a fast one.
  $effect(() => {
    const profileName = selected.profile;
    const table = selected.table;
    const key = engine === REDIS ? table : null;
    const sql = browseSql;

    if (profileName === null || table === null) {
      clearPanel(null);
      return;
    }
    if (key !== null) {
      void fetchRedisKey(profileName, key);
      return;
    }
    if (sql === "") {
      // Something is selected and this Engine cannot write a statement that
      // names it — a MongoDB collection whose name is outside the grammar.
      // Saying so beats a panel that sits on "Loading…" for ever.
      clearPanel(engine === MONGODB ? UNNAMEABLE_COLLECTION : null);
      return;
    }
    void fetchRows(profileName, sql);
  });

  // Empties the panel, optionally leaving one line in place of the results.
  // Bumping the request number is what makes it stick: a fetch already in
  // flight lands on a number that is no longer the latest and drops itself.
  function clearPanel(reason: string | null) {
    latestRequest += 1;
    result = null;
    failure = reason;
    loading = false;
    shownSql = "";
    shownKey = null;
    shownType = null;
    selectedRows = [];
    valueEdits.abandon();
    valueEdits.reset();
  }

  // The box shows the condition in force: applying one normalises what is
  // typed, and switching tables (which drops the condition) empties the box
  // rather than leaving a stale one behind, applied or not.
  $effect(() => {
    selected.database;
    selected.table;
    filterInput = browse.filter;
  });

  // A window that shrinks must shrink the record pane with it, or the grid
  // disappears off the edge with no way to drag it back — and a pane that
  // starts comparing is widened to the comparison's floor the same way.
  $effect(() => {
    const min = detailMin;
    const max = detailMax;
    if (bodyWidth <= 0) return;
    const next = clamp(untrack(() => layout.explorerDetailWidth), min, max);
    if (next !== layout.explorerDetailWidth) layout.explorerDetailWidth = next;
  });

  async function fetchRows(profileName: string, sql: string) {
    const request = (latestRequest += 1);
    // Rows picked out of the old result set cannot survive a new one — the
    // indices would land on some other rows, or on none.
    selectedRows = [];
    // Nor can edits made against it, for the same reason, and a save waiting
    // on a confirm is a save for rows that are about to be replaced.
    edits.abandon();
    edits.revert();
    // The document editor goes the same way: its open card is a card in a
    // result set that is about to be replaced.
    valueEdits.abandon();
    valueEdits.reset();
    // Whatever is on screen is not a Redis key's value any more.
    shownKey = null;
    shownType = null;
    // Rows from the table we were looking at a moment ago are worse than no
    // rows: they would sit under another table's name, or another condition.
    // They go the instant the statement changes — but re-running the same one
    // keeps them, so a refresh does not blink the grid out and back.
    // (untracked: this runs inside the statement effect, which must not
    // re-fire on the caption it is itself writing.)
    if (untrack(() => shownSql) !== sql) {
      result = null;
      failure = null;
      shownSql = "";
    }
    loading = true;
    try {
      // No database: the browse statement is qualified with the schema it
      // reads, and runs on the Profile's own connection as it always has.
      const next = await runRead(profileName, sql);
      if (request !== latestRequest) return;
      result = next;
      failure = null;
      shownSql = sql;
    } catch (err) {
      if (request !== latestRequest) return;
      result = null;
      failure = String(err);
      shownSql = sql;
    } finally {
      if (request === latestRequest) loading = false;
    }
  }

  // Browsing one Redis key, which takes two reads: TYPE to learn what is in
  // it, and then the one command that reads that type (browse.ts maps them).
  //
  // Two reads rather than one guess. Redis has no command that reads a key of
  // any type, and trying GET first would answer WRONGTYPE for four of the six
  // — an error where there is data. Both reads go through RunQuery, so both
  // are classified and both are in the audit log, which is the same bargain
  // the rest of this panel makes: the reads are visible because they are real.
  //
  // The caption ends up showing the second command, which is the one that
  // produced what is on screen.
  async function fetchRedisKey(profileName: string, key: string) {
    const typeCommand = redisTypeCommand(key);
    if (typeCommand === null) {
      clearPanel(UNQUOTABLE_KEY);
      return;
    }

    const request = (latestRequest += 1);
    selectedRows = [];
    edits.abandon();
    edits.revert();
    valueEdits.abandon();
    valueEdits.reset();
    // What is on screen belongs to the key that was selected a moment ago, and
    // leaving it under another key's name would be worse than showing nothing.
    // Re-reading the same key keeps it, so refresh does not blink — the same
    // bargain fetchRows makes with the statement, made with the key because
    // the statement is not known until TYPE has answered.
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
      const typed = await runRead(profileName, typeCommand);
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
        clearPanel(MISSING_KEY);
        return;
      }
      const readCommand = redisReadCommand(key, type);
      if (readCommand === null) {
        clearPanel(noReadForType(type));
        return;
      }

      const next = await runRead(profileName, readCommand);
      if (request !== latestRequest) return;
      result = next;
      failure = null;
      shownSql = readCommand;
      // Recorded beside the statement it belongs to, and only once the read it
      // describes has landed: a type set before the value arrived would tell
      // the pane how to lay out a reply that is not there yet.
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

  // One read through the gate, with the one thing this panel always does to a
  // withheld statement: cancel it. Nothing here confirms a write, and an
  // Inline Confirm nobody answers has no expiry.
  async function runRead(profileName: string, statement: string) {
    const answer = await RunQuery(profileName, "", statement);
    if (answer.status === "requires_confirmation" && answer.pending_id) {
      void CancelPending(answer.pending_id);
    }
    return answer;
  }

  // The statement, and the only place it is built. For MySQL the table is
  // qualified with the database it was picked from — the tree can show two
  // schemas with a `users` in each, and an unqualified name would read
  // whichever the connection happens to default to. Empty condition means no
  // WHERE clause at all, rather than a WHERE that is quietly always true.
  //
  // MongoDB takes neither: its grammar has one call per statement and the
  // adapter caps what comes back, so there is no condition or limit to apply.
  // Redis is empty here on purpose — see browseSql.
  function buildStatement(
    engineName: string,
    database: string | null,
    table: string | null,
    filter: string,
    limit: number,
  ): string {
    if (table === null) return "";
    if (engineName === MONGODB) return browseMongoCollection(table) ?? "";
    if (engineName !== MYSQL) return "";
    if (database === null) return "";
    return browseTableSql(database, table, filter, limit, qualify);
  }

  function refresh() {
    if (selected.profile === null) return;
    if (mode === "structure" && structured) {
      if (tableNode !== null && selected.database !== null) {
        refreshTableStructure(selected.profile, selected.database, tableNode);
      }
      return;
    }
    // A key is refreshed from the top, TYPE included: what a key holds can
    // change between two looks at it, and re-running only the second command
    // would answer WRONGTYPE where the honest answer is the new value.
    if (engine === REDIS) {
      if (selected.table !== null) void fetchRedisKey(selected.profile, selected.table);
      return;
    }
    if (browseSql === "") return;
    void fetchRows(selected.profile, browseSql);
  }

  // Enter applies. A condition that has not changed still re-runs the query —
  // pressing Enter and having nothing at all happen is the kind of silence
  // that gets read as a broken box.
  function applyFilter() {
    const next = filterInput.trim();
    if (next === browse.filter) refresh();
    else browse.filter = next;
  }

  function clearFilter() {
    filterInput = "";
    applyFilter();
  }

  function handleFilterKey(event: KeyboardEvent) {
    if (event.key !== "Enter") return;
    event.preventDefault();
    applyFilter();
  }

  // Clicking a row picks it and nothing else — including when it was already
  // one of several, which is the way back from a comparison to a single row.
  // Cmd (or Ctrl) adds a row to the comparison, or takes it back out, and the
  // order rows go in is the order their columns appear in the pane. Shift
  // extends the comparison instead of toggling one row: every row between the
  // last-picked one and the one just clicked joins it, in the order they sit
  // on screen, skipping whichever of them were already picked. There is no
  // cap: the pane scrolls sideways under the field-name column once the value
  // columns outrun the width, so comparing more rows than fit is a scroll
  // away, not a wall.
  function pickRow(index: number, options: { additive: boolean; range: boolean }) {
    if (options.range && selectedRows.length > 0) {
      const anchor = selectedRows[selectedRows.length - 1];
      const [from, to] = anchor <= index ? [anchor, index] : [index, anchor];
      const already = new Set(selectedRows);
      const added: number[] = [];
      for (let i = from; i <= to; i += 1) {
        if (!already.has(i)) added.push(i);
      }
      selectedRows = [...selectedRows, ...added];
      return;
    }
    if (!options.additive) {
      selectedRows = [index];
      return;
    }
    if (selectedRows.includes(index)) {
      selectedRows = selectedRows.filter((picked) => picked !== index);
      return;
    }
    selectedRows = [...selectedRows, index];
  }

  // Up/down walk one row through the rows — but never while a control has the
  // focus, where those keys already mean something to it. Walking out of a
  // comparison collapses it: the keys move a single row, and the pane follows
  // what they moved. Escape is RecordPaneDock's own business (it also has to
  // arbitrate the pop-out), so this only ever sees the arrow keys.
  function handleWindowKey(event: KeyboardEvent) {
    if (selectedRows.length === 0) return;
    if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
    const tag = (event.target as HTMLElement | null)?.tagName;
    if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
    event.preventDefault();
    const step = event.key === "ArrowDown" ? 1 : -1;
    const from = selectedRows[selectedRows.length - 1];
    selectedRows = [clamp(from + step, 0, shownRows.length - 1)];
  }

  // The statement the Editor is handed is the one that ran, falling back to
  // the one that would: a Redis key has no statement until TYPE has answered,
  // and taking what is on screen to the Editor is the point of the button.
  let editorStatement = $derived(browseSql !== "" ? browseSql : shownSql);

  function openInEditor() {
    if (selected.profile === null || editorStatement === "") return;
    onOpenInEditor(selected.profile, editorStatement);
  }

  // Sends the dirty rows to the gate, one UPDATE and one Inline Confirm at a
  // time. Only a run that got all the way through re-fetches: a cancel part
  // way leaves the rows it never reached dirty, and fetching over them would
  // throw away exactly the edits the human kept.
  async function saveEdits() {
    const profileName = selected.profile;
    const database = selected.database;
    if (profileName === null || database === null || !editable.editable) return;
    const saved = await edits.save({
      profileName,
      database,
      table: editable.table,
      keyColumns: editable.keyColumns,
      columns: shownColumns,
      rows: shownRows,
    });
    if (saved > 0 && edits.changes === 0 && selected.profile === profileName) {
      void fetchRows(profileName, shownSql);
    }
  }

  // One generated statement, through the gate, and then the read again.
  //
  // Everything the browse pane writes comes through here, whichever Engine and
  // whichever affordance built it, because the sequence is the same for all of
  // them and it is the sequence that matters: build (a refusal stops here and
  // never reaches RunQuery), submit, wait for the Inline Confirm, and — only
  // when it actually ran — re-read, so the pane shows the database's own answer
  // rather than this app's idea of what it should now hold.
  //
  // A run that did not happen leaves the pane exactly as it was, box and all.
  // That is what makes Cancel a way back: the draft lives in valueEdits, which
  // outlives the confirm panel that unmounted the box while it was up.
  async function runWrite(build: () => string) {
    const profileName = selected.profile;
    if (profileName === null) return;
    const ran = await valueEdits.submit(profileName, build);
    // The selection can move while a confirm is on screen; re-reading then
    // would fetch the new selection under the old one's request number and
    // land rows nobody asked for.
    if (ran && selected.profile === profileName) refresh();
  }

  // The Redis affordances. The key they name is shownKey — the key whose value
  // is on screen — rather than the selection, and the distinction is the same
  // one shownSql makes for the caption: a write belongs to what is being looked
  // at, not to what has just been clicked. The two agree except for the moment
  // a fetch is in flight, and in that moment shownKey is the honest one, since
  // it is the key shownType was recorded for and the elements were read with.
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

  // The MongoDB affordances. The collection is the selection's own name, and
  // the _id is the document's own — carried back from the card exactly as it
  // arrived, so an ObjectId's {"$oid": "…"} round-trips rather than being
  // rebuilt from something that looks like it.
  function replaceDocument(id: unknown, document: unknown) {
    const collection = selected.table;
    if (collection === null) return;
    void runWrite(() => replaceOneCommand(collection, id, document));
  }

  function deleteDocument(id: unknown) {
    const collection = selected.table;
    if (collection === null) return;
    void runWrite(() => deleteOneCommand(collection, id));
  }

  // Whether the pane is showing a browsed Redis key's value — which is the one
  // thing "Delete key" can honestly mean. A value the gate or the server
  // answered with instead is not a key's contents.
  let browsingKey = $derived(
    engine === REDIS && shownKey !== null && result?.status === "ok" && result.value !== undefined,
  );

  function resizeTree(next: number) {
    layout.explorerTreeWidth = clamp(next, TREE_MIN_PX, TREE_MAX_PX);
    persistLayout();
  }
</script>

<svelte:window onkeydown={handleWindowKey} />

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
    <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border bg-surface-panel px-3">
      {#if selected.table === null || selected.profile === null}
        <span class="text-base text-text-muted">No table selected</span>
      {:else}
        <!-- The name as the statement writes it: the database dimmed, the
             table it qualifies in full — two schemas can each have a `users`.
             A Redis key is shown alone, because the index it is in is not part
             of any command that names it. -->
        <span
          class="truncate font-mono text-md font-medium text-text"
          title={engine === REDIS ? selected.table : `${selected.database}.${selected.table}`}
        >
          {#if engine !== REDIS}<span class="text-text-subtle">{selected.database}.</span>{/if}{selected.table}
        </span>
        <span
          class="shrink-0 rounded-full border border-accent/40 bg-accent/10 px-2 py-0.5 text-xs font-medium text-accent"
          title="Profile"
        >
          {selected.profile}
        </span>
        {#if mode === "data"}
          {#if loading}
            <span class="shrink-0 text-sm text-text-subtle">loading…</span>
          {:else if rowCount !== null}
            <span class="shrink-0 text-sm tabular-nums text-text-muted">
              {rowCount === 1 ? "1 row" : `${rowCount.toLocaleString()} rows`}
            </span>
          {/if}
        {/if}

        <div class="flex-1"></div>

        <!-- The Data | Structure toggle, in the app's own pill/tab language
             (App.svelte's nav bar): a bordered strip, the active side filled
             with the accent, the other muted until hovered.
             Structure is MySQL's: a key has no columns and no indexes, so the
             toggle is absent rather than present and empty. -->
        {#if structured}
        <div
          class="flex h-8 shrink-0 items-center gap-0.5 rounded-control border border-border bg-surface p-0.5 text-sm"
          role="group"
          aria-label="Data or Structure"
        >
          <button
            type="button"
            class="rounded-control px-2.5 py-1 font-medium transition-colors {mode === 'data'
              ? 'bg-accent text-white'
              : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
            aria-pressed={mode === "data"}
            onclick={() => (mode = "data")}
          >
            Data
          </button>
          <button
            type="button"
            class="rounded-control px-2.5 py-1 font-medium transition-colors {mode === 'structure'
              ? 'bg-accent text-white'
              : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
            aria-pressed={mode === "structure"}
            onclick={() => (mode = "structure")}
          >
            Structure
          </button>
        </div>
        {/if}

        <!-- Deleting the whole key is a pane-level action, not an element's,
             so it sits with the pane's other pane-level buttons — and only
             while a key's value is what the pane is showing. It is one
             statement like every other affordance here, and goes the same way
             through the gate. -->
        {#if browsingKey}
          <button
            type="button"
            class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-base text-text-muted transition-colors hover:border-danger/50 hover:text-danger disabled:cursor-not-allowed disabled:text-text-subtle"
            onclick={deleteKey}
            disabled={valueEdits.saving}
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

        <button
          type="button"
          class="inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border border-border bg-surface-raised px-2.5 text-base text-text-muted transition-colors hover:border-border-strong hover:text-text"
          onclick={openInEditor}
          title="Open this SELECT in the Editor"
        >
          <svg
            class="h-3.5 w-3.5"
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="M9.25 2.75h4v4M13.25 2.75 7.5 8.5" />
            <path d="M12.25 9.75v2.5a1.5 1.5 0 0 1-1.5 1.5h-7a1.5 1.5 0 0 1-1.5-1.5v-7a1.5 1.5 0 0 1 1.5-1.5h2.5" />
          </svg>
          Open in editor
        </button>
        <button
          type="button"
          class="flex h-8 w-8 shrink-0 items-center justify-center rounded-control border border-border bg-surface-raised text-text-muted transition-colors hover:border-border-strong hover:text-text disabled:cursor-not-allowed disabled:text-text-subtle"
          onclick={refresh}
          disabled={mode === "data" ? loading : structureLoading}
          title="Run the query again"
          aria-label="Run the query again"
        >
          <svg
            class="h-4 w-4"
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            stroke-width="1.4"
            stroke-linecap="round"
            stroke-linejoin="round"
            aria-hidden="true"
          >
            <path d="M13.25 8a5.25 5.25 0 1 1-1.6-3.78" />
            <path d="M13.4 2.4v2.85h-2.85" />
          </svg>
        </button>
      {/if}
    </div>

    <div class="flex min-h-0 flex-1 flex-col p-3">
      <!-- A save in progress takes the whole panel, exactly as it does in the
           Editor: one withheld statement, its Impact Preview, and the two keys
           that decide it. The pane comes back the moment it is answered —
           holding whatever was typed into it, since the draft outlives this
           panel (valueEdits.svelte.ts). Whichever editor raised it, it is
           rendered once: the two can never both be withholding one. -->
      {#if confirming !== null && confirming.held.pending_id && confirming.held.preview}
        <InlineConfirm
          reason={confirming.held.classification.reason}
          preview={confirming.held.preview}
          pendingId={confirming.held.pending_id}
          sql={confirming.sql}
          onResolved={confirming.settle}
        />
      {:else}
      <section
        class="flex min-h-0 flex-1 flex-col overflow-hidden rounded-panel border border-border bg-surface-panel shadow-panel"
      >
        <!-- WHERE and LIMIT are edits to a SELECT, so they are shown where
             there is one. A find and a GET take neither: the adapter caps
             what a find returns, and a key is one key. -->
        {#if selected.table !== null && mode === "data" && engine === MYSQL}
          <div class="flex h-11 shrink-0 items-center gap-3 border-b border-border px-3">
            <div
              class="flex h-8 min-w-0 flex-1 items-center gap-2 rounded-control border border-border bg-surface px-2.5 transition-colors focus-within:border-accent hover:border-border-strong"
            >
              <span class="shrink-0 font-mono text-xs font-medium tracking-wide text-text-subtle">
                WHERE
              </span>
              <input
                class="min-w-0 flex-1 bg-transparent font-mono text-base text-text placeholder:text-text-subtle focus:outline-none"
                placeholder="city = 'Bangkok' — filter condition"
                bind:value={filterInput}
                onkeydown={handleFilterKey}
                spellcheck="false"
                autocapitalize="off"
                autocomplete="off"
                aria-label="WHERE condition"
              />
              {#if filterInput !== ""}
                <button
                  type="button"
                  class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control text-text-subtle transition-colors hover:bg-surface-overlay hover:text-text"
                  onclick={clearFilter}
                  title="Clear the condition"
                  aria-label="Clear the condition"
                >
                  <svg
                    class="h-3 w-3"
                    viewBox="0 0 12 12"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.5"
                    stroke-linecap="round"
                    aria-hidden="true"
                  >
                    <path d="M3 3l6 6M9 3l-6 6" />
                  </svg>
                </button>
              {/if}
              <!-- The apply affordance, and only when there is something to
                   apply: a condition on screen that is not the one that ran. -->
              {#if filterDirty}
                <button
                  type="button"
                  class="flex h-5 shrink-0 items-center gap-1 rounded-control border border-accent/40 bg-accent/10 px-1.5 text-xs font-medium text-accent transition-colors hover:bg-accent/20"
                  onclick={applyFilter}
                  title="Apply the condition (Enter)"
                >
                  <svg
                    class="h-3 w-3"
                    viewBox="0 0 12 12"
                    fill="none"
                    stroke="currentColor"
                    stroke-width="1.4"
                    stroke-linecap="round"
                    stroke-linejoin="round"
                    aria-hidden="true"
                  >
                    <path d="M9.5 2.5v3.25a1.5 1.5 0 0 1-1.5 1.5H2.75" />
                    <path d="M4.75 5.25 2.5 7.25l2.25 2" />
                  </svg>
                  Apply
                </button>
              {/if}
            </div>

            <label class="flex shrink-0 items-center gap-1.5 text-xs font-medium tracking-wide text-text-muted uppercase">
              Limit
              <select
                class="h-8 rounded-control border border-border bg-surface-raised px-2 text-base font-normal text-text tabular-nums transition-colors hover:border-border-strong"
                bind:value={browse.limit}
              >
                {#each BROWSE_LIMITS as option (option)}
                  <option value={option}>{option}</option>
                {/each}
              </select>
            </label>
          </div>
        {/if}

        <div class="flex h-8 shrink-0 items-center justify-between gap-3 border-b border-border px-3">
          {#if mode === "structure" && structured}
            <span class="text-xs font-medium tracking-wide text-text-subtle uppercase">Structure</span>
          {:else if shownSql === ""}
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

        {#if mode === "structure" && structured && selected.table !== null}
          <StructureView node={tableNode} />
        {:else if selected.table === null}
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
        {:else if failure !== null}
          <div class="p-3">
            <p
              class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
            >
              {failure}
            </p>
          </div>
        {:else if result === null}
          <div class="m-auto px-6 py-8 text-center">
            <p class="text-base text-text-muted">
              {engine === MYSQL ? "Loading rows…" : "Loading…"}
            </p>
          </div>
        {:else if result.status === "ok" && result.documents !== undefined}
          <!-- The Documents arm (ADR-0006) — what browsing a MongoDB
               collection answers with. The cards are editable here because
               this panel knows which collection they came from: it is the
               selection in the tree, and every call built from it addresses
               one document by its own _id. -->
          <DocumentsView
            documents={result.documents}
            message={result.message}
            edits={valueEdits}
            collection={selected.table}
            onReplace={replaceDocument}
            onDelete={deleteDocument}
          />
        {:else if result.status === "ok" && result.value !== undefined}
          <!-- The Value arm (ADR-0006) — what browsing a Redis key answers
               with. The elements are editable here for the same reason the
               documents above are: the key is the selection, and the type
               TYPE reported is what says how to read the reply that came
               back. -->
          <ValueView
            value={result.value}
            message={result.message}
            edits={valueEdits}
            type={shownType}
            onWrite={writeElement}
            onRemove={removeElement}
          />
        {:else if result.status === "ok"}
          <div class="flex min-h-0 min-w-0 flex-1" bind:clientWidth={bodyWidth}>
            <div class="flex min-w-0 flex-1">
              <ResultsTable
                columns={shownColumns}
                rows={shownRows}
                truncated={result.truncated}
                selectedIndices={pickedIndices}
                {edits}
                editable={editable.editable}
                {readOnlyHint}
                onRowClick={(index, options) => pickRow(index, options)}
              />
            </div>

            <RecordPaneDock
              columns={shownColumns}
              rows={pickedRows}
              indices={pickedIndices}
              totalRows={shownRows.length}
              {edits}
              editable={editable.editable}
              {readOnlyHint}
              {bodyWidth}
              gridMinPx={GRID_MIN_PX}
              splitterPx={SPLITTER_PX}
              detailWidth={layout.explorerDetailWidth}
              {detailMin}
              {detailMax}
              onResizeDetail={(next) => {
                layout.explorerDetailWidth = next;
                persistLayout();
              }}
              onClose={() => (selectedRows = [])}
            />
          </div>
        {:else}
          <!-- Anything but "ok" is the connection, the database, or the gate
               talking (not_connected, failed, and — if a condition classifies
               as a mutation — requires_confirmation or blocked). Shown
               verbatim, above controls that still work, since the fix is
               almost always a word in the WHERE box. Nothing here offers to
               confirm a write: this panel reads. -->
          <div class="p-3">
            <p
              class="rounded-control border border-danger/40 bg-danger/10 px-3 py-2 font-mono text-base text-danger"
            >
              {result.message}
            </p>
            {#if result.status === "requires_confirmation"}
              <!-- The gate's message ends "Confirm to run it", and there is
                   nothing here to confirm with — on purpose. Saying where that
                   decision does live beats leaving a dead end on screen. -->
              <p class="px-3 py-2 text-sm text-text-muted">
                Nothing ran. This panel only reads — fix the condition, or take the statement to
                the Editor with “Open in editor” and decide on it there.
              </p>
            {/if}
          </div>
        {/if}

        {#if edits.changes > 0}
          <SaveBar
            changes={edits.changes}
            rows={edits.dirtyRows.length}
            saving={edits.saving}
            failure={edits.failure}
            onSave={saveEdits}
            onRevert={() => edits.revert()}
          />
        {/if}
      </section>
      {/if}
    </div>
  </div>
</div>
