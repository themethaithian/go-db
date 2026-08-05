<script lang="ts">
  // App shell: design tokens + status bar skeleton (issue #4). Connection
  // state and live RAM reporting land in later issues.
</script>

<div class="flex h-screen w-screen flex-col bg-surface font-sans text-base text-text">
  <main class="flex flex-1 items-center justify-center">
    <span class="text-lg text-text-muted">go-db</span>
  </main>

  <footer
    class="flex h-8 shrink-0 items-center justify-between border-t border-border bg-surface-raised px-3 text-xs text-text-muted"
  >
    <span>go-db · not connected</span>
    <span>— MB</span>
  </footer>
</div>
