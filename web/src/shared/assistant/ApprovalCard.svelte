<script lang="ts" module>
  export const ARM_MS = 900;
  const steps = Array.from({ length: 101 }, (_, index) => index / 100);
  const touchQuery = '(width < 861px), ((pointer: coarse) and (not (any-pointer: fine)))';
</script>

<script lang="ts">
  import { type Snippet, tick } from 'svelte';
  import { on } from 'svelte/events';
  import { announce } from '../announce.svelte';
  import Button from '../ui/Button.svelte';
  import Dialog from '../ui/Dialog.svelte';
  import ChangeView from './ChangeView.svelte';
  import { formatClock, formatDayTime } from './format';
  import HoldButton, { type Presentation } from './HoldButton.svelte';
  import ReceiptLine, { UNDO_MS } from './ReceiptLine.svelte';
  import type { Approval, Blocked, DecidedElsewhere, Decision } from './types';
  import './assistant.css';

  type Handler = () => void | Promise<void>;

  interface Props {
    approval: Approval;
    presentation?: Presentation | 'auto';
    decided?: DecidedElsewhere;
    blocked?: Blocked;
    insetTop?: number;
    insetBottom?: number;
    level?: 2 | 3 | 4;
    more?: Snippet;
    onapprove: Handler;
    ondecline: Handler;
    onlater?: Handler;
    onedit?: () => void;
    onrepair?: () => void;
    onundo: (decision: Decision) => void | Promise<void>;
  }

  let {
    approval,
    presentation = 'auto',
    decided,
    blocked,
    insetTop = 0,
    insetBottom = 0,
    level = 3,
    more,
    onapprove,
    ondecline,
    onlater,
    onedit,
    onrepair,
    onundo,
  }: Props = $props();

  const uid = $props.id();
  let article = $state<HTMLElement>();
  let row = $state<HTMLElement>();
  let sentinel = $state<HTMLElement>();
  let receiptEl = $state<HTMLElement>();
  let rowHeight = $state(0);
  let touch = $state(false);
  let endSeen = $state(false);
  let cardSeen = $state(false);
  let rowClear = $state(true);
  let arming = $state(false);
  let confirming = $state(false);
  let phase = $state<'open' | 'busy' | 'counting' | 'decided'>('open');
  let busyOn = $state<Decision>();
  let left = $state(0);
  let receipt = $state<{ decision: Decision; title: string; at: Date; note?: string; undoUntil?: number }>();
  let armTimer: number | undefined;
  let countTimer: number | undefined;

  const mode = $derived<Presentation>(presentation === 'auto' ? (touch ? 'hold' : 'click') : presentation);
  const away = $derived(!cardSeen || (!rowClear && !confirming));
  const rowInert = $derived(away || arming || phase !== 'open');
  const heading = $derived(`h${level}`);
  const gated = $derived(approval.irreversible && !endSeen);
  const margin = $derived(`${-insetTop}px 0px ${-insetBottom}px 0px`);

  $effect(() => {
    const query = matchMedia(touchQuery);
    touch = query.matches;
    return on(query, 'change', () => (touch = query.matches));
  });

  $effect(() => {
    if (!row) return;
    const observer = new ResizeObserver(() => (rowHeight = row?.offsetHeight ?? 0));
    observer.observe(row);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!article) return;
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const root = entry.rootBounds?.height ?? window.innerHeight;
          const need = 0.5 * Math.min(entry.boundingClientRect.height, root);
          cardSeen = entry.isIntersecting && entry.intersectionRect.height >= need - 1;
        }
      },
      { rootMargin: margin, threshold: steps },
    );
    observer.observe(article);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!row) return;
    const tracked = 'isVisible' in IntersectionObserverEntry.prototype;
    const options = {
      rootMargin: margin,
      threshold: [0, 1],
      ...(tracked ? { trackVisibility: true, delay: 100 } : {}),
    };
    const observer = new IntersectionObserver((entries) => {
      if (confirming || row?.querySelector(':popover-open')) return;
      for (const entry of entries) {
        const visible = (entry as IntersectionObserverEntry & { isVisible?: boolean }).isVisible;
        rowClear = entry.isIntersecting && (!tracked || visible !== false);
      }
    }, options);
    observer.observe(row);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (!sentinel || endSeen) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) endSeen = true;
      },
      { rootMargin: `${-insetTop}px 0px ${-(insetBottom + rowHeight)}px 0px` },
    );
    observer.observe(sentinel);
    return () => observer.disconnect();
  });

  $effect(() => {
    if (away) {
      clearTimeout(armTimer);
      arming = false;
      return;
    }
    arming = true;
    armTimer = window.setTimeout(() => (arming = false), ARM_MS);
    return () => clearTimeout(armTimer);
  });

  $effect(() => () => clearInterval(countTimer));

  $effect(() => {
    const focused = document.activeElement;
    if (rowInert && focused instanceof HTMLElement && row?.contains(focused)) focused.blur();
  });

  function onReveal(): void {
    sentinel?.scrollIntoView({
      block: 'center',
      behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
    });
  }

  function words(decision: Decision): { title: string; note?: string; undo: boolean } {
    if (decision === 'decline') return { title: 'Declined. atrium will not do this', undo: true };
    if (decision === 'later') return { title: 'Moved to Later', undo: true };
    if (approval.irreversible) return { title: approval.done, note: 'It cannot be recalled', undo: false };
    return { title: approval.done, undo: true };
  }

  async function decide(decision: Decision, handler: Handler): Promise<void> {
    if (phase !== 'open' && phase !== 'counting') return;
    const focused = article?.contains(document.activeElement) || document.activeElement === document.body;
    phase = 'busy';
    busyOn = decision;
    try {
      await handler();
    } catch {
      phase = 'open';
      busyOn = undefined;
      return;
    }
    const said = words(decision);
    const at = new Date();
    receipt = {
      decision,
      title: said.title,
      at,
      note: said.note,
      undoUntil: said.undo ? at.getTime() + UNDO_MS : undefined,
    };
    phase = 'decided';
    busyOn = undefined;
    announce(said.title);
    await tick();
    if (focused) receiptEl?.focus();
  }

  function onSend(): void {
    if (rowInert) return;
    if (mode === 'hold') {
      void decide('approve', onapprove);
      return;
    }
    if (phase !== 'open') return;
    phase = 'counting';
    left = UNDO_MS / 1000;
    announce(`${approval.sending ?? 'Sending'} in ${left} seconds. Press Undo to stop.`);
    countTimer = window.setInterval(() => {
      left -= 1;
      if (left > 0) return;
      clearInterval(countTimer);
      void decide('approve', onapprove);
    }, 1000);
  }

  async function onStop(): Promise<void> {
    if (phase !== 'counting') return;
    clearInterval(countTimer);
    phase = 'open';
    announce('Not sent. Nothing changed.');
    await tick();
    row?.querySelector<HTMLElement>('button')?.focus();
  }

  function onConfirmOpen(): void {
    confirming = true;
  }

  function onConfirm(): void {
    confirming = false;
    if (phase !== 'open' || gated) return;
    void decide('approve', onapprove);
  }

  async function onUndo(): Promise<void> {
    const decision = receipt?.decision;
    if (decision === undefined) return;
    await onundo(decision);
    receipt = undefined;
    phase = 'open';
    announce('Undone. The card is open again.');
  }

  function onApprove(): void {
    if (!rowInert) void decide('approve', onapprove);
  }

  function onDecline(): void {
    if (!rowInert) void decide('decline', ondecline);
  }

  function onLater(): void {
    if (onlater && !rowInert) void decide('later', onlater);
  }
