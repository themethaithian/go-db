<script lang="ts">
  // The Raw | JSON control a string reply grows once it parses as JSON
  // (jsonReply.ts) — ExplorerView's own Data | Structure toggle, in
  // miniature: the same bordered pill with the active side filled in the
  // accent, scaled down to sit inline beside one value instead of a
  // toolbar. One instance per value that offers the choice; `mode` is
  // owned by the caller (its default depends on the value's own length —
  // see JSON_REPLY_THRESHOLD) and flows back here two-way so a click flips
  // it in place.
  let { mode = $bindable() }: { mode: "raw" | "json" } = $props();
</script>

<div
  class="inline-flex h-5 shrink-0 items-center gap-0.5 rounded-control border border-border bg-surface p-0.5 text-xs"
  role="group"
  aria-label="Raw or JSON"
>
  <button
    type="button"
    class="rounded-control px-1.5 font-medium transition-colors {mode === 'raw'
      ? 'bg-accent text-white'
      : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
    aria-pressed={mode === "raw"}
    onclick={() => (mode = "raw")}
  >
    Raw
  </button>
  <button
    type="button"
    class="rounded-control px-1.5 font-medium transition-colors {mode === 'json'
      ? 'bg-accent text-white'
      : 'text-text-muted hover:bg-surface-overlay hover:text-text'}"
    aria-pressed={mode === "json"}
    onclick={() => (mode = "json")}
  >
    JSON
  </button>
</div>
