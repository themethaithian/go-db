<script lang="ts">
  // Thin CodeMirror 6 wrapper: creates the editor on mount, destroys it on
  // unmount, and is otherwise stateless — the SQL text lives in EditorView
  // and flows out through onChange. Mod-Enter runs the query in place so the
  // human never has to reach for the mouse mid-edit.
  //
  // It reports where the cursor is and paints one range for whoever is
  // listening, and knows nothing about why: statements, the Approval Gate and
  // what "active" means are EditorView's business.
  import { onMount, onDestroy } from "svelte";
  import { EditorState, StateEffect, StateField } from "@codemirror/state";
  import {
    Decoration,
    EditorView,
    keymap,
    lineNumbers,
    highlightActiveLine,
    highlightActiveLineGutter,
    drawSelection,
    type DecorationSet,
  } from "@codemirror/view";
  import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
  import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
  import { sql, MySQL } from "@codemirror/lang-sql";
  import { tags } from "@lezer/highlight";

  let {
    value,
    highlight = null,
    onChange,
    onCursorChange,
    onRun,
  }: {
    value: string;
    /** A range to tint, in code-unit offsets — the statement Run would send. */
    highlight?: { from: number; to: number } | null;
    onChange: (value: string) => void;
    /** Where the caret is, and whatever is selected under it ("" when nothing is). */
    onCursorChange: (cursor: number, selection: string) => void;
    onRun: () => void;
  } = $props();

  let container: HTMLDivElement;
  // $state so the sync effect below re-runs once the view exists — an effect
  // that read a plain `let` would run before onMount and never again.
  // CodeMirror's EditorView is a class instance, which $state leaves
  // unproxied, so nothing about its internals is touched by this.
  let view = $state<EditorView | undefined>(undefined);

  // Theme built from the design tokens (app.css @theme) — no hard-coded
  // colors, so the editor stays in step with the rest of the app.
  const theme = EditorView.theme(
    {
      "&": {
        height: "100%",
        backgroundColor: "var(--color-surface-panel)",
        color: "var(--color-text)",
        fontSize: "var(--text-md)",
      },
      ".cm-content": {
        fontFamily: "var(--font-mono)",
        caretColor: "var(--color-text)",
        padding: "0.5rem 0",
      },
      ".cm-line": {
        padding: "0 0.75rem",
      },
      ".cm-scroller": {
        fontFamily: "var(--font-mono)",
        lineHeight: "1.6",
      },
      ".cm-gutters": {
        backgroundColor: "var(--color-surface-panel)",
        color: "var(--color-text-subtle)",
        borderRight: "1px solid var(--color-border)",
        paddingRight: "0.125rem",
      },
      ".cm-lineNumbers .cm-gutterElement": {
        padding: "0 0.5rem 0 0.75rem",
        minWidth: "2rem",
      },
      ".cm-activeLine": {
        backgroundColor: "var(--color-surface-raised)",
      },
      // The statement Run would send. A tint of the accent rather than another
      // grey: it has to be legible on top of the active line (which is already
      // a raised grey) without competing with the selection (the same accent
      // at 25%), so it sits well below both and never carries text or a
      // border of its own.
      ".cm-activeStatement": {
        backgroundColor: "color-mix(in oklab, var(--color-accent) 14%, transparent)",
      },
      ".cm-activeLineGutter": {
        backgroundColor: "var(--color-surface-raised)",
        color: "var(--color-text-muted)",
      },
      "&.cm-focused": {
        outline: "none",
      },
      ".cm-selectionBackground, &.cm-focused .cm-selectionBackground": {
        backgroundColor: "var(--color-accent)",
        opacity: "0.25",
      },
      ".cm-cursor": {
        borderLeftColor: "var(--color-text)",
      },
    },
    { dark: true },
  );

  // Syntax colors, also from the tokens: keywords, strings, numbers and
  // comments are the only distinctions SQL actually needs to scan quickly.
  const syntax = HighlightStyle.define([
    { tag: tags.keyword, color: "var(--color-syntax-keyword)", fontWeight: "500" },
    { tag: [tags.string, tags.special(tags.string)], color: "var(--color-syntax-string)" },
    { tag: [tags.number, tags.bool, tags.null], color: "var(--color-syntax-number)" },
    { tag: [tags.typeName, tags.atom], color: "var(--color-syntax-type)" },
    { tag: tags.comment, color: "var(--color-text-subtle)", fontStyle: "italic" },
    { tag: [tags.operator, tags.punctuation, tags.separator], color: "var(--color-text-muted)" },
    { tag: [tags.variableName, tags.propertyName], color: "var(--color-text)" },
  ]);

  // The highlighted range as CodeMirror state. A field rather than a prop read
  // at render time because the editor's document is its own source of truth:
  // between the caller handing over a range and the next one arriving, the
  // human may have typed, and mapping the range through their edit keeps the
  // tint on the statement instead of on a fixed pair of offsets.
  const setHighlight = StateEffect.define<{ from: number; to: number } | null>();
  const statementMark = Decoration.mark({ class: "cm-activeStatement" });
  const highlightField = StateField.define<DecorationSet>({
    create: () => Decoration.none,
    update(decorations, transaction) {
      for (const effect of transaction.effects) {
        if (!effect.is(setHighlight)) continue;
        const range = effect.value;
        return range === null
          ? Decoration.none
          : Decoration.set([statementMark.range(range.from, range.to)]);
      }
      return decorations.map(transaction.changes);
    },
    provide: (field) => EditorView.decorations.from(field),
  });

  onMount(() => {
    view = new EditorView({
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          highlightActiveLine(),
          highlightActiveLineGutter(),
          drawSelection(),
          history(),
          sql({ dialect: MySQL }),
          syntaxHighlighting(syntax),
          highlightField,
          theme,
          keymap.of([
            { key: "Mod-Enter", run: () => (onRun(), true) },
            ...historyKeymap,
            ...defaultKeymap,
          ]),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              onChange(update.state.doc.toString());
            }
            // Typing moves the caret as surely as an arrow key does, so both
            // are reported: what Run would send depends on where the caret
            // ended up, not on how it got there.
            if (update.docChanged || update.selectionSet) {
              const { head, from, to } = update.state.selection.main;
              onCursorChange(head, update.state.sliceDoc(from, to));
            }
          }),
        ],
      }),
      parent: container,
    });
  });

  // Pushes a value set from outside — the Database tree generating a SELECT
  // for a clicked table — into the document. Typing is unaffected: the text
  // the human just produced is already the value, so the guard finds them
  // equal and dispatches nothing, and the cursor stays where they left it.
  $effect(() => {
    const next = value;
    if (view === undefined || view.state.doc.toString() === next) return;
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: next } });
  });

  // Paints the range the caller asked for. Offsets are clamped to the document
  // rather than trusted: the caller computes them from text it was handed a
  // moment ago, and CodeMirror throws on a range past the end.
  $effect(() => {
    const range = highlight;
    if (view === undefined) return;
    const length = view.state.doc.length;
    const from = Math.min(Math.max(range?.from ?? 0, 0), length);
    const to = Math.min(Math.max(range?.to ?? 0, 0), length);
    view.dispatch({ effects: setHighlight.of(range !== null && from < to ? { from, to } : null) });
  });

  onDestroy(() => {
    view?.destroy();
  });
</script>

<div bind:this={container} class="h-full w-full overflow-hidden"></div>