</script>

<article
  bind:this={article}
  class="card approval"
  data-approval={approval.id}
  data-state={decided ? 'elsewhere' : blocked ? 'blocked' : phase}
  aria-labelledby="{uid}-title"
>
  <p class="approval__who">
    {#if approval.fromHref}
      <a class="link" href={approval.fromHref}>{approval.from}</a>
    {:else}
      {approval.from}
    {/if}
    <span class="meta">· {formatDayTime(approval.created)}</span>
  </p>
  <svelte:element this={heading} class="card__title" id="{uid}-title">{approval.title}</svelte:element>

  <ChangeView change={approval.change} plain={approval.plain} current={approval.current} />
  <div class="approval__end" bind:this={sentinel} aria-hidden="true"></div>

  <dl class="approval__facts" aria-label="What happens">
    <div>
      <dt>Reversible</dt>
      <dd>{approval.consequence.reversible}</dd>
    </div>
    <div>
      <dt>Who sees it</dt>
      <dd>{approval.consequence.audience}</dd>
    </div>
    <div>
      <dt>Cost</dt>
      <dd>{approval.consequence.cost}</dd>
    </div>
    <div>
      <dt>Outside atrium</dt>
      <dd>{approval.consequence.outside}</dd>
    </div>
  </dl>

  {#if approval.why}
    <details class="approval__why">
      <summary>Why atrium asks</summary>
      <p>{approval.why}</p>
    </details>
  {/if}

  {#if decided}
    <p class="approval__note" role="status">Decided on {decided.where} at {formatClock(decided.at)}</p>
    <div class="approval__row">
      <Button variant="primary" disabled>{approval.primary}</Button>
      <Button variant="secondary" disabled>Decline</Button>
    </div>
  {:else if phase === 'decided' && receipt}
    <ReceiptLine
      bind:element={receiptEl}
      title={receipt.title}
      at={receipt.at}
      note={receipt.note}
      tone={receipt.decision === 'decline' ? 'declined' : 'done'}
      undoUntil={receipt.undoUntil}
      onundo={receipt.undoUntil === undefined ? undefined : onUndo}
    />
  {:else if phase === 'counting'}
    <ReceiptLine tone="pending" title="{approval.sending ?? 'Sending'} in {left} s" onundo={onStop} />
  {:else}
    {#if blocked}
      <p class="approval__note">{blocked.reason}</p>
    {/if}
    <div
      bind:this={row}
      class="approval__row"
      inert={rowInert}
      data-away={away || undefined}
      data-arming={arming || undefined}
    >
      {#if blocked}
        <Button variant="primary" onclick={onrepair}>{blocked.repair}</Button>
      {:else if approval.irreversible}
        <HoldButton
          label={approval.primary}
          presentation={mode}
          {gated}
          disabled={phase !== 'open'}
          onreveal={onReveal}
          onconfirm={onConfirmOpen}
          onsend={onSend}
        />
      {:else}
        <Button variant="primary" pending={busyOn === 'approve'} onclick={onApprove}>{approval.primary}</Button>
      {/if}
      {#if onedit}
        <Button variant="secondary" onclick={onedit}>Edit</Button>
      {/if}
      {#if !blocked}
        <Button variant="secondary" pending={busyOn === 'decline'} onclick={onDecline}>Decline</Button>
      {/if}
      {#if onlater}
        <Button variant="quiet" pending={busyOn === 'later'} onclick={onLater}>Later</Button>
      {/if}
      {@render more?.()}
    </div>
  {/if}
</article>

<Dialog bind:open={confirming} title="Send this now?" initialFocus="cancel">
  <p>{approval.title}. Once sent it cannot be recalled.</p>
  {#snippet actions()}
    <Button variant="quiet" data-cancel onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="primary" onclick={onConfirm}>Send now</Button>
  {/snippet}
</Dialog>
