<script lang="ts" module>
  export type ViewKind = 'empty' | 'loading' | 'error' | 'offline' | 'blocked' | 'pending' | 'partial';
</script>

<script lang="ts">
  import type { Snippet } from 'svelte';
  import Skeleton from './Skeleton.svelte';
  import Spinner from './Spinner.svelte';
  import './kit.css';

  interface Props {
    kind: ViewKind;
    title?: string;
    level?: 2 | 3 | 4;
    children?: Snippet;
    action?: Snippet;
  }

  let { kind, title, level = 3, children, action }: Props = $props();
</script>

<div class="statebox" data-kind={kind}>
  {#if kind === 'loading'}
    <Skeleton lines={4} label={title ?? 'Loading'} />
  {:else}
    {#if title}
      <svelte:element this={`h${level}`} class="statebox__title">
        {#if kind === 'pending'}
          <Spinner />
        {/if}
        {title}
      </svelte:element>
    {/if}
    {#if children}
      <p class="statebox__text">{@render children()}</p>
    {/if}
    {#if action}
      <div class="cluster">{@render action()}</div>
    {/if}
  {/if}
</div>
