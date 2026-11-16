<script lang="ts">
  import { on } from 'svelte/events';
  import type { Space } from '../../contract';
  import { formatDay, plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p, route } from '../../shell/router';
  import { useSpaces } from './knowledge';
  import SpaceDialog from './SpaceDialog.svelte';
  import SpaceView from './SpaceView.svelte';
  import './knowledge.css';

  const LEDE = 'Each space keeps its subjects, chats, tasks and files together.';

  let phone = $state(false);
  let creating = $state(false);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  const raw = $derived(route.params.space);
  const id = $derived(raw !== undefined && /^[1-9]\d*$/.test(raw) ? Number(raw) : undefined);
  const invalid = $derived(raw !== undefined && id === undefined);
  const spaces = useSpaces();
  const rows = $derived(spaces.data?.pages.flatMap((page) => page.items) ?? []);
  const offline = $derived(unreachable(spaces));

  function meta(space: Space): string {
    const parts = [plural(space.thread_count, 'chat')];
    if (space.last_activity) parts.push(`active ${formatDay(space.last_activity)}`);
    else parts.push(`made ${formatDay(space.created_at)}`);
    return parts.join(' · ');
  }

  function onCreate(): void {
    creating = true;
  }

  function onMore(): void {
    void spaces.fetchNextPage();
  }

  function onRetry(): void {
    void spaces.refetch();
  }
</script>

{#snippet region(
    level: 2 | 3,
  )}
  <div class="subjects__region" aria-busy={spaces.isFetching || undefined}>
    {#if offline}
      <ViewState kind="offline" title="Cannot reach atrium" {level}>
        Spaces load again when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if spaces.isPending}
      <ViewState kind="loading" title="Loading spaces" />
    {:else if spaces.isError && rows.length === 0}
      <ViewState kind="error" title="Spaces could not be loaded" {level}>
        {spaces.error.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if rows.length === 0}
      <ViewState kind="empty" title="No spaces yet" {level}>
        A space gathers the subjects, chats and files of one project. Make one with New space.
      </ViewState>
    {:else}
      <List label="Spaces">
        {#each rows as space (space.id)}
          <ListRow
            title={space.name}
            meta={meta(space)}
            href={p('/knowledge/spaces/:space', { params: { space: String(space.id) } })}
            selected={space.id === id}
          >
            {#snippet end()}
              {#if space.private}
                <Badge>Private</Badge>
              {/if}
            {/snippet}
          </ListRow>
        {/each}
      </List>
      {#if spaces.hasNextPage}
        <Button class="subjects__more" pending={spaces.isFetchingNextPage} onclick={onMore}>Show more</Button>
      {/if}
      {#if spaces.isError}
        <p class="subjects__note" role="status">More spaces could not be loaded. {spaces.error.message}</p>
      {/if}
    {/if}
  </div>
{/snippet}

{#snippet pane()}
  {#if invalid || id === undefined}
    <h1 class="visually-hidden">Spaces</h1>
    <ViewState kind="empty" title="This space does not exist" level={2}>
      The address does not name a space. Pick one from the list.
    </ViewState>
  {:else}
    <SpaceView {id} {phone} />
  {/if}
{/snippet}

{#if raw === undefined}
  <Page title="Spaces" eyebrow="Knowledge" lede={LEDE}>
    {#snippet actions()}
      <Button icon="plus" onclick={onCreate}>New space</Button>
    {/snippet}
    {@render region(2)}
  </Page>
{:else if phone}
  <div class="knowledge knowledge--phone">
    <a class="knowledge__all" href={p('/knowledge/spaces')}><Icon name="knowledge" />All spaces</a>
    {@render pane()}
  </div>
{:else}
  <div class="knowledge">
    <nav class="knowledge__nav" aria-label="Spaces">
      <div class="subjects">
        <div class="kspaces__head">
          <h2 class="subjects__title">Spaces</h2>
          <Button size="sm" icon="plus" onclick={onCreate}>New space</Button>
        </div>
        {@render region(3)}
      </div>
    </nav>
    <div class="knowledge__pane">
      {@render pane()}
    </div>
  </div>
{/if}

<SpaceDialog bind:open={creating} {phone} />
