// The edits pending on one result set, and what happens when they are saved.
//
// A dirty cell is a value the human has typed over a fetched one and not yet
// sent anywhere. It is held here, beside the row index it belongs to, and the
// grid and the record pane both read it — the rows themselves are never
// written over, because the row as it was fetched is what identifies it when
// the UPDATE is finally built.
//
// Saving is a sequence, not a batch: one statement per dirty row, submitted
// through RunQuery, each one withheld by the Approval Gate and answered by the
// human in the same Inline Confirm any typed mutation gets. A confirmed row
// loses its dirty marks and the next statement goes out; a cancelled one stops
// the run there and leaves every remaining mark exactly where it was, which is
// what makes Cancel a way back rather than a loss. The view supplies nothing
// but the target and renders the panel when `pending` is set.
//
// One of these belongs to one view (Explorer, Editor), not to the module: two
// views showing two result sets have two independent sets of dirty rows, and a
// row index means nothing across them.

import type { service } from "../../wailsjs/go/models";
import { GateRun } from "./gate.svelte";
import { buildUpdate, type CellValue } from "./mutate";

/** One row's pending values, by column name. */
type RowPatch = Record<string, CellValue>;

/** Everything the save needs about the rows it is saving. */
export type SaveTarget = {
  profileName: string;
  /**
   * The schema the table lives in, or "" for the connection's own default.
   * The Explorer browses a named database and so writes a qualified UPDATE;
   * the Editor's statements are unqualified on purpose, and omit this.
   */
  database?: string;
  /**
   * The database the UPDATE is *run* in — the connection whose own default
   * schema it is, "" for the Profile's own connection. It is a different
   * question from `database` above, and both answers are honest: the Explorer
   * qualifies its statement and runs it on the Profile's connection, while the
   * Editor runs on the connection for the database its tab selected and leaves
   * the table name bare, exactly as a typed statement does.
   */
  runIn?: string;
  table: string;
  /** The primary key's columns, in the index's own order. */
  keyColumns: string[];
  /** The result's columns, which is the order the SET clause is written in. */
  columns: string[];
  /** The rows as fetched — where the WHERE clause's values come from. */
  rows: CellValue[][];
};

export class RowEdits {
  // Row index → column name → the value typed over it. A column is absent
  // until it is edited, and goes absent again when the edit is taken back, so
  // "is this cell dirty" is a lookup rather than a comparison.
  #patches = $state<Record<number, RowPatch>>({});

  // One statement's trip through the gate, which is the same trip whatever
  // built the statement (gate.svelte.ts). This class supplies the loop around
  // it and the row bookkeeping either side; the withheld statement, the wait
  // for the Inline Confirm and the cancelling of one nobody will answer all
  // live in there, shared with the value editor that has the same need.
  #gate = new GateRun();

  // Whether a save is running, which is the loop rather than one statement:
  // the gate's own `running` goes false between two dirty rows, and a save bar
  // that flickered off in that gap would be reporting the wrong thing.
  #saving = $state(false);

  /** The withheld statement, for the view to raise an Inline Confirm on. */
  get pending(): service.QueryResult | null {
    return this.#gate.pending;
  }

  /** That statement's text, to be shown with it. */
  get pendingSql(): string | null {
    return this.#gate.pendingSql;
  }

  /** Whether a save is in flight, including while it waits on a confirm. */
  get saving(): boolean {
    return this.#saving;
  }

  /** Why the last save stopped short, or null. */
  get failure(): string | null {
    return this.#gate.failure;
  }

  /** How many cells are dirty — the number the save bar counts. */
  get changes(): number {
    return Object.values(this.#patches).reduce(
      (total, patch) => total + Object.keys(patch).length,
      0,
    );
  }

