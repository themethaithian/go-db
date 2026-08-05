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
  import { sql, MySQL } from "@codemirror/lang-sql";

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
        backgroundColor: "var(--color-surface)",
        color: "var(--color-text)",
        fontSize: "var(--text-base)",
      },
      ".cm-content": {
        fontFamily: "var(--font-mono)",
        caretColor: "var(--color-text)",
      },
      ".cm-scroller": {
        fontFamily: "var(--font-mono)",
      },
      ".cm-gutters": {
        backgroundColor: "var(--color-surface-raised)",
        color: "var(--color-text-muted)",
        border: "none",
      },
      ".cm-activeLine": {
        backgroundColor: "var(--color-surface-overlay)",
      },
      ".cm-activeLineGutter": {
        backgroundColor: "var(--color-surface-overlay)",
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
