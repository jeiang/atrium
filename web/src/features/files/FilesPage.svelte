<script lang="ts">
  import type { LibraryEntry } from '../../contract';
  import { formatDay } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { libraryOwner, previewHref, sizeText, useLibrary } from './files';
  import './files.css';

  const library = useLibrary();
  const rows = $derived(library.data?.pages.flatMap((page) => page.items) ?? []);
  const offline = $derived(unreachable(library));

  function meta(entry: LibraryEntry): string {
    const parts = [sizeText(entry.bytes)];
    if (entry.thread_id !== undefined) parts.push(`chat: ${entry.thread_title || 'Untitled chat'}`);
    else if (entry.project_id !== undefined) parts.push(`space: ${entry.project_name || 'Untitled space'}`);
    parts.push(`added ${formatDay(entry.created_at)}`);
    return parts.join(' · ');
  }

  function href(entry: LibraryEntry): string | undefined {
    const owner = libraryOwner(entry);
    return owner ? previewHref(owner, entry.id) : undefined;
  }

  function onMore(): void {
    void library.fetchNextPage();
  }

  function onRetry(): void {
    void library.refetch();
  }
</script>

<Page title="Files" eyebrow="Knowledge" lede="Every file in your chats and spaces, newest first.">
  <div class="flist" aria-busy={library.isFetching || undefined}>
    {#if offline}
      <ViewState kind="offline" title="Cannot reach atrium" level={2}>
        Files load again when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if library.isPending}
      <ViewState kind="loading" title="Loading files" />
    {:else if library.isError && rows.length === 0}
      <ViewState kind="error" title="Files could not be loaded" level={2}>
        {library.error.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if rows.length === 0}
      <ViewState kind="empty" title="No files yet" level={2}>
        Files you add to a space, or share in a chat, appear here.
      </ViewState>
    {:else}
      <List label="Files">
        {#each rows as entry (entry.id)}
          <ListRow title={entry.name} meta={meta(entry)} href={href(entry)} />
        {/each}
      </List>
      {#if library.hasNextPage}
        <Button class="flist__more" pending={library.isFetchingNextPage} onclick={onMore}>Show more</Button>
      {/if}
      {#if library.isError}
        <p class="fpage__note" role="status">More files could not be loaded. {library.error.message}</p>
      {/if}
    {/if}
  </div>
</Page>
