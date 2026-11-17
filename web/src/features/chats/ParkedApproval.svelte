<script lang="ts" module>
  import { SvelteMap } from 'svelte/reactivity';
  import {
    ApiError,
    OFFLINE_MESSAGE,
    ok,
    reviewApprove,
    reviewDismiss,
    reviewEffects,
    reviewProposal,
    reviewResolve,
    reviewUndo,
    reviewUndoable,
  } from '../../contract';
  import { resolutionOf, stuckChoices, stuckOf } from '../../shared/assistant/stuck';
  import { device, outcomeUnknown, unreachable } from '../../shared/device.svelte';
  import { queryClient } from '../../shared/query';
  import { toast } from '../../shared/ui/toast.svelte';

  const deferred = new SvelteMap<number, number>();

  function failureText(error: unknown): string {
    if (outcomeUnknown(error)) return `The outcome is unknown. ${error.message} atrium did not try again.`;
    if (error instanceof ApiError) return error.message;
    return 'The outcome is unknown. The connection failed before atrium answered, so this may or may not have happened. atrium did not try again.';
  }

  async function handleDismissDue(id: number): Promise<void> {
    deferred.delete(id);
    try {
      ok(await reviewDismiss(id, {}));
    } catch (error) {
      toast(failureText(error), { tone: 'error' });
    }
    await queryClient.invalidateQueries({ queryKey: ['proposal', id] });
    await queryClient.invalidateQueries({ queryKey: ['thread'] });
  }
</script>

<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import ApprovalCard from '../../shared/assistant/ApprovalCard.svelte';
  import { proposalToApproval } from '../../shared/assistant/proposalApproval';
  import { UNDO_MS } from '../../shared/assistant/ReceiptLine.svelte';
  import type { Decision } from '../../shared/assistant/types';
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
  const resolved = $derived(effects.data ? resolutionOf(effects.data) : undefined);

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

  function onDecline(): void {
    if (!device.online) refuse(new ApiError(0, { error: { code: 'offline', message: OFFLINE_MESSAGE } }));
    deferred.set(
      id,
      window.setTimeout(() => void handleDismissDue(id), UNDO_MS),
    );
  }

  async function onUndo(decision: Decision): Promise<void> {
    if (decision === 'decline') {
      clearTimeout(deferred.get(id));
      deferred.delete(id);
      return;
    }
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
{:else if proposal.data.status !== 'pending' && !deferred.has(id)}
  <ViewState
    kind="partial"
    title={resolved?.outcome ?? (proposal.data.status === 'approved' ? 'Approved' : 'Declined')}
    level={4}
  >
    {proposal.data.title}
  </ViewState>
{:else}
  <ApprovalCard {approval} level={4} onapprove={onApprove} ondecline={onDecline} onundo={onUndo} />
{/if}
