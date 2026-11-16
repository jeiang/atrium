<script lang="ts">
  import type { Snippet } from 'svelte';
  import './kit.css';

  interface Props {
    title: string;
    eyebrow?: string;
    lede?: string;
    actions?: Snippet;
    children: Snippet;
  }

  let { title, eyebrow, lede, actions, children }: Props = $props();
</script>

<div class="page">
  <header class="page-head">
    <div class="page-head__text">
      {#if eyebrow}
        <span class="eyebrow">{eyebrow}</span>
      {/if}
      <h1 class="page-title">{title}</h1>
      {#if lede}
        <p class="page-lede">{lede}</p>
      {/if}
    </div>
    {#if actions}
      <div class="page-head__actions">{@render actions()}</div>
    {/if}
  </header>
  {@render children()}
</div>
