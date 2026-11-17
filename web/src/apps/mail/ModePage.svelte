<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import CommandDialog from './CommandDialog.svelte';
  import MailFrame from './MailFrame.svelte';
  import {
    type Command,
    commandWords,
    mailEnabled,
    type ReplayRun,
    refreshRules,
    useChecks,
    useStatus,
  } from './manage';
  import StatusView from './StatusView.svelte';

  const client = useQueryClient();
  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  const status = useStatus(() => ready);
  const checks = useChecks(() => ready && status.data?.watch_closely.active === true);

  let command = $state<Command>('go_live');
  let asking = $state(false);
  let blocked = $state<Command>();
  let checking = $state(false);
  let lastRun = $state<ReplayRun>();

  function onAsk(next: Command): void {
    if (!device.online || blocked) return;
    command = next;
    asking = true;
  }

  function onDone(done: Command, run?: ReplayRun): void {
    if (run) lastRun = run;
    refreshRules(client);
    toast(commandWords[done].done);
  }

  async function onCheck(): Promise<void> {
    if (checking) return;
    checking = true;
    try {
      const result = await status.refetch();
      if (!result.isError) blocked = undefined;
    } finally {
      checking = false;
    }
  }
</script>

<MailFrame title="Mode" current={p('/apps/mail/mode')}>
  {#if unreachable(status) || (!device.online && !status.data)}
    <ViewState kind="offline" title="Cannot reach atrium">
      The mail mode loads when atrium answers again. Nothing from it is kept on this device.
    </ViewState>
  {:else if status.isError && !status.data}
    <ViewState kind="error" title="The mail status did not load">
      {status.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void status.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !status.data}
    <Skeleton lines={4} label="Loading the mail status" />
  {:else}
    <StatusView
      status={status.data}
      checks={checks.data}
      replay={lastRun}
      {blocked}
      {checking}
      onask={onAsk}
      oncheck={() => void onCheck()}
    />
  {/if}
  {#snippet after()}
    <CommandDialog {command} bind:open={asking} ondone={onDone} onunknown={(stuck) => (blocked = stuck)} />
  {/snippet}
</MailFrame>