  /** The dirty rows' indices, ascending — one statement each. */
  get dirtyRows(): number[] {
    return Object.keys(this.#patches)
      .map(Number)
      .sort((a, b) => a - b);
  }

  /** What this cell shows: the pending value if there is one, else the fetched one. */
  value(row: number, column: string, fetched: CellValue): CellValue {
    const patch = this.#patches[row];
    if (patch !== undefined && Object.hasOwn(patch, column)) return patch[column];
    return fetched;
  }

  /** Whether this cell is holding an unsaved value. */
  dirty(row: number, column: string): boolean {
    const patch = this.#patches[row];
    return patch !== undefined && Object.hasOwn(patch, column);
  }

  /**
   * Records an edit — or takes one back, when the value typed is the value
   * that was already there. Setting a cell back to what the database gave is
   * how a human undoes a change they are halfway through, and leaving it dirty
   * would put it in an UPDATE that changes nothing.
   */
  set(row: number, column: string, value: CellValue, fetched: CellValue) {
    if (value === fetched) {
      this.revertCell(row, column);
      return;
    }
    const patch = { ...(this.#patches[row] ?? {}), [column]: value };
    this.#patches = { ...this.#patches, [row]: patch };
  }

  /** Drops one cell's edit, and the row with it once it holds nothing. */
  revertCell(row: number, column: string) {
    const patch = this.#patches[row];
    if (patch === undefined || !Object.hasOwn(patch, column)) return;
    const { [column]: _dropped, ...rest } = patch;
    const next = { ...this.#patches };
    if (Object.keys(rest).length === 0) delete next[row];
    else next[row] = rest;
    this.#patches = next;
  }

  /** Drops every edit, saved or not — the save bar's Revert, and every re-fetch. */
  revert() {
    this.#patches = {};
    this.#gate.clearFailure();
  }

  /**
   * Sends every dirty row through the gate, one statement at a time, and
   * returns how many of them the human confirmed. A count short of the dirty
   * rows means the run stopped — cancelled, refused, or abandoned — and the
   * rows it never reached are still dirty.
   */
  async save(target: SaveTarget): Promise<number> {
    if (this.#saving) return 0;
    this.#saving = true;
    this.#gate.clearFailure();
    let executed = 0;
    try {
      for (const row of this.dirtyRows) {
        let statement: string;
        try {
          statement = this.#statement(row, target);
        } catch (err) {
          this.#gate.fail(err instanceof Error ? err.message : String(err));
          break;
        }

        // Null is every ending but "it ran": cancelled, abandoned, refused, or
        // failed. Which of those it was is already in `failure` when it was
        // worth saying, so the loop only has to know that it stopped.
        const outcome = await this.#gate.submit(
          target.profileName,
          target.runIn ?? "",
          statement,
        );
        if (outcome === null) break;
        this.#forget(row);
        executed += 1;
      }
    } finally {
      this.#saving = false;
    }
    return executed;
  }

  /** The Inline Confirm's answer, whichever way it went. */
  resolved(outcome: service.QueryResult) {
    this.#gate.resolved(outcome);
  }

  /**
   * Gives up on a confirm nobody is going to answer — the view is being torn
   * down, or the rows it was built from have been fetched again.
   */
  abandon() {
    this.#gate.abandon();
  }

  // One row's UPDATE: its dirty columns in the result's own order, matched on
  // the primary key's values as they were fetched.
  #statement(row: number, target: SaveTarget): string {
    const patch = this.#patches[row] ?? {};
    const fetched = target.rows[row];
    if (fetched === undefined) throw new Error("that row is no longer on screen");

    const changes = target.columns
      .filter((column) => Object.hasOwn(patch, column))
      .map((column) => ({ column, value: patch[column] }));
    const keys = target.keyColumns.map((column) => ({
      column,
      value: fetched[target.columns.indexOf(column)] ?? null,
    }));
    return buildUpdate(target.table, changes, keys, target.database ?? "");
  }

  #forget(row: number) {
    const next = { ...this.#patches };
    delete next[row];
    this.#patches = next;
  }
}
