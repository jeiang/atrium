<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError, type CommitmentItem } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import type { InboxId } from '../../shared/assistant/inboxId';
  import ReceiptLine, { UNDO_MS } from '../../shared/assistant/ReceiptLine.svelte';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextArea from '../../shared/ui/TextArea.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import {
    dueText,
    handleCommitmentVerb,
    handleUndoEffects,
    linkText,
    type Problem,
    problemOf,
    refreshPlan,
    statusWords,
    useCommitment,
    verbWords,
  } from '../plan/plan';
  import { handleAnswer, type InboxRow, inboxKey, queueRow, useQueueItem } from './inbox';

  interface Props {
    target: InboxId;
    level?: 2 | 3;
    ondecided?: (key: string) => void;
  }

  let { target, level = 2, ondecided }: Props = $props();

  const client = useQueryClient();
  const key = $derived(inboxKey(target));
  const query = useQueueItem(() => target);
  const commitment = useCommitment(() =>
    target.type === 'task' || target.type === 'commitment' ? `${target.type}:${target.id}` : undefined,
  );
  let held = $state<InboxRow>();
  let receipt = $state<{ title: string; effects: number[]; until: number }>();
  let answered = $state(false);
  let answer = $state('');
  let elsewhere = $state<string>();
  let busy = $state(false);
  let problem = $state<Problem>();

  const row = $derived(held ?? (query.data ? queueRow(query.data) : undefined));
  const item = $derived<CommitmentItem | undefined>(commitment.data ?? undefined);
  const verbs = $derived(
    receipt || !item ? [] : (['done', 'dismiss'] as const).filter((verb) => item.actions.includes(verb)),
  );
  const closedElsewhere = $derived(
    !row && !receipt && item && item.status !== 'open' ? statusWords[item.status as 'done' | 'dismissed'] : undefined,
  );
  const heading = $derived(`h${level}`);

  async function onVerb(verb: 'done' | 'dismiss'): Promise<void> {
    if (!item || busy) return;
    problem = undefined;
    busy = true;
    held = row;
    try {
      const effects = await handleCommitmentVerb(item.ref, verb);
      receipt = { title: verbWords[verb].done, effects, until: Date.now() + UNDO_MS };
      ondecided?.(key);
    } catch (error) {
      held = undefined;
      problem = problemOf(error);
    } finally {
      busy = false;
      refreshPlan(client);
    }
  }

  async function onUndo(): Promise<void> {
    if (!receipt) return;
    problem = undefined;
    try {
      await handleUndoEffects(receipt.effects);
      receipt = undefined;
      held = undefined;
    } catch (error) {
      problem = problemOf(error);
    } finally {
      refreshPlan(client);
    }
  }

  async function onAnswer(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    const text = answer.trim();
    if (!text || busy) return;
    problem = undefined;
    busy = true;
    held = row;
    try {
      await handleAnswer(Number(target.id), text);
      answered = true;
      ondecided?.(key);
    } catch (error) {
      held = undefined;
      if (error instanceof ApiError && error.code === 'no_question') elsewhere = 'It was answered elsewhere.';
      else problem = problemOf(error);
    } finally {
      busy = false;
      refreshPlan(client);
    }
  }

  function onRetry(): void {
    void query.refetch();
  }
</script>

<div class="inbox-card" data-item={key}>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if row}
    <article class="card ask" aria-labelledby="{key}-title">
      <p class="ask__eyebrow">{row.label}</p>
      <svelte:element this={heading} class="ask__title" id="{key}-title">{row.title}</svelte:element>
      {#if row.summary}
        <p class="ask__summary">{row.summary}</p>
      {/if}
      <dl class="ask__facts">
        <dt>Waiting since</dt>
        <dd>{formatDayTime(row.created * 1000)}</dd>
        {#if item?.due ?? row.due}
          <dt>Due</dt>
          <dd>{dueText(item?.due ?? row.due)}</dd>
        {/if}
        {#if item?.counterpart}
          <dt>{item.owner === 'them' ? 'Owed by' : 'Owed to'}</dt>
          <dd>{item.counterpart}</dd>
        {/if}
        {#each item?.links ?? [] as link (`${link.kind}:${link.id}`)}
          <dt>Link</dt>
          <dd>{linkText(link)}</dd>
        {/each}
      </dl>
      {#if receipt}
        <ReceiptLine
          title={receipt.title}
          undoUntil={receipt.until}
          onundo={receipt.effects.length > 0 ? onUndo : undefined}
        />
      {:else if answered}
        <ReceiptLine title="Answer sent" note="The assistant picks the chat back up." />
      {:else if elsewhere}
        <ReceiptLine title="Already answered" note={elsewhere} />
      {:else if target.type === 'question'}
        <form class="ask__answer" onsubmit={onAnswer}>
          <TextArea label="Your answer" bind:value={answer} rows={3} required />
          <div class="ask__actions">
            <Button variant="primary" type="submit" pending={busy}>Send answer</Button>
            <a class="btn btn--quiet" href={p('/chat/:id', { params: { id: target.id } })}>Open the chat</a>
          </div>
        </form>
      {:else if target.type === 'plan'}
        <div class="ask__actions">
          <a class="btn btn--primary" href={p('/chat/:id', { params: { id: target.id } })}>Open the chat</a>
        </div>
      {:else if item}
        <div class="ask__actions">
          {#each verbs as verb (verb)}
            <Button variant={verb === 'done' ? 'primary' : 'secondary'} pending={busy} onclick={() => onVerb(verb)}>
              {verbWords[verb].label}
            </Button>
          {/each}
          <a class="btn btn--quiet" href={p('/plan/item/:ref', { params: { ref: item.ref } })}>Open in Plan</a>
        </div>
      {/if}
    </article>
  {:else if closedElsewhere}
    <ViewState kind="empty" title="Handled elsewhere" {level}>
      This was marked {closedElsewhere.toLowerCase()} somewhere else.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/inbox')}>Back to the Inbox</a>
      {/snippet}
    </ViewState>
  {:else if unreachable(query)}
    <ViewState kind="offline" title="Cannot reach atrium" {level}>
      This item loads when the connection is back. Nothing was decided.
    </ViewState>
  {:else if query.isPending || (commitment.isPending && commitment.fetchStatus !== 'idle')}
    <div class="card inbox-card__loading"><Skeleton lines={9} label="Loading the item" /></div>
  {:else if query.isError}
    <ViewState kind="error" title="This item did not load" {level}>
      {query.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else}
    <ViewState kind="empty" title="Nothing waits here" {level}>
      It was handled or removed, or the address is wrong.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/inbox')}>Back to the Inbox</a>
      {/snippet}
    </ViewState>
  {/if}
</div>
