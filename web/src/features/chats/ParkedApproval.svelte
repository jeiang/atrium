<script lang="ts" module>
  import { SvelteMap } from 'svelte/reactivity';
  import { ok, reviewApprove, reviewDismiss, reviewProposal, reviewUndo, reviewUndoable } from '../../contract';
  import { queryClient } from '../../shared/query';
  import { toast } from '../../shared/ui/toast.svelte';

  const deferred = new SvelteMap<number, number>();

  async function handleDismissDue(id: number): Promise<void> {
    deferred.delete(id);
    try {
      ok(await reviewDismiss(id, {}));
    } catch (error) {
      toast(error instanceof Error ? error.message : 'The ask could not be declined.', { tone: 'error' });
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

  async function onApprove(): Promise<void> {
    ok(await reviewApprove(id, {}));
    ondecided();
  }

  function onDecline(): void {
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

{#if proposal.isPending}
  <ViewState kind="loading" title="Loading the ask" />
{:else if proposal.isError || !approval || !proposal.data}
  <ViewState kind="error" title="The ask could not be loaded" level={4}>
    {proposal.error?.message ?? 'Open the Inbox to decide it.'}
  </ViewState>
{:else if proposal.data.status !== 'pending' && !deferred.has(id)}
  <ViewState kind="partial" title={proposal.data.status === 'approved' ? 'Approved' : 'Declined'} level={4}>
    {proposal.data.title}
  </ViewState>
{:else}
  <ApprovalCard {approval} level={4} onapprove={onApprove} ondecline={onDecline} onundo={onUndo} />
{/if}
