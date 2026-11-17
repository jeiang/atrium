<script lang="ts">
  import type { Package } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import {
    delivered,
    fromMail,
    type Problem,
    reached,
    stages,
    statusOf,
    titleOf,
    tracked,
    troubled,
  } from './packages.svelte';

  interface Props {
    item: Package;
    offline?: boolean;
    pending?: boolean;
    trackReason?: string;
    level?: 2 | 3;
    problem?: Problem;
    ontrack?: () => void;
    onuntrack?: () => void;
    ondismiss?: () => void;
  }

  let {
    item,
    offline = false,
    pending = false,
    trackReason,
    level = 2,
    problem,
    ontrack,
    onuntrack,
    ondismiss,
  }: Props = $props();

  const title = $derived(titleOf(item));
  const words = $derived(statusOf(item));
  const step = $derived(reached(item));
  const trouble = $derived(troubled(item));
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);
  const source = $derived(tracked(item) ? 'Tracked with Ship24' : fromMail(item) ? 'Found in mail' : 'Added by you');
  const latest = $derived(item.last_event_description || item.status_text);
  const when = $derived(item.last_event_ts > 0 ? item.last_event_ts : item.updated_ts);
</script>

<Card {title} {level} {pending}>
  {#snippet meta()}
    <p class="pk-card__meta">
      <StatusMark tone={words.tone}>{words.label}</StatusMark>
      <Badge tone={tracked(item) ? 'accent' : 'neutral'}>{source}</Badge>
    </p>
  {/snippet}
  <ol class="pk-route" data-trouble={trouble || undefined} aria-label="Delivery progress for {title}">
    {#each stages as stage, index (stage.key)}
      <li
        class="pk-route__stop"
        data-done={index < step || (index === step && !trouble) || undefined}
        data-here={index === step || undefined}
        aria-current={index === step ? 'step' : undefined}
      >
        <span class="pk-route__label">{stage.label}</span>
      </li>
    {/each}
  </ol>
  <dl class="pk-card__facts">
    {#if item.carrier}
      <div>
        <dt>Carrier</dt>
        <dd>{item.carrier}</dd>
      </div>
    {/if}
    <div>
      <dt>Tracking number</dt>
      <dd class="pk-card__number">{item.tracking_number}</dd>
    </div>
    {#if latest}
      <div>
        <dt>Latest</dt>
        <dd>{latest}{when > 0 ? `, ${formatDayTime(when)}` : ''}</dd>
      </div>
    {/if}
  </dl>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#snippet actions()}
    {#if ontrack && onuntrack && ondismiss}
      {#if tracked(item)}
        <Button
          variant="secondary"
          {pending}
          disabled={offline}
          {reason}
          onclick={onuntrack}
          label="Stop tracking {title}"
          >Stop tracking</Button
        >
      {:else if !delivered(item)}
        <Button
          variant="secondary"
          {pending}
          disabled={trackReason !== undefined}
          reason={trackReason}
          onclick={ontrack}
          label="Track {title}"
          >Track</Button
        >
      {/if}
      <Button variant="quiet" disabled={offline || pending} {reason} onclick={ondismiss} label="Dismiss {title}"
        >Dismiss</Button
      >
    {/if}
  {/snippet}
</Card>
