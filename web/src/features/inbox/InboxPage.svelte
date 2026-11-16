<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { tick } from 'svelte';
  import { on } from 'svelte/events';
  import { formatDayTime, plural } from '../../shared/assistant/format';
  import { parseInboxId } from '../../shared/assistant/inboxId';
  import Button from '../../shared/ui/Button.svelte';
  import DataTable, { type Column } from '../../shared/ui/DataTable.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { useShell } from '../../shell/frame';
  import { navigate, p, route } from '../../shell/router';
  import InboxEntry from './InboxEntry.svelte';
  import { type InboxRow, inboxKey, useQueue } from './inbox';
  import './inbox.css';

  const columns: readonly Column[] = [
    { key: 'what', label: 'What' },
    { key: 'kind', label: 'Kind' },
    { key: 'waiting', label: 'Waiting since' },
  ];

  const pending = useQueue();
  const shell = useShell();
  const client = useQueryClient();
  let reconciled = 0;
  let phone = $state(false);
  let chosen = $state<string>();
  let decidedHere = $state<string[]>([]);
  let scrolledTo = '';

  const address = $derived(route.params.item);
  const selected = $derived(address ? parseInboxId(address) : undefined);
  const rows = $derived(pending.data?.rows ?? []);
  const waiting = $derived(pending.data?.total ?? 0);
  const counts = $derived.by(() => {
    const c = pending.data?.counts;
    if (!c) return undefined;
    const parts = [
      [c.ask, 'ask'],
      [c.proposal, 'proposal'],
      [c.draft, 'draft'],
      [c.attention, 'reminder'],
    ] as const;
    const shown = parts.filter(([n]) => n > 0);
    return shown.length > 1 ? shown.map(([n, word]) => plural(n, word)).join(', ') : undefined;
  });
  const currentKey = $derived(selected ? inboxKey(selected) : (chosen ?? rows[0]?.key));
  const current = $derived(currentKey ? parseInboxId(currentKey) : undefined);
  const deck = $derived.by(() => {
    const next = rows.find((row) => !decidedHere.includes(row.key));
    const keys = next ? [...decidedHere, next.key] : decidedHere;
    return keys.flatMap((key) => {
      const target = parseInboxId(key);
      return target ? [{ key, target }] : [];
    });
  });
  const offline = $derived(pending.isPending && pending.fetchStatus === 'paused');
  const lede = $derived.by(() => {
    if (!pending.data) return undefined;
    if (waiting === 0) return 'Nothing waits on you.';
    const head = `${plural(waiting, 'item')} waiting on you`;
    return counts ? `${head}: ${counts}` : head;
  });

  $effect(() => {
    const last = deck.at(-1);
    if (!phone || decidedHere.length === 0 || !last || decidedHere.includes(last.key)) return;
    if (last.key === scrolledTo) return;
    scrolledTo = last.key;
    void tick().then(() =>
      document.querySelector(`[data-item="${last.key}"]`)?.scrollIntoView({
        block: 'start',
        behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'instant' : 'smooth',
      }),
    );
  });

  $effect(() => {
    const loaded = pending.dataUpdatedAt;
    const badge = shell.data?.needs_you;
    if (!pending.data || badge === undefined || badge === pending.data.total || reconciled === loaded) return;
    reconciled = loaded;
    void client.invalidateQueries({ queryKey: ['shell'] });
  });

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  function onOpen(row: InboxRow): void {
    chosen = row.key;
    void navigate('/inbox/:item', { params: { item: row.key } });
  }

  function onDecided(key: string): void {
    chosen = key;
    if (!decidedHere.includes(key)) decidedHere = [...decidedHere, key];
  }

  function onRetry(): void {
    void pending.refetch();
  }
</script>

{#snippet listState()}
  {#if offline}
    <ViewState kind="offline" title="You are offline">
      The Inbox loads when the connection is back. Nothing was decided.
    </ViewState>
  {:else if pending.isError && !pending.data}
    <ViewState kind="error" title="The Inbox did not load">
      {pending.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if pending.data && rows.length === 0}
    <ViewState kind="empty" title="All clear">Nothing waits on you. Finished work is in History.</ViewState>
  {/if}
{/snippet}

{#snippet cell(
    row: InboxRow,
    column: Column,
  )}
  {#if column.key === 'what'}
    <span class="inbox__what">{row.title}</span>
    {#if row.blocked}
      <StatusMark tone="warn">Blocked</StatusMark>
    {/if}
  {:else if column.key === 'kind'}
    <span class="meta">{row.label}</span>
  {:else}
    <span class="meta">{formatDayTime(row.created * 1000)}</span>
  {/if}
{/snippet}

<Page title="Inbox" {lede}>
  {#if address && !selected}
    <ViewState kind="empty" title="Nothing waits here">
      This address does not name an item atrium knows.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/inbox')}>Back to the Inbox</a>
      {/snippet}
    </ViewState>
  {:else if phone && selected}
    <div class="deck">
      <a class="link deck__back" href={p('/inbox')}>All decisions</a>
      <InboxEntry target={selected} ondecided={onDecided} />
    </div>
  {:else if phone}
    <div class="deck">
      {#if pending.isPending && !offline}
        <div class="card inbox-card__loading"><Skeleton lines={9} label="Loading the Inbox" /></div>
      {/if}
      {#each deck as card (card.key)}
        <InboxEntry target={card.target} ondecided={onDecided} />
      {/each}
      {@render listState()}
    </div>
  {:else}
    <div class="inbox">
      <div class="inbox__list">
        {#if offline || (pending.isError && !pending.data)}
          {@render listState()}
        {:else}
          <DataTable
            caption="Needs you"
            {columns}
            {rows}
            key={(row) => row.key}
            {cell}
            current={currentKey}
            loading={pending.isPending}
            empty="Nothing waits on you."
            onopen={onOpen}
          />
        {/if}
      </div>
      <div class="inbox__pane">
        {#if current}
          {#key currentKey}
            <InboxEntry target={current} ondecided={onDecided} />
          {/key}
        {:else if pending.isPending && !offline}
          <div class="card inbox-card__loading"><Skeleton lines={9} label="Loading the item" /></div>
        {:else if pending.data}
          {@render listState()}
        {/if}
      </div>
    </div>
  {/if}
</Page>
