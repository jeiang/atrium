<script lang="ts">
  import Button from './Button.svelte';
  import { dismissToast, pauseToast, resumeToast, type Toast, toasts } from './toast.svelte';
  import './kit.css';

  let region = $state<HTMLElement>();

  $effect(() => {
    if (!region) return;
    const shown = region.matches(':popover-open');
    if (toasts.length > 0 && !shown) region.showPopover();
    else if (toasts.length === 0 && shown) region.hidePopover();
  });

  function onAction(item: Toast): void {
    item.action?.run();
    dismissToast(item.id);
  }
</script>

<ul class="toasts" popover="manual" aria-label="Notifications" bind:this={region}>
  {#each toasts as item (item.id)}
    <li
      class={['toast', item.tone === 'error' && 'toast--error']}
      onpointerenter={() => pauseToast(item.id)}
      onpointerleave={() => resumeToast(item.id)}
      onfocusin={() => pauseToast(item.id)}
      onfocusout={() => resumeToast(item.id)}
    >
      <span class="toast__text">{item.message}</span>
      {#if item.action}
        <Button variant="quiet" onclick={() => onAction(item)}>{item.action.label}</Button>
      {/if}
      <Button variant="quiet" icon="close" label="Dismiss" onclick={() => dismissToast(item.id)} />
    </li>
  {/each}
</ul>
