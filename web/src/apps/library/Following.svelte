<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { ApiError, type MediaChannel } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Checkbox from '../../shared/ui/Checkbox.svelte';
  import Combobox from '../../shared/ui/Combobox.svelte';
  import type { ListOption } from '../../shared/ui/Listbox.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    findShows,
    followToast,
    handleFollow,
    handlePoll,
    handleUnfollow,
    IMDB,
    lookupShow,
    type PreviewInput,
    type Problem,
    pollProblem,
    problemOf,
    refreshLibrary,
    unfollowToast,
    useFollowing,
    useReleasePreview,
  } from './library';

  let { ready }: { ready: boolean } = $props();

  const client = useQueryClient();
  const following = useFollowing(() => ready);
  const offline = $derived(!device.online);
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  let show = $state<ListOption>();
  let searchProblem = $state<Problem>();
  let looking = $state(false);
  let lookupNote = $state<string>();
  let name = $state('');
  let imdb = $state('');
  let pattern = $state('');
  let caseSensitive = $state(false);
  let adding = $state(false);
  let addProblem = $state<Problem>();
  let polling = $state(false);
  let pollIssue = $state<Problem>();
  let pending = $state<Record<number, boolean>>({});
  let problems = $state<Record<number, Problem>>({});
  let previewInput = $state<PreviewInput>();

  const preview = useReleasePreview(() => previewInput);
  const previewProblem = $derived(preview.isError ? previewText(preview.error) : undefined);

  $effect(() => {
    const value = imdb.trim();
    const next: PreviewInput | undefined = IMDB.test(value)
      ? { imdb: value, pattern: pattern.trim(), caseSensitive }
      : undefined;
    const timer = window.setTimeout(() => (previewInput = next), 300);
    return () => window.clearTimeout(timer);
  });

  function previewText(error: Error): Problem {
    if (unreachable(preview))
      return { tone: 'warn', title: 'Cannot reach atrium', text: 'The preview loads when atrium answers again.' };
    if (error instanceof ApiError && error.status === 422)
      return { tone: 'warn', title: 'atrium cannot preview this', text: error.message };
    return { tone: 'error', title: 'The preview did not load', text: error.message };
  }

  async function searchShows(q: string, signal: AbortSignal): Promise<ListOption[]> {
    try {
      const shows = await findShows(q, signal);
      searchProblem = undefined;
      return shows.map((found) => ({
        value: String(found.id),
        label: found.name,
        ...(found.year ? { detail: found.year } : {}),
      }));
    } catch (error) {
      if (!signal.aborted) {
        searchProblem =
          error instanceof ApiError && error.status === 503
            ? { tone: 'warn', title: 'Show search is unavailable', text: error.message }
            : problemOf(error);
      }
      throw error;
    }
  }

  async function onPick(option: ListOption | undefined): Promise<void> {
    show = option;
    lookupNote = undefined;
    if (!option) return;
    if (name.trim() === '') name = option.label;
    looking = true;
    try {
      const found = await lookupShow(Number(option.value));
      if (show !== option) return;
      if (!found) lookupNote = `TMDB has no IMDb id for ${option.label}.`;
      else if (imdb.trim() === '') imdb = found;
      else if (imdb.trim() !== found) lookupNote = `TMDB lists ${found}. The IMDb id you typed stays.`;
    } catch (error) {
      if (show === option) lookupNote = `The IMDb lookup failed. ${problemOf(error).text}`;
    } finally {
      looking = false;
    }
  }

  async function onFollow(event: SubmitEvent): Promise<void> {
    event.preventDefault();
    if (name.trim() === '' || adding) return;
    adding = true;
    addProblem = undefined;
    try {
      const channel = await handleFollow({
        name: name.trim(),
        ...(imdb.trim() ? { imdb: imdb.trim() } : {}),
        ...(pattern.trim() ? { pattern: pattern.trim() } : {}),
        ...(caseSensitive ? { case_sensitive: true } : {}),
        ...(show ? { tmdb_id: Number(show.value) } : {}),
      });
      show = undefined;
      lookupNote = undefined;
      name = '';
      imdb = '';
      pattern = '';
      caseSensitive = false;
      followToast(channel, client);
    } catch (error) {
      addProblem = problemOf(error);
    } finally {
      adding = false;
      refreshLibrary(client);
    }
  }

  async function onPoll(): Promise<void> {
    if (polling) return;
    polling = true;
    pollIssue = undefined;
    try {
      const result = await handlePoll();
      const added = result.added > 0 ? `${plural(result.added, 'new episode')} sent to Downloads.` : 'Nothing new.';
      const errors = result.errors > 0 ? ` ${plural(result.errors, 'show')} could not be checked.` : '';
      toast(`Checked followed shows. ${added}${errors}`);
    } catch (error) {
      pollIssue = pollProblem(error);
    } finally {
      polling = false;
      refreshLibrary(client);
    }
  }

  async function onUnfollow(channel: MediaChannel): Promise<void> {
    pending[channel.id] = true;
    delete problems[channel.id];
    try {
      await handleUnfollow(channel);
      unfollowToast(channel, client);
    } catch (error) {
      problems[channel.id] = problemOf(error);
    } finally {
      delete pending[channel.id];
      refreshLibrary(client);
    }
  }

  function onRetry(): void {
    void following.refetch();
  }
