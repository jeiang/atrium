<script lang="ts">
  import { createInfiniteQuery } from '@tanstack/svelte-query';
  import { assistantThreadList, type ListedThread, ok } from '../../contract';
  import { formatDayTime } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import './chats.css';

  const PAGE = 30;

  const threads = createInfiniteQuery(() => ({
    queryKey: ['threads'],
    queryFn: async ({ pageParam, signal }) =>
      ok(await assistantThreadList({ limit: PAGE, cursor: pageParam || undefined }, { signal })),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    meta: { topics: ['assistant'] },
  }));

  const rows = $derived(threads.data?.pages.flatMap((page) => page.items) ?? []);

  function state(thread: ListedThread): { label: string; tone: 'accent' | 'warn' } | undefined {
    if (thread.run_state === 'running' || thread.run_state === 'interrupted')
      return { label: 'Running', tone: 'accent' };
    if (thread.run_state === 'parked') return { label: 'Waiting for you', tone: 'warn' };
    return undefined;
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
  {#if threads.fetchStatus === 'paused' && rows.length === 0}
    <ViewState kind="offline" title="You are offline" level={2}>
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
  {:else if rows.length === 0}
    <ViewState kind="empty" title="No chats yet" level={2}>
      Conversations with atrium appear here.
      {#snippet action()}
        <a class="btn btn--primary" href={p('/chat/new')}>Start a chat</a>
      {/snippet}
    </ViewState>
  {:else}
    <List label="Chats" class="chats-list">
      {#each rows as thread (thread.id)}
        {const mark = $derived(state(thread))}
        <ListRow
          title={thread.title || 'Untitled chat'}
          meta={formatDayTime(thread.updated_at)}
          href={p('/chat/:id', { params: { id: String(thread.id) } })}
        >
          {#snippet end()}
            {#if mark}
              <StatusMark tone={mark.tone}>{mark.label}</StatusMark>
            {/if}
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
