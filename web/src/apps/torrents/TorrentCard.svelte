<script lang="ts">
  import type { MediaTorrent } from '../../contract';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import StatusMark from '../../shared/ui/StatusMark.svelte';
  import {
    addedByAtrium,
    bytes,
    type HeldRemoval,
    type Problem,
    percent,
    phaseOf,
    phaseWords,
    stopped,
  } from './torrents.svelte';

  interface Props {
    torrent: MediaTorrent;
    offline?: boolean;
    pending?: boolean;
    hold?: HeldRemoval;
    problem?: Problem;
    oncontrol?: (action: 'pause' | 'resume') => void;
    onremove?: () => void;
    onundo?: () => void;
    onfiles?: () => void;
  }

  let {
    torrent,
    offline = false,
    pending = false,
    hold,
    problem,
    oncontrol,
    onremove,
    onundo,
    onfiles,
  }: Props = $props();

  const phase = $derived(phaseOf(torrent));
  const words = $derived(phaseWords[phase]);
  const ours = $derived(addedByAtrium(torrent));
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);
</script>

<Card title={torrent.name} level={2} {pending}>
  {#snippet meta()}
    <p class="dl-card__meta">
      <StatusMark tone={words.tone}>{words.label}</StatusMark>
      <span class="meta">{bytes(torrent.size)}</span>
      <Badge tone={ours ? 'accent' : 'neutral'}>{ours ? 'Added by atrium' : 'Not added by atrium'}</Badge>
    </p>
  {/snippet}
  <div class="dl-card__progress">
    <progress class="dl-card__bar" max="1" value={torrent.progress} aria-label="{torrent.name} progress"></progress>
    <span class="meta dl-card__percent">{percent(torrent.progress)}</span>
  </div>
  {#if hold}
    <Notice tone="warn" variant="inline" title="Removing with its files">
      This torrent and its files are deleted when the 10 seconds end.
      {#snippet action()}
        <Button variant="secondary" icon="undo" onclick={onundo}>Undo</Button>
      {/snippet}
    </Notice>
  {/if}
  {#if problem}
    <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
  {/if}
  {#snippet actions()}
    {#if oncontrol && onremove && !hold}
      {#if stopped(phase)}
        <Button
          variant="secondary"
          {pending}
          disabled={offline}
          {reason}
          onclick={() => oncontrol('resume')}
          label="Resume {torrent.name}"
          >Resume</Button
        >
      {:else}
        <Button
          variant="secondary"
          {pending}
          disabled={offline}
          {reason}
          onclick={() => oncontrol('pause')}
          label="Pause {torrent.name}"
          >Pause</Button
        >
      {/if}
      <Button variant="quiet" disabled={pending} onclick={onfiles} label="Files in {torrent.name}">Files</Button>
      <Button variant="quiet" disabled={offline || pending} {reason} onclick={onremove} label="Remove {torrent.name}"
        >Remove</Button
      >
    {/if}
  {/snippet}
</Card>
