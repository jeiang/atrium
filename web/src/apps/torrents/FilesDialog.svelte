<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { MediaTorrent } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    addedByAtrium,
    bytes,
    handleSelect,
    type Problem,
    percent,
    problemOf,
    refreshTorrents,
    useFiles,
    wanted,
  } from './torrents.svelte';

  interface Props {
    open?: boolean;
    torrent?: MediaTorrent;
    sheet?: boolean;
    offline?: boolean;
  }

  let { open = $bindable(false), torrent, sheet = false, offline = false }: Props = $props();

  const client = useQueryClient();
  const files = useFiles(() => (open && torrent ? torrent.hash : undefined));
  const ours = $derived(torrent ? addedByAtrium(torrent) : false);
  const items = $derived(files.data ?? []);

  let choices = $state<Record<number, boolean>>({});
  let sending = $state(false);
  let problem = $state<Problem>();
  let shownFor: string | undefined;

  $effect.pre(() => {
    const hash = open ? torrent?.hash : undefined;
    if (hash === shownFor) return;
    shownFor = hash;
    choices = {};
    problem = undefined;
  });

  const changed = $derived(items.filter((file) => file.index in choices && choices[file.index] !== wanted(file)));
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  function chosen(index: number, fallback: boolean): boolean {
    return choices[index] ?? fallback;
  }

  function onChoose(index: number, value: boolean): void {
    choices[index] = value;
  }

  function onAll(value: boolean): void {
    for (const file of items) choices[file.index] = value;
  }

  async function onApply(): Promise<void> {
    const target = torrent;
    if (!target || sending) return;
    const plan = changed.map((file) => ({ index: file.index, want: choices[file.index] === true }));
    sending = true;
    problem = undefined;
    let saved = 0;
    try {
      for (const step of plan) {
        await handleSelect(target.hash, step.index, step.want);
        saved += 1;
      }
      open = false;
      toast(`Saved file choices for ${target.name}.`);
    } catch (error) {
      const base = problemOf(error);
      problem =
        saved === 0
          ? base
          : {
              ...base,
              text: `${base.text} ${saved} of ${plural(plan.length, 'change')} were saved first. atrium did not send the rest.`,
            };
      choices = {};
    } finally {
      sending = false;
      refreshTorrents(client);
    }
  }

  function onRetry(): void {
    void files.refetch();
  }
</script>

<Dialog
  bind:open
  title={torrent ? `Files in ${torrent.name}` : 'Files'}
  {sheet}
  pending={sending}
  initialFocus="cancel"
>
  <div class="dl-files">
    {#if unreachable(files) || (offline && !files.data)}
      <ViewState kind="offline" title="Cannot reach atrium">The file list loads when atrium answers again.</ViewState>
    {:else if files.isError && !files.data}
      <ViewState kind="error" title="The file list did not load">
        {files.error?.message}
        {#snippet action()}
          <Button onclick={onRetry}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !files.data}
      <Skeleton lines={4} label="Loading files" />
    {:else if items.length === 0}
      <ViewState kind="empty" title="No file list yet">
        qBittorrent lists the files once it has the torrent details. This can take a minute for a magnet link.
        {#snippet action()}
          <Button onclick={onRetry} pending={files.isFetching}>Check again</Button>
        {/snippet}
      </ViewState>
    {:else}
      {#if !ours}
        <Notice tone="info" variant="inline" title="Read only">
          atrium did not add this torrent, so it shows the files but cannot change them.
        </Notice>
      {/if}
      <fieldset class="dl-files__set" disabled={!ours || sending}>
        <legend class="dl-files__legend">Download these files</legend>
        {#if ours && items.length > 1}
          <div class="dl-files__bulk">
            <Button variant="quiet" size="sm" onclick={() => onAll(true)}>Select all</Button>
            <Button variant="quiet" size="sm" onclick={() => onAll(false)}>Select none</Button>
          </div>
        {/if}
        <ul class="dl-files__list">
          {#each items as file (file.index)}
            <li data-file={file.index}>
              <Checkbox
                label={file.name}
                help="{bytes(file.size)}, {percent(file.progress)} downloaded"
                checked={chosen(file.index, wanted(file))}
                onchange={(event) => onChoose(file.index, event.currentTarget.checked)}
              />
            </li>
          {/each}
        </ul>
      </fieldset>
    {/if}
    {#if problem}
      <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
    {/if}
  </div>
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={sending} onclick={() => (open = false)}
      >{ours && files.data && items.length > 0 ? 'Cancel' : 'Close'}</Button
    >
    {#if ours && items.length > 0}
      <Button
        variant="primary"
        pending={sending}
        disabled={offline || changed.length === 0}
        {reason}
        onclick={() => void onApply()}
        >Apply</Button
      >
    {/if}
  {/snippet}
</Dialog>
