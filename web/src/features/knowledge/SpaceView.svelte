<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError, type SpaceFile, type SpaceRecord, type SpaceTask } from '../../contract';
  import { formatDay, formatDayTime, plural } from '../../shared/assistant/format';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { p } from '../../shell/router';
  import Markdown from '../chats/Markdown.svelte';
  import AddToSpaceDialog from './AddToSpaceDialog.svelte';
  import {
    failureText,
    handleRemoveLink,
    handleUndoEffects,
    KINDS,
    refreshKnowledge,
    relationLabel,
    useSpace,
  } from './knowledge';
  import SpaceDialog from './SpaceDialog.svelte';

  let { id, phone }: { id: number; phone: boolean } = $props();

  const client = useQueryClient();
  const detail = useSpace(() => id);
  let renaming = $state(false);
  let adding = $state(false);
  let removing = $state<number>();

  const data = $derived(detail.data);
  const space = $derived(data?.space);
  const offline = $derived(detail.fetchStatus === 'paused' && !data);
  const missing = $derived(detail.error instanceof ApiError && detail.error.status === 404);

  function recordMeta(record: SpaceRecord): string {
    const parts = [KINDS[record.kind] ?? record.kind, relationLabel(record.relation)];
    if (record.state !== 'live') parts.push(record.state);
    parts.push(`added ${formatDay(record.created_at)}`);
    return parts.join(' · ');
  }

  function taskMeta(task: SpaceTask): string {
    const status = task.status === 'done' ? 'Done' : task.status === 'dismissed' ? 'Dismissed' : 'Open';
    return task.due ? `${status} · due ${formatDay(task.due)}` : status;
  }

  function fileMeta(file: SpaceFile): string {
    const size = file.bytes < 1024 ? `${file.bytes} bytes` : `${Math.round(file.bytes / 1024)} KB`;
    return `${size} · added ${formatDay(file.created_at)}`;
  }

  async function handleUndo(effects: readonly number[], title: string, name: string): Promise<void> {
    try {
      await handleUndoEffects(effects);
      toast(`Put ${title} back in ${name}.`);
    } catch (failure) {
      toast(failureText(failure, 'You are offline. Nothing was undone.'), { tone: 'error' });
    } finally {
      await refreshKnowledge(client);
    }
  }

  async function handleRemove(record: SpaceRecord): Promise<void> {
    if (!space) return;
    const name = space.name;
    const title = record.title || 'Untitled';
    removing = record.link_id;
    try {
      const change = await handleRemoveLink(record.link_id);
      const effects = change.effect_ids ?? [];
      toast(`Took ${title} out of ${name}.`, {
        action: effects.length > 0 ? { label: 'Undo', run: () => void handleUndo(effects, title, name) } : undefined,
      });
      await refreshKnowledge(client);
    } catch (failure) {
      if (failure instanceof ApiError && failure.status === 404) {
        toast(`${title} was already taken out. The space has been reloaded.`);
        await refreshKnowledge(client);
      } else {
        toast(failureText(failure, 'You are offline. Nothing changed.'), { tone: 'error' });
      }
    } finally {
      removing = undefined;
    }
  }

  function onRename(): void {
    renaming = true;
  }

  function onAdd(): void {
    adding = true;
  }

  function onRetry(): void {
    void detail.refetch();
  }
</script>

