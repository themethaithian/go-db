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

import { untrack } from "svelte";
import { CancelPending, RunQuery } from "../../wailsjs/go/app/App";
import type { service } from "../../wailsjs/go/models";
import { buildUpdate, type CellValue } from "./mutate";

/** One row's pending values, by column name. */
type RowPatch = Record<string, CellValue>;

/** Everything the save needs about the rows it is saving. */
export type SaveTarget = {
  profileName: string;
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

  // The statement the gate is holding right now, if the save has reached one,
  // and its text. The text is kept because nobody typed it: a generated
  // statement the human cannot read is one they cannot meaningfully confirm,
  // and the result the gate hands back does not carry the SQL.
  #pending = $state<service.QueryResult | null>(null);
  #pendingSql = $state<string | null>(null);
  #saving = $state(false);
  #failure = $state<string | null>(null);

  // How the save loop waits for the human. Resolving with null means the
  // confirm was abandoned rather than answered — the view went away, or the
  // rows underneath it did.
  #settle: ((outcome: service.QueryResult | null) => void) | null = null;

  /** The withheld statement, for the view to raise an Inline Confirm on. */
  get pending(): service.QueryResult | null {
    return this.#pending;
  }

  /** That statement's text, to be shown with it. */
  get pendingSql(): string | null {
    return this.#pendingSql;
  }

  /** Whether a save is in flight, including while it waits on a confirm. */
  get saving(): boolean {
    return this.#saving;
  }

  /** Why the last save stopped short, or null. */
  get failure(): string | null {
    return this.#failure;
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
    this.#failure = null;
  }

  /**
   * The dirty cells as grid coordinates — `row:column index` — so the results
   * table can tint and show them without being told about columns by name.
   */
  cells(columns: string[]): Map<string, CellValue> {
    const map = new Map<string, CellValue>();
    for (const [row, patch] of Object.entries(this.#patches)) {
      for (const [column, value] of Object.entries(patch)) {
        const index = columns.indexOf(column);
        if (index !== -1) map.set(`${row}:${index}`, value);
      }
    }
    return map;
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
    this.#failure = null;
    let executed = 0;
    try {
      for (const row of this.dirtyRows) {
        let statement: string;
        try {
          statement = this.#statement(row, target);
        } catch (err) {
          this.#failure = err instanceof Error ? err.message : String(err);
          break;
        }

        let outcome: service.QueryResult | null;
        try {
          outcome = await RunQuery(target.profileName, statement);
        } catch (err) {
          this.#failure = String(err);
          break;
        }

        // The expected route: the gate withheld it, and the human answers in
        // the Inline Confirm the view raises off `pending`.
        if (outcome.status === "requires_confirmation" && outcome.pending_id && outcome.preview) {
          this.#pendingSql = statement;
          this.#pending = outcome;
          outcome = await new Promise<service.QueryResult | null>((resolve) => {
            this.#settle = resolve;
          });
          this.#pending = null;
          this.#pendingSql = null;
          this.#settle = null;
        }

        if (outcome === null || outcome.status === "cancelled") break;
        if (outcome.status !== "executed") {
          this.#failure = outcome.message;
          break;
        }
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
    this.#settle?.(outcome);
  }

  /**
   * Gives up on a confirm nobody is going to answer — the view is being torn
   * down, or the rows it was built from have been fetched again. The gate is
   * told, since an Inline Confirm has no expiry and a pending left behind is a
   * statement the audit log never gets an outcome for.
   *
   * Callers reach for this on the way into a fetch, which is often inside an
   * effect, so it reads the pending untracked: an effect that took a
   * dependency on it here would re-run the moment a save raised one, and
   * cancel the very confirm it had just put on screen.
   */
  abandon() {
    const pending = untrack(() => this.#pending);
    if (pending?.pending_id) void CancelPending(pending.pending_id);
    this.#pending = null;
    this.#pendingSql = null;
    this.#settle?.(null);
    this.#settle = null;
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
    return buildUpdate(target.table, changes, keys);
  }

  #forget(row: number) {
    const next = { ...this.#patches };
    delete next[row];
    this.#patches = next;
  }
}
