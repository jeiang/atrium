<script lang="ts">
  import { on } from 'svelte/events';
  import { appScreens } from '../../apps';
  import { device, unreachable } from '../../shared/device.svelte';
  import Button from '../../shared/ui/Button.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Page from '../../shared/ui/Page.svelte';
  import Skeleton from '../../shared/ui/Skeleton.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';
  import { repairHref, stateOf, unmetNeeds, useApps } from '../../shell/manifest';
  import NotFound from '../../shell/NotFound.svelte';
  import { isActive } from '../../shell/router';
  import Agenda from './Agenda.svelte';
  import Candidates from './Candidates.svelte';
  import { addDays, type CalEvent, mondayOf, todayIso, useEvents, view, weekDays, weekLabel } from './calendar.svelte';
  import EventDialog from './EventDialog.svelte';
  import Week from './Week.svelte';
  import './calendar.css';

  const apps = useApps();
  const settled = $derived(apps.isFetchedAfterMount || !apps.isFetching);
  const app = $derived(settled ? apps.data?.find((item) => item.id === 'calendar') : undefined);
  const current = $derived(
    isActive('/apps/calendar/week') ? 'week' : isActive('/apps/calendar/candidates') ? 'candidates' : 'agenda',
  );
  const today = todayIso();
  const events = useEvents(
    () => view.week,
    () => current !== 'candidates',
  );
  const offline = $derived(unreachable(events));
  const days = $derived(events.data ? weekDays(view.week, events.data.events) : []);
  const empty = $derived(days.every((day) => day.events.length === 0));
  const thisWeek = $derived(view.week === mondayOf(today));
  const needs = $derived(app ? unmetNeeds(app) : []);
  const links = appScreens.calendar ?? [];
  const base = import.meta.env.BASE_URL.replace(/\/$/, '');

  let phone = $state(false);
  let dialogOpen = $state(false);
  let target = $state<CalEvent>();
  let session = $state(0);

  $effect(() => {
    const query = matchMedia('(width < 861px)');
    phone = query.matches;
    return on(query, 'change', () => (phone = query.matches));
  });

  function openEvent(event: CalEvent): void {
    target = event;
    session += 1;
    dialogOpen = true;
  }

  function openCreate(): void {
    target = undefined;
    session += 1;
    dialogOpen = true;
  }

  function step(days: number): void {
    view.week = days === 0 ? mondayOf(today) : addDays(view.week, days);
  }
</script>

{#if settled && apps.data && !app?.enabled}
  <NotFound />
{:else}
  <Page title="Calendar" eyebrow="Apps">
    {#snippet actions()}
      {#if current !== 'candidates'}
        <Button
          variant="primary"
          icon="plus"
          disabled={!device.online}
          reason={device.online ? undefined : 'Cannot reach atrium.'}
          onclick={openCreate}
          >New event</Button
        >
      {/if}
    {/snippet}
    <nav class="cal-nav" aria-label="Calendar">
      {#each links as link (link.key)}
        <a
          class="cal-nav__link"
          href="{base}{link.path}"
          data-wide={link.wide || undefined}
          aria-current={current === link.key ? 'page' : undefined}
          >{link.label}</a
        >
      {/each}
    </nav>
    {#if app && app.state !== 'enabled'}
      <Notice tone={stateOf(app).tone === 'error' ? 'error' : 'warn'} title={stateOf(app).label}>
        {app.reason ?? 'Some calendar events may be missing or stale.'}
        {#snippet action()}
          {#if needs[0]}
            <a class="btn btn--secondary" href={repairHref(needs[0])}>Repair</a>
          {/if}
        {/snippet}
      </Notice>
    {/if}
    {#if current === 'candidates'}
      <Candidates />
    {:else}
      <div class="cal-bar">
        <h2 class="cal-head">{weekLabel(view.week)}</h2>
        <div class="cluster">
          <Button variant="secondary" onclick={() => step(-7)}>Previous week</Button>
          <Button variant="secondary" disabled={thisWeek} onclick={() => step(0)}>This week</Button>
          <Button variant="secondary" onclick={() => step(7)}>Next week</Button>
        </div>
      </div>
      {#if events.data?.notes.length}
        <Notice tone="warn" variant="inline" title="Some events may be missing">{events.data.notes.join(' ')}</Notice>
      {/if}
      {#if offline}
        <ViewState kind="offline" title="Cannot reach atrium" level={3}>
          The calendar loads only while atrium answers. Nothing from it is kept on this device.
        </ViewState>
      {:else if events.isError && !events.data}
        <ViewState kind="error" title="The calendar did not load" level={3}>
          {events.error.message}
          {#snippet action()}
            <Button variant="secondary" onclick={() => void events.refetch()}>Retry</Button>
          {/snippet}
        </ViewState>
      {:else if !events.data}
        <div class="cal-loading"><Skeleton lines={6} label="Loading the calendar" /></div>
      {:else if current === 'week'}
        <Week {days} {today} onopen={openEvent} />
      {:else if empty}
        <ViewState kind="empty" title="Nothing this week" level={3}>
          {#snippet action()}
            <Button variant="secondary" onclick={() => step(7)}>Next week</Button>
          {/snippet}
        </ViewState>
      {:else}
        <Agenda {days} {today} onopen={openEvent} />
      {/if}
    {/if}
  </Page>
  {#key session}
    {#if session > 0}
      <EventDialog
        bind:open={dialogOpen}
        {target}
        day={target ? target.start.slice(0, 10) : view.week === mondayOf(today) ? today : view.week}
        {phone}
      />
    {/if}
  {/key}
{/if}
