<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLAttributes } from 'svelte/elements';
  import './kit.css';

  type Props = Omit<HTMLAttributes<HTMLElement>, 'title' | 'children'> & {
    title: string;
    meta?: string;
    actions?: Snippet;
    children: Snippet;
  };

  let { title, meta, actions, children, ...rest }: Props = $props();
  const uid = $props.id();
</script>

<section {...rest} class="section" aria-labelledby="{uid}-title">
  <header class="section-head">
    <h2 class="section-head__title" id="{uid}-title">{title}</h2>
    {#if meta}
      <span class="section-head__meta">{meta}</span>
    {/if}
    {@render actions?.()}
  </header>
  {@render children()}
</section>
