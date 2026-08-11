// One statement through the Approval Gate, and the wait for the human's answer.
//
// This is the sequencing every generated statement in the app goes through, and
// it is exactly one thing: submit it, and if the gate withholds it, hold the
// withheld result where a view can raise an Inline Confirm on it and wait until
// that confirm is answered. Nothing here knows what the statement says or where
// it came from — an UPDATE built from a dirty row (edits.svelte.ts) and a HSET
// built from an edited hash field (valueEdits.svelte.ts) are the same shape of
// thing to it.
//
// It lives on its own because both of those callers need it and the parts that
// are easy to get wrong are the parts that would be copied: cancelling a
// withheld statement nobody will answer (an Inline Confirm has no expiry, so
// one left behind is a statement the audit log never gets an outcome for), and
// reading the pending untracked while doing it, since callers reach for
// `abandon` inside effects and an effect that took a dependency on the pending
// would cancel the very confirm it had just put on screen.
//
// One of these belongs to one run of one view. Two views with two withheld
// statements have two of them, and a `settle` means nothing across them.

import { untrack } from "svelte";
import { CancelPending, RunQuery } from "../../wailsjs/go/app/App";
import type { service } from "../../wailsjs/go/models";

export class GateRun {
  // The statement the gate is holding right now, if there is one, and its
  // text. The text is kept because nobody typed it: a generated statement the
  // human cannot read is one they cannot meaningfully confirm, and the result
  // the gate hands back does not carry the SQL.
  #pending = $state<service.QueryResult | null>(null);
  #pendingSql = $state<string | null>(null);
  #running = $state(false);
  #failure = $state<string | null>(null);

  // How the run waits for the human. Resolving with null means the confirm was
  // abandoned rather than answered — the view went away, or the thing it was
  // built from did.
  #settle: ((outcome: service.QueryResult | null) => void) | null = null;

  /** The withheld statement, for the view to raise an Inline Confirm on. */
  get pending(): service.QueryResult | null {
    return this.#pending;
  }

  /** That statement's text, to be shown with it. */
  get pendingSql(): string | null {
    return this.#pendingSql;
  }

  /** Whether a statement is in flight, including while it waits on a confirm. */
  get running(): boolean {
    return this.#running;
  }

  /** Why the last statement stopped short, or null. */
  get failure(): string | null {
    return this.#failure;
  }

  /** Records why something stopped before there was a statement to submit. */
  fail(reason: string) {
    this.#failure = reason;
  }

  /** Forgets the last failure — a fresh run, or an undo of what failed. */
  clearFailure() {
    this.#failure = null;
  }

  /**
   * Submits one statement, and answers with the result only when it ran.
   *
   * Null is every other ending, and `failure` says which of them was worth
   * reporting: the call itself failed or the gate refused (a reason), or the
   * human cancelled the Inline Confirm or nobody was left to answer it (no
   * reason — a cancel is a way back, not an error).
   */
  async submit(
    profileName: string,
    database: string,
    statement: string,
  ): Promise<service.QueryResult | null> {
    this.#running = true;
    try {
      let submitted: service.QueryResult;
      try {
        submitted = await RunQuery(profileName, database, statement);
      } catch (err) {
        this.#failure = String(err);
        return null;
      }

      // The expected route for a mutation: the gate withheld it, and the human
      // answers in the Inline Confirm the view raises off `pending`.
      let outcome: service.QueryResult | null = submitted;
      if (submitted.status === "requires_confirmation" && submitted.pending_id && submitted.preview) {
        this.#pendingSql = statement;
        this.#pending = submitted;
        outcome = await new Promise<service.QueryResult | null>((resolve) => {
          this.#settle = resolve;
        });
        this.#pending = null;
        this.#pendingSql = null;
        this.#settle = null;
      }

      if (outcome === null || outcome.status === "cancelled") return null;
      if (outcome.status !== "executed") {
        this.#failure = outcome.message;
        return null;
      }
      return outcome;
    } finally {
      this.#running = false;
    }
  }

  /** The Inline Confirm's answer, whichever way it went. */
  resolved(outcome: service.QueryResult) {
    this.#settle?.(outcome);
  }

  /**
   * Gives up on a confirm nobody is going to answer — the view is being torn
   * down, or what it was built from has been fetched again. The gate is told,
   * since an Inline Confirm has no expiry.
   *
   * Reads the pending untracked: callers reach for this on the way into a
   * fetch, which is often inside an effect, and an effect that took a
   * dependency on the pending here would re-run the moment a submit raised one
   * and cancel the very confirm it had just put on screen.
   */
  abandon() {
    const pending = untrack(() => this.#pending);
    if (pending?.pending_id) void CancelPending(pending.pending_id);
    this.#pending = null;
    this.#pendingSql = null;
    this.#settle?.(null);
    this.#settle = null;
  }
}
