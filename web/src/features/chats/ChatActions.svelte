<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import {
    ApiError,
    assistantThreadDelete,
    assistantThreadProject,
    knowledgeSpaceList,
    ok,
    type Thread,
  } from '../../contract';
  import { queryClient } from '../../shared/query';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Menu, { type MenuItem } from '../../shared/ui/Menu.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import RadioGroup from '../../shared/ui/RadioGroup.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { navigate } from '../../shell/router';
  import { chatTitle, failureText, handleFlag } from './manage';

  interface Props {
    thread: Thread;
    running: boolean;
    phone: boolean;
    onwork: () => void;
  }

  let { thread, running, phone, onwork }: Props = $props();

  const title = $derived(chatTitle(thread.title));
  let deleting = $state(false);
  let deletePending = $state(false);
  let deleteError = $state('');
  let moving = $state(false);
  let movePending = $state(false);
  let moveError = $state('');
  let choice = $state('0');

  const spaces = createQuery(() => ({
    queryKey: ['knowledge', 'spaces', 'move-targets'],
    queryFn: async ({ signal }) => ok(await knowledgeSpaceList({ limit: 100 }, { signal })),
    enabled: moving,
    meta: { topics: ['knowledge'] },
  }));

  const current = $derived(String(thread.project_id ?? 0));
  const targets = $derived([
    { value: '0', label: 'No space' },
    ...(spaces.data?.items ?? [])
      .filter((space) => !space.archived || String(space.id) === current)
      .map((space) => ({ value: String(space.id), label: space.name })),
  ]);

  const items = $derived<MenuItem[]>([
    { value: 'pin', label: thread.pinned ? 'Unpin' : 'Pin to the top' },
    { value: 'archive', label: thread.archived ? 'Restore from archive' : 'Archive' },
    { value: 'move', label: 'Move to a space' },
    { value: 'delete', label: 'Delete chat', danger: true },
  ]);

  function onSelect(value: string): void {
    if (value === 'pin') void handleFlag(thread.id, title, 'pinned', !thread.pinned);
    else if (value === 'archive') void handleFlag(thread.id, title, 'archived', !thread.archived);
    else if (value === 'move') {
      choice = current;
      moveError = '';
      moving = true;
    } else {
      deleteError = '';
      deleting = true;
    }
  }

  async function onDelete(): Promise<void> {
    deletePending = true;
    deleteError = '';
    try {
      ok(await assistantThreadDelete(thread.id));
    } catch (error) {
      deleteError =
        error instanceof ApiError && error.status === 409
          ? 'This chat has a run in progress. Stop the run, then delete the chat.'
          : failureText(error);
      deletePending = false;
      return;
    }
    deletePending = false;
    deleting = false;
    queryClient.removeQueries({ queryKey: ['thread', thread.id] });
    void queryClient.invalidateQueries({ queryKey: ['threads'] });
    toast(`Deleted “${title}”.`);
    await navigate('/chat', { replace: true });
  }

  async function onMove(): Promise<void> {
    movePending = true;
    moveError = '';
    const target = Number(choice);
    try {
      ok(await assistantThreadProject(thread.id, { project_id: target }));
    } catch (error) {
      moveError = failureText(error);
      movePending = false;
      return;
    }
    movePending = false;
    moving = false;
    void queryClient.invalidateQueries({ queryKey: ['thread', thread.id] });
    void queryClient.invalidateQueries({ queryKey: ['threads'] });
    const name = targets.find((entry) => entry.value === choice)?.label;
    toast(target === 0 ? `Took “${title}” out of its space.` : `Moved “${title}” to ${name}.`);
  }

  function onRetrySpaces(): void {
    void spaces.refetch();
  }
</script>

<div class="chat__tools">
  <Button variant="quiet" size="sm" onclick={onwork}>Work</Button>
  <Menu label="Chat actions" icon="more" iconOnly variant="quiet" size="sm" {items} onselect={onSelect} />
</div>

<Dialog bind:open={deleting} title="Delete this chat?" initialFocus="cancel" sheet={phone} pending={deletePending}>
  <p class="chat-dialog__text">
    “{title}” is deleted with its messages, its files and the work of its helpers. This cannot be undone.
  </p>
  {#if running}
    <Notice tone="warn" variant="inline">A run is in progress. atrium refuses to delete a running chat.</Notice>
  {/if}
  {#if deleteError}
    <Notice tone="error" variant="inline">{deleteError}</Notice>
  {/if}
  {#snippet actions()}
    <Button data-cancel disabled={deletePending} onclick={() => (deleting = false)}>Cancel</Button>
    <Button variant="danger" pending={deletePending} onclick={onDelete}>Delete chat</Button>
  {/snippet}
</Dialog>

<Dialog bind:open={moving} title="Move to a space" sheet={phone} pending={movePending}>
  {#if spaces.isPending}
    <div class="chat-dialog__loading"><ViewState kind="loading" title="Loading spaces" /></div>
  {:else if spaces.isError}
    <ViewState kind="error" title="Spaces could not be loaded">
      {failureText(spaces.error)}
      {#snippet action()}
        <Button onclick={onRetrySpaces}>Try again</Button>
      {/snippet}
    </ViewState>
  {:else}
    <RadioGroup legend="Space for “{title}”" options={targets} bind:value={choice} disabled={movePending} />
  {/if}
  {#if moveError}
    <Notice tone="error" variant="inline">{moveError}</Notice>
  {/if}
  {#snippet actions()}
    <Button disabled={movePending} onclick={() => (moving = false)}>Cancel</Button>
    <Button variant="primary" pending={movePending} disabled={!spaces.isSuccess || choice === current} onclick={onMove}>
      Move
    </Button>
  {/snippet}
</Dialog>
