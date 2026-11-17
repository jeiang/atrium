<script lang="ts">
  import { createQuery } from '@tanstack/svelte-query';
  import { assistantRunDetail, assistantThreadHistory, assistantThreadWork, ok } from '../../contract';
  import { formatDayTime, plural, webHref } from '../../shared/assistant/format';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Icon from '../../shared/ui/Icon.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import HistoryNode from './HistoryNode.svelte';
  import { failureText } from './manage';

  interface Props {
    open: boolean;
    thread: number;
    run: number | undefined;
  }

  let { open = $bindable(), thread, run }: Props = $props();

  const runStates: Record<string, string> = {
    running: 'Running',
    parked: 'Waiting for you',
    interrupted: 'Interrupted',
    done: 'Finished',
    stopped: 'Stopped',
    failed: 'Failed',
  };

  const detail = createQuery(() => ({
    queryKey: ['thread', thread, 'run', run],
    queryFn: async ({ signal }) => ok(await assistantRunDetail(thread, run ?? 0, { signal })),
    enabled: open && run !== undefined,
    meta: { topics: ['assistant'] },
  }));

  const work = createQuery(() => ({
    queryKey: ['thread', thread, 'work'],
    queryFn: async ({ signal }) => ok(await assistantThreadWork(thread, { signal })),
    enabled: open,
    meta: { topics: ['assistant'] },
  }));

  const history = createQuery(() => ({
    queryKey: ['thread', thread, 'history'],
    queryFn: async ({ signal }) => ok(await assistantThreadHistory(thread, undefined, { signal })),
    enabled: open,
    meta: { topics: ['assistant'] },
  }));

  function draftTitle(draft: Record<string, unknown>): string {
    for (const key of ['title', 'subject', 'name']) {
      const value = draft[key];
      if (typeof value === 'string' && value) return value;
    }
    return 'Untitled draft';
  }

  function size(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
    return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  }

  function onRetry(): void {
    void work.refetch();
    void history.refetch();
    if (run !== undefined) void detail.refetch();
  }
</script>

{#snippet failed(
    error: Error,
  )}
  <ViewState kind="error" title="This could not be loaded" level={4}>
    {failureText(error)}
    {#snippet action()}
      <Button size="sm" onclick={onRetry}>Try again</Button>
    {/snippet}
  </ViewState>
{/snippet}

<Dialog bind:open title="Work" sheet>
  <div class="work">
    <section class="work__part" aria-labelledby="work-run">
      <h3 class="work__title" id="work-run">Latest run</h3>
      {#if run === undefined}
        <p class="work__empty">No run yet.</p>
      {:else if detail.isPending}
        <p class="work__empty">Loading the run</p>
      {:else if detail.isError}
        {@render failed(detail.error)}
      {:else}
        <dl class="work__facts">
          <dt>State</dt>
          <dd>{runStates[detail.data.state] ?? detail.data.state}</dd>
          <dt>Steps</dt>
          <dd>{detail.data.steps}</dd>
          <dt>Started</dt>
          <dd>{formatDayTime(detail.data.started_at)}</dd>
          {#if detail.data.ended_at != null}
            <dt>Ended</dt>
            <dd>{formatDayTime(detail.data.ended_at)}</dd>
          {/if}
          {#if detail.data.error}
            <dt>Error</dt>
            <dd>{detail.data.error}</dd>
          {/if}
        </dl>
      {/if}
    </section>

    {#if work.isPending}
      <div class="work__loading"><ViewState kind="loading" title="Loading the work" level={4} /></div>
    {:else if work.isError}
      {@render failed(work.error)}
    {:else}
      {const data = work.data}
      <section class="work__part" aria-labelledby="work-plan">
        <h3 class="work__title" id="work-plan">Plan</h3>
        {#if data.plan && data.plan.items.length > 0}
          <ul class="work__list">
            {#each data.plan.items as step, index (index)}
              <li class="work__item">
                {#if step.done}
                  <Icon name="check" /><span class="visually-hidden">Done:</span>
                {/if}
                {step.text}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">No plan.</p>
        {/if}
      </section>
      <section class="work__part" aria-labelledby="work-needs">
        <h3 class="work__title" id="work-needs">Asks</h3>
        {#if data.needs.length > 0}
          <ul class="work__list">
            {#each data.needs as need (need.id)}
              <li class="work__item">{need.title}</li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">Nothing waits for you.</p>
        {/if}
      </section>
      <section class="work__part" aria-labelledby="work-sources">
        <h3 class="work__title" id="work-sources">Sources</h3>
        {#if data.sources.length > 0}
          <ul class="work__list">
            {#each data.sources as source (source.key)}
              {const href = webHref(source.url)}
              <li class="work__item">
                {#if href}
                  <a class="link" {href} target="_blank" rel="noopener noreferrer">{source.title || source.url}</a>
                {:else}
                  {source.title || source.url}
                {/if}
              </li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">No sources.</p>
        {/if}
      </section>
      <section class="work__part" aria-labelledby="work-subruns">
        <h3 class="work__title" id="work-subruns">Sub-runs</h3>
        {#if data.subagents.length > 0}
          <ul class="work__list">
            {#each data.subagents as card (card.thread_id)}
              <li class="work__item">
                {card.title || card.role || 'Helper'}
                <span class="work__meta">{runStates[card.status] ?? card.status} · {plural(card.steps, 'step')}</span>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">No sub-runs.</p>
        {/if}
      </section>
      <section class="work__part" aria-labelledby="work-files">
        <h3 class="work__title" id="work-files">Files</h3>
        {#if data.files.length > 0}
          <ul class="work__list">
            {#each data.files as file (file.id)}
              <li class="work__item">{file.name} <span class="work__meta">{size(file.bytes)}</span></li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">No files.</p>
        {/if}
      </section>
      <section class="work__part" aria-labelledby="work-drafts">
        <h3 class="work__title" id="work-drafts">Drafts</h3>
        {#if data.drafts.length > 0}
          <ul class="work__list">
            {#each data.drafts as draft, index (index)}
              <li class="work__item">{draftTitle(draft)}</li>
            {/each}
          </ul>
        {:else}
          <p class="work__empty">No drafts.</p>
        {/if}
      </section>
    {/if}

    <section class="work__part" aria-labelledby="work-context">
      <h3 class="work__title" id="work-context">Context</h3>
      {#if history.isPending}
        <p class="work__empty">Loading what atrium holds</p>
      {:else if history.isError}
        {@render failed(history.error)}
      {:else if history.data.lines.length === 0}
        <p class="work__empty">Nothing is summarized yet. atrium holds this chat word for word.</p>
      {:else}
        <ul class="history__lines" aria-label="Summaries of earlier messages">
          {#each history.data.lines as line (`${line.from}:${line.n}`)}
            <HistoryNode {thread} {line} />
          {/each}
        </ul>
      {/if}
    </section>
  </div>
</Dialog>
