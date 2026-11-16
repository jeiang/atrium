<script lang="ts">
  import type { CommitmentItem } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import { p } from '../../shell/router';
  import { dueText, linkHref, linkText, type Status, statusWords, type Verb, verbWords } from './plan';

  interface Props {
    item: CommitmentItem;
    busy: boolean;
    onverb: (item: CommitmentItem, verb: Verb) => void;
  }

  let { item, busy, onverb }: Props = $props();

  const verbs = $derived((['done', 'dismiss', 'reopen'] as const).filter((verb) => item.actions.includes(verb)));
</script>

<article class="card plan-detail" aria-labelledby="plan-detail-title">
  <p class="plan-detail__eyebrow">
    {item.source === 'task' ? 'Task' : 'From mail'}
    {#if item.status !== 'open'}
      <StatusMark tone="ok">{statusWords[item.status as Status]}</StatusMark>
    {/if}
  </p>
  <h2 class="plan-detail__title" id="plan-detail-title">{item.title}</h2>
  {#if item.notes}
    <p class="plan-detail__notes">{item.notes}</p>
  {/if}
  <dl class="plan-detail__facts">
    {#if item.due}
      <dt>Due</dt>
      <dd>{dueText(item.due)}</dd>
    {/if}
    {#if item.counterpart}
      <dt>{item.owner === 'them' ? 'Owed by' : 'Owed to'}</dt>
      <dd>{item.counterpart}</dd>
    {/if}
    {#if item.priority && item.priority !== 'normal'}
      <dt>Priority</dt>
      <dd>{item.priority}</dd>
    {/if}
    <dt>Added</dt>
    <dd>{formatDayTime(item.created_ts * 1000)}</dd>
    {#each item.links as link (`${link.kind}:${link.id}`)}
      <dt>Link</dt>
      <dd>
        {#if linkHref(link)}
          <a class="link" href={linkHref(link)}>{linkText(link)}</a>
        {:else}
          {linkText(link)}
        {/if}
      </dd>
    {/each}
  </dl>
  <div class="plan-detail__actions">
    {#each verbs as verb (verb)}
      <Button variant={verb === 'dismiss' ? 'secondary' : 'primary'} pending={busy} onclick={() => onverb(item, verb)}>
        {verbWords[verb].label}
      </Button>
    {/each}
    {#if item.source === 'task'}
      <a class="btn btn--quiet" href={p('/plan/tasks/:id', { params: { id: String(item.id) } })}>Edit in Tasks</a>
    {/if}
  </div>
</article>
