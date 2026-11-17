import { createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type CalendarEvent,
  calendarEvent,
  calendarEventCreate,
  calendarEventDelete,
  calendarEventUpdate,
  calendarList,
  type Event,
  type EventCreate,
  type EventPatch,
  ok,
  type QueueItem,
  reviewQueue,
  reviewUndoEffect,
} from '../../contract';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';

const CALENDAR_TOPICS = ['calendar', 'settings'] as const;
const CANDIDATE_TOPICS = ['review', 'calendar'] as const;
const QUEUE_PAGE = 100;

export const READ_ONLY = 'birthdays';
export type Target = 'icloud' | 'google';
export const targets: readonly { value: Target; label: string }[] = [
  { value: 'icloud', label: 'iCloud' },
  { value: 'google', label: 'Google' },
];

export interface CalEvent extends Event {
  key: string;
  allDay: boolean;
  overlaps: boolean;
}

export interface Day {
  date: string;
  events: CalEvent[];
}

export interface Problem {
  tone: 'warn' | 'error';
  title: string;
  text: string;
  inbox?: boolean;
}

export interface Draft {
  calendar: Target;
  title: string;
  allDay: boolean;
  start: string;
  end: string;
  location: string;
  notes: string;
}

export const view = $state({ week: mondayOf(todayIso()) });
export const locks = $state<{ keys: string[] }>({ keys: [] });

function pad(value: number): string {
  return String(value).padStart(2, '0');
}

export function isoDate(date: Date): string {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function parseDate(iso: string): Date {
  const [y = 0, m = 1, d = 1] = iso.slice(0, 10).split('-').map(Number);
  return new Date(y, m - 1, d);
}

export function todayIso(): string {
  return isoDate(new Date());
}

export function addDays(iso: string, days: number): string {
  const date = parseDate(iso);
  date.setDate(date.getDate() + days);
  return isoDate(date);
}

export function mondayOf(iso: string): string {
  const day = parseDate(iso).getDay();
  return addDays(iso, -((day + 6) % 7));
}

export function eventKey(event: Pick<Event, 'calendar' | 'uid'>): string {
  return `${event.calendar}/${event.uid}`;
}

export function writeTarget(calendar: string): Target | undefined {
  if (calendar === READ_ONLY) return undefined;
  return calendar === 'google' ? 'google' : 'icloud';
}

export function calendarName(calendar: string): string {
  if (calendar === 'google') return 'Google';
  if (calendar === READ_ONLY) return 'Birthdays';
  if (calendar === 'icloud') return 'iCloud';
  return calendar;
}

export function stripe(calendar: string): string {
  return writeTarget(calendar) ?? READ_ONLY;
}

const dayWords = new Intl.DateTimeFormat(undefined, { weekday: 'long', day: 'numeric', month: 'short' });
const shortDay = new Intl.DateTimeFormat(undefined, { weekday: 'short' });
const monthDay = new Intl.DateTimeFormat(undefined, { day: 'numeric', month: 'short' });

export function dayLabel(iso: string): string {
  return dayWords.format(parseDate(iso));
}

export function weekdayLabel(iso: string): string {
  return shortDay.format(parseDate(iso));
}

export function dateNumber(iso: string): number {
  return parseDate(iso).getDate();
}

export function weekLabel(monday: string): string {
  return `Week of ${monthDay.format(parseDate(monday))}`;
}

export function clock(value: string): string {
  return value.length > 10 ? value.slice(11, 16) : '';
}

export function whenText(event: Pick<Event, 'start' | 'end'>, allDay: boolean): string {
  if (allDay) {
    const last = event.end ? addDays(event.end, -1) : event.start;
    return last > event.start ? `All day, until ${dayLabel(last)}` : 'All day';
  }
  const end = clock(event.end);
  return end ? `${clock(event.start)} to ${end}` : clock(event.start);
}

function dayEnd(event: CalEvent): string {
  if (!event.allDay) return event.start.slice(0, 10);
  return event.end ? addDays(event.end.slice(0, 10), -1) : event.start;
}

export function weekDays(monday: string, events: readonly Event[]): Day[] {
  const shaped = events.map(
    (event): CalEvent => ({
      ...event,
      key: eventKey(event),
      allDay: event.start.length === 10,
      overlaps: false,
    }),
  );
  const timed = shaped.filter((event) => !event.allDay);
  for (const event of timed) {
    event.overlaps = timed.some(
      (other) =>
        other !== event && other.start < (event.end || event.start) && event.start < (other.end || other.start),
    );
  }
  return Array.from({ length: 7 }, (_, index) => {
    const date = addDays(monday, index);
    const today = shaped
      .filter((event) => event.start.slice(0, 10) <= date && dayEnd(event) >= date)
      .sort((a, b) => Number(b.allDay) - Number(a.allDay) || a.start.localeCompare(b.start));
    return { date, events: today };
  });
}

export function useEvents(monday: () => string, enabled: () => boolean) {
  return createQuery(() => {
    const from = monday();
    const to = addDays(from, 7);
    return {
      queryKey: ['calendar', 'events', from],
      queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await calendarList({ from, to }, { signal })).data,
      enabled: enabled(),
      meta: { topics: CALENDAR_TOPICS, persist: false },
    };
  });
}

