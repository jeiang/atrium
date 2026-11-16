<script lang="ts" module>
  import type { IconName } from './Icon.svelte';

  export interface MenuItem {
    value: string;
    label: string;
    icon?: IconName;
    disabled?: boolean;
    danger?: boolean;
  }
</script>

<script lang="ts">
  import { anchorTo } from './anchor';
  import Button from './Button.svelte';
  import Icon from './Icon.svelte';
  import { isTypeaheadKey, typeahead } from './typeahead';
  import './widgets.css';

  interface Props {
    label: string;
    items: readonly MenuItem[];
    icon?: IconName;
    iconOnly?: boolean;
    variant?: 'primary' | 'secondary' | 'quiet';
    size?: 'md' | 'sm';
    onselect: (value: string) => void;
  }

  let { label, items, icon, iconOnly = false, variant = 'secondary', size = 'md', onselect }: Props = $props();

  const uid = $props.id();
  const find = typeahead();
  let trigger = $state<HTMLButtonElement>();
  let menu = $state<HTMLDivElement>();
  let open = $state(false);
  let entry: 'first' | 'last' = 'first';

  function elements(): HTMLElement[] {
    return [...(menu?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? [])];
  }

  function focusAt(index: number): void {
    const all = elements();
    all[(index + all.length) % all.length]?.focus();
  }

  function onTriggerKeydown(event: KeyboardEvent): void {
    if (event.key !== 'ArrowDown' && event.key !== 'ArrowUp') return;
    event.preventDefault();
    entry = event.key === 'ArrowUp' ? 'last' : 'first';
    if (menu?.matches(':popover-open')) focusAt(entry === 'first' ? 0 : -1);
    else trigger?.click();
  }

  function onToggle(event: ToggleEvent): void {
    open = event.newState === 'open';
    if (open) {
      focusAt(entry === 'first' ? 0 : -1);
      entry = 'first';
      return;
    }
    const active = document.activeElement;
    if (!active || active === document.body || menu?.contains(active)) trigger?.focus();
  }

  function onMenuKeydown(event: KeyboardEvent): void {
    const index = elements().indexOf(document.activeElement as HTMLElement);
    if (event.key === 'ArrowDown') focusAt(index + 1);
    else if (event.key === 'ArrowUp') focusAt(index - 1);
    else if (event.key === 'Home' || event.key === 'PageUp') focusAt(0);
    else if (event.key === 'End' || event.key === 'PageDown') focusAt(-1);
    else if (event.key === 'Tab' && event.shiftKey) {
      menu?.hidePopover();
      trigger?.focus();
    } else if (isTypeaheadKey(event)) {
      const match = find(
        event.key,
        items.map((item) => item.label),
        index,
      );
      if (match >= 0) focusAt(match);
    } else return;
    event.preventDefault();
  }

  function onFocusout(event: FocusEvent): void {
    const next = event.relatedTarget;
    if (next instanceof Node && !menu?.contains(next) && next !== trigger) menu?.hidePopover();
  }

  function onChoose(item: MenuItem): void {
    if (item.disabled) return;
    menu?.hidePopover();
    trigger?.focus();
    onselect(item.value);
  }

  const triggerProps = $derived({
    id: `${uid}-trigger`,
    variant,
    size,
    popovertarget: `${uid}-menu`,
    'aria-haspopup': 'menu' as const,
    'aria-expanded': open,
    'aria-controls': `${uid}-menu`,
    onkeydown: onTriggerKeydown,
  });
</script>

{#if iconOnly}
  <Button bind:element={trigger} {...triggerProps} {icon} {label} />
{:else}
  <Button bind:element={trigger} {...triggerProps}>
    {#if icon}
      <Icon name={icon} />
    {/if}
    {label}
    <Icon name="chevron" />
  </Button>
{/if}
<div
  bind:this={menu}
  {@attach anchorTo(() => trigger)}
  id="{uid}-menu"
  class="pop"
  popover="auto"
  role="menu"
  tabindex="-1"
  aria-labelledby="{uid}-trigger"
  ontoggle={onToggle}
  onkeydown={onMenuKeydown}
  onfocusout={onFocusout}
>
  {#each items as item (item.value)}
    <button
      type="button"
      role="menuitem"
      tabindex="-1"
      class={['menu-item', item.danger && 'menu-item--danger']}
      aria-disabled={item.disabled || undefined}
      onclick={() => onChoose(item)}
      onpointermove={(event) => {
    if (document.activeElement !== event.currentTarget) event.currentTarget.focus();
  }}
    >
      {#if item.icon}
        <Icon name={item.icon} />
      {/if}
      {item.label}
    </button>
  {/each}
</div>
