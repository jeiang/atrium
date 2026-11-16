<script lang="ts">
  import Spinner from './Spinner.svelte';
  import './kit.css';

  interface Props {
    label: string;
    help?: string;
    checked?: boolean;
    pending?: boolean;
    locked?: string;
    onchange?: (checked: boolean) => void;
  }

  let { label, help, checked = $bindable(false), pending = false, locked, onchange }: Props = $props();
  const uid = $props.id();
  const blocked = $derived(pending || locked !== undefined);
  const describedby = $derived(
    [help && `${uid}-help`, locked && `${uid}-locked`].filter(Boolean).join(' ') || undefined,
  );

  function onClick(event: MouseEvent): void {
    if (blocked) event.preventDefault();
  }

  function onChange(event: Event & { currentTarget: HTMLInputElement }): void {
    onchange?.(event.currentTarget.checked);
  }
</script>

<label class="switch">
  <span class="switch__text">
    {label}
    {#if help}
      <span class="check__help" id="{uid}-help">{help}</span>
    {/if}
    {#if locked}
      <span class="check__help" id="{uid}-locked">{locked}</span>
    {/if}
  </span>
  <span class="switch__side">
    {#if pending}
      <Spinner />
    {/if}
    <input
      type="checkbox"
      role="switch"
      bind:checked
      aria-checked={checked}
      aria-disabled={blocked || undefined}
      aria-busy={pending || undefined}
      aria-describedby={describedby}
      onclick={onClick}
      onchange={onChange}
    >
  </span>
</label>
