<script lang="ts">
  // A draggable divider between two workspace panes. Hand-rolled on pointer
  // events — pointer capture means the drag keeps tracking even when the
  // cursor outruns the 6px hit area, which is the whole difference between a
  // splitter that feels solid and one that keeps slipping out of your hand.
  //
  // It owns no size of its own: it reports where the divider should now be
  // (clamped to min/max) and the parent decides what that means. Arrow keys
  // move it too, so the layout is reachable without a mouse.
  import { clamp } from "./layout.svelte";

  let {
    orientation,
    value,
    min,
    max,
    label,
    onChange,
  }: {
    /** "vertical" is a vertical line resized left/right; "horizontal" is a horizontal line resized up/down. */
    orientation: "vertical" | "horizontal";
    /** Current size of the pane before the divider, in pixels. */
    value: number;
    min: number;
    max: number;
    label: string;
    onChange: (next: number) => void;
  } = $props();

  const KEY_STEP_PX = 16;

  let dragging = $state(false);
  let originPointer = 0;
  let originValue = 0;

  let isVertical = $derived(orientation === "vertical");

  function coordinate(event: PointerEvent): number {
    return isVertical ? event.clientX : event.clientY;
  }

  function handlePointerDown(event: PointerEvent) {
    if (event.button !== 0) return;
    event.preventDefault();
    (event.currentTarget as HTMLElement).setPointerCapture(event.pointerId);
    dragging = true;
    originPointer = coordinate(event);
    originValue = value;
    // While the pointer is captured the cursor is ours everywhere on screen,
    // and a drag that selects half the UI as a side effect is nobody's idea
    // of a resize.
    document.body.style.cursor = isVertical ? "col-resize" : "row-resize";
    document.body.style.userSelect = "none";
  }

  function handlePointerMove(event: PointerEvent) {
    if (!dragging) return;
    onChange(clamp(originValue + coordinate(event) - originPointer, min, max));
  }

  function endDrag(event: PointerEvent) {
    if (!dragging) return;
    dragging = false;
    (event.currentTarget as HTMLElement).releasePointerCapture(event.pointerId);
    document.body.style.cursor = "";
    document.body.style.userSelect = "";
  }

  function handleKeyDown(event: KeyboardEvent) {
    const back = isVertical ? "ArrowLeft" : "ArrowUp";
    const forward = isVertical ? "ArrowRight" : "ArrowDown";
    if (event.key !== back && event.key !== forward) return;
    event.preventDefault();
    onChange(clamp(value + (event.key === forward ? KEY_STEP_PX : -KEY_STEP_PX), min, max));
  }
</script>

<!--
  A focusable separator is the ARIA window-splitter pattern: role="separator"
  with aria-value* and arrow-key handling is exactly what a resizable divider
  is meant to be. Svelte's a11y rules class every separator as non-interactive
  because most of them are decorative rules; this one is not.
-->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
  role="separator"
  tabindex="0"
  aria-orientation={orientation}
  aria-label={label}
  aria-valuenow={Math.round(value)}
  aria-valuemin={min}
  aria-valuemax={Math.round(max)}
  class="group flex shrink-0 touch-none items-stretch justify-center {isVertical
    ? 'w-1.5 cursor-col-resize'
    : 'my-1 h-1.5 cursor-row-resize'}"
  onpointerdown={handlePointerDown}
  onpointermove={handlePointerMove}
  onpointerup={endDrag}
  onpointercancel={endDrag}
  onkeydown={handleKeyDown}
>
  <span
    class="{isVertical ? 'w-px' : 'h-px w-full self-center'} transition-colors {dragging
      ? 'bg-accent'
      : 'bg-border group-hover:bg-border-strong'}"
  ></span>
</div>
