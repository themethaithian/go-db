// Turning query results into text for the clipboard.
//
// File export stays out of scope until v2 ships multi-engine support — this
// is the copy-what-you-see affordance ResultsTable's footer offers instead:
// the same columns/rows the grid renders, in three shapes a human might
// paste them into. Each function is a straight columns+rows in, one string
// out — nothing reactive, nothing that reaches for the clipboard itself, so
// it can be read in a sitting the way statements.ts and mutate.ts are.

import type { CellValue } from "./mutate";

/**
 * Tab-separated: a header row, then one row per data row, cells joined by
 * tabs. TSV has no quoting mechanism at all, so a cell already holding a
 * tab, newline or carriage return is flattened to a single space rather than
 * left in — a mangled cell reads better than a paste where every row after
 * it has drifted one column over. NULL copies as an empty cell, the same as
 * the grid shows it blank rather than as the word "NULL".
 */
export function toTsv(columns: string[], rows: CellValue[][]): string {
  const cell = (value: CellValue) => (value === null ? "" : value.replace(/[\t\r\n]/g, " "));
  const lines = [columns.map(cell), ...rows.map((row) => row.map(cell))];
  return lines.map((line) => line.join("\t")).join("\n");
}

/**
 * RFC 4180 CSV: comma-separated, CRLF between rows, a cell quoted only when
 * it contains a comma, a quote or a newline (with any quote inside it
 * doubled). Quoting every cell would still be legal CSV, but it would make a
 * diff between two exports noisy for no reason a paste ever needs. NULL
 * copies as an empty, unquoted cell.
 */
export function toCsv(columns: string[], rows: CellValue[][]): string {
  const cell = (value: CellValue) => {
    if (value === null) return "";
    return /[",\r\n]/.test(value) ? `"${value.replaceAll('"', '""')}"` : value;
  };
  const lines = [columns, ...rows].map((line) => line.map(cell).join(","));
  return lines.join("\r\n");
}

/**
 * An array of objects keyed by column name, NULL kept as JSON null rather
 * than dropped or turned into the string "null", pretty-printed with the
 * same two-space indent CopyJson.svelte uses everywhere else a value is
 * copied — a cell copied on its own and a grid copied wholesale should read
 * the same way once they land in an editor.
 *
 * Two result columns can share a name (a join, an unaliased duplicate) and a
 * plain object key would silently let the second overwrite the first, so a
 * repeat is suffixed instead — `name`, `name_2`, `name_3` — trading a slightly
 * odd key for not losing a column's data on the way to the clipboard.
 */
export function toJson(columns: string[], rows: CellValue[][]): string {
  const keys = dedupeColumnNames(columns);
  const objects = rows.map((row) => {
    const record: Record<string, CellValue> = {};
    keys.forEach((key, i) => {
      record[key] = row[i] ?? null;
    });
    return record;
  });
  return JSON.stringify(objects, null, 2);
}

function dedupeColumnNames(columns: string[]): string[] {
  const seen = new Map<string, number>();
  return columns.map((column) => {
    const count = (seen.get(column) ?? 0) + 1;
    seen.set(column, count);
    return count === 1 ? column : `${column}_${count}`;
  });
}
