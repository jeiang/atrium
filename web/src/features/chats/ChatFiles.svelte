<script lang="ts">
  import { on } from 'svelte/events';
  import type { FileSummary } from '../../contract';
  import { formatDay } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { previewHref, sizeText, useOwnerFiles } from '../files/files';

  let { thread }: { thread: number } = $props();

  let open = $state(false);
  let phone = $state(false);
  const owner = $derived({ kind: 'thread' as const, id: thread });
  const listed = useOwnerFiles(() => (open ? owner : undefined));
  const rows = $derived(listed.data?.pages.flatMap((page) => page.items) ?? []);
  const offline = $derived(unreachable(listed));

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  function meta(file: FileSummary): string {
    const origin = file.origin === 'generated' ? 'Generated' : 'Uploaded';
    return `${origin} · ${sizeText(file.bytes)} · ${formatDay(file.created_at)}`;
  }

  function onOpen(): void {
    open = true;
  }

  function onMore(): void {
    void listed.fetchNextPage();
  }

  function onRetry(): void {
    void listed.refetch();
  }
</script>

<Button class="chat__files" variant="quiet" size="sm" onclick={onOpen}>Files</Button>

{#snippet close()}
  <Button onclick={() => (open = false)}>Close</Button>
{/snippet}

<Dialog bind:open sheet={phone} title="Files in this chat" actions={phone ? undefined : close}>
  <div class="chat__filelist" aria-busy={listed.isFetching || undefined}>
    {#if offline}
      <ViewState kind="offline" title="Cannot reach atrium" level={3}>
        The files load when the connection returns.
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if listed.isPending}
      <ViewState kind="loading" title="Loading files" />
    {:else if listed.isError && rows.length === 0}
      <ViewState kind="error" title="Files could not be loaded" level={3}>
        {listed.error.message}
        {#snippet action()}
          <Button onclick={onRetry}>Try again</Button>
        {/snippet}
      </ViewState>
    {:else if rows.length === 0}
      <p class="chat__filenote">No files in this chat yet.</p>
    {:else}
      <List label="Files in this chat">
        {#each rows as file (file.id)}
          <ListRow title={file.name} meta={meta(file)} href={previewHref(owner, file.id)} />
        {/each}
      </List>
      {#if listed.hasNextPage}
        <Button pending={listed.isFetchingNextPage} onclick={onMore}>Show more</Button>
      {/if}
      {#if listed.isError}
        <p class="chat__filenote" role="status">More files could not be loaded. {listed.error.message}</p>
      {/if}
    {/if}
  </div>
</Dialog>
