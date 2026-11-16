<script lang="ts">
  import type { Snippet } from 'svelte';
  import './kit.css';

  interface Props {
    title?: string;
    level?: 2 | 3 | 4;
    meta?: Snippet;
    selected?: boolean;
    pending?: boolean;
    children: Snippet;
    actions?: Snippet;
  }

  let { title, level = 3, meta, selected = false, pending = false, children, actions }: Props = $props();
</script>

<article class="card" data-selected={selected || undefined} aria-busy={pending || undefined}>
  {#if title || meta}
    <header class="card__head">
      {#if title}
        <svelte:element this={`h${level}`} class="card__title">{title}</svelte:element>
      {/if}
      {@render meta?.()}
    </header>
  {/if}
  {@render children()}
  {#if actions}
    <div class="card__actions">{@render actions()}</div>
  {/if}
</article>
