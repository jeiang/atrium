<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { MediaChannel } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import TextField from '../../shared/ui/TextField.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    followToast,
    handleFollow,
    handleUnfollow,
    type Problem,
    problemOf,
    refreshLibrary,
    unfollowToast,
    useFollowing,
  } from './library';

  let { ready }: { ready: boolean } = $props();

  const client = useQueryClient();
  const following = useFollowing(() => ready);
  const offline = $derived(!device.online);
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  let name = $state('');
  let imdb = $state('');
  let pattern = $state('');
  let adding = $state(false);
  let addProblem = $state<Problem>();
  let pending = $state<Record<number, boolean>>({});
  let problems = $state<Record<number, Problem>>({});

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
      });
      name = '';
      imdb = '';
      pattern = '';
      followToast(channel, client);
    } catch (error) {
      addProblem = problemOf(error);
    } finally {
      adding = false;
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
    <TextField label="Show name" bind:value={name} required autocomplete="off" />
    <TextField label="IMDb id" bind:value={imdb} help="Optional, like tt0000000" autocomplete="off" />
    <TextField
      label="Release name pattern"
      bind:value={pattern}
      help="Optional. atrium matches release names against it."
      autocomplete="off"
    />
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
