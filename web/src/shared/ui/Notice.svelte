<script lang="ts">
  import type { Snippet } from 'svelte';
  import Icon, { type IconName } from './Icon.svelte';
  import './kit.css';

  interface Props {
    tone?: 'ok' | 'warn' | 'error' | 'info';
    variant?: 'banner' | 'inline';
    title?: string;
    children: Snippet;
    action?: Snippet;
  }

  let { tone = 'info', variant = 'banner', title, children, action }: Props = $props();
  const icons: Record<NonNullable<Props['tone']>, IconName> = {
    ok: 'check',
    warn: 'alert',
    error: 'error',
    info: 'info',
  };
</script>

<div class="notice notice--{tone} notice--{variant}">
  <Icon name={icons[tone]} />
  <div class="notice__body">
    {#if title}
      <strong class="notice__title">{title}</strong>
    {/if}
    <span>{@render children()}</span>
  </div>
  {#if action}
    <div class="notice__action">{@render action()}</div>
  {/if}
</div>
