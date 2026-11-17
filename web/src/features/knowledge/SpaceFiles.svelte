<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError, type FileSummary } from '../../contract';
  import { formatDay, plural } from '../../shared/assistant/format';
  import { refusedOffline } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { previewHref, sizeText, useOwnerFiles } from '../files/files';
  import { handleDeleteFile, handleUploadFiles, refreshKnowledge } from './knowledge';

  let { space, phone }: { space: { id: number; name: string; archived: boolean }; phone: boolean } = $props();

  const client = useQueryClient();
  const owner = $derived({ kind: 'space' as const, id: space.id });
  const listed = useOwnerFiles(() => owner);
  const rows = $derived(listed.data?.pages.flatMap((page) => page.items) ?? []);

  let picker = $state<HTMLInputElement>();
  let staged = $state<File[]>([]);
  let uploading = $state(false);
  let uploadFailed = $state<string>();
  let doomed = $state<FileSummary>();
  let confirming = $state(false);
  let deleting = $state(false);
  let deleteFailed = $state<string>();

  const stagedBytes = $derived(staged.reduce((sum, file) => sum + file.size, 0));

  function fileMeta(file: FileSummary): string {
    return `${sizeText(file.bytes)} · added ${formatDay(file.created_at)}`;
  }

  function failure(error: unknown, nothing: string, unknown: string): string {
    if (refusedOffline(error) && error instanceof Error) return error.message;
    if (error instanceof ApiError && error.status === 403) return 'You can read this but not change it.';
    if (error instanceof ApiError && error.status > 0) return `${error.message} ${nothing}`;
    return unknown;
  }

  function onPick(): void {
    picker?.click();
  }

  function onPicked(event: Event & { currentTarget: HTMLInputElement }): void {
    const picked = [...(event.currentTarget.files ?? [])];
    event.currentTarget.value = '';
    if (picked.length === 0) return;
    uploadFailed = undefined;
    staged = [...staged, ...picked];
  }

  function onUnstage(index: number): void {
    staged = staged.filter((_, at) => at !== index);
  }

  function onClear(): void {
    staged = [];
    uploadFailed = undefined;
  }

  async function onUpload(): Promise<void> {
    if (uploading || staged.length === 0) return;
    uploading = true;
    uploadFailed = undefined;
    try {
      const added = await handleUploadFiles(space.id, staged);
      staged = [];
      toast(`Added ${plural(added, 'file')} to ${space.name}.`);
      await refreshKnowledge(client);
    } catch (error) {
      uploadFailed = failure(
        error,
        'Nothing was uploaded.',
        'The connection failed before atrium answered, so the files may or may not have been added. atrium did not try again. Check the list before uploading again.',
      );
      if (!(error instanceof ApiError && error.status > 0)) await refreshKnowledge(client);
    } finally {
      uploading = false;
    }
  }

  function onAskDelete(file: FileSummary): void {
    doomed = file;
    deleteFailed = undefined;
    confirming = true;
  }

  async function onDelete(): Promise<void> {
    if (deleting || !doomed) return;
    const file = doomed;
    deleting = true;
    deleteFailed = undefined;
    try {
      await handleDeleteFile(space.id, file.id);
      confirming = false;
      toast(`Deleted ${file.name}.`);
      await refreshKnowledge(client);
    } catch (error) {
      if (error instanceof ApiError && error.status === 404) {
        confirming = false;
        toast(`${file.name} was already deleted. The list has been reloaded.`);
        await refreshKnowledge(client);
      } else {
        deleteFailed = failure(
          error,
          'Nothing was deleted.',
          'The connection failed before atrium answered. Reload the list to see whether it was deleted. atrium did not try again.',
        );
      }
    } finally {
      deleting = false;
    }
  }

  function onMore(): void {
    void listed.fetchNextPage();
  }

  function onRetry(): void {
    void listed.refetch();
  }
</script>

<Section title="Files" meta={rows.length > 0 ? plural(rows.length, 'file') : undefined}>
  {#snippet actions()}
    {#if !space.archived}
      <Button size="sm" icon="plus" onclick={onPick}>Add files</Button>
    {/if}
  {/snippet}
  {#if !space.archived}
    <input
      bind:this={picker}
      class="visually-hidden"
      type="file"
      multiple
      tabindex="-1"
      aria-hidden="true"
      onchange={onPicked}
    >
  {/if}
  {#if staged.length > 0}
    <div class="kfiles__staged">
      <p class="kfiles__label">Ready to upload: {plural(staged.length, 'file')}, {sizeText(stagedBytes)}</p>
      <List label="Files ready to upload">
        {#each staged as file, index (index)}
          <ListRow title={file.name} meta={sizeText(file.size)}>
            {#snippet end()}
              <Button
                variant="quiet"
                size="sm"
                icon="close"
                label="Take {file.name} off the upload"
                disabled={uploading}
                onclick={() => onUnstage(index)}
              />
            {/snippet}
          </ListRow>
        {/each}
      </List>
      {#if uploadFailed}
        <Notice tone="error" variant="inline">{uploadFailed}</Notice>
      {/if}
      <div class="kfiles__actions">
        <Button variant="primary" pending={uploading} onclick={onUpload}>
          Upload {plural(staged.length, 'file')}
        </Button>
        <Button variant="quiet" disabled={uploading} onclick={onClear}>Clear</Button>
      </div>
    </div>
  {/if}
  {#if listed.isPending}
    <ViewState kind="loading" title="Loading files" />
  {:else if listed.isError && rows.length === 0}
    <ViewState kind="error" title="Files could not be loaded" level={3}>
      {listed.error.message}
      {#snippet action()}
        <Button onclick={onRetry}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else if rows.length === 0}
    <p class="kpage__blank">No files in this space yet.</p>
  {:else}
    <List label="Files in this space">
      {#each rows as file (file.id)}
        <ListRow
          title={file.name}
          meta={fileMeta(file)}
          href={previewHref(owner, file.id)}
          pending={deleting && doomed?.id === file.id}
        >
          {#snippet end()}
            {#if !space.archived}
              <Button
                variant="quiet"
                size="sm"
                icon="close"
                label="Delete {file.name}"
                onclick={() => onAskDelete(file)}
              />
            {/if}
          {/snippet}
        </ListRow>
      {/each}
    </List>
    {#if listed.hasNextPage}
      <Button class="subjects__more" pending={listed.isFetchingNextPage} onclick={onMore}>Show more</Button>
    {/if}
    {#if listed.isError}
      <p class="subjects__note" role="status">More files could not be loaded. {listed.error.message}</p>
    {/if}
  {/if}
</Section>

<Dialog
  bind:open={confirming}
  sheet={phone}
  title="Delete {doomed?.name ?? 'this file'}?"
  pending={deleting}
  initialFocus="cancel"
>
  <p>This deletes the file from {space.name}. It cannot be undone.</p>
  {#if deleteFailed}
    <Notice tone="error" variant="inline">{deleteFailed}</Notice>
  {/if}
  {#snippet actions()}
    <Button data-cancel disabled={deleting} onclick={() => (confirming = false)}>Cancel</Button>
    <Button variant="danger" pending={deleting} onclick={onDelete}>Delete file</Button>
  {/snippet}
</Dialog>
