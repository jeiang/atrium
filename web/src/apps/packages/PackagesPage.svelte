<script lang="ts" module>
  const touchQuery = '(width < 861px), ((pointer: coarse) and (not (any-pointer: fine)))';
</script>

<script lang="ts">
  import { useQueryClient } from '@tanstack/svelte-query';
  import { on } from 'svelte/events';
  import type { Package } from '../../contract';
  import { plural } from '../../shared/assistant/format';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Dialog from '../../shared/ui/Dialog.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import { toast } from '../../shared/ui/toast.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { appGone, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { p } from '../../shell/router';
  import AddPackage from './AddPackage.svelte';
  import PackageCard from './PackageCard.svelte';
  import {
    delivered,
    handleDismiss,
    handlePoll,
    handleTrack,
    handleUndo,
    handleUntrack,
    type Problem,
    problemOf,
    quotaLine,
    refreshPackages,
    titleOf,
    trackBlocked,
    troubled,
    usePackages,
  } from './packages.svelte';
  import './packages.css';

  const client = useQueryClient();
  const apps = useApps();
  const app = $derived(apps.data?.find((item) => item.id === 'packages'));
  const list = usePackages(() => app?.enabled === true);
  const offline = $derived(!device.online);
  const items = $derived([...(list.data?.items ?? [])].sort((a, b) => rank(a) - rank(b)));
  const trackReason = $derived(trackBlocked(list.data, offline));

  let pending = $state<Record<number, boolean>>({});
  let problems = $state<Record<number, Problem>>({});
  let polling = $state(false);
  let pollProblem = $state<Problem>();
  let touch = $state(false);
  let confirm = $state<{ kind: 'track' | 'dismiss'; item: Package }>();
  let open = $state(false);
  let sending = $state(false);
  let dialogProblem = $state<Problem>();

  const lede = $derived.by(() => {
    if (!list.data) return undefined;
    const done = items.filter(delivered).length;
    return `${plural(items.length, 'package')}: ${items.length - done} on the way, ${done} delivered`;
  });

  function rank(item: Package): number {
    if (troubled(item)) return 0;
    return delivered(item) ? 2 : 1;
  }

  $effect(() => {
    const query = matchMedia(touchQuery);
    touch = query.matches;
    return on(query, 'change', () => (touch = query.matches));
  });

  function ask(kind: 'track' | 'dismiss', item: Package): void {
    confirm = { kind, item };
    dialogProblem = undefined;
    open = true;
  }

  async function onConfirm(): Promise<void> {
    const target = confirm;
    if (!target || sending) return;
    sending = true;
    dialogProblem = undefined;
    const title = titleOf(target.item);
    try {
      if (target.kind === 'track') {
        const result = await handleTrack(target.item.id);
        toast(`Tracking ${title}. ${result.status_text || 'Ship24 reports each change.'}`);
      } else {
        await handleDismiss(target.item.id);
        toast(`Dismissed ${title}.`);
      }
      open = false;
    } catch (error) {
      dialogProblem = problemOf(error);
    } finally {
      sending = false;
      refreshPackages(client);
    }
  }

  async function onUntrack(item: Package): Promise<void> {
    if (pending[item.id]) return;
    pending[item.id] = true;
    delete problems[item.id];
    const title = titleOf(item);
    try {
      const change = await handleUntrack(item.id);
      const effects = change.effect_ids ?? [];
      toast(`Stopped tracking ${title}.`, {
        action:
          change.undoable && effects.length > 0 ? { label: 'Undo', run: () => void onUndo(effects, title) } : undefined,
      });
    } catch (error) {
      problems[item.id] = problemOf(error);
    } finally {
      delete pending[item.id];
      refreshPackages(client);
    }
  }

  async function onUndo(effects: readonly number[], title: string): Promise<void> {
    try {
      await handleUndo(effects);
      toast(`Tracking ${title} again.`);
    } catch (error) {
      const problem = problemOf(error);
      toast(`${problem.title}. ${problem.text}`, { tone: 'error' });
    } finally {
      refreshPackages(client);
    }
  }

  async function onPoll(): Promise<void> {
    if (polling) return;
    polling = true;
    pollProblem = undefined;
    try {
      const poll = await handlePoll();
      const skipped = poll.rate_limited ? ' Ship24 asked atrium to slow down, so some were skipped.' : '';
      const failed = poll.errors?.length ? ` ${plural(poll.errors.length, 'check')} failed.` : '';
      toast(`Checked ${plural(poll.checked, 'package')}, ${poll.updated} updated.${skipped}${failed}`, {
        tone: failed ? 'error' : 'neutral',
      });
    } catch (error) {
      pollProblem = problemOf(error);
    } finally {
      polling = false;
      refreshPackages(client);
    }
  }

  async function onAdded(id: number, number: string): Promise<void> {
    await client.refetchQueries({ queryKey: ['packages'], exact: true });
    const added = list.data?.items.find((item) => item.id === id);
    toast(`Added ${added ? titleOf(added) : number}.`);
  }

  function onRetry(): void {
    void list.refetch();
  }
</script>

{#if appGone(apps, 'packages')}
  <NotFound />
{:else}
  <Page title="Packages" {lede}>
    {#snippet actions()}
      {#if list.data}
        <Button
          variant="secondary"
          pending={polling}
          disabled={offline || !list.data.ship24}
          reason={offline ? 'Cannot reach atrium' : !list.data.ship24 ? 'Ship24 is not connected' : undefined}
          onclick={() => void onPoll()}
          >Check now</Button
        >
      {/if}
    {/snippet}
    <a class="link pk-back" href={p('/apps')}>Apps</a>
    {#if list.data}
      <p class="pk-quota meta" data-spent={list.data.ship24 && list.data.quota.remaining === 0 ? true : undefined}>
        {quotaLine(list.data)}
      </p>
    {/if}
    {#if pollProblem}
      <Notice tone={pollProblem.tone} variant="inline" title={pollProblem.title}>{pollProblem.text}</Notice>
    {/if}
    {#if !list.isError || list.data}
      <AddPackage {offline} onadded={(id, number) => void onAdded(id, number)} />
    {/if}
    {#if unreachable(list)}
      <ViewState kind="offline" title="Cannot reach atrium">
        Packages load when atrium answers again. Nothing is sent while offline.
      </ViewState>
    {:else if list.isError && !list.data}
      <ViewState kind="error" title="Packages did not load">
        {list.error.message}
        {#snippet action()}
          <Button variant="secondary" onclick={onRetry}>Retry</Button>
        {/snippet}
      </ViewState>
    {:else if !list.data}
      <ul class="pk-list" aria-label="Loading packages">
        {#each [0, 1, 2] as key (key)}
          <li class="pk-list__skeleton"><Skeleton lines={4} label="Loading packages" /></li>
        {/each}
      </ul>
    {:else}
      {#if !list.data.ship24}
        <Notice tone="info" title="Ship24 is not connected">
          atrium still finds packages in your mail. Connect Ship24 to follow them from carrier to door.
          {#snippet action()}
            <a
              class="link"
              href={p('/settings/connections/:kind/:name', { params: { kind: 'service', name: 'ship24' } })}
              >Connect Ship24</a
            >
          {/snippet}
        </Notice>
      {:else if list.data.quota.remaining === 0}
        <Notice tone="warn" title="No Ship24 lookups left this month">
          Packages already tracked keep updating. New ones can be tracked when the month turns over.
        </Notice>
      {/if}
      {#if offline}
        <Notice tone="warn" title="Cannot reach atrium"
          >This list is from before the connection dropped. Nothing can be changed until atrium answers.</Notice
        >
      {/if}
      {#if items.length === 0}
        <ViewState kind="empty" title="No packages"
          >Shipping notices in your mail show up here. Add a tracking number to follow a package yourself.</ViewState
        >
      {:else}
        <ul class="pk-list" aria-label="Packages">
          {#each items as item (item.id)}
            <li data-package={item.id}>
              <PackageCard
                {item}
                {offline}
                {trackReason}
                pending={pending[item.id]}
                problem={problems[item.id]}
                ontrack={() => ask('track', item)}
                onuntrack={() => void onUntrack(item)}
                ondismiss={() => ask('dismiss', item)}
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
  title={confirm ? `${confirm.kind === 'track' ? 'Track' : 'Dismiss'} ${titleOf(confirm.item)}?` : 'Confirm'}
  sheet={touch}
  pending={sending}
  initialFocus="cancel"
>
  {#if confirm?.kind === 'track' && list.data}
    <p>
      This uses one Ship24 lookup. {list.data.quota.remaining} of {list.data.quota.limit} are left this month. Ship24
      then reports each change to atrium.
    </p>
  {:else}
    <p>The package leaves this list for good. This cannot be undone.</p>
  {/if}
  {#if dialogProblem}
    <Notice tone={dialogProblem.tone} variant="inline" title={dialogProblem.title}>{dialogProblem.text}</Notice>
  {/if}
  {#snippet actions()}
    <Button variant="quiet" data-cancel disabled={sending} onclick={() => (open = false)}>Cancel</Button>
    {#if confirm?.kind === 'track'}
      <Button
        variant="primary"
        pending={sending}
        disabled={trackReason !== undefined}
        reason={trackReason}
        onclick={() => void onConfirm()}
        >Track</Button
      >
    {:else}
      <Button
        variant="danger"
        pending={sending}
        disabled={offline}
        reason={offline ? 'Cannot reach atrium' : undefined}
        onclick={() => void onConfirm()}
        >Dismiss</Button
      >
    {/if}
  {/snippet}
</Dialog>
