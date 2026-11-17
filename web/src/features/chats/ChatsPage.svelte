<script lang="ts">
  import { createInfiniteQuery } from '@tanstack/svelte-query';
  import { assistantThreadList, type ListedThread, ok } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import { device } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Menu, { type MenuItem } from '../../shared/ui/Menu.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Segmented from '../../shared/ui/Segmented.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import { chatTitle, handleFlag } from './manage';
  import './chats.css';

  const PAGE = 30;
  const views = [
    { value: 'chats', label: 'Chats' },
    { value: 'archived', label: 'Archived' },
  ] as const;

  let view = $state<string>('chats');
  const archived = $derived(view === 'archived');

  const threads = createInfiniteQuery(() => ({
    queryKey: archived ? ['threads', 'archived'] : ['threads'],
    queryFn: async ({ pageParam, signal }) =>
      ok(
        await assistantThreadList(
          { limit: PAGE, cursor: pageParam || undefined, view: archived ? 'archived' : undefined },
          { signal },
        ),
      ),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    meta: { topics: ['assistant'] },
  }));

  const rows = $derived(threads.data?.pages.flatMap((page) => page.items) ?? []);

  function runMark(thread: ListedThread): { label: string; tone: 'accent' | 'warn' } | undefined {
    if (thread.run_state === 'running' || thread.run_state === 'interrupted')
      return { label: 'Running', tone: 'accent' };
    if (thread.run_state === 'parked') return { label: 'Waiting for you', tone: 'warn' };
    return undefined;
  }

  function meta(thread: ListedThread): string {
    return [thread.pinned ? 'Pinned' : '', thread.project_name ?? '', formatDayTime(thread.updated_at)]
      .filter(Boolean)
      .join(' · ');
  }

  function rowActions(thread: ListedThread): MenuItem[] {
    return [
      { value: 'pin', label: thread.pinned ? 'Unpin' : 'Pin to the top' },
      { value: 'archive', label: thread.archived ? 'Restore from archive' : 'Archive' },
    ];
  }

  function onAction(thread: ListedThread, value: string): void {
    const title = chatTitle(thread.title);
    if (value === 'pin') void handleFlag(thread.id, title, 'pinned', !thread.pinned);
    else void handleFlag(thread.id, title, 'archived', !thread.archived);
  }

  function onMore(): void {
    void threads.fetchNextPage();
  }

  function onRetry(): void {
    void threads.refetch();
  }
</script>

<Page title="Chats">
  {#snippet actions()}
    <a class="btn btn--primary" href={p('/chat/new')}><Icon name="plus" />New chat</a>
  {/snippet}
  <div class="chats-views">
    <Segmented label="Which chats" options={views} bind:value={view} />
  </div>
  {#if !device.online && rows.length === 0}
    <ViewState kind="offline" title="Cannot reach atrium" level={2}>
      Chats load again when the connection returns.
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if threads.isPending}
    <div class="chats-skeleton"><ViewState kind="loading" title="Loading chats" /></div>
  {:else if threads.isError && rows.length === 0}
    <ViewState kind="error" title="Chats could not be loaded" level={2}>
      {threads.error.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if rows.length === 0 && archived}
    <ViewState kind="empty" title="No archived chats" level={2}>Chats you archive appear here.</ViewState>
  {:else if rows.length === 0}
    <ViewState kind="empty" title="No chats yet" level={2}>
      Conversations with atrium appear here.
      {#snippet action()}
        <a class="btn btn--primary" href={p('/chat/new')}>Start a chat</a>
      {/snippet}
    </ViewState>
  {:else}
    <List label={archived ? 'Archived chats' : 'Chats'} class="chats-list">
      {#each rows as thread (thread.id)}
        {const mark = $derived(runMark(thread))}
        <ListRow
          title={chatTitle(thread.title)}
          meta={meta(thread)}
          href={p('/chat/:id', { params: { id: String(thread.id) } })}
        >
          {#snippet end()}
            {#if mark}
              <StatusMark tone={mark.tone}>{mark.label}</StatusMark>
            {/if}
            <Menu
              label="Actions for {chatTitle(thread.title)}"
              icon="more"
              iconOnly
              variant="quiet"
              size="sm"
              items={rowActions(thread)}
              onselect={(value) => onAction(thread, value)}
            />
          {/snippet}
        </ListRow>
      {/each}
    </List>
    {#if threads.hasNextPage}
      <div class="chats-more">
        <Button pending={threads.isFetchingNextPage} onclick={onMore}>Show more chats</Button>
      </div>
    {/if}
  {/if}
</Page>
