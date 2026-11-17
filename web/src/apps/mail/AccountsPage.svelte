<script lang="ts">
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import AccountRow from './AccountRow.svelte';
  import MailFrame from './MailFrame.svelte';
  import { useAccounts } from './mail';
  import { mailEnabled } from './manage';

  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  const accounts = useAccounts(() => ready);
</script>

<MailFrame title="Accounts" current={p('/apps/mail/accounts')}>
  <p class="mail-lede">Each mailbox atrium reads. Open one to change how it signs in or what atrium may do there.</p>
  {#if unreachable(accounts) || (!device.online && !accounts.data)}
    <ViewState kind="offline" title="Cannot reach atrium">Accounts load when atrium answers again.</ViewState>
  {:else if accounts.isError && !accounts.data}
    <ViewState kind="error" title="Accounts did not load">
      {accounts.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void accounts.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !accounts.data}
    <ul class="mail-list" aria-label="Loading accounts">
      {#each [0, 1] as key (key)}
        <li class="mail-list__skeleton"><Skeleton lines={2} label="Loading accounts" /></li>
      {/each}
    </ul>
  {:else if accounts.data.length === 0}
    <ViewState kind="empty" title="No mail accounts">
      Add a mail account under Connections.
      {#snippet action()}
        <a class="link" href={p('/settings/connections')}>Open Connections</a>
      {/snippet}
    </ViewState>
  {:else}
    <ul class="mail-list" aria-label="Mail accounts">
      {#each accounts.data as account (account.id)}
        <AccountRow {account} />
      {/each}
    </ul>
  {/if}
</MailFrame>
