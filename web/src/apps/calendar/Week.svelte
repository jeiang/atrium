<script lang="ts">
  import {
    type CalEvent,
    calendarName,
    clock,
    type Day,
    dateNumber,
    dayLabel,
    stripe,
    weekdayLabel,
  } from './calendar.svelte';

  interface Props {
    days: readonly Day[];
    today: string;
    onopen: (event: CalEvent) => void;
  }

  let { days, today, onopen }: Props = $props();
</script>

<div class="cal-week">
  <ol class="cal-week__grid" aria-label="Days">
    {#each days as day (day.date)}
      <li class="cal-col" data-day={day.date} data-today={day.date === today || undefined}>
        <h3 class="cal-col__head">
          <span class="cal-col__weekday">{weekdayLabel(day.date)}</span>
          <span class="cal-col__date">{dateNumber(day.date)}</span>
          <span class="visually-hidden">{dayLabel(day.date)}{day.date === today ? ', today' : ''}</span>
        </h3>
        {#if day.events.length > 0}
          <ul class="cal-col__events" aria-label={dayLabel(day.date)}>
            {#each day.events as event (event.key)}
              <li>
                <button
                  type="button"
                  class="cal-chip"
                  data-calendar={stripe(event.calendar)}
                  data-all-day={event.allDay || undefined}
                  data-overlaps={event.overlaps || undefined}
                  onclick={() => onopen(event)}
                >
                  <span class="cal-chip__time">{event.allDay ? 'All day' : clock(event.start)}</span>
                  <span class="cal-chip__title">{event.title}</span>
                  <span class="visually-hidden"
                    >, {calendarName(event.calendar)}{event.overlaps ? ', overlaps another event' : ''}</span
                  >
                </button>
              </li>
            {/each}
          </ul>
        {/if}
      </li>
    {/each}
  </ol>
</div>
