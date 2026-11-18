<script lang="ts">
  import Badge from '../ui/Badge.svelte';
  import Icon from '../ui/Icon.svelte';
  import { formatDayTime, formatSpan, plural } from './format';
  import type {
    ApprovalChange,
    CalendarSlot,
    PlainPreview,
    PreviewCondition,
    PreviewGroup,
    PreviewMessage,
  } from './types';
  import './assistant.css';

  interface Props {
    change: ApprovalChange | null;
    plain: PlainPreview;
    current?: CalendarSlot;
  }

  let { change, plain, current }: Props = $props();

  const known = new Set([
    'mail_draft',
    'calendar_event',
    'calendar_rsvp',
    'calendar_update',
    'deletion_rule',
    'gmail_label_merge',
    'inbox_backlog',
    'mail_triage',
    'rebalance_merge',
    'rebalance_split',
    'reminder',
    'guarded_change',
    'tool_call',
  ]);
  const shown = $derived(change && known.has(change.kind) ? change : null);

  function text(value: unknown): string {
    if (value === null || value === undefined) return '';
    if (typeof value === 'string') return value;
    if (typeof value === 'number' || typeof value === 'boolean') return String(value);
    return JSON.stringify(value, null, 2);
  }

  function condition(item: PreviewCondition): string {
    return `${item.field} ${item.op.replaceAll('_', ' ')} ${text(item.value)}`;
  }

  const answers: Record<string, string> = { accepted: 'Accept', declined: 'Decline', tentative: 'Maybe' };
</script>

