// Panel sizes for the resizable views — the Explorer's tree column and the
// Editor's query pane.
//
// These live at module scope, not inside the views, for one reason: a view is
// unmounted whenever the human switches to another one, and a layout that
// resets itself every time is not a layout. The same state is mirrored into
// localStorage so it also survives a reload.
//
// One key per view seam, never one shared "panel size": the Explorer's tree
// and the Editor's query pane are dragged for different reasons, and the sizes
// that suit one say nothing about the other.
//
// Sizes are stored in CSS pixels. Minimums are the module's own business —
// callers clamp against them (the query pane's maximum depends on how tall
// the window is, which only the component measuring it knows).

/** Minimum and maximum width of the Explorer's Database tool window. */
export const TREE_MIN_PX = 180;
export const TREE_MAX_PX = 420;

/** Minimum height of the Editor's Query pane and of the Results pane below it. */
export const QUERY_MIN_PX = 120;
export const RESULTS_MIN_PX = 160;

type Layout = {
  /** Explorer: width of the Database tool window. */
  explorerTreeWidth: number;
  /** Editor: height of the Query pane; Results takes whatever is left. */
  editorQueryHeight: number;
};

const DEFAULTS: Layout = { explorerTreeWidth: 244, editorQueryHeight: 236 };

const STORAGE_KEY = "go-db:workspace-layout";

export function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), Math.max(min, max));
}

// A stored layout is untrusted input like any other: anything that is not a
// finite number falls back to the default rather than propagating NaN into a
// style attribute, where it would silently collapse a panel to nothing.
function restore(): Layout {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw === null) return { ...DEFAULTS };
    const parsed = JSON.parse(raw) as Partial<Layout>;
    return {
      explorerTreeWidth: size(parsed.explorerTreeWidth, DEFAULTS.explorerTreeWidth),
      editorQueryHeight: size(parsed.editorQueryHeight, DEFAULTS.editorQueryHeight),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

function size(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

export const layout = $state<Layout>(restore());

/** Records the current sizes so they survive a reload. */
export function persistLayout() {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        explorerTreeWidth: layout.explorerTreeWidth,
        editorQueryHeight: layout.editorQueryHeight,
      }),
    );
  } catch {
    // A full or disabled storage costs the human a remembered layout on the
    // next launch, and nothing else — never a broken workspace.
  }
}
