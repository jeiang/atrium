<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError } from '../../contract';
  import ApprovalCard from '../../shared/assistant/ApprovalCard.svelte';
  import type { InboxId } from '../../shared/assistant/inboxId';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import {
    handleApprove,
    handleDecline,
    handleUndo,
    type InboxItem,
    inboxKey,
    refreshInbox,
    useInboxItem,
  } from './inbox';

  interface Props {
    target: InboxId;
    level?: 2 | 3;
    ondecided?: (key: string) => void;
  }

  let { target, level = 2, ondecided }: Props = $props();

  const client = useQueryClient();
  const query = useInboxItem(() => target);
  const key = $derived(inboxKey(target));
  let held = $state<InboxItem>();
  let undoneHere = $state(false);
  let problem = $state<{ tone: 'warn' | 'error'; title: string; text: string }>();

  const shown = $derived(held ?? query.data ?? undefined);
  const decided = $derived(
    held || !shown?.decided ? undefined : undoneHere ? { ...shown.decided, where: 'this device' } : shown.decided,
  );
  const blocked = $derived(
    shown?.blocked
      ? {
          reason:
            target.type === 'draft'
              ? 'The mail account for this draft is not set up, so it cannot be sent from here.'
              : 'An earlier attempt is stuck and needs a repair before anything else happens.',
          repair: 'Open in the current interface',
        }
      : undefined,
  );

  function describe(error: unknown): { tone: 'warn' | 'error'; title: string; text: string } {
    if (error instanceof ApiError && (error.status === 409 || error.status === 412)) {
      return {
        tone: 'warn',
        title: 'This changed since it loaded',
        text: 'atrium reloaded it. Read it again before you decide.',
      };
    }
    if (error instanceof ApiError) {
      return { tone: 'error', title: 'atrium refused this', text: `${error.message} Nothing was done.` };
    }
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'The connection failed before atrium answered, so this may or may not have happened. atrium reloaded the item from its ledger and did not try again.',
    };
  }

  async function decideWith(run: () => Promise<string | undefined>): Promise<void> {
    problem = undefined;
    held = query.data ?? undefined;
    try {
      const warning = await run();
      if (warning) problem = { tone: 'warn', title: 'It did not go as planned', text: warning };
      ondecided?.(key);
    } catch (error) {
      held = undefined;
      problem = describe(error);
      throw error;
    } finally {
      refreshInbox(client);
    }
  }

  function onApprove(): Promise<void> {
    return decideWith(() => handleApprove(target, query.data?.revision));
  }

  function onDecline(): Promise<void> {
    return decideWith(() => handleDecline(target));
  }

  async function onUndo(): Promise<void> {
    problem = undefined;
    try {
      await handleUndo(target);
      await client.refetchQueries({ queryKey: ['inbox', 'item', key] });
    } catch (error) {
      problem = describe(error);
      throw error;
    } finally {
      refreshInbox(client);
    }
    undoneHere = true;
    held = undefined;
  }

  function onRepair(): void {
    location.assign('/review');
  }

  function onRetry(): void {
    void query.refetch();
  }
</script>

<div class="inbox-card" data-item={key}>
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if shown}
    {#key shown.approval.id}
      <ApprovalCard
        approval={shown.approval}
        {decided}
        {blocked}
        {level}
        undoable={shown.undoable}
        onapprove={onApprove}
        ondecline={onDecline}
        onrepair={onRepair}
        onundo={onUndo}
      />
    {/key}
  {:else if query.isPending && query.fetchStatus === 'paused'}
    <ViewState kind="offline" title="You are offline" {level}>
      This item loads when the connection is back. Nothing was decided.
    </ViewState>
  {:else if query.isPending}
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
      It was decided or removed, or the address is wrong.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/inbox')}>Back to the Inbox</a>
      {/snippet}
    </ViewState>
  {/if}
</div>
