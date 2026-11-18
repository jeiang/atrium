<script lang="ts" module>
  import {
    ApiError,
    ok,
    reviewApprove,
    reviewDismiss,
    reviewEffects,
    reviewProposal,
    reviewResolve,
    reviewUndo,
    reviewUndoable,
  } from '../../contract';
  import { stuckChoices, stuckOf } from '../../shared/assistant/stuck';
  import { outcomeUnknown, unreachable } from '../../shared/device.svelte';
  import { queryClient } from '../../shared/query';
  import { toast } from '../../shared/ui/toast.svelte';

  function failureText(error: unknown): string {
    if (outcomeUnknown(error)) return `The outcome is unknown. ${error.message} atrium did not try again.`;
    if (error instanceof ApiError) return error.message;
    return 'The outcome is unknown. The connection failed before atrium answered, so this may or may not have happened. atrium did not try again.';
  }
</script>

<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import ApprovalCard from '../../shared/assistant/ApprovalCard.svelte';
  import {
    handleUndoProposal,
    proposalCanUndo,
    proposalOutcome,
    proposalToApproval,
  } from '../../shared/assistant/proposalApproval';
  import Button from '../../shared/ui/Button.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  interface Props {
    id: number;
    run: number;
    thread: string;
    ondecided: () => void;
  }

  let { id, run, thread, ondecided }: Props = $props();

  const proposal = createQuery(() => ({
    queryKey: ['proposal', id],
    queryFn: async ({ signal }) => ok(await reviewProposal(id, { signal })).data,
    meta: { topics: ['review'] },
  }));

  const approval = $derived(
    proposal.data
      ? proposalToApproval(proposal.data, {
          id: String(proposal.data.id),
          from: thread,
          why: 'atrium paused this chat to ask before it acts.',
          inRun: true,
        })
      : undefined,
  );

  const settled = $derived(
    proposal.data !== undefined &&
      proposal.data.status !== 'pending' &&
      (proposal.data.blocked || proposal.data.status === 'failed' || proposal.data.status === 'applied'),
  );
  const effects = createQuery(() => ({
    queryKey: ['proposal', id, 'effects'],
    queryFn: async ({ signal }) => ok(await reviewEffects({ proposal_id: String(id) }, { signal })).items,
    enabled: settled,
    meta: { topics: ['review'] },
  }));
  const blocked = $derived(
    settled && proposal.data?.blocked && effects.data ? stuckChoices[stuckOf(effects.data)] : undefined,
  );
  const settledTitle = $derived(proposal.data ? proposalOutcome(proposal.data, effects.data) : '');
  const canUndo = $derived(proposal.data ? proposalCanUndo(proposal.data) : false);
  let undoing = $state(false);

  async function onUndoApplied(): Promise<void> {
    undoing = true;
    try {
      await handleUndoProposal(id);
    } catch (error) {
      toast(failureText(error), { tone: 'error' });
    } finally {
      undoing = false;
      await queryClient.invalidateQueries({ queryKey: ['proposal', id] });
      void queryClient.invalidateQueries({ queryKey: ['thread'] });
    }
  }

  async function onResolve(outcome: string): Promise<void> {
    try {
      ok(await reviewResolve(id, { outcome }, { headers: { 'Idempotency-Key': crypto.randomUUID() } }));
    } catch (error) {
      toast(failureText(error), { tone: 'error' });
      throw error;
    } finally {
      await queryClient.invalidateQueries({ queryKey: ['proposal', id] });
      void queryClient.invalidateQueries({ queryKey: ['thread'] });
    }
  }

  function refuse(error: unknown): never {
    toast(failureText(error), { tone: 'error' });
    throw error;
  }

  async function onApprove(): Promise<void> {
    try {
      ok(await reviewApprove(id, {}));
    } catch (error) {
      void queryClient.invalidateQueries({ queryKey: ['proposal', id] });
      refuse(error);
    }
    ondecided();
  }

  async function onDecline(): Promise<void> {
    try {
      ok(await reviewDismiss(id, {}));
    } catch (error) {
      void queryClient.invalidateQueries({ queryKey: ['proposal', id] });
      refuse(error);
    }
    await queryClient.invalidateQueries({ queryKey: ['proposal', id] });
    void queryClient.invalidateQueries({ queryKey: ['thread'] });
  }

  async function onUndo(): Promise<void> {
    const entries = ok(await reviewUndoable({ run_id: String(run) })).items;
    for (const entry of entries.filter((item) => item.undoable && !item.undone_at)) ok(await reviewUndo(entry.id));
    ondecided();
  }
</script>

{#if unreachable(proposal)}
  <ViewState kind="offline" title="Cannot reach atrium" level={4}>Decide it when atrium answers.</ViewState>
{:else if proposal.isPending}
  <ViewState kind="loading" title="Loading the ask" />
{:else if proposal.isError || !approval || !proposal.data}
  <ViewState kind="error" title="The ask could not be loaded" level={4}>
    {proposal.error?.message ?? 'Open the Inbox to decide it.'}
  </ViewState>
{:else if proposal.data.blocked && proposal.data.status !== 'pending'}
  {#if blocked}
    <ApprovalCard
      {approval}
      {blocked}
      level={4}
      onapprove={onApprove}
      ondecline={onDecline}
      onresolve={onResolve}
      onundo={onUndo}
    />
  {:else if effects.isError}
    <ViewState kind="error" title="The outcome is unknown" level={4}>
      {proposal.data.title}. {effects.error.message}
    </ViewState>
  {:else}
    <ViewState kind="loading" title="Loading the ask" />
  {/if}
{:else if proposal.data.status !== 'pending'}
  <ViewState kind="partial" title={settledTitle} level={4} action={canUndo ? undoAction : undefined}>
    {proposal.data.title}
  </ViewState>
{:else}
  <ApprovalCard
    {approval}
    level={4}
    undoable={['approve']}
    onapprove={onApprove}
    ondecline={onDecline}
    onundo={onUndo}
  />
{/if}

{#snippet undoAction()}
  <Button variant="secondary" pending={undoing} onclick={onUndoApplied}>Undo</Button>
{/snippet}
