<script lang="ts">
  import type { MediaFileLink } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Badge from '../../shared/ui/Badge.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p, route } from '../../shell/router';
  import LibraryNav from './LibraryNav.svelte';
  import { blockedBy, episodeText, handleLink, type Problem, problemOf, sizeText, useItem } from './library';
  import './library.css';

  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'media'));
  const blocked = $derived(blockedBy(app));
  const target = $derived.by(() => {
    const kind = route.params.kind;
    const id = route.params.id;
    if ((kind !== 'movie' && kind !== 'show') || typeof id !== 'string') return undefined;
    return { kind, id } as const;
  });
  const detail = useItem(
    () => target,
    () => app?.enabled === true && blocked === undefined,
  );
  const offline = $derived(!device.online);

  let links = $state<Record<string, MediaFileLink>>({});
  let loading = $state<Record<string, boolean>>({});
  let problems = $state<Record<string, Problem>>({});

  async function onLink(fileId: string): Promise<void> {
    loading[fileId] = true;
    delete problems[fileId];
    try {
      links[fileId] = await handleLink(fileId);
    } catch (error) {
      problems[fileId] = problemOf(error);
    } finally {
      delete loading[fileId];
    }
  }

  function minutes(seconds: number): string {
    return seconds >= 3600
      ? plural(Math.round(seconds / 3600), 'hour')
      : plural(Math.max(1, Math.round(seconds / 60)), 'minute');
  }

  function onRetry(): void {
    void detail.refetch();
  }
</script>

{#if appGone(apps, 'media') || !target}
  <NotFound />
{:else}
  <Page
    title={detail.data?.entry.title ?? 'Library'}
    eyebrow={detail.data ? (detail.data.entry.kind === 'show' ? 'Show' : 'Movie') : 'Library'}
    lede={detail.data?.entry.year || undefined}
  >
    <a class="link lib-back" href={p('/apps/media')}>Library</a>
    <LibraryNav current="catalogue" />
    {#if blocked}
      <ViewState kind="blocked" title="Library is unavailable">
        {blocked}
        {#snippet action()}
          <a class="link lib-link" href={p('/apps/directory')}>Open the apps directory</a>
        {/snippet}
      </ViewState>
    {:else if unreachable(detail) || (offline && !detail.data)}
      <ViewState kind="offline" title="Cannot reach atrium">This title loads when atrium answers again.</ViewState>
    {:else if detail.isError && !detail.data}
      <ViewState kind="error" title="This title did not load">
        {detail.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={onRetry}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !detail.data}
      <Skeleton lines={5} label="Loading this title" />
    {:else}
      {#if detail.data.catalogue.stale}
        <Notice tone="warn" title="The catalogue may be out of date">
          The library server did not answer the last scan, so this is from
          {new Date(detail.data.catalogue.observed_at).toLocaleString()}.
        </Notice>
      {/if}
      {#if detail.data.items.length === 0}
        <ViewState kind="empty" title="No files">The library server lists no files for this title.</ViewState>
      {:else}
        <p class="meta">{plural(detail.data.items.length, 'file')}</p>
        <ul class="list lib-files" aria-label="Files">
          {#each detail.data.items as file (file.id)}
            {const link = $derived(links[file.id])}
            {const problem = $derived(problems[file.id])}
            <li class="lib-files__row">
              <div class="lib-files__text">
                <span class="lib-files__name">{file.name}</span>
                <span class="meta"
                  >{[episodeText(file.season, file.episode), sizeText(file.size)].filter(Boolean).join(' · ')}</span
                >
                {#if link}
                  <span class="lib-files__links">
                    {#if link.public}
                      <a class="link lib-link" href={link.public} rel="noreferrer" target="_blank">Open the link</a>
                    {/if}
                    {#if link.vpn}
                      <a class="link lib-link" href={link.vpn} rel="noreferrer" target="_blank">Open on the VPN</a>
                    {/if}
                    <Badge tone="neutral">Works for {minutes(link.ttl_seconds)}</Badge>
                  </span>
                  {#if !link.public && !link.vpn}
                    <span class="meta">atrium has no address to share this file from.</span>
                  {/if}
                {/if}
                {#if problem}
                  <Notice tone={problem.tone} variant="inline" title={problem.title}>{problem.text}</Notice>
                {/if}
              </div>
              <Button
                variant="secondary"
                icon="external"
                pending={loading[file.id]}
                disabled={offline}
                reason={offline ? 'Cannot reach atrium' : undefined}
                onclick={() => void onLink(file.id)}
                label="{link ? 'New link' : 'Get link'} for {file.name}"
                >{link ? 'New link' : 'Get link'}</Button
              >
            </li>
          {/each}
        </ul>
      {/if}
    {/if}
  </Page>
{/if}
