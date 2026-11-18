<script lang="ts" module>
  export const UNDO_MS = 10_000;
</script>

<script lang="ts">
  import Button from '../ui/Button.svelte';
  import Icon from '../ui/Icon.svelte';
  import { formatClock } from './format';
  import './assistant.css';

  interface Props {
    title: string;
    at?: string | number | Date;
    actor?: string;
    note?: string;
    tone?: 'done' | 'declined';
    undoUntil?: number;
    undoLabel?: string;
    onundo?: () => void | Promise<void>;
    element?: HTMLElement;
  }

  let {
    title,
    at,
    actor,
    note,
    tone = 'done',
    undoUntil,
    undoLabel = 'Undo',
    onundo,
    element = $bindable(),
  }: Props = $props();

  let now = $state(Date.now());
  let undoing = $state(false);
  let used = $state(false);

  const open = $derived(onundo !== undefined && !used && (undoUntil === undefined || now < undoUntil));
  const meta = $derived([actor, at === undefined ? undefined : `at ${formatClock(at)}`].filter(Boolean).join(' '));

  $effect(() => {
    if (undoUntil === undefined || used) return;
    const left = undoUntil - Date.now();
    if (left <= 0) {
      now = Date.now();
      return;
    }
    const handle = window.setTimeout(() => (now = Date.now()), left);
    return () => clearTimeout(handle);
  });

  async function onUndo(): Promise<void> {
    if (!open || undoing) return;
    undoing = true;
    used = true;
    try {
      await onundo?.();
    } finally {
      undoing = false;
    }
  }
</script>

<div class="receipt" data-tone={tone} tabindex="-1" bind:this={element}>
  <span class="receipt__mark">
    <Icon name={tone === 'declined' ? 'close' : 'check'} />
  </span>
  <span class="receipt__text">
    <span class="receipt__title">{title}</span>
    {#if meta || note}
      <span class="receipt__meta">{[meta, note].filter(Boolean).join('. ')}</span>
    {/if}
  </span>
  {#if open || undoing}
    <Button variant="quiet" size="sm" icon="undo" pending={undoing} onclick={onUndo}>{undoLabel}</Button>
  {/if}
</div>