{#if !data}
  <h1 class="visually-hidden">Space</h1>
{/if}
{#if offline}
  <ViewState kind="offline" title="You are offline" level={2}>
    This space loads when the connection returns.
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else if detail.isPending}
  <div class="kpage-loading"><ViewState kind="loading" title="Loading the space" /></div>
{:else if missing}
  <ViewState kind="empty" title="This space does not exist" level={2}>
    It may have been removed. Pick another space.
  </ViewState>
{:else if detail.isError || !data || !space}
  <ViewState kind="error" title="This space could not be loaded" level={2}>
    {detail.error?.message}
    {#snippet action()}
      <Button onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{:else}
  <article class="kpage kpage--single">
    <div class="kpage__main">
      <header class="kpage__head">
        <p class="eyebrow">Space</p>
        <h1 class="page-title">{space.name}</h1>
        <div class="kpage__tags">
          {#if space.private}
            <Badge>Private</Badge>
          {/if}
          {#if space.archived}
            <Badge>Archived</Badge>
          {/if}
          <span class="kpage__note">
            {plural(data.counts.records, 'subject')}
            · {plural(data.counts.threads, 'chat')} · {plural(data.counts.tasks, 'task')} ·
            {plural(data.counts.files, 'file')}
          </span>
        </div>
        <div class="kpage__actions">
          <Button icon="plus" variant="primary" onclick={onAdd}>Add a subject</Button>
          <Button icon="tool" variant="quiet" onclick={onRename}>Rename</Button>
        </div>
      </header>

      <Section title="Instructions">
        {#if space.instructions.trim()}
          <div class="kpage__body prose"><Markdown text={space.instructions} /></div>
        {:else}
          <p class="kpage__blank">No instructions for this space. Chats here follow the standing instructions.</p>
        {/if}
      </Section>

      <Section title="Subjects" meta={data.records.length > 0 ? plural(data.records.length, 'subject') : undefined}>
        {#if data.records.length === 0}
          <p class="kpage__blank">No subjects in this space yet. Add one with Add a subject.</p>
        {:else}
          <List label="Subjects in this space">
            {#each data.records as record (record.link_id)}
              <ListRow
                title={record.title || 'Untitled'}
                meta={recordMeta(record)}
                href={p('/knowledge/:id', { params: { id: String(record.id) } })}
                pending={removing === record.link_id}
              >
                {#snippet end()}
                  {#if record.state === 'retracted'}
                    <Badge tone="error">Retracted</Badge>
                  {/if}
                  <Button
                    variant="quiet"
                    size="sm"
                    icon="close"
                    label="Take {record.title || 'Untitled'} out of {space?.name}"
                    pending={removing === record.link_id}
                    onclick={() => handleRemove(record)}
                  />
                {/snippet}
              </ListRow>
            {/each}
          </List>
        {/if}
      </Section>

      <Section title="Chats" meta={data.threads.length > 0 ? plural(data.threads.length, 'chat') : undefined}>
        {#if data.threads.length === 0}
          <p class="kpage__blank">No chats in this space yet.</p>
        {:else}
          <List label="Chats in this space">
            {#each data.threads as thread (thread.id)}
              <ListRow
                title={thread.title || 'Untitled chat'}
                meta="{thread.archived ? 'Archived · ' : ''}last message {formatDayTime(thread.updated_at)}"
                href={p('/chat/:id', { params: { id: String(thread.id) } })}
              />
            {/each}
          </List>
        {/if}
      </Section>

      <Section title="Tasks" meta={data.tasks.length > 0 ? plural(data.tasks.length, 'task') : undefined}>
        {#if data.tasks.length === 0}
          <p class="kpage__blank">No tasks in this space yet.</p>
        {:else}
          <List label="Tasks in this space">
            {#each data.tasks as task (task.id)}
              <ListRow
                title={task.title}
                meta={taskMeta(task)}
                href={p('/tasks/:id', { params: { id: String(task.id) } })}
              />
            {/each}
          </List>
        {/if}
      </Section>

      <Section title="Files" meta={data.files.length > 0 ? plural(data.files.length, 'file') : undefined}>
        {#if data.files.length === 0}
          <p class="kpage__blank">No files in this space yet.</p>
        {:else}
          <List label="Files in this space">
            {#each data.files as file (file.id)}
              <ListRow title={file.name} meta={fileMeta(file)} />
            {/each}
          </List>
        {/if}
      </Section>
    </div>
  </article>
  <SpaceDialog bind:open={renaming} {phone} space={{ id: space.id, name: space.name }} />
  <AddToSpaceDialog bind:open={adding} {phone} space={{ id: space.id, name: space.name }} />
{/if}
