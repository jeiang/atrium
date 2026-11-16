<script lang="ts" module>
  export interface TabItem {
    value: string;
    label: string;
    disabled?: boolean;
  }
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import './widgets.css';

  interface Props {
    label: string;
    tabs: readonly TabItem[];
    value?: string;
    panel: Snippet<[string]>;
    onchange?: (value: string) => void;
  }

  let { label, tabs, value = $bindable(), panel, onchange }: Props = $props();

  const uid = $props.id();
  let list = $state<HTMLDivElement>();
  const current = $derived(value ?? tabs.find((tab) => !tab.disabled)?.value);

  function onSelect(index: number): void {
    const tab = tabs[index];
    if (!tab || tab.disabled) return;
    list?.querySelector<HTMLElement>(`#${CSS.escape(`${uid}-tab-${index}`)}`)?.focus();
    if (tab.value === current) return;
    value = tab.value;
    onchange?.(tab.value);
  }

  function onKeydown(event: KeyboardEvent, from: number): void {
    const count = tabs.length;
    const seek = (start: number, by: 1 | -1): number => {
      for (let offset = 0; offset < count; offset++) {
        const index = (((start + by * offset) % count) + count) % count;
        if (!tabs[index]?.disabled) return index;
      }
      return from;
    };
    if (event.key === 'ArrowRight') onSelect(seek(from + 1, 1));
    else if (event.key === 'ArrowLeft') onSelect(seek(from - 1, -1));
    else if (event.key === 'Home') onSelect(seek(0, 1));
    else if (event.key === 'End') onSelect(seek(count - 1, -1));
    else return;
    event.preventDefault();
  }
</script>

<div class="tabs">
  <div bind:this={list} class="tablist" role="tablist" aria-label={label}>
    {#each tabs as tab, index (tab.value)}
      <button
        type="button"
        class="tab"
        role="tab"
        id="{uid}-tab-{index}"
        aria-selected={tab.value === current}
        aria-controls="{uid}-panel-{index}"
        aria-disabled={tab.disabled || undefined}
        tabindex={tab.value === current ? 0 : -1}
        onclick={() => onSelect(index)}
        onkeydown={(event) => onKeydown(event, index)}
      >
        {tab.label}
      </button>
    {/each}
  </div>
  {#each tabs as tab, index (tab.value)}
    <div
      class="tabpanel"
      role="tabpanel"
      id="{uid}-panel-{index}"
      aria-labelledby="{uid}-tab-{index}"
      hidden={tab.value !== current}
    >
      {#if tab.value === current}
        {@render panel(tab.value)}
      {/if}
    </div>
  {/each}
</div>
