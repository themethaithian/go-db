<script lang="ts">
  import type { db } from "../../wailsjs/go/models";
  import { isGroupCollapsed, toggleGroupCollapsed } from "./profileGroups.svelte";
  import { engineLabel } from "./schema.svelte";

  // Sidebar: lists saved Profiles and lets the user pick one to edit or
  // start a new one. Holds no domain state of its own — ConnectionsView owns
  // the list, the current selection, and which Profiles are connected — only
  // the drag-in-progress and which group headers are collapsed are this
  // component's (the latter delegated to profileGroups.svelte.ts so it
  // survives this component unmounting).
  //
  // Profiles render grouped: ungrouped first, in saved order, then each
  // named Group in the order it first appears in the saved order — never
  // alphabetized, because the whole point of a saved order is that it is the
  // human's own. Dragging a row reorders within a group or moves it to
  // another (including back to ungrouped); ConnectionsView is handed the
  // resulting name order and, when the Group changed, the dragged Profile's
  // new Group, and does the actual saving.
  let {
    profiles,
    selectedName,
    connectedProfiles,
    onSelect,
    onCreate,
    onReorder,
  }: {
    profiles: db.Profile[];
    selectedName: string | null;
    connectedProfiles: string[];
    onSelect: (name: string) => void;
    onCreate: () => void;
    onReorder: (change: { name: string; group: string; order: string[] }) => void;
  } = $props();

  function isConnected(profile: db.Profile): boolean {
    return connectedProfiles.includes(profile.Name);
  }

  function subtitle(profile: db.Profile): string {
    const location = `${profile.Host}:${profile.Port}`;
    return profile.Database ? `${location} / ${profile.Database}` : location;
  }

  /** A Profile's Group, normalized: blank (including all-whitespace) means
   * ungrouped, the same "" every section and drop target uses. */
  function groupOf(profile: db.Profile): string {
    return profile.Group?.trim() ? profile.Group : "";
  }

  type Section = { group: string; profiles: db.Profile[] };

  // Ungrouped Profiles first (always present, even empty, so there is always
  // somewhere to drag a Profile back out of a group to), then each Group in
  // the order it was first seen walking `profiles` — which is saved order,
  // so this is nothing this component decided on its own.
  let sections = $derived.by((): Section[] => {
    const ungrouped: db.Profile[] = [];
    const order: string[] = [];
    const byGroup = new Map<string, db.Profile[]>();
    for (const profile of profiles) {
      const group = groupOf(profile);
      if (group === "") {
        ungrouped.push(profile);
        continue;
      }
      if (!byGroup.has(group)) {
        byGroup.set(group, []);
        order.push(group);
      }
      byGroup.get(group)!.push(profile);
    }
    const sections: Section[] = [{ group: "", profiles: ungrouped }];
    for (const group of order) sections.push({ group, profiles: byGroup.get(group)! });
    return sections;
  });

  // --- Drag to reorder ---------------------------------------------------
  //
  // Plain HTML5 drag-and-drop, no new dependency. A row names itself as the
  // drag payload; every row and every section's trailing drop zone can be a
  // target. dropTarget names where a drop would land — a specific row (drop
  // before or after it) or a section's end (anchor null) — purely to drive
  // the indicator line; the actual reordering is computed fresh in
  // handleDrop from `profiles`, never from stale indices.
  let draggingName = $state<string | null>(null);
  let dropTarget = $state<{ group: string; anchor: string | null; position: "before" | "after" } | null>(
    null,
  );

  function handleDragStart(event: DragEvent, name: string) {
    draggingName = name;
    event.dataTransfer?.setData("text/plain", name);
    if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
  }

  function handleDragEnd() {
    draggingName = null;
    dropTarget = null;
  }

  // Rows stop the dragover/drop from bubbling to their section's own
  // handler below — without that, every dragover over a row would fire twice
  // (row, then the section div it sits in) and the section's "append at the
  // end" would win every time, since it runs second and simply overwrites
  // dropTarget. The section handler is only ever meant to catch a drag over
  // the gaps a row does not cover: the header, empty space, past the last row.
  function handleRowDragOver(event: DragEvent, group: string, profile: db.Profile) {
    if (draggingName === null || draggingName === profile.Name) return;
    event.preventDefault();
    event.stopPropagation();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    const row = event.currentTarget as HTMLElement;
    const midpoint = row.getBoundingClientRect().top + row.getBoundingClientRect().height / 2;
    const position = event.clientY < midpoint ? "before" : "after";
    dropTarget = { group, anchor: profile.Name, position };
  }

  function handleRowDrop(event: DragEvent) {
    event.stopPropagation();
    handleDrop(event);
  }

  function handleSectionDragOver(event: DragEvent, group: string) {
    if (draggingName === null) return;
    event.preventDefault();
    if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
    dropTarget = { group, anchor: null, position: "after" };
  }

  function handleDrop(event: DragEvent) {
    event.preventDefault();
    const name = draggingName;
    const target = dropTarget;
    handleDragEnd();
    if (name === null || target === null) return;

    const dragged = profiles.find((p) => p.Name === name);
    if (dragged === undefined) return;

    const rest = profiles.filter((p) => p.Name !== name);
    let insertAt: number;
    if (target.anchor === null) {
      let lastInGroup = -1;
      for (let i = 0; i < rest.length; i++) {
        if (groupOf(rest[i]) === target.group) lastInGroup = i;
      }
      insertAt = lastInGroup === -1 ? rest.length : lastInGroup + 1;
    } else {
      const anchorAt = rest.findIndex((p) => p.Name === target.anchor);
      insertAt = anchorAt === -1 ? rest.length : anchorAt + (target.position === "after" ? 1 : 0);
    }

    const reordered = [...rest.slice(0, insertAt), dragged, ...rest.slice(insertAt)];
    onReorder({ name, group: target.group, order: reordered.map((p) => p.Name) });
  }

  function indicatorBefore(group: string, name: string): boolean {
    return dropTarget?.group === group && dropTarget.anchor === name && dropTarget.position === "before";
  }

  function indicatorAfter(group: string, name: string): boolean {
    return dropTarget?.group === group && dropTarget.anchor === name && dropTarget.position === "after";
  }

  function indicatorAtEnd(group: string): boolean {
    return dropTarget?.group === group && dropTarget.anchor === null;
  }
