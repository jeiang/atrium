<script lang="ts">
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useApps } from '../../shell/manifest';
  import { p } from '../../shell/router';
  import DecisionCard from './DecisionCard.svelte';
  import MailFrame from './MailFrame.svelte';
  import { useAccounts } from './mail';
  import { agreementWords, type DecisionFilter, mailEnabled, useDecisions } from './manage';

  const apps = useApps();
  const ready = $derived(mailEnabled(apps.data));
  let filter = $state<DecisionFilter>({ q: '', account: '', agreement: '' });
  let q = $state('');
  let account = $state('');
  let agreement = $state('');

  const accounts = useAccounts(() => ready);
  const decisions = useDecisions(
    () => filter,
    () => ready,
  );
  const items = $derived(decisions.data?.pages.flatMap((page) => page.items) ?? []);
  const total = $derived(decisions.data?.pages[0]?.total ?? 0);
  const filtered = $derived(filter.q !== '' || filter.account !== '' || filter.agreement !== '');
  const accountOptions = $derived([
    { value: '', label: 'All accounts' },
    ...(accounts.data ?? []).map((item) => ({ value: item.id, label: item.label || item.id })),
  ]);
  const agreementOptions = [
    { value: '', label: 'Any' },
    ...Object.entries(agreementWords).map(([value, label]) => ({ value, label })),
  ];

  function onSearch(event: SubmitEvent): void {
    event.preventDefault();
    filter = { q: q.trim(), account, agreement };
  }

  function onClear(): void {
    q = '';
    account = '';
    agreement = '';
    filter = { q: '', account: '', agreement: '' };
  }
</script>

<MailFrame title="Decisions" current={p('/apps/mail/decisions')}>
  <search>
    <form class="mail-filters" onsubmit={onSearch}>
      <TextField label="Search decisions" type="search" bind:value={q} enterkeyhint="search" />
      <SelectField label="Account" options={accountOptions} bind:value={account} />
      <SelectField label="Where it is now" options={agreementOptions} bind:value={agreement} />
      <span class="cluster mail-filters__go">
        <Button type="submit" variant="secondary" icon="search">Search</Button>
        {#if filtered}
          <Button variant="quiet" onclick={onClear}>Clear</Button>
        {/if}
      </span>
    </form>
  </search>
  {#if unreachable(decisions) || (!device.online && !decisions.data)}
    <ViewState kind="offline" title="Cannot reach atrium">
      Decisions load when atrium answers again. Nothing from the mailbox is kept on this device.
    </ViewState>
  {:else if decisions.isError && !decisions.data}
    <ViewState kind="error" title="Decisions did not load">
      {decisions.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={() => void decisions.refetch()}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !decisions.data}
    <ul class="mail-list mail-list--cards" aria-label="Loading decisions">
      {#each [0, 1, 2] as key (key)}
        <li class="mail-list__skeleton"><Skeleton lines={2} label="Loading decisions" /></li>
      {/each}
    </ul>
  {:else if items.length === 0}
    <ViewState kind="empty" title={filtered ? 'Nothing matches' : 'No decisions yet'}>
      {filtered ? 'No decision matches these filters.' : 'Each message atrium sorts shows here with the reason.'}
    </ViewState>
  {:else}
    <p class="meta">{plural(total, 'decision')}</p>
    <ul class="mail-list mail-list--cards" aria-label="Decisions">
      {#each items as decision (decision.decision_id)}
        <DecisionCard {decision} />
      {/each}
    </ul>
    {#if decisions.hasNextPage}
      <Button variant="secondary" pending={decisions.isFetchingNextPage} onclick={() => void decisions.fetchNextPage()}
        >Show more</Button
      >
    {/if}
  {/if}
</MailFrame>
