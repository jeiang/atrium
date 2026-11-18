<script lang="ts" module>
  export const HOLD_MS = 850;
  export const HOLD_REDUCED_MS = 600;
  export type Presentation = 'hold' | 'click';
</script>

<script lang="ts">
  import { on } from 'svelte/events';
  import { announce } from '../announce.svelte';
  import './assistant.css';

  interface Props {
    label: string;
    presentation: Presentation;
    gated: boolean;
    disabled?: boolean;
    onconfirm: () => void;
    onreveal: () => void;
    onsend: () => void;
  }

  let { label, presentation, gated, disabled = false, onconfirm, onreveal, onsend }: Props = $props();

  const uid = $props.id();
  let phase = $state<'ready' | 'holding' | 'sent'>('ready');
  let progress = $state(0);
  let note = $state('');
  let button = $state<HTMLButtonElement>();
  let timer: number | undefined;
  let frame = 0;

  const hold = $derived(presentation === 'hold');
  const shown = $derived(hold ? `Hold to ${label.charAt(0).toLowerCase()}${label.slice(1)}` : label);
  const blocked = $derived(disabled || phase === 'sent');
  const describedBy = $derived([gated && `${uid}-cue`, hold && `${uid}-how`].filter(Boolean).join(' ') || undefined);

  function duration(): number {
    return matchMedia('(prefers-reduced-motion: reduce)').matches ? HOLD_REDUCED_MS : HOLD_MS;
  }

  function away(): boolean {
    return !button?.isConnected || button.closest('[inert]') !== null;
  }

  function stop(): void {
    clearTimeout(timer);
    cancelAnimationFrame(frame);
    timer = undefined;
    progress = 0;
  }

  function fire(): void {
    if (blocked || gated || away()) {
      cancel();
      return;
    }
    stop();
    phase = 'sent';
    onsend();
  }

  function cancel(): void {
    if (phase !== 'holding') return;
    stop();
    phase = 'ready';
    note = 'Keep holding to send. Nothing was sent.';
    announce(note);
  }

  function reveal(): void {
    onreveal();
    announce('Showing the end of the change. Read it before you send.');
  }

  function onPointerDown(event: PointerEvent & { currentTarget: HTMLButtonElement }): void {
    if (!hold || blocked || event.button !== 0 || phase === 'holding') return;
    if (gated) {
      event.preventDefault();
      reveal();
      return;
    }
    event.preventDefault();
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    phase = 'holding';
    const ms = duration();
    const started = performance.now();
    timer = window.setTimeout(fire, ms);
    const step = (now: number): void => {
      if (phase !== 'holding') return;
      progress = Math.min(1, (now - started) / ms);
      frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
  }

  function onKeyPath(): void {
    if (blocked || away()) return;
    if (gated) {
      reveal();
      return;
    }
    onconfirm();
  }

  function onKeyDown(event: KeyboardEvent): void {
    if (!hold) return;
    if (event.key === 'Escape') {
      cancel();
      return;
    }
    if (event.key !== 'Enter' && event.key !== ' ') return;
    event.preventDefault();
    if (event.key === 'Enter' && !event.repeat) onKeyPath();
  }

  function onKeyUp(event: KeyboardEvent): void {
    if (!hold || event.key !== ' ') return;
    event.preventDefault();
    window.setTimeout(onKeyPath);
  }

  function onClick(event: MouseEvent): void {
    if (hold) {
      if (event.detail === 0) onKeyPath();
      return;
    }
    if (blocked || away()) return;
    if (gated) {
      reveal();
      return;
    }
    phase = 'sent';
    onsend();
  }

  $effect(() => {
    if (phase !== 'holding') return;
    const offs = [
      on(window, 'pointerup', cancel),
      on(window, 'pointercancel', cancel),
      on(window, 'blur', cancel),
      on(document, 'visibilitychange', cancel),
    ];
    return () => {
      for (const off of offs) off();
    };
  });

  $effect(() => {
    if ((gated || disabled) && phase === 'holding') cancel();
  });

  $effect(() => stop);
</script>

<span class="hold-wrap">
  <button
    bind:this={button}
    type="button"
    class={['btn', 'btn--primary', hold && 'hold']}
    style:--p={progress}
    data-phase={phase}
    data-gated={gated || undefined}
    aria-disabled={gated || blocked || undefined}
    aria-describedby={describedBy}
    onpointerdown={onPointerDown}
    onpointerleave={cancel}
    onkeydown={onKeyDown}
    onkeyup={onKeyUp}
    onclick={onClick}
    oncontextmenu={(event) => hold && event.preventDefault()}
  >
    {#if hold}
      <span class="hold__fill" aria-hidden="true"></span>
    {/if}
    <span class="hold__label">{shown}</span>
  </button>
  {#if hold}
    <span class="visually-hidden" id="{uid}-how"
      >Press and hold to send. With a keyboard, press Enter and confirm.</span
    >
  {/if}
  {#if gated}
    <span class="hold-cue" id="{uid}-cue">More below. Read to the end to send</span>
  {:else if note}
    <span class="hold-cue">{note}</span>
  {/if}
</span>
