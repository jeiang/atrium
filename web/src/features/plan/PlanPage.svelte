<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { on } from 'svelte/events';
  import type { CommitmentItem } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { isActive, p, route } from '../../shell/router';
  import CommitmentDetail from './CommitmentDetail.svelte';
  import PlanNav from './PlanNav.svelte';
  import {
    bucketOf,
    bucketWords,
    doneToast,
    dueText,
    grouped,
    handleCommitmentVerb,
    type Problem,
    problemOf,
    refreshPlan,
    type Side,
    type Status,
    statuses,
    statusWords,
    useCommitment,
    useCommitments,
    type Verb,
    verbWords,
  } from './plan';
  import './plan.css';

  const client = useQueryClient();
  let phone = $state(false);
  let status = $state<Status>('open');
  let typed = $state('');
  let q = $state('');
  let busy = $state<string>();
  let problem = $state<Problem>();

  const ref = $derived.by(() => {
    const raw = route.params.ref;
    if (!raw) return undefined;
    try {
      return decodeURIComponent(raw);
    } catch {
      return raw;
    }
  });
  const detail = useCommitment(() => ref);
  const side = $derived<Side>(
    ref ? ((detail.data?.side as Side | undefined) ?? 'todo') : isActive('/plan/waiting') ? 'waiting' : 'todo',
  );
  const list = useCommitments(() => ({ side, status, q }));
  const items = $derived(list.data?.pages.flatMap((page) => page.items) ?? []);
  const counts = $derived(list.data?.pages[0]?.counts);
  const sideCounts = $derived(counts?.[side]);
  const overdue = $derived(status === 'open' ? items.filter((item) => bucketOf(item.due) === 'overdue').length : 0);
  const offline = $derived(unreachable(list));
  const statusOptions = $derived(
    statuses.map((value) => ({
      value,
      label: sideCounts ? `${statusWords[value]} ${sideCounts[value]}` : statusWords[value],
    })),
  );
  const lede = $derived.by(() => {
    if (!sideCounts) return undefined;
    const open = sideCounts.open;
    if (side === 'waiting') return open === 0 ? 'Nobody owes you anything.' : `${plural(open, 'thing')} owed to you`;
    if (open === 0) return 'Nothing to do.';
    return overdue > 0 ? `${plural(open, 'thing')} you owe, ${overdue} overdue` : `${plural(open, 'thing')} you owe`;
  });
  const groups = $derived(status === 'open' ? grouped(items) : [{ bucket: undefined, items }]);
  const emptyTitle = $derived(
    q
      ? 'Nothing matches'
      : side === 'waiting'
        ? 'Nobody owes you anything'
        : `Nothing ${statusWords[status].toLowerCase()}`,
  );

  $effect(() => {
    const value = typed.trim();
    const handle = setTimeout(() => (q = value), 250);
    return () => clearTimeout(handle);
  });

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  function meta(item: CommitmentItem): string {
    return [
      item.source === 'task' ? 'Task' : 'From mail',
      item.side === 'waiting' && item.counterpart ? `${item.counterpart} owes you` : undefined,
      dueText(item.due),
    ]
      .filter(Boolean)
      .join(' · ');
  }

  async function onVerb(item: CommitmentItem, verb: Verb): Promise<void> {
    if (busy) return;
    problem = undefined;
    busy = item.ref;
    try {
      const effects = await handleCommitmentVerb(item.ref, verb);
      doneToast(`${verbWords[verb].done}: ${item.title}`, effects, client);
    } catch (error) {
      problem = problemOf(error);
    } finally {
      busy = undefined;
      refreshPlan(client);
    }
  }

  function onMore(): void {
    void list.fetchNextPage();
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

{#snippet rowEnd(
    item: CommitmentItem,
  )}
  {#if item.actions.includes('done')}
    <Button
      size="sm"
      variant="quiet"
      icon="check"
      pending={busy === item.ref}
      label="Done: {item.title}"
      onclick={() => onVerb(item, 'done')}
    >
      Done
    </Button>
  {:else if item.actions.includes('reopen')}
    <Button
      size="sm"
      variant="quiet"
      icon="undo"
      pending={busy === item.ref}
      label="Reopen: {item.title}"
      onclick={() => onVerb(item, 'reopen')}
    >
      Reopen
    </Button>
  {/if}
{/snippet}

{#snippet listBody()}
  {#if offline}
    <ViewState kind="offline" title="Cannot reach atrium">Your plan loads when the connection is back.</ViewState>
  {:else if list.isError && !list.data}
    <ViewState kind="error" title="Your plan did not load">
      {list.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if list.isPending}
    <div class="plan__loading"><Skeleton lines={8} label="Loading your plan" /></div>
  {:else if items.length === 0}
    <ViewState kind="empty" title={emptyTitle}>
      {#if side === 'todo' && !q}
        Tasks you add and promises found in mail show up here.
      {:else if !q}
        Things other people promised you in mail show up here.
      {/if}
    </ViewState>
  {:else}
    {#each groups as group (group.bucket ?? 'all')}
      <Section title={group.bucket ? bucketWords[group.bucket] : statusWords[status]} meta={String(group.items.length)}>
        <List>
          {#each group.items as item (item.ref)}
            <ListRow
              title={item.title}
              meta={meta(item)}
              href={p('/plan/item/:ref', { params: { ref: item.ref } })}
              selected={item.ref === ref}
              pending={busy === item.ref}
            >
              {#snippet end()}
                {@render rowEnd(item)}
              {/snippet}
            </ListRow>
          {/each}
        </List>
      </Section>
    {/each}
    {#if list.hasNextPage}
      <Button variant="secondary" pending={list.isFetchingNextPage} onclick={onMore}>Load more</Button>
    {/if}
  {/if}
{/snippet}

{#snippet detailBody()}
  {#if detail.data}
    <CommitmentDetail item={detail.data} busy={busy === detail.data.ref} onverb={onVerb} />
  {:else if detail.isPending}
    <div class="card plan__loading"><Skeleton lines={8} label="Loading the item" /></div>
  {:else if detail.isError}
    <ViewState kind="error" title="This item did not load">{detail.error.message}</ViewState>
  {:else}
    <ViewState kind="empty" title="Nothing here">
      It was deleted, or the address is wrong.
      {#snippet action()}
        <a class="btn btn--secondary" href={p('/plan')}>Back to the plan</a>
      {/snippet}
    </ViewState>
  {/if}
{/snippet}

<Page title="Plan" {lede}>
  <PlanNav current={side} todo={counts?.todo.open} waiting={counts?.waiting.open} />
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#if phone && ref}
    <div class="plan__pane">
      <a class="link plan__back" href={side === 'waiting' ? p('/plan/waiting') : p('/plan')}>
        {side === 'waiting' ? 'All waiting' : 'All to do'}
      </a>
      {@render detailBody()}
    </div>
  {:else}
    <div class="plan">
      <div class="plan__list">
        <div class="plan__filters">
          <Segmented label="Status" options={statusOptions} bind:value={status} />
          <TextField label="Search" type="search" bind:value={typed} autocomplete="off" />
        </div>
        {@render listBody()}
      </div>
      {#if !phone}
        <div class="plan__pane">
          {#if ref}
            {@render detailBody()}
          {:else}
            <ViewState kind="empty" title="Pick something">Its details and links show here.</ViewState>
          {/if}
        </div>
      {/if}
    </div>
  {/if}
</Page>
