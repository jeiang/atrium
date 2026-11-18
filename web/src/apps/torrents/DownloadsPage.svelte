<script lang="ts" module>
  const touchQuery = '(width < 861px), ((pointer: coarse) and (not (any-pointer: fine)))';
</script>

<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { on } from 'svelte/events';
  import type { MediaTorrent } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import HoldButton from '../../shared/assistant/HoldButton.svelte';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import AddTorrent from './AddTorrent.svelte';
  import FilesDialog from './FilesDialog.svelte';
  import TorrentCard from './TorrentCard.svelte';
  import {
    controlToast,
    handleControl,
    type Problem,
    phaseOf,
    problemOf,
    refreshTorrents,
    useTorrents,
  } from './torrents.svelte';
  import './torrents.css';

  const client = useQueryClient();
  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'torrents'));
  const list = useTorrents(() => app?.enabled === true);
  const offline = $derived(!device.online);
  const torrents = $derived(list.data?.items ?? []);

  let pending = $state<Record<string, boolean>>({});
  let problems = $state<Record<string, Problem>>({});
  let removing = $state<MediaTorrent>();
  let open = $state(false);
  let withFiles = $state(false);
  let sending = $state(false);
  let dialogProblem = $state<Problem>();
  let touch = $state(false);
  let filesOpen = $state(false);
  let filesHash = $state<string>();
  let filesFallback = $state<MediaTorrent>();
  const filesTorrent = $derived(torrents.find((item) => item.hash === filesHash) ?? filesFallback);

  const lede = $derived.by(() => {
    if (!list.data) return undefined;
    const counts = { active: 0, stopped: 0, seeding: 0 };
    for (const torrent of torrents) {
      const phase = phaseOf(torrent);
      if (phase === 'seeding' || phase === 'done') counts.seeding += 1;
      else if (phase === 'paused' || phase === 'errored') counts.stopped += 1;
      else counts.active += 1;
    }
    return `${plural(torrents.length, 'torrent')}: ${counts.active} active, ${counts.stopped} stopped, ${counts.seeding} done`;
  });

  $effect(() => {
    const query = matchMedia(touchQuery);
    touch = query.matches;
    return on(query, 'change', () => (touch = query.matches));
  });

  async function onControl(torrent: MediaTorrent, action: 'pause' | 'resume'): Promise<void> {
    pending[torrent.hash] = true;
    delete problems[torrent.hash];
    try {
      await handleControl(torrent.hash, action);
      controlToast(torrent, action, client);
    } catch (error) {
      problems[torrent.hash] = problemOf(error);
    } finally {
      delete pending[torrent.hash];
      refreshTorrents(client);
    }
  }

  function onRemove(torrent: MediaTorrent): void {
    removing = torrent;
    withFiles = false;
    dialogProblem = undefined;
    open = true;
  }

  async function onRemoveKeep(): Promise<void> {
    const torrent = removing;
    if (!torrent) return;
    sending = true;
    dialogProblem = undefined;
    try {
      await handleControl(torrent.hash, 'remove', false);
      open = false;
      toast(`Removed ${torrent.name}. Its files stay on disk.`);
    } catch (error) {
      dialogProblem = problemOf(error);
    } finally {
      sending = false;
      refreshTorrents(client);
    }
  }

  async function onRemoveFilesNow(): Promise<void> {
    const torrent = removing;
    if (!torrent) return;
    sending = true;
    dialogProblem = undefined;
    try {
      await handleControl(torrent.hash, 'remove', true);
      open = false;
      toast(`Removed ${torrent.name} and deleted its files.`);
    } catch (error) {
      dialogProblem = problemOf(error);
    } finally {
      sending = false;
      refreshTorrents(client);
    }
  }

  function onFiles(torrent: MediaTorrent): void {
    filesHash = torrent.hash;
    filesFallback = torrent;
    filesOpen = true;
  }

  async function onAdded(hash: string, shown: string): Promise<void> {
    await client.refetchQueries({ queryKey: ['torrents'], exact: true });
    const added = list.data?.items.find((item) => item.hash === hash);
    if (!added) {
      toast(`Added ${shown}. It shows here once qBittorrent lists it.`);
      return;
    }
    toast(`Added ${added.name}.`, { action: { label: 'Choose files', run: () => onFiles(added) } });
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

{#if appGone(apps, 'torrents')}
  <NotFound />
{:else}
  <Page title="Downloads" eyebrow="Torrents" {lede}>
    <a class="link dl-back" href={p('/apps')}>Apps</a>
    {#if !list.isError || list.data}
      <AddTorrent {offline} onadded={(hash, shown) => void onAdded(hash, shown)} />
    {/if}
    {#if unreachable(list) || (offline && !list.data)}
      <ViewState kind="offline" title="Cannot reach atrium">
        Downloads load when atrium answers again. Nothing is sent while offline.
      </ViewState>
    {:else if list.isError && !list.data}
      <ViewState kind="error" title="Downloads did not load">
        {list.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={onRetry}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !list.data}
      <ul class="dl-list" aria-label="Loading downloads">
        {#each [0, 1, 2] as key (key)}
          <li class="dl-list__skeleton"><Skeleton lines={3} label="Loading downloads" /></li>
        {/each}
      </ul>
    {:else}
      {#if list.data.unavailable}
        <Notice tone="warn" title="qBittorrent is not answering">
          atrium cannot read the download client right now. The list shows what it could read.
        </Notice>
      {/if}
      {#if offline}
        <Notice tone="warn" title="Cannot reach atrium"
          >This list is from before the connection dropped. Nothing can be changed until atrium answers.</Notice
        >
      {/if}
      {#if torrents.length === 0}
        <ViewState kind="empty" title="No downloads"
          >Torrents you add here, from Library or through the assistant show up in this list.</ViewState
        >
      {:else}
        <ul class="dl-list" aria-label="Downloads">
          {#each torrents as torrent (torrent.hash)}
            <li data-torrent={torrent.hash}>
              <TorrentCard
                {torrent}
                {offline}
                pending={pending[torrent.hash]}
                problem={problems[torrent.hash]}
                oncontrol={(action) => void onControl(torrent, action)}
                onremove={() => onRemove(torrent)}
                onfiles={() => onFiles(torrent)}
              />
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </Page>
{/if}

<Dialog
  bind:open
  title={removing ? `Remove ${removing.name}?` : 'Remove'}
  sheet={touch}
  pending={sending}
  initialFocus="cancel"
>
  <Checkbox label="Also delete the downloaded files" bind:checked={withFiles} disabled={sending} />
  {#if withFiles}
    <p>The torrent leaves qBittorrent and its files are deleted. This cannot be undone.</p>
  {:else}
    <p>The torrent leaves qBittorrent. Its files stay on disk.</p>
  {/if}
  {#if dialogProblem}
    <Notice tone={dialogProblem.tone} variant="inline" title={dialogProblem.title}>{dialogProblem.text}</Notice>
  {/if}
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={sending} onclick={() => (open = false)}>Cancel</Button>
    {#if withFiles}
      <HoldButton
        label="Remove and delete files"
        presentation={touch ? 'hold' : 'click'}
        gated={false}
        disabled={sending || offline}
        onconfirm={() => void onRemoveFilesNow()}
        onreveal={() => undefined}
        onsend={() => void onRemoveFilesNow()}
      />
    {:else}
      <Button
        variant="danger"
        pending={sending}
        disabled={offline}
        reason={offline ? 'Cannot reach atrium' : undefined}
        onclick={() => void onRemoveKeep()}
        >Remove</Button
      >
    {/if}
  {/snippet}
</Dialog>

<FilesDialog bind:open={filesOpen} torrent={filesTorrent} sheet={touch} {offline} />