{#snippet messages(
    list: PreviewMessage[],
    label: string,
  )}
  {#if list.length > 0}
    <ul class="change__list" aria-label={label}>
      {#each list as message (message.id)}
        <li>
          <span class="change__subject">{message.subject}</span>
          <span class="change__meta">{message.from_name || message.from_addr} · {formatDayTime(message.date_ts)}</span>
        </li>
      {/each}
    </ul>
  {/if}
{/snippet}

{#snippet group(
    item: PreviewGroup,
  )}
  <li>
    <span class="change__subject">{item.name} · {plural(item.count, 'message')}</span>
    {#if item.senders.length > 0}
      <span class="change__meta">{item.senders.map((sender) => `${sender.label} (${sender.count})`).join(', ')}</span>
    {/if}
  </li>
{/snippet}

{#snippet slot(
    item: CalendarSlot,
  )}
  <span class="change__subject">{item.title}</span>
  <span class="change__meta">{formatSpan(item.start, item.end)}{item.location ? ` · ${item.location}` : ''}</span>
{/snippet}

<div class="change" data-kind={shown?.kind ?? 'plain'}>
  {#if shown?.kind === 'mail_draft'}
    <dl class="change__head">
      <dt>From</dt>
      <dd>{shown.account}</dd>
      <dt>To</dt>
      <dd>{shown.to}</dd>
      {#if shown.cc}
        <dt>Cc</dt>
        <dd>{shown.cc}</dd>
      {/if}
      <dt>Subject</dt>
      <dd>{shown.subject}</dd>
    </dl>
    {#if shown.unexpected_recipients.length > 0}
      <p class="change__warn">
        <Badge tone="warn">New recipient</Badge>
        {shown.unexpected_recipients.join(', ')}
      </p>
    {/if}
    <div class="change__body">{shown.body}</div>
  {:else if shown?.kind === 'calendar_event'}
    <div class="change__slot">
      <span class="change__subject">{shown.title}</span>
      <span class="change__meta">{formatSpan(shown.start, shown.end, shown.all_day)} · {shown.calendar}</span>
      {#if shown.location}
        <span class="change__meta">{shown.location}</span>
      {/if}
    </div>
    {#if shown.notes}
      <div class="change__body">{shown.notes}</div>
    {/if}
  {:else if shown?.kind === 'calendar_update'}
    <dl class="change__move">
      <div>
        <dt>Before</dt>
        <dd>
          {#if current}
            <del class="change__slot">{@render slot(current)}</del>
          {:else}
            <span class="change__meta">Event {shown.uid} as it is now</span>
          {/if}
        </dd>
      </div>
      <div>
        <dt>After</dt>
        <dd><ins class="change__slot">{@render slot(shown)}</ins></dd>
      </div>
    </dl>
    <p class="change__meta">{shown.calendar}</p>
  {:else if shown?.kind === 'calendar_rsvp'}
    {#if current}
      <div class="change__slot">{@render slot(current)}</div>
    {/if}
    <p>Your answer: <strong>{answers[shown.response] ?? shown.response}</strong> · {shown.calendar}</p>
  {:else if shown?.kind === 'deletion_rule'}
    <p class="change__rule">
      In {shown.rule.account}, delete mail where {shown.rule.when.map(condition).join(' and ')}
    </p>
    <p>Also deletes <strong>{plural(shown.backlog, 'message')}</strong> already there. {shown.reason}</p>
    {#if shown.senders.length > 0}
      <p class="change__meta">{shown.senders.map((sender) => `${sender.label} (${sender.count})`).join(', ')}</p>
    {/if}
    {@render messages(shown.messages, 'Sample messages')}
  {:else if shown?.kind === 'gmail_label_merge'}
    <dl class="change__move">
      <div>
        <dt>From</dt>
        <dd>
          <span class="change__subject">{shown.from}</span>
          <span class="change__meta">{plural(shown.from_count, 'message')}</span>
        </dd>
      </div>
      <div>
        <dt>Into</dt>
        <dd>
          <span class="change__subject">{shown.into}</span>
          <span class="change__meta">{plural(shown.into_count, 'message')} now</span>
        </dd>
      </div>
    </dl>
    {@render messages(shown.messages, 'Messages that move')}
  {:else if shown?.kind === 'inbox_backlog'}
    <p>From <strong>{shown.folder}</strong>:</p>
    <ul class="change__list" aria-label="Groups">
      {#each shown.groups as item (item.name)}
        {@render group(item)}
      {/each}
    </ul>
  {:else if shown?.kind === 'mail_triage'}
    <p><strong>{plural(shown.count, 'message')}</strong></p>
    <ul class="change__list" aria-label="Messages">
      {#each shown.messages as message (message.id)}
        <li>
          <span class="change__subject">{message.subject}</span>
          <span class="change__meta">{message.from_addr}</span>
          <span class="change__action">{message.action}<Icon name="arrow" />{message.label}</span>
        </li>
      {/each}
    </ul>
  {:else if shown?.kind === 'rebalance_merge'}
    <dl class="change__move">
      <div>
        <dt>From</dt>
        <dd>
          {#each shown.sources as source (source.folder)}
            <span class="change__subject">{source.folder}</span>
            <span class="change__meta">{plural(source.count, 'message')}</span>
          {/each}
        </dd>
      </div>
      <div>
        <dt>Into</dt>
        <dd><span class="change__subject">{shown.into}</span></dd>
      </div>
    </dl>
    {@render messages(shown.group.messages, 'Sample messages')}
  {:else if shown?.kind === 'rebalance_split'}
    <p>Split <strong>{shown.folder}</strong> into:</p>
    <ul class="change__list" aria-label="New folders">
      {#each shown.groups as item (item.name)}
        {@render group(item)}
      {/each}
    </ul>
  {:else if shown?.kind === 'reminder'}
    <div class="change__slot">
      <span class="change__subject">{shown.text}</span>
      <span class="change__meta">Remind at {formatDayTime(shown.remind_at)} · due {shown.due} · {shown.owner}</span>
      <span class="change__meta">From the mail “{shown.subject}”</span>
    </div>
  {:else if shown?.kind === 'guarded_change'}
    <p class="change__subject">{shown.subject}</p>
    <p class="change__meta">{shown.rule}</p>
    {#each shown.fields as field (field.name)}
      <dl class="change__move" aria-label={field.name}>
        <div>
          <dt>{field.name}</dt>
          <dd>
            {#if field.before}
              <del class="change__body">{field.before}</del>
            {:else}
              <span class="change__meta">Empty</span>
            {/if}
          </dd>
        </div>
        <div>
          <dt>Becomes</dt>
          <dd>
            {#if field.after}
              <ins class="change__body">{field.after}</ins>
            {:else}
              <span class="change__meta">Empty</span>
            {/if}
          </dd>
        </div>
      </dl>
    {/each}
    {#if shown.note}
      <p>Reason: {shown.note}</p>
    {/if}
  {:else if shown?.kind === 'tool_call'}
    <pre class="change__code"><code>{shown.tool}({text(shown.args)})</code></pre>
  {:else}
    <p class="change__subject">{plain.label}</p>
    {#if plain.args.length > 0}
      <dl class="change__head">
        {#each plain.args as arg, index (index)}
          <dt>{arg.label}</dt>
          <dd>{arg.value}</dd>
        {/each}
      </dl>
    {/if}
    {#if typeof plain.preview === 'string' && plain.preview}
      <div class="change__body">{plain.preview}</div>
    {:else if plain.preview !== null && plain.preview !== undefined && typeof plain.preview !== 'string'}
      <pre class="change__code"><code>{text(plain.preview)}</code></pre>
    {:else if plain.args.length === 0}
      <p class="change__meta">atrium has no preview for this kind of change. The title above is all it sent.</p>
    {/if}
  {/if}
</div>
