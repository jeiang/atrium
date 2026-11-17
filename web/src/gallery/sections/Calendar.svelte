<script lang="ts">
  import Agenda from '../../apps/calendar/Agenda.svelte';
  import { blankDraft, type Draft, weekDays, weekLabel } from '../../apps/calendar/calendar.svelte';
  import '../../apps/calendar/calendar.css';
  import EventForm from '../../apps/calendar/EventForm.svelte';
  import Week from '../../apps/calendar/Week.svelte';
  import { listed, seedEvents } from '../../mocks/calendar';
  import Card from '../../shared/ui/Card.svelte';
  import Notice from '../../shared/ui/Notice.svelte';
  import Section from '../../shared/ui/Section.svelte';
  import ViewState from '../../shared/ui/ViewState.svelte';

  const monday = '2026-11-16';
  const today = '2026-11-17';
  const days = weekDays(monday, seedEvents(monday).map(listed));
  let draft = $state<Draft>({ ...blankDraft(today), title: 'Tile showroom visit', location: 'Harbour Road 12' });

  function onOpen(): void {}
  function onSubmit(): void {}
</script>

<Section id="g-calendar" data-gallery="calendar" title="Calendar">
  <h3 class="cal-head">{weekLabel(monday)}</h3>
  <Agenda {days} {today} onopen={onOpen} />
  <Week {days} {today} onopen={onOpen} />
  <Notice tone="error" variant="inline" title="The outcome is unknown">
    The last change to this event was not confirmed. It stays locked until the outcome is resolved.
  </Notice>
  <Notice tone="warn" variant="inline" title="This event changed since it loaded">
    atrium reloaded it and wrote nothing. Make your change again if you still want it.
  </Notice>
  <Card title="New event" level={3}>
    <EventForm id="g-calendar-form" bind:draft creating onsubmit={onSubmit} />
  </Card>
  <div class="grid">
    <ViewState kind="loading" title="Loading the calendar" />
    <ViewState kind="empty" title="Nothing this week" />
    <ViewState kind="error" title="The calendar did not load">The calendar mirror is not reachable.</ViewState>
    <ViewState kind="offline" title="Cannot reach atrium">
      The calendar loads only while atrium answers. Nothing from it is kept on this device.
    </ViewState>
    <ViewState kind="empty" title="No proposed events"
      >Events atrium finds in mail wait here for your decision.</ViewState
    >
  </div>
</Section>
