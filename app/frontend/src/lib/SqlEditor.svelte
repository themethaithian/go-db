<script lang="ts">
  // Thin CodeMirror 6 wrapper: creates the editor on mount, destroys it on
  // unmount, and is otherwise stateless — the SQL text lives in EditorView
  // and flows out through onChange. Mod-Enter runs the query in place so the
  // human never has to reach for the mouse mid-edit.
  import { onMount, onDestroy } from "svelte";
  import { EditorState } from "@codemirror/state";
  import {
    EditorView,
    keymap,
    lineNumbers,
    highlightActiveLine,
    highlightActiveLineGutter,
    drawSelection,
  } from "@codemirror/view";
  import { defaultKeymap, history, historyKeymap } from "@codemirror/commands";
  import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
  import { sql, MySQL } from "@codemirror/lang-sql";
  import { tags } from "@lezer/highlight";

  let {
    value,
    onChange,
    onRun,
  }: {
    value: string;
    onChange: (value: string) => void;
    onRun: () => void;
  } = $props();

  let container: HTMLDivElement;
  let view: EditorView | undefined;

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
  const highlight = HighlightStyle.define([
    { tag: tags.keyword, color: "var(--color-syntax-keyword)", fontWeight: "500" },
    { tag: [tags.string, tags.special(tags.string)], color: "var(--color-syntax-string)" },
    { tag: [tags.number, tags.bool, tags.null], color: "var(--color-syntax-number)" },
    { tag: [tags.typeName, tags.atom], color: "var(--color-syntax-type)" },
    { tag: tags.comment, color: "var(--color-text-subtle)", fontStyle: "italic" },
    { tag: [tags.operator, tags.punctuation, tags.separator], color: "var(--color-text-muted)" },
    { tag: [tags.variableName, tags.propertyName], color: "var(--color-text)" },
  ]);

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
          syntaxHighlighting(highlight),
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
          }),
        ],
      }),
      parent: container,
    });
  });

  onDestroy(() => {
    view?.destroy();
  });
</script>

<div bind:this={container} class="h-full w-full overflow-hidden"></div>
