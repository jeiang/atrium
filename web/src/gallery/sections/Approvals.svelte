<script lang="ts">
  import ApprovalCard from '../../shared/assistant/ApprovalCard.svelte';
  import type { Approval, Decision } from '../../shared/assistant/types';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import {
    blockedApproval,
    draftApproval,
    elsewhereApproval,
    moveApproval,
    triageApproval,
    unknownApproval,
  } from '../assistant-fixtures';

  let log = $state<string[]>([]);
  let sheet = $state(false);

  function settle(entry: string): Promise<void> {
    log.push(entry);
    return new Promise((resolve) => window.setTimeout(resolve, 300));
  }

  function handlers(item: Approval) {
    return {
      onapprove: () => settle(`approve ${item.id}`),
      ondecline: () => settle(`decline ${item.id}`),
      onlater: () => settle(`later ${item.id}`),
      onundo: (decision: Decision) => settle(`undo ${decision} ${item.id}`),
    };
  }

  function onRepair(): void {
    log.push(`repair ${blockedApproval.id}`);
    toast('Calendar reconnected');
  }

  function onEdit(): void {
    log.push(`edit ${draftApproval.id}`);
  }
</script>

<Section id="g-approvals" data-gallery="approvals" title="Approval cards" meta="Decisions in fixed order">
  <div class="stack">
    <ApprovalCard approval={draftApproval} {...handlers(draftApproval)} onedit={onEdit} />
    <ApprovalCard approval={moveApproval} {...handlers(moveApproval)} />
    <ApprovalCard approval={triageApproval} {...handlers(triageApproval)} />
    <ApprovalCard approval={unknownApproval} {...handlers(unknownApproval)} />
    <ApprovalCard
      approval={elsewhereApproval}
      {...handlers(elsewhereApproval)}
      decided={{ where: 'iPhone', at: '2026-11-19T09:02:00+01:00' }}
    />
    <ApprovalCard
      approval={blockedApproval}
      {...handlers(blockedApproval)}
      blocked={{ reason: 'The Home calendar is disconnected, so the reminder cannot be saved.', repair: 'Reconnect calendar' }}
      onrepair={onRepair}
    />
    <div class="cluster">
      <Button onclick={() => (sheet = true)}>Open approval in a sheet</Button>
    </div>
    <p class="meta" data-testid="approval-log" aria-live="polite">{log.length ? log.join(', ') : 'No decisions yet'}</p>
  </div>
</Section>

<Dialog bind:open={sheet} title="Decide" sheet>
  <ApprovalCard approval={{ ...moveApproval, id: 'a-sheet' }} {...handlers({ ...moveApproval, id: 'a-sheet' })} />
</Dialog>
