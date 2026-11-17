<script lang="ts">
  import type { Snippet } from 'svelte';
  import { on } from 'svelte/events';
  import Button from './Button.svelte';
  import './kit.css';

  interface Props {
    open?: boolean;
    title: string;
    sheet?: boolean;
    pending?: boolean;
    initialFocus?: 'first' | 'cancel';
    children: Snippet;
    actions?: Snippet;
    onclose?: () => void;
  }

  let {
    open = $bindable(false),
    title,
    sheet = false,
    pending = false,
    initialFocus = 'first',
    children,
    actions,
    onclose,
  }: Props = $props();

  const uid = $props.id();
  let dialog = $state<HTMLDialogElement>();
  let opener: HTMLElement | null = null;
  let drag = $state<{ start: number; offset: number; moved: boolean }>();
  let dragged = false;

  $effect(() => {
    if (!dialog) return;
    if (open && !dialog.open) {
      opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
      dialog.showModal();
      if (initialFocus === 'cancel') dialog.querySelector<HTMLElement>('[data-cancel]')?.focus();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  });

  $effect(() => {
    if (!dialog) return;
    return on(dialog, 'click', (event) => {
      if (event.target === dialog && !pending) open = false;
    });
  });

  function onClose(): void {
    if (dialog?.open) return;
    open = false;
    drag = undefined;
    if (opener?.isConnected) opener.focus();
    opener = null;
    onclose?.();
  }

  function onCancel(event: Event): void {
    if (pending) event.preventDefault();
    else open = false;
  }

  function onGrabClick(): void {
    if (dragged) {
      dragged = false;
      return;
    }
    if (!pending) open = false;
  }

  function onGrabDown(event: PointerEvent & { currentTarget: HTMLElement }): void {
    event.currentTarget.setPointerCapture(event.pointerId);
    drag = { start: event.clientY, offset: 0, moved: false };
  }

  function onGrabMove(event: PointerEvent): void {
    if (!drag) return;
    const offset = Math.max(0, event.clientY - drag.start);
    drag = { ...drag, offset, moved: drag.moved || offset > 4 };
  }

  function onGrabUp(): void {
    if (!drag || !dialog) return;
    dragged = drag.moved;
    const dismiss = drag.moved && drag.offset > dialog.offsetHeight / 4;
    drag = undefined;
    if (dismiss && !pending) open = false;
  }
</script>

<dialog
  bind:this={dialog}
  class={['dialog', sheet && 'sheet']}
  aria-labelledby="{uid}-title"
  aria-busy={pending || undefined}
  data-dragging={drag?.moved || undefined}
  style:translate={drag?.moved ? `0 ${drag.offset}px` : undefined}
  onclose={onClose}
  oncancel={onCancel}
>
  {#if sheet}
    <button
      type="button"
      class="dialog__grab"
      aria-label="Close"
      onclick={onGrabClick}
      onpointerdown={onGrabDown}
      onpointermove={onGrabMove}
      onpointerup={onGrabUp}
      onpointercancel={() => (drag = undefined)}
    >
      <span></span>
    </button>
  {/if}
  <div class="dialog__body">
    <div class="dialog__head">
      <h2 class="dialog__title" id="{uid}-title">{title}</h2>
      {#if sheet}
        <Button variant="quiet" icon="close" label="Close" disabled={pending} onclick={() => (open = false)} />
      {/if}
    </div>
    {@render children()}
    {#if actions}
      <div class="dialog__actions">{@render actions()}</div>
    {/if}
  </div>
</dialog>
