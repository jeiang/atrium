<script lang="ts">
  import Badge from '../../shared/ui/Badge.svelte';
  import List from '../../shared/ui/List.svelte';
  import ListRow from '../../shared/ui/ListRow.svelte';
  import { type CalEvent, calendarName, clock, type Day, dayLabel, stripe } from './calendar.svelte';

  interface Props {
    days: readonly Day[];
    today: string;
    onopen: (event: CalEvent) => void;
  }

  let { days, today, onopen }: Props = $props();

  const shown = $derived(days.filter((day) => day.events.length > 0 || day.date === today));

  function meta(event: CalEvent): string {
    return [event.location, calendarName(event.calendar)].filter(Boolean).join(' · ');
  }
</script>

<div class="cal-agenda">
  {#each shown as day (day.date)}
    <section class="cal-day" aria-labelledby="cal-day-{day.date}" data-day={day.date}>
      <h3 class="cal-day__head" id="cal-day-{day.date}">
        {dayLabel(day.date)}
        {#if day.date === today}
          <Badge tone="accent">Today</Badge>
        {/if}
      </h3>
      {#if day.events.length === 0}
        <p class="meta">Nothing planned.</p>
      {:else}
        <List label={dayLabel(day.date)} class="cal-list">
          {#each day.events as event (event.key)}
            <ListRow title={event.title} meta={meta(event)} onclick={() => onopen(event)}>
              {#snippet lead()}
                <span class="cal-time" data-calendar={stripe(event.calendar)}>
                  {event.allDay ? 'All day' : clock(event.start)}
                </span>
              {/snippet}
              {#snippet end()}
                {#if event.overlaps}
                  <Badge tone="warn">Overlaps</Badge>
                {/if}
              {/snippet}
            </ListRow>
          {/each}
        </List>
      {/if}
    </section>
  {/each}
</div>
