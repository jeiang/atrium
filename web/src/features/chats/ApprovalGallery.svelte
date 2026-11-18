<script lang="ts">
  import { ApiError, ok, type ProposalDetail, reviewApprove, reviewDismiss } from '../../contract';
  import { announce } from '../../shared/announce.svelte';
  import { queryClient } from '../../shared/query';
  import Button from '../../shared/ui/Button.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ParkedApproval, { declining, release } from './ParkedApproval.svelte';

  interface Props {
    ids: number[];
    run: number;
    thread: string;
    ondecided: () => void;
  }

  let { ids, run, thread, ondecided }: Props = $props();

  const uid = $props.id();
  let busy = $state<'approve' | 'deny'>();

  function open(id: number): boolean {
    return queryClient.getQueryData<ProposalDetail>(['proposal', id])?.status === 'pending';
  }

  async function decideAll(decision: 'approve' | 'deny'): Promise<void> {
    if (busy) return;
    busy = decision;
    const todo = ids.filter((id) => open(id) && (decision === 'deny' || !declining(id)));
    try {
      for (const id of todo) {
        release(id);
        ok(decision === 'approve' ? await reviewApprove(id, {}) : await reviewDismiss(id, {}));
      }
      announce(decision === 'approve' ? `Approved ${todo.length} asks` : `Denied ${todo.length} asks`);
    } catch (error) {
      toast(error instanceof ApiError ? error.message : 'atrium did not answer. Some asks may still be open.', {
        tone: 'error',
      });
    } finally {
      busy = undefined;
      await Promise.all(ids.map((id) => queryClient.invalidateQueries({ queryKey: ['proposal', id] })));
      ondecided();
    }
  }
</script>

<section class="asks" aria-labelledby="{uid}-title">
  <div class="asks__head">
    <p class="asks__title" id="{uid}-title">{ids.length} asks</p>
    <div class="asks__actions">
      <Button
        variant="primary"
        pending={busy === 'approve'}
        disabled={busy === 'deny'}
        onclick={() => decideAll('approve')}>Approve all</Button
      >
      <Button pending={busy === 'deny'} disabled={busy === 'approve'} onclick={() => decideAll('deny')}>Deny all</Button>
    </div>
  </div>
  <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
  <ul class="asks__track" aria-labelledby="{uid}-title" tabindex="0">
    {#each ids as id (id)}
      <li class="asks__card">
        <ParkedApproval {id} {run} {thread} {ondecided} />
      </li>
    {/each}
  </ul>
</section>
