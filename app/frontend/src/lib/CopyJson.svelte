<script lang="ts">
  // The copy-this-JSON button, as JsonTree grew it and DocumentsView's card
  // header now needs one too.
  //
  // It is its own component rather than a snippet in either of them because
  // the part worth not duplicating is not the icon: it is the contract around
  // the tick — the feedback shows on the button that was pressed, for a fixed
  // moment, and never for a clipboard that refused. Two copies of that would
  // be two chances to get it subtly different.
  //
  // The click never reaches an ancestor. A copy button lives inside a row that
  // may itself do something on click, and a stray copy that also opened an
  // editor is exactly the bug that appears only once this is reused somewhere
  // new.
  // Whether it is visible at rest is the caller's to say, by where it puts
  // this — JsonTree drops it into a row's own reveal-on-hover group, and a
  // card header keeps it visible. Nothing about that belongs in here.
  let {
    value,
    title = "Copy this JSON",
  }: {
    /** The value to write to the clipboard, pretty-printed. */
    value: unknown;
    title?: string;
  } = $props();

  const COPIED_MS = 1200;

  let copied = $state(false);
  let timer: ReturnType<typeof setTimeout> | null = null;
  $effect(() => () => {
    if (timer !== null) clearTimeout(timer);
  });

  async function copy(event: MouseEvent) {
    event.stopPropagation();
    try {
      await navigator.clipboard.writeText(JSON.stringify(value, null, 2));
    } catch {
      // Claiming "copied" for something that never reached the clipboard is
      // the one outcome worth avoiding.
      return;
    }
    copied = true;
    if (timer !== null) clearTimeout(timer);
    timer = setTimeout(() => (copied = false), COPIED_MS);
  }
</script>

<button
  type="button"
  class="flex h-5 w-5 shrink-0 items-center justify-center rounded-control border transition-colors {copied
    ? 'border-success/40 bg-surface-raised text-success opacity-100'
    : 'border-border bg-surface-raised text-text-subtle hover:border-border-strong hover:text-text'}"
  onclick={copy}
  {title}
  aria-label={title}
>
  {#if copied}
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.5"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <path d="M2.5 6.25 5 8.75l4.5-5.5" />
    </svg>
  {:else}
    <svg
      class="h-3 w-3"
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      stroke-width="1.2"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
    >
      <rect x="4.25" y="4.25" width="5.5" height="5.5" rx="1.25" />
      <path d="M7.75 2.25h-5.5v5.5" />
    </svg>
  {/if}
</button>
