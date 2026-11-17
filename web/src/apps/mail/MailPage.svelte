<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import SelectField from '../../shared/ui/SelectField.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, repairHref, stateOf, unmetNeeds, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import Compose, { type Start } from './Compose.svelte';
  import MailNav from './MailNav.svelte';
  import MessageCard from './MessageCard.svelte';
  import { type Filter, refreshMail, useAccounts, useMessages } from './mail';
  import './mail.css';

  const client = useQueryClient();
  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'mail'));
  const ready = $derived(app?.enabled !== false);
  const needs = $derived(app ? unmetNeeds(app) : []);

  let filter = $state<Filter>({ q: '', account: '', folder: '' });
  let q = $state('');
  let account = $state('');
  let folder = $state('');
  let view = $state('cards');
  const shown = $derived(view === 'list' ? 'list' : 'cards');
  let composing = $state(false);
  let start = $state<Start>({ kind: 'new' });

  const accounts = useAccounts(() => ready);
  const messages = useMessages(
    () => filter,
    () => ready,
  );
  const items = $derived(messages.data?.pages.flatMap((page) => page.items) ?? []);
  const accountOptions = $derived([
    { value: '', label: 'All accounts' },
    ...(accounts.data ?? []).map((item) => ({ value: item.id, label: item.label || item.id })),
  ]);
  const filtered = $derived(filter.q !== '' || filter.account !== '' || filter.folder !== '');

  function onSearch(event: SubmitEvent): void {
    event.preventDefault();
    filter = { q: q.trim(), account, folder: folder.trim() };
  }

  function onClear(): void {
    q = '';
    account = '';
    folder = '';
    filter = { q: '', account: '', folder: '' };
  }

  function onNew(): void {
    if (start.kind !== 'new') start = { kind: 'new' };
    composing = true;
  }
</script>

{#if appGone(apps, 'mail')}
  <NotFound />
{:else}
  <Page title="Mail" eyebrow="Apps">
    {#snippet actions()}
      <Button
        variant="primary"
        icon="plus"
        disabled={!device.online || !ready}
        reason={device.online ? undefined : 'Cannot reach atrium.'}
        onclick={onNew}
        >New email</Button
      >
    {/snippet}
    <a class="link mail-back" href={p('/apps')}>Apps</a>
    <MailNav {app} current={p('/apps/mail')} />
    {#if app && !app.enabled}
      <ViewState kind="blocked" title="Mail is off">
        Turn Mail on in the apps directory, then messages show here.
        {#snippet action()}
          <a class="link" href={p('/apps/directory')}>Open the apps directory</a>
        {/snippet}
      </ViewState>
    {:else}
      {#if app && app.state !== 'enabled'}
        <Notice tone={stateOf(app).tone === 'error' ? 'error' : 'warn'} title={stateOf(app).label}>
          {app.reason ?? 'Some messages may be missing or stale.'}
          {#snippet action()}
            {#if needs[0]}
              <a class="btn btn--secondary" href={repairHref(needs[0])}>Repair</a>
            {/if}
          {/snippet}
        </Notice>
      {/if}
      <search>
        <form class="mail-filters" onsubmit={onSearch}>
          <TextField label="Search mail" type="search" bind:value={q} enterkeyhint="search" />
          <SelectField label="Account" options={accountOptions} bind:value={account} />
          <TextField label="Folder" bind:value={folder} placeholder="Any folder" enterkeyhint="search" />
          <span class="cluster mail-filters__go">
            <Button type="submit" variant="secondary" icon="search">Search</Button>
            {#if filtered}
              <Button variant="quiet" onclick={onClear}>Clear</Button>
            {/if}
          </span>
        </form>
      </search>
      {#if unreachable(messages) || (!device.online && !messages.data)}
        <ViewState kind="offline" title="Cannot reach atrium">
          Messages load when atrium answers again. Nothing from the mailbox is kept on this device.
        </ViewState>
      {:else if messages.isError && !messages.data}
        <ViewState kind="error" title="Messages did not load">
          {messages.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void messages.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !messages.data}
        <ul class="mail-list mail-list--cards" aria-label="Loading messages">
          {#each [0, 1, 2] as key (key)}
            <li class="mail-list__skeleton"><Skeleton lines={2} label="Loading messages" /></li>
          {/each}
        </ul>
      {:else if items.length === 0}
        <ViewState kind="empty" title={filtered ? 'Nothing matches' : 'No messages yet'}>
          {filtered ? 'No message matches these filters.' : 'Messages show here once atrium has read the mailbox.'}
        </ViewState>
      {:else}
        <div class="mail-bar">
          <p class="meta">{plural(items.length, 'message')}{messages.hasNextPage ? ' so far' : ''}</p>
          <Segmented
            label="Show messages as"
            options={[
    { value: 'cards', label: 'Cards' },
    { value: 'list', label: 'List' },
  ]}
            bind:value={view}
          />
        </div>
        <ul class="mail-list mail-list--{shown}" aria-label="Messages">
          {#each items as hit (hit.message_id)}
            <MessageCard {hit} view={shown} />
          {/each}
        </ul>
        {#if messages.hasNextPage}
          <Button
            variant="secondary"
            pending={messages.isFetchingNextPage}
            onclick={() => void messages.fetchNextPage()}
            >Show more</Button
          >
        {/if}
      {/if}
    {/if}
  </Page>
  <Compose bind:open={composing} {start} accounts={accounts.data ?? []} onchanged={() => refreshMail(client)} />
{/if}
