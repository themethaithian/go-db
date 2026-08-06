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

/**
 * Minimum width of a record pane (Explorer or Editor), and of the grid it
 * sits beside. One pair of constants for both: the pane is the same
 * component in both views, and there is nothing about either view that
 * wants a different floor.
 *
 * The pane's floor is what it costs to read one value, not what it costs to
 * draw one: a field name column, a value, and the field's own buttons beside
 * it. Below this the values start wrapping mid-word, which is the one thing
 * the pane must never do — so the floor is set where an email still fits on
 * a line (about twenty characters of the mono face), and the splitter simply
 * stops there rather than letting the pane shrink into uselessness.
 */
export const DETAIL_MIN_PX = 360;
export const GRID_MIN_PX = 240;

/**
 * Minimum width of the record pane while it is comparing rows: two value
 * columns squeezed into one row's worth of pane compare nothing legibly, so
 * the floor is the name column plus two columns at their own minimum. Wider
 * columns than that are the pane's own doing — they are sized to what is in
 * them — and what will not fit is scrolled to.
 */
export const COMPARE_MIN_PX = 400;

type Layout = {
  /** Explorer: width of the Database tool window. */
  explorerTreeWidth: number;
  /** Explorer: width of the Row pane beside the grid, when one row is open. */
  explorerDetailWidth: number;
  /** Editor: height of the Query pane; Results takes whatever is left. */
  editorQueryHeight: number;
  /** Editor: width of the Row pane beside the results grid, when one is open. */
  editorDetailWidth: number;
};

const DEFAULTS: Layout = {
  explorerTreeWidth: 244,
  explorerDetailWidth: 400,
  editorQueryHeight: 236,
  // The pane opens a step above its own floor, wide enough that the values in
  // it are read rather than decoded — an address or an email on one line,
  // with the field's buttons beside the text and not over it. Narrower is a
  // drag away; opening cramped by default is a first impression nobody drags
  // back from.
  editorDetailWidth: 400,
};

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
      // Widths remembered from before a floor moved are floors that were
      // agreed to under the old layout, not preferences for the new one: a
      // pane stored at 240px would come back too narrow to read a value in,
      // with nothing on screen saying why. Only the floor is applied here —
      // the ceiling depends on how wide the window is, which the views clamp
      // against once they have measured themselves.
      explorerDetailWidth: atLeast(
        size(parsed.explorerDetailWidth, DEFAULTS.explorerDetailWidth),
        DETAIL_MIN_PX,
      ),
      editorQueryHeight: size(parsed.editorQueryHeight, DEFAULTS.editorQueryHeight),
      editorDetailWidth: atLeast(
        size(parsed.editorDetailWidth, DEFAULTS.editorDetailWidth),
        DETAIL_MIN_PX,
      ),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

function size(value: unknown, fallback: number): number {
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

function atLeast(value: number, floor: number): number {
  return Math.max(value, floor);
}

export const layout = $state<Layout>(restore());

/** Records the current sizes so they survive a reload. */
export function persistLayout() {
  try {
    localStorage.setItem(
      STORAGE_KEY,
      JSON.stringify({
        explorerTreeWidth: layout.explorerTreeWidth,
        explorerDetailWidth: layout.explorerDetailWidth,
        editorQueryHeight: layout.editorQueryHeight,
        editorDetailWidth: layout.editorDetailWidth,
      }),
    );
  } catch {
    // A full or disabled storage costs the human a remembered layout on the
    // next launch, and nothing else — never a broken workspace.
  }
}