export function useEvent(target: () => { calendar: string; uid: string } | undefined) {
  return createQuery(() => {
    const at = target();
    const path = at ? writeTarget(at.calendar) : undefined;
    return {
      queryKey: ['calendar', 'event', at?.calendar, at?.uid],
      queryFn: async ({ signal }: { signal: AbortSignal }): Promise<CalendarEvent> =>
        ok(await calendarEvent(path ?? '', at?.uid ?? '', { signal })).data,
      enabled: at !== undefined && path !== undefined,
      meta: { topics: CALENDAR_TOPICS, persist: false },
    };
  });
}

export function useCandidates() {
  return createQuery(() => ({
    queryKey: ['calendar', 'candidates'],
    queryFn: async ({ signal }: { signal: AbortSignal }): Promise<QueueItem[]> => {
      let page = ok(await reviewQueue({ kind: 'proposal', limit: QUEUE_PAGE }, { signal })).data;
      const items = [...page.items];
      while (page.next_cursor) {
        page = ok(
          await reviewQueue({ kind: 'proposal', limit: QUEUE_PAGE, cursor: page.next_cursor }, { signal }),
        ).data;
        items.push(...page.items);
      }
      return items.filter((item) => item.module === 'calendar');
    },
    meta: { topics: CANDIDATE_TOPICS, persist: false },
  }));
}

function gesture(revision?: string): RequestInit {
  const headers: Record<string, string> = { 'Idempotency-Key': crypto.randomUUID() };
  if (revision) headers['If-Match'] = `W/"${revision}"`;
  return { headers };
}

export function blankDraft(date: string): Draft {
  return { calendar: 'icloud', title: '', allDay: false, start: `${date}T09:00`, end: '', location: '', notes: '' };
}

export function draftOf(event: CalendarEvent): Draft {
  return {
    calendar: writeTarget(event.calendar) ?? 'icloud',
    title: event.title,
    allDay: event.all_day,
    start: event.all_day ? event.start.slice(0, 10) : event.start.slice(0, 16),
    end: event.all_day ? addDays(event.end.slice(0, 10), -1) : event.end.slice(0, 16),
    location: event.location,
    notes: event.notes,
  };
}

function wireEnd(draft: Draft): string | undefined {
  if (!draft.end) return draft.allDay ? addDays(draft.start, 1) : undefined;
  return draft.allDay ? addDays(draft.end, 1) : draft.end;
}

export async function handleCreate(draft: Draft) {
  const body: EventCreate = {
    calendar: draft.calendar,
    title: draft.title.trim(),
    start: draft.start,
    all_day: draft.allDay,
    location: draft.location.trim(),
    notes: draft.notes.trim(),
  };
  const end = wireEnd(draft);
  if (end) body.end = end;
  return ok(await calendarEventCreate(body, gesture()));
}

export async function handleUpdate(event: CalendarEvent, draft: Draft) {
  const body: EventPatch = {
    title: draft.title.trim(),
    start: draft.start,
    all_day: draft.allDay,
    location: draft.location.trim(),
    notes: draft.notes.trim(),
  };
  const end = wireEnd(draft);
  if (end) body.end = end;
  return ok(await calendarEventUpdate(writeTarget(event.calendar) ?? '', event.uid, body, gesture(event.revision)));
}

export async function handleDelete(event: CalendarEvent) {
  return ok(await calendarEventDelete(writeTarget(event.calendar) ?? '', event.uid, gesture(event.revision)));
}

export async function handleUndo(effects: readonly number[]): Promise<void> {
  for (const id of effects) ok(await reviewUndoEffect(id, gesture()));
}

export function lock(key: string): void {
  if (!locks.keys.includes(key)) locks.keys.push(key);
}

export function problemOf(error: unknown, nothing: string): Problem {
  if (refusedOffline(error) && error instanceof Error) {
    return { tone: 'warn', title: 'Cannot reach atrium', text: error.message };
  }
  if (error instanceof ApiError && error.status === 412) {
    return {
      tone: 'warn',
      title: 'This event changed since it loaded',
      text: 'atrium reloaded it and wrote nothing. Make your change again if you still want it.',
    };
  }
  if (outcomeUnknown(error)) {
    return {
      tone: 'error',
      title: 'The outcome is unknown',
      text: 'atrium could not confirm the change and did not try again. The event stays locked until the outcome is resolved.',
      inbox: true,
    };
  }
  if (error instanceof ApiError && error.status === 404) {
    return {
      tone: 'warn',
      title: 'This event is gone',
      text: 'It was removed elsewhere. atrium reloaded the calendar.',
    };
  }
  if (error instanceof ApiError && error.status === 409) {
    return { tone: 'warn', title: 'atrium refused the change', text: `${error.message} ${nothing}` };
  }
  if (error instanceof ApiError && error.status === 422) {
    return { tone: 'error', title: 'Check the event', text: error.message };
  }
  if (error instanceof ApiError) {
    return { tone: 'error', title: 'The change failed', text: `${error.message} ${nothing}` };
  }
  return {
    tone: 'error',
    title: 'The outcome is unknown',
    text: 'The connection failed before atrium answered. atrium reloaded the calendar and did not try again.',
  };
}

export function refreshCalendar(client: QueryClient): void {
  void client.invalidateQueries({ queryKey: ['calendar'] });
}

export function draftReady(draft: Draft): boolean {
  if (!draft.title.trim() || !draft.start) return false;
  if (!draft.end) return true;
  return draft.allDay ? draft.end >= draft.start : draft.end > draft.start;
}
