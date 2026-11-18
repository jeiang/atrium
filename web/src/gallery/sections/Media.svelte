<script lang="ts">
  import '../../apps/library/library.css';
  import TorrentCard from '../../apps/torrents/TorrentCard.svelte';
  import '../../apps/torrents/torrents.css';
  import type { MediaTorrent } from '../../contract';
  import Button from '../../shared/ui/Button.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const torrents: MediaTorrent[] = [
    {
      hash: 'g1',
      name: 'Harbour.Lights.S02E07.1080p.WEB',
      state: 'downloading',
      tags: 'atrium',
      progress: 0.42,
      size: 2_400_000_000,
    },
    {
      hash: 'g2',
      name: 'The.Quiet.Orchard.2024.1080p',
      state: 'pausedDL',
      tags: 'atrium',
      progress: 0.18,
      size: 8_100_000_000,
    },
    {
      hash: 'g3',
      name: 'Lantern.Street.S01E03.720p',
      state: 'uploading',
      tags: 'media',
      progress: 1,
      size: 950_000_000,
    },
    {
      hash: 'g4',
      name: 'Paper.Boats.1998.Remastered',
      state: 'error',
      tags: 'atrium',
      progress: 0.63,
      size: 4_300_000_000,
    },
    { hash: 'g5', name: 'Garden.Club.Lectures.Vol2', state: 'stalledUP', tags: '', progress: 1, size: null },
  ];
  const [downloading, paused, seeding, errored, untagged] = torrents as [
    MediaTorrent,
    MediaTorrent,
    MediaTorrent,
    MediaTorrent,
    MediaTorrent,
  ];
  const problem = {
    tone: 'error' as const,
    title: 'atrium refused this',
    text: 'qBittorrent refused the change. Nothing was changed.',
  };

  function onNothing(): void {}
</script>

<Section title="Downloads and Library" data-gallery="media">
  <ul class="dl-list" aria-label="Torrent states">
    <li><TorrentCard torrent={downloading} oncontrol={onNothing} onremove={onNothing} /></li>
    <li><TorrentCard torrent={paused} oncontrol={onNothing} onremove={onNothing} /></li>
    <li><TorrentCard torrent={seeding} oncontrol={onNothing} onremove={onNothing} /></li>
    <li><TorrentCard torrent={errored} {problem} oncontrol={onNothing} onremove={onNothing} /></li>
    <li><TorrentCard torrent={untagged} offline oncontrol={onNothing} onremove={onNothing} /></li>
  </ul>
  <ViewState kind="empty" title="Not following anything" level={3}>
    Follow a show and atrium downloads its new episodes.
  </ViewState>
  <ViewState kind="blocked" title="Library is unavailable" level={3}>
    Torrents is off (needs torrents). Turn it on in the apps directory, then Library opens here.
    {#snippet action()}
      <a class="link lib-link" href="#media">Open the apps directory</a>
    {/snippet}
  </ViewState>
  <Skeleton lines={3} label="Loading downloads" />
  <ViewState kind="error" title="Downloads did not load" level={3}>
    The download list could not be read.
    {#snippet action()}
      <Button variant="secondary" onclick={onNothing}>Retry</Button>
    {/snippet}
  </ViewState>
  <ViewState kind="offline" title="Cannot reach atrium" level={3}>
    Downloads load when atrium answers again. Nothing is sent while offline.
  </ViewState>
</Section>
