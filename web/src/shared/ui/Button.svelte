<script lang="ts">
  import type { Snippet } from 'svelte';
  import type { HTMLButtonAttributes } from 'svelte/elements';
  import Icon, { type IconName } from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import './kit.css';

  interface Props extends Omit<HTMLButtonAttributes, 'disabled' | 'children'> {
    variant?: 'primary' | 'secondary' | 'quiet' | 'danger';
    size?: 'md' | 'sm';
    pending?: boolean;
    disabled?: boolean;
    reason?: string;
    icon?: IconName;
    label?: string;
    children?: Snippet;
    element?: HTMLButtonElement;
  }

  let {
    variant = 'secondary',
    size = 'md',
    pending = false,
    disabled = false,
    reason,
    icon,
    label,
    children,
    type = 'button',
    onclick,
    class: className,
    element = $bindable(),
    ...rest
  }: Props = $props();

  const reasonId = $props.id();
  const explained = $derived(disabled && reason !== undefined);
  const blocked = $derived(pending || explained);

  function onClick(event: MouseEvent & { currentTarget: EventTarget & HTMLButtonElement }): void {
    if (blocked) {
      event.preventDefault();
      return;
    }
    onclick?.(event);
  }
</script>

<button
  {...rest}
  bind:this={element}
  {type}
  class={['btn', `btn--${variant}`, size === 'sm' && 'btn--sm', !children && 'btn--icon', className]}
  disabled={disabled && !explained}
  aria-disabled={blocked || undefined}
  aria-busy={pending || undefined}
  aria-label={label}
  aria-describedby={explained ? reasonId : rest['aria-describedby']}
  onclick={onClick}
>
  {#if pending}
    <Spinner />
  {:else if icon}
    <Icon name={icon} />
  {/if}
  {@render children?.()}
</button>
{#if explained}
  <span class="btn-reason" id={reasonId}>{reason}</span>
{/if}
