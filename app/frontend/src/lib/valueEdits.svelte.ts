// The edit open on one browsed value or document, and what happens when it is
// saved.
//
// RowEdits' sibling (edits.svelte.ts), for the answers that are not rows. The
// two differ in the one way that matters and share everything else. A grid
// collects edits — many cells, many rows, saved as a sequence — because a row
// is a unit the human assembles before sending. A value has no such unit: a
// hash field, a list element, a sorted set's score, one document — each is one
// element addressed by one statement, and there is nothing to batch. So this
// holds one open editor rather than a patch set, and submits one statement
// rather than looping.
//
// What is deliberately identical is the trip through the Approval Gate, and it
// is identical because both classes hand it to the same GateRun
// (gate.svelte.ts) rather than each keeping their own copy of the sequencing.
// The statement is built, withheld, shown in an Inline Confirm and answered the
// same way whether a grid or a browse pane produced it — which is the whole
// claim inline editing makes.
//
// Cancel is a way back here too, and that is why the draft lives in this module
// rather than in the component that renders the box. An Inline Confirm takes
// the panel while it is on screen, which unmounts the pane and the box with it;
// a draft held in the component would be gone by the time the human said no.
// Held here, the box comes back holding exactly what they typed.
//
// One of these belongs to one view, like RowEdits: an id means nothing across
// two panes showing two different keys.

import type { service } from "../../wailsjs/go/models";
import { GateRun } from "./gate.svelte";

export class ValueEdits {
  #gate = new GateRun();

  // Which row or card is open for typing, by the id its view gave it, and the
  // text in the box. The id is opaque here on purpose — the pane knows whether
  // "field:name" and "doc:2" mean a hash field and a document card; this class
  // only has to know that one of them is open and which.
  #open = $state<string | null>(null);
  #draft = $state("");

  // The box's second field, for the one box that has two: adding a field to a
  // JSON object needs a name as well as a value, and both have to survive a
  // declined Inline Confirm for the same reason the draft does. Every other
  // box leaves it empty and never reads it.
  #name = $state("");

  // Why the statement could not be built from what is in the box — a value the
  // quoter cannot represent, a score that is not a number, JSON that does not
  // parse. It is kept apart from the gate's own failure while it is being set,
  // and merged with it on the way out, because a view wants one line to show
  // and does not care which side of the submit it came from.
  #refusal = $state<string | null>(null);

  /** The withheld statement, for the view to raise an Inline Confirm on. */
  get pending(): service.QueryResult | null {
    return this.#gate.pending;
  }

  /** That statement's text, to be shown with it. */
  get pendingSql(): string | null {
    return this.#gate.pendingSql;
  }

  /** Whether a statement is in flight, including while it waits on a confirm. */
  get saving(): boolean {
    return this.#gate.running;
  }

  /** Why the last edit stopped short, or null — refused here, or refused there. */
  get failure(): string | null {
    return this.#refusal ?? this.#gate.failure;
  }

  /** The id of the row or card open for typing, or null. */
  get open(): string | null {
    return this.#open;
  }

  /** The text in the open box. */
  get draft(): string {
    return this.#draft;
  }

  set draft(text: string) {
    this.#draft = text;
  }

  /** The name in the open box, for the box that asks for one. */
  get name(): string {
    return this.#name;
  }

  set name(text: string) {
    this.#name = text;
  }

  /**
   * Opens one row or card for typing, holding the text it shows right now.
   * Opening clears the last refusal: the message was about what was in the box
   * before, and leaving it up would attach it to what is in it now.
   *
   * `name` is the second field, and only the add-a-field box has one.
   */
  begin(id: string, text: string, name: string = "") {
    this.#open = id;
    this.#draft = text;
    this.#name = name;
    this.#refusal = null;
    this.#gate.clearFailure();
  }

  /**
   * Records a refusal the view worked out for itself, leaving the box open.
   *
   * There is one: JSON that does not parse. DocumentsView has to parse before
   * it can call anything — the callback it would call takes a document, not
   * text — so its refusal happens a step earlier than `submit`'s, and lands in
   * the same place so that one line renders both.
   */
  refuse(reason: string) {
    this.#refusal = reason;
  }

  /** Closes the box without sending anything. */
  cancel() {
    this.#open = null;
    this.#draft = "";
    this.#name = "";
    this.#refusal = null;
    this.#gate.clearFailure();
  }

  /**
   * Forgets everything — a fresh browse answer, which is a new set of rows the
   * open id no longer points into.
   */
  reset() {
    this.cancel();
  }

  /**
   * Builds a statement and sends it through the gate, and reports whether it
   * ran. The caller re-reads the pane on true and leaves the box alone on
   * false, which is what makes a cancelled confirm a way back rather than a
   * loss.
   *
   * `build` is a closure rather than a statement because a refusal is part of
   * building one: a value the quoter cannot write and a document that is not
   * JSON both throw, and neither should ever have reached RunQuery. Held here,
   * that refusal lands in the same place a gate refusal does and the box stays
   * exactly as it was.
   */
  async submit(profileName: string, build: () => string): Promise<boolean> {
    if (this.#gate.running) return false;
    this.#refusal = null;
    this.#gate.clearFailure();

    let statement: string;
    try {
      statement = build();
    } catch (err) {
      this.#refusal = err instanceof Error ? err.message : String(err);
      return false;
    }

    // The browse pane runs on the Profile's own connection, exactly as its
    // reads do: a write aimed anywhere else would be a write to a place the
    // human was not looking at.
    const outcome = await this.#gate.submit(profileName, "", statement);
    if (outcome === null) return false;

    this.#open = null;
    this.#draft = "";
    this.#name = "";
    return true;
  }

  /** The Inline Confirm's answer, whichever way it went. */
  resolved(outcome: service.QueryResult) {
    this.#gate.resolved(outcome);
  }

  /**
   * Gives up on a confirm nobody is going to answer — the view is being torn
   * down, or the value it was built from has been read again.
   */
  abandon() {
    this.#gate.abandon();
  }
}
