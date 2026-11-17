<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import type { MediaFilingPreview, MediaQueueItem } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Card from '../../shared/ui/Card.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import {
    episodeText,
    handleFile,
    handleIgnore,
    type Problem,
    problemOf,
    refreshLibrary,
    usePreview,
    useQueue,
  } from './library';

  let { ready }: { ready: boolean } = $props();

  const client = useQueryClient();
  const queue = useQueue(() => ready);
  const offline = $derived(!device.online);
  const reason = $derived(offline ? 'Cannot reach atrium' : undefined);

  let opened = $state<string>();
  const preview = usePreview(() => opened);
  let pending = $state<Record<string, boolean>>({});
  let problems = $state<Record<string, Problem>>({});

  function onPreview(item: MediaQueueItem): void {
    opened = opened === item.id ? undefined : item.id;
  }

  async function onFile(item: MediaQueueItem, plan: MediaFilingPreview): Promise<void> {
    pending[item.id] = true;
    delete problems[item.id];
    try {
      const placed = await handleFile(plan);
      opened = undefined;
      toast(`Filed ${plan.title}: ${plural(placed, 'file')} placed.`);
    } catch (error) {
      problems[item.id] = problemOf(error);
    } finally {
      delete pending[item.id];
      refreshLibrary(client);
    }
  }

  async function onIgnore(item: MediaQueueItem): Promise<void> {
    pending[item.id] = true;
    delete problems[item.id];
    try {
      await handleIgnore(item.id);
      if (opened === item.id) opened = undefined;
      toast(`Ignored ${item.name}. It stays in the download folder.`);
    } catch (error) {
      problems[item.id] = problemOf(error);
    } finally {
      delete pending[item.id];
      refreshLibrary(client);
    }
  }

  function onRetry(): void {
    void queue.refetch();
  }
</script>

{#if unreachable(queue) || (offline && !queue.data)}
  <ViewState kind="offline" title="Cannot reach atrium">Intake loads when atrium answers again.</ViewState>
{:else if queue.isError && !queue.data}
  <ViewState kind="error" title="Intake did not load">
    {queue.error.message}
    {#snippet action()}
      <Button variant="secondary" onclick={onRetry}>Retry</Button>
    {/snippet}
  </ViewState>
{:else if !queue.data}
  <ul class="lib-intake" aria-label="Loading intake">
    {#each [0, 1] as key (key)}
      <li class="lib-grid__skeleton"><Skeleton lines={3} label="Loading intake" /></li>
    {/each}
  </ul>
{:else}
  {#if queue.data.error}
    <Notice tone="warn" title="Part of the download folder could not be read">{queue.data.error}</Notice>
  {/if}
  {#if queue.data.items.length === 0}
    <ViewState kind="empty" title="Nothing to file">Finished downloads that need filing show up here.</ViewState>
  {:else}
    <ul class="lib-intake" aria-label="Waiting to be filed">
      {#each queue.data.items as item (item.id)}
        {const plan = $derived(opened === item.id ? preview.data : undefined)}
        {const problem = $derived(problems[item.id])}
        <li>
          <Card title={item.name} level={2} pending={pending[item.id]}>
            {#snippet meta()}
              <p class="lib-intake__meta">
                <Badge tone="neutral">{item.torrent ? 'From a torrent' : 'In the download folder'}</Badge>
                {#if item.error}
                  <Badge tone="error">Needs a look</Badge>
                {/if}
              </p>
            {/snippet}
            {#if item.error}
              <p class="meta">{item.error}</p>
            {/if}
            {#if opened === item.id}
              <div class="lib-preview" aria-live="polite">
                {#if preview.isError}
                  <Notice tone="error" variant="inline" title="The plan did not load">{preview.error.message}</Notice>
                {:else if !plan}
                  <Skeleton lines={3} label="Loading the filing plan" />
                {:else}
                  <p class="lib-preview__title">
                    {plan.kind === 'show' ? 'Show' : 'Movie'}: {plan.title}{plan.year ? ` (${plan.year})` : ''}
                  </p>
                  {#if plan.error}
                    <Notice tone="warn" variant="inline" title="atrium cannot file this yet">{plan.error}</Notice>
                  {/if}
                  <ul class="lib-preview__files" aria-label="Files and where they go">
                    {#each plan.files as file (file.id)}
                      <li>
                        <span class="lib-preview__name">{file.name}</span>
                        <span class="meta">
                          {[episodeText(file.season, file.episode), file.destination ? `to ${file.destination}` : 'Not filed']
    .filter(Boolean)
    .join(' · ')}
                        </span>
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>
            {/if}
            {#if problem}
              <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
            {/if}
            {#snippet actions()}
              <Button variant="secondary" aria-expanded={opened === item.id} onclick={() => onPreview(item)}
                >{opened === item.id ? 'Hide plan' : 'Show plan'}</Button
              >
              {#if plan}
                <Button
                  variant="primary"
                  pending={pending[item.id]}
                  disabled={offline || !plan.ready}
                  reason={offline ? reason : plan.ready ? undefined : 'atrium cannot file this yet'}
                  onclick={() => void onFile(item, plan)}
                  label="File {item.name}"
                  >File</Button
                >
              {/if}
              <Button
                variant="quiet"
                disabled={offline || pending[item.id]}
                {reason}
                onclick={() => void onIgnore(item)}
                label="Ignore {item.name}"
                >Ignore</Button
              >
            {/snippet}
          </Card>
        </li>
      {/each}
    </ul>
  {/if}
{/if}