</script>

<Section title="Follow a show">
  <form class="lib-follow" onsubmit={(event) => void onFollow(event)}>
    <Combobox
      label="Find on TMDB"
      placeholder="Type a show name"
      search={searchShows}
      selected={show}
      onchange={(option) => void onPick(option)}
    />
    {#if searchProblem}
      <Notice tone={searchProblem.tone} variant="inline" title={searchProblem.title}>{searchProblem.text}</Notice>
    {/if}
    <TextField label="Show name" bind:value={name} required autocomplete="off" />
    <TextField label="IMDb id" bind:value={imdb} help="Optional, like tt0000000" autocomplete="off" />
    {#if looking}
      <p class="meta" role="status">Looking up the IMDb id</p>
    {:else if lookupNote}
      <p class="meta" role="status">{lookupNote}</p>
    {/if}
    <TextField
      label="Release name pattern"
      bind:value={pattern}
      help="Optional. atrium matches release names against it."
      autocomplete="off"
    />
    <Checkbox label="Match case" bind:checked={caseSensitive} />
    {#if previewInput}
      <section class="lib-releases" aria-label="Recent releases" aria-busy={preview.isFetching || undefined}>
        {#if previewProblem}
          <Notice tone={previewProblem.tone} variant="inline" title={previewProblem.title}
            >{previewProblem.text}</Notice
          >
        {:else if !preview.data}
          <Skeleton lines={3} label="Loading recent releases" />
        {:else if preview.data.items.length === 0}
          <p class="meta" role="status">No recent releases for this show.</p>
        {:else}
          <p class="lib-releases__summary" role="status">
            {previewInput.pattern
    ? `${preview.data.matched} of ${plural(preview.data.items.length, 'recent release')} match`
    : plural(preview.data.items.length, 'recent release')}
          </p>
          <ul class="lib-releases__list">
            {#each preview.data.items as release, index (`${index}:${release.title}`)}
              <li class="lib-releases__row" data-match={previewInput.pattern ? release.match : undefined}>
                <span class="lib-releases__title">{release.title}</span>
                {#if previewInput.pattern && release.match}
                  <Badge tone="ok">Matches</Badge>
                {/if}
              </li>
            {/each}
          </ul>
        {/if}
      </section>
    {/if}
    <div class="lib-follow__actions">
      <Button type="submit" variant="primary" pending={adding} disabled={offline || name.trim() === ''} {reason}
        >Follow</Button
      >
    </div>
    {#if addProblem}
      <Notice tone={addProblem.tone} variant="inline" title={addProblem.title}>{addProblem.text}</Notice>
    {/if}
  </form>
</Section>

<Section title="Following" meta={following.data ? plural(following.data.length, 'show') : undefined}>
  {#snippet actions()}
    {#if following.data && following.data.length > 0}
      <Button size="sm" icon="search" pending={polling} disabled={offline} {reason} onclick={() => void onPoll()}
        >Check now</Button
      >
    {/if}
  {/snippet}
  {#if pollIssue}
    <Notice tone={pollIssue.tone} variant="inline" title={pollIssue.title}>{pollIssue.text}</Notice>
  {/if}
  {#if unreachable(following) || (offline && !following.data)}
    <ViewState kind="offline" title="Cannot reach atrium">The list loads when atrium answers again.</ViewState>
  {:else if following.isError && !following.data}
    <ViewState kind="error" title="Following did not load">
      {following.error.message}
      {#snippet action()}
        <Button variant="secondary" onclick={onRetry}>Retry</Button>
      {/snippet}
    </ViewState>
  {:else if !following.data}
    <Skeleton lines={4} label="Loading followed shows" />
  {:else if following.data.length === 0}
    <ViewState kind="empty" title="Not following anything"
      >Follow a show and atrium downloads its new episodes.</ViewState
    >
  {:else}
    <ul class="list lib-follows" aria-label="Followed shows">
      {#each following.data as channel (channel.id)}
        {const problem = $derived(problems[channel.id])}
        <li class="lib-follows__row" aria-busy={pending[channel.id] || undefined}>
          <div class="lib-follows__text">
            <span class="lib-follows__name">{channel.name}</span>
            <span class="meta">
              {[channel.imdb, channel.pattern && `Pattern ${channel.pattern}`, channel.primed ? 'Watching' : 'Catching up']
    .filter(Boolean)
    .join(' · ')}
            </span>
            {#if problem}
              <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
            {/if}
          </div>
          <Button
            variant="quiet"
            pending={pending[channel.id]}
            disabled={offline}
            {reason}
            onclick={() => void onUnfollow(channel)}
            label="Unfollow {channel.name}"
            >Unfollow</Button
          >
        </li>
      {/each}
    </ul>
  {/if}
</Section>
