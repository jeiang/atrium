<script lang="ts">
  import type { Snippet } from 'svelte';
  import './kit.css';

  interface Props {
    title: string;
    meta?: string;
    href?: string;
    onclick?: (event: MouseEvent) => void;
    selected?: boolean;
    unread?: boolean;
    pending?: boolean;
    lead?: Snippet;
    end?: Snippet;
  }

  let { title, meta, href, onclick, selected = false, unread = false, pending = false, lead, end }: Props = $props();
</script>

{#snippet body()}
  {#if lead || unread}
    <span class="row__lead">
      {#if unread}
        <span class="row__unread"></span><span class="visually-hidden">Unread</span>
      {/if}
      {@render lead?.()}
    </span>
  {/if}
  <span class="row__title">{title}</span>
  {#if meta}
    <span class="row__meta">{meta}</span>
  {/if}
{/snippet}

<li
  class="row"
  data-selected={selected || undefined}
  data-unread={unread || undefined}
  aria-busy={pending || undefined}
>
  {#if href}
    <a class="row__main" {href} aria-current={selected || undefined}>{@render body()}</a>
  {:else if onclick}
    <button type="button" class="row__main" {onclick} aria-current={selected || undefined}>{@render body()}</button>
  {:else}
    <div class="row__main">{@render body()}</div>
  {/if}
  {#if end}
    <div class="row__end">{@render end()}</div>
  {/if}
</li>