</script>

<!-- The drop indicator: a thin line in the app's own accent, the same
     border-token language the selected row's left border already uses. -->
{#snippet dropLine()}
  <div class="mx-3.5 my-0.5 h-0.5 rounded-full bg-accent"></div>
{/snippet}

{#snippet profileRow(profile: db.Profile, group: string)}
  <div
    role="listitem"
    draggable="true"
    ondragstart={(e) => handleDragStart(e, profile.Name)}
    ondragover={(e) => handleRowDragOver(e, group, profile)}
    ondrop={handleRowDrop}
    ondragend={handleDragEnd}
    class="transition-opacity {draggingName === profile.Name ? 'opacity-40' : ''}"
  >
    {#if indicatorBefore(group, profile.Name)}{@render dropLine()}{/if}
    <button
      type="button"
      class="flex w-full flex-col items-start gap-1 border-l-2 py-2.5 pr-3 pl-3.5 text-left transition-colors {profile.Name ===
      selectedName
        ? 'border-accent bg-surface-overlay'
        : 'border-transparent hover:bg-surface-raised'}"
      onclick={() => onSelect(profile.Name)}
    >
      <span class="flex w-full items-center gap-2">
        <span
          class="h-1.5 w-1.5 shrink-0 rounded-full {isConnected(profile)
            ? 'bg-success'
            : 'bg-text-subtle'}"
          aria-label={isConnected(profile) ? "Connected" : "Not connected"}
        ></span>
        <span class="truncate text-base font-medium text-text">{profile.Name}</span>
        <!-- The Engine badge: MySQL included, since the point is telling
             three otherwise-identical rows apart at a glance. Same pill
             classes the ssh chip beside it already uses, and the same ones
             the Explorer tree's Profile rows reuse in turn. -->
        <span
          class="ml-auto shrink-0 rounded-full border border-border bg-surface-raised px-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
          title="Engine: {engineLabel(profile.Engine || 'mysql')}"
        >
          {engineLabel(profile.Engine || "mysql")}
        </span>
        {#if profile.SSH}
          <span
            class="shrink-0 rounded-full border border-border bg-surface-raised px-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
            title="Reached through an SSH tunnel"
          >
            ssh
          </span>
        {/if}
        {#if profile.TLS}
          <span
            class="shrink-0 rounded-full border border-border bg-surface-raised px-1.5 text-xs font-medium tracking-wide text-text-muted uppercase"
            title={profile.TLS.SkipVerify
              ? "Encrypted with TLS, certificate not verified"
              : "Encrypted with TLS"}
          >
            tls
          </span>
        {/if}
      </span>
      <span class="truncate pl-3.5 font-mono text-xs text-text-subtle">
        {subtitle(profile)}
      </span>
    </button>
    {#if indicatorAfter(group, profile.Name)}{@render dropLine()}{/if}
  </div>
{/snippet}

<aside class="flex w-60 shrink-0 flex-col border-r border-border bg-surface-panel">
  <div class="flex h-11 shrink-0 items-center justify-between border-b border-border pr-2 pl-4">
    <h2 class="text-xs font-medium tracking-wide text-text-muted uppercase">Profiles</h2>
    <button
      type="button"
      class="flex h-7 w-7 items-center justify-center rounded-control border border-transparent text-text-muted transition-colors hover:border-border hover:bg-surface-overlay hover:text-text"
      onclick={onCreate}
      title="New profile"
      aria-label="New profile"
    >
      <svg
        class="h-4 w-4"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linecap="round"
        aria-hidden="true"
      >
        <path d="M8 3.5v9M3.5 8h9" />
      </svg>
    </button>
  </div>

  <nav class="flex-1 overflow-y-auto py-2">
    {#if profiles.length === 0}
      <p class="px-4 py-2 text-sm text-text-subtle">No profiles yet</p>
    {:else}
      {#each sections as section (section.group)}
        {#if section.group !== ""}
          <button
            type="button"
            class="flex h-7 w-full items-center gap-1 px-4 text-left text-xs font-medium tracking-wide text-text-muted uppercase transition-colors hover:text-text"
            onclick={() => toggleGroupCollapsed(section.group)}
            ondragover={(e) => handleSectionDragOver(e, section.group)}
            ondrop={handleDrop}
            aria-expanded={!isGroupCollapsed(section.group)}
          >
            <svg
              class="h-2.5 w-2.5 shrink-0 transition-transform {isGroupCollapsed(section.group)
                ? ''
                : 'rotate-90'}"
              viewBox="0 0 12 12"
              fill="none"
              stroke="currentColor"
              stroke-width="1.8"
              stroke-linecap="round"
              stroke-linejoin="round"
              aria-hidden="true"
            >
              <path d="M4.5 2.5 8 6l-3.5 3.5" />
            </svg>
            <span class="truncate">{section.group}</span>
            <span class="ml-auto shrink-0 font-normal normal-case text-text-subtle">
              {section.profiles.length}
            </span>
          </button>
        {/if}

        {#if section.group === "" || !isGroupCollapsed(section.group)}
          <div role="list" ondragover={(e) => handleSectionDragOver(e, section.group)} ondrop={handleDrop}>
            {#each section.profiles as profile (profile.Name)}
              {@render profileRow(profile, section.group)}
            {/each}
            {#if indicatorAtEnd(section.group)}
              {@render dropLine()}
            {:else if section.profiles.length === 0}
              <div class="h-2"></div>
            {/if}
          </div>
        {/if}
      {/each}
    {/if}
  </nav>
</aside>
