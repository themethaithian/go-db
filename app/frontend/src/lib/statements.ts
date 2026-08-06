// Reading statements out of an editor buffer.
//
// The splitting itself is the gate's — guard.SplitStatements owns the MySQL
// grammar, and a semicolon scan here would be a second, worse answer to a
// question already answered properly. What is left for this module is the two
// things the parser cannot know: where those statements are in the editor's
// own coordinates, and which one the human is standing in.

import type { guard } from "../../wailsjs/go/models";

/**
 * One statement's extent in the editor's coordinates — UTF-16 code units, the
 * units CodeMirror counts positions in.
 */
export type StatementRange = {
  from: number;
  to: number;
  text: string;
};

/**
 * Places the gate's spans in the editor's buffer.
 *
 * A span's offsets are byte offsets into UTF-8 (see guard.StatementSpan) and
 * the editor counts UTF-16 code units, so the two agree only while the buffer
 * is ASCII — which it is, right up until someone puts an accented word or an
 * emoji in a string literal, and every highlight below it lands a few
 * characters off. The conversion is the whole reason this function exists.
 */
export function editorRanges(sql: string, spans: guard.StatementSpan[]): StatementRange[] {
  // The overwhelmingly common case, and worth its own path: one byte per code
  // unit means the offsets are already the editor's.
  if (!NON_ASCII.test(sql)) {
    return spans.map((span) => ({ from: span.start, to: span.end, text: span.text }));
  }

  const index = utf16Index(sql);
  const at = (offset: number) => index.get(offset) ?? sql.length;
  return spans.map((span) => ({ from: at(span.start), to: at(span.end), text: span.text }));
}

/**
 * Which statement the cursor is in — an index into `ranges`, or -1 when there
 * is no statement to be in.
 *
 * A cursor inside a statement picks that statement. A cursor in the whitespace
 * (or the trailing comment) between two of them picks the one it just left,
 * because that is the statement the human was working on a keystroke ago and
 * running the *next* one on their behalf would be a surprise with teeth. A
 * cursor before the first statement has nothing behind it and picks the first.
 *
 * Both boundaries count as inside, which matters at the end: parking after the
 * final semicolon is how most people leave the editor, and it has to mean the
 * statement that semicolon closed.
 */
export function statementAt(ranges: StatementRange[], cursor: number): number {
  let at = -1;
  for (let i = 0; i < ranges.length; i += 1) {
    if (ranges[i].from <= cursor) at = i;
  }
  // Only reachable when the cursor sits before the first statement — there is
  // no statement behind it, so the one ahead is the honest answer.
  if (at === -1 && ranges.length > 0) return 0;
  return at;
}

/**
 * The table a generated statement reads from, for naming the tab it lands in.
 *
 * This is a label, not an analysis: it looks at the first FROM in the text and
 * gives up quietly on anything it does not recognise, and the caller falls
 * back to a plain "Query N". Nothing is executed on the strength of it.
 */
export function subjectTable(sql: string): string | null {
  const quoted = /\bfrom\s+`((?:[^`]|``)+)`/i.exec(sql);
  if (quoted !== null) return quoted[1].replaceAll("``", "`");

  const bare = /\bfrom\s+([A-Za-z0-9_$]+)/i.exec(sql);
  return bare !== null ? bare[1] : null;
}

const NON_ASCII = /[^\x00-\x7f]/;

// Maps byte offset to code-unit offset for every code point boundary in sql.
// Spans only ever start and end on a boundary, so every lookup a caller makes
// is in here.
function utf16Index(sql: string): Map<number, number> {
  const index = new Map<number, number>();
  let byte = 0;
  for (let i = 0; i < sql.length; ) {
    index.set(byte, i);
    const code = sql.codePointAt(i) ?? 0;
    byte += utf8Width(code);
    i += code > 0xffff ? 2 : 1;
  }
  index.set(byte, sql.length);
  return index;
}

function utf8Width(code: number): number {
  if (code < 0x80) return 1;
  if (code < 0x800) return 2;
  if (code < 0x10000) return 3;
  return 4;
}
