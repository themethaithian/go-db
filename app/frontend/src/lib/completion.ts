// Schema-aware SQL completion, built on top of @codemirror/lang-sql's own
// keyword list plus a source of our own for tables and columns.
//
// The built-in schemaCompletionSource bakes its table/column tree in once,
// at the moment the extension is built — which is wrong for us, since the
// schema store fetches columns lazily and keeps loading well after the
// editor opens. So completionSource below is handed a *getter* instead of a
// snapshot, and reads it fresh on every keystroke that triggers completion:
// nothing here is ever stale, and nothing has to be reconfigured when a
// fetch resolves.

import { keywordCompletionSource, MySQL } from "@codemirror/lang-sql";
import type {
  Completion,
  CompletionContext,
  CompletionResult,
  CompletionSource,
} from "@codemirror/autocomplete";

/**
 * What the editor knows about the connected Profile's schema, as of right
 * now. Both members are cheap, synchronous reads — a caller whose columns
 * are not cached yet is expected to kick off a fetch as a side effect of
 * building this (or of answering columnsFor) and simply return what it has,
 * rather than block the keystroke that asked.
 */
export type CompletionSchema = {
  /** Every table name for the active Profile, or empty before they load. */
  tables: string[];
  /** table's column names, or undefined before they have been fetched. */
  columnsFor(table: string): string[] | undefined;
};

/**
 * Which of `tables` appear as a bare word anywhere in `sql` — the "mentioned
 * in this document" set that unqualified column completion (typing inside a
 * WHERE, say) draws from. A text scan rather than a FROM/JOIN parse: the
 * gate already owns real SQL parsing, and a false positive here only ever
 * costs an extra column suggestion, never a wrong query.
 */
export function mentionedTables(sql: string, tables: readonly string[]): string[] {
  if (tables.length === 0 || sql === "") return [];
  return tables.filter((table) => wordBoundary(table).test(sql));
}

function wordBoundary(word: string): RegExp {
  return new RegExp(`\\b${word.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\b`, "i");
}

function columnCompletions(names: string[] | undefined): Completion[] {
  return (names ?? []).map((name) => ({ label: name, type: "property" }));
}

// Matches `identifier.partial` ending at the cursor — CodeMirror's matchBefore
// anchors this to the end of the string it searches, so it only fires when
// the dot and (optional) partial column name are right behind the caret.
const QUALIFIED = /([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z0-9_]*)$/;
const WORD = /[A-Za-z_][A-Za-z0-9_]*$/;

/**
 * Builds the schema half of completion: `table.column` once a known table
 * precedes the dot (regardless of where in the statement that dot sits —
 * matching how most SQL editors behave), table names elsewhere, and,
 * unqualified, the columns of whichever tables the document already
 * mentions — so a WHERE clause offers `city` without the human having to
 * spell out `orders.city`.
 *
 * getSchema is called on every request rather than once at construction:
 * that is what lets a column fetched a moment after the popup first opened
 * show up the next time the human asks, with no extension to reconfigure.
 */
export function schemaCompletion(getSchema: () => CompletionSchema | null): CompletionSource {
  return (context: CompletionContext): CompletionResult | null => {
    const schema = getSchema();
    if (schema === null) return null;

    const qualified = context.matchBefore(QUALIFIED);
    if (qualified !== null) {
      const match = QUALIFIED.exec(qualified.text);
      const table = match?.[1] ?? "";
      const isKnown = schema.tables.some((name) => name.toLowerCase() === table.toLowerCase());
      if (!isKnown) return null;
      const options = columnCompletions(schema.columnsFor(table));
      if (options.length === 0) return null;
      return { from: qualified.from + table.length + 1, options, validFor: /^\w*$/ };
    }

    const word = context.matchBefore(WORD);
    if (word === null || (word.from === word.to && !context.explicit)) return null;

    const seen = new Set<string>();
    const options: Completion[] = [];
    for (const name of schema.tables) {
      seen.add(name.toLowerCase());
      options.push({ label: name, type: "type" });
    }
    for (const table of mentionedTables(context.state.doc.toString(), schema.tables)) {
      for (const name of schema.columnsFor(table) ?? []) {
        if (seen.has(name.toLowerCase())) continue;
        seen.add(name.toLowerCase());
        options.push({ label: name, type: "property" });
      }
    }
    if (options.length === 0) return null;
    return { from: word.from, options, validFor: /^\w*$/ };
  };
}

/**
 * Keyword completion for the MySQL dialect — SELECT, FROM, WHERE, ORDER BY,
 * LIMIT, INSERT, JOIN and the rest of @codemirror/lang-sql's built-in MySQL
 * word list. Casing follows the human's lead rather than a house style: a
 * prefix typed in all caps completes to SELECT, anything else to select.
 */
const lowerKeywords = keywordCompletionSource(MySQL, false);

// The lowercase source hands back the same static options array on every
// call, so the uppercase twin is derived once and reused.
let upperOptions: Completion[] | null = null;

export const keywordCompletion: CompletionSource = (context) => {
  const result = lowerKeywords(context);
  // The keyword source is synchronous in practice; the Promise arm exists
  // only to satisfy CompletionSource's signature.
  if (result === null || result instanceof Promise) return result;
  const typed = context.state.sliceDoc(result.from, context.pos);
  if (typed === "" || typed !== typed.toUpperCase()) return result;
  upperOptions ??= result.options.map((option) => ({
    ...option,
    label: option.label.toUpperCase(),
  }));
  return { ...result, options: upperOptions };
};
