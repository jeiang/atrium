import { delay, HttpResponse, http } from 'msw';
import type { CalendarEvent, Event } from '../contract';
import { API_BASE } from '../contract';

export interface CalendarScenario {
  empty?: boolean;
  error?: boolean;
  stale?: boolean;
  unknown?: boolean;
  delay?: number;
}

export interface CalendarRequest {
  method: string;
  path: string;
  ifMatch: string | null;
  key: string | null;
}

const ZONE = '+01:00';
const log = globalThis as typeof globalThis & { atriumCalendarRequests?: CalendarRequest[] };

function shift(monday: string, days: number): string {
  const [y = 0, m = 1, d = 1] = monday.split('-').map(Number);
  const date = new Date(Date.UTC(y, m - 1, d + days));
  return date.toISOString().slice(0, 10);
}

function timed(
  monday: string,
  day: number,
  from: string,
  to: string,
  fields: Pick<CalendarEvent, 'uid' | 'calendar' | 'title'> & Partial<CalendarEvent>,
): CalendarEvent {
  const date = shift(monday, day);
  return {
    all_day: false,
    location: '',
    notes: '',
    recurring: false,
    revision: '1',
    ...fields,
    start: `${date}T${from}:00${ZONE}`,
    end: `${date}T${to}:00${ZONE}`,
  };
}

function allDay(
  monday: string,
  day: number,
  fields: Pick<CalendarEvent, 'uid' | 'calendar' | 'title'> & Partial<CalendarEvent>,
): CalendarEvent {
  return {
    all_day: true,
    location: '',
    notes: '',
    recurring: false,
    revision: '1',
    ...fields,
    start: shift(monday, day),
    end: shift(monday, day + 1),
  };
}

export function seedEvents(monday: string): CalendarEvent[] {
  return [
    timed(monday, 0, '09:30', '10:00', {
      uid: 'evt-standup',
      calendar: 'Home',
      title: 'Studio standup',
      location: 'Studio',
      recurring: true,
    }),
    timed(monday, 1, '12:00', '13:00', {
      uid: 'evt-lunch',
      calendar: 'google',
      title: 'Lunch with Joris',
      location: 'Café Noord',
    }),
    timed(monday, 1, '12:30', '13:30', {
      uid: 'evt-call',
      calendar: 'Home',
      title: 'Call with Sanne about the tiles',
      notes: 'Bring the three quotes.',
    }),
    allDay(monday, 2, { uid: 'evt-fair', calendar: 'google', title: 'Garden club fair' }),
    timed(monday, 3, '08:15', '09:00', { uid: 'evt-train', calendar: 'Home', title: 'Train to Rotterdam' }),
    timed(monday, 4, '19:00', '21:00', {
      uid: 'evt-dinner',
      calendar: 'google',
      title: 'Dinner with Ilse',
      location: 'Mill Lane 4',
    }),
    allDay(monday, 5, { uid: 'birthday-truus-vos', calendar: 'birthdays', title: 'Truus Vos, turns 88' }),
    timed(monday, 7, '10:00', '10:30', {
      uid: 'evt-dentist',
      calendar: 'Home',
      title: 'Dentist check-up',
      location: 'Riverside Dental',
    }),
    allDay(monday, 9, { uid: 'birthday-lotte-smit', calendar: 'birthdays', title: 'Lotte Smit, turns 41' }),
  ];
}

export function listed(event: CalendarEvent): Event {
  return {
    uid: event.uid,
    calendar: event.calendar,
    start: event.start,
    end: event.end,
    title: event.title,
    location: event.location,
  };
}

function thisMonday(): string {
  const now = new Date();
  now.setDate(now.getDate() - ((now.getDay() + 6) % 7));
  return `${now.getFullYear()}-${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`;
}

function refusal(status: number, code: string, message: string, extra: Record<string, unknown> = {}): Response {
  return HttpResponse.json({ error: { code, message, ...extra } }, { status });
}

function target(calendar: string): 'icloud' | 'google' | 'birthdays' | undefined {
  if (calendar === 'icloud' || calendar === 'google' || calendar === 'birthdays') return calendar;
  return undefined;
}

export function calendarHandlers(read: () => { calendar?: CalendarScenario }) {
  let store: CalendarEvent[] = [];
  let seededFrom: string | undefined;
  let staleUsed = false;
  let nextEffect = 9001;
  let nextUid = 1;
  const effects = new Map<number, { done: boolean; undo: () => void }>();

  const scenario = (): CalendarScenario => {
    const current = read().calendar ?? {};
    const key = JSON.stringify(current);
    if (key !== seededFrom) {
      seededFrom = key;
      staleUsed = false;
      store = current.empty ? [] : seedEvents(thisMonday());
    }
    return current;
  };

  const record = (request: Request): void => {
    log.atriumCalendarRequests ??= [];
    log.atriumCalendarRequests.push({
      method: request.method,
      path: new URL(request.url).pathname.replace(/^.*\/web\/v1/, ''),
      ifMatch: request.headers.get('If-Match'),
      key: request.headers.get('Idempotency-Key'),
    });
  };

  const effect = (undo: () => void): number[] => {
    const id = nextEffect++;
    effects.set(id, { done: false, undo });
    return [id];
  };

  const bump = (event: CalendarEvent): void => {
    event.revision = String(Number(event.revision) + 1);
  };

  const find = (uid: string): CalendarEvent | undefined => store.find((event) => event.uid === uid);

  const guard = async (state: CalendarScenario, path: string): Promise<Response | undefined> => {
    if (state.delay) await delay(state.delay);
    const at = target(path);
    if (!at) return refusal(422, 'invalid', 'calendar must be one of icloud, google');
    if (at === 'birthdays') return refusal(409, 'refused', 'Birthdays come from contacts and cannot be changed.');
    if (state.unknown) {
      return refusal(409, 'effect_unknown', 'The provider did not confirm the change.', { effect_ids: [nextEffect++] });
    }
    return undefined;
  };

  const precondition = (state: CalendarScenario, event: CalendarEvent, header: string | null): Response | undefined => {
    const expected = header?.replace(/^W\//, '').replaceAll('"', '');
    if (state.stale && !staleUsed) {
      staleUsed = true;
      event.title = `${event.title} (moved)`;
      bump(event);
    }
    if (expected !== undefined && expected !== event.revision) {
      return refusal(412, 'precondition_failed', 'The event changed since it was read. Nothing was written.');
    }
    return undefined;
  };

  return [
    http.get(`*${API_BASE}/calendar/events`, async ({ request }) => {
      const state = scenario();
      if (state.delay) await delay(state.delay);
      if (state.error) return refusal(503, 'unavailable', 'The calendar mirror is not reachable.');
      const url = new URL(request.url);
      const from = url.searchParams.get('from') ?? '0000-01-01';
      const to = url.searchParams.get('to') ?? '9999-12-31';
      const events = store
        .filter((event) => event.start.slice(0, 10) < to && (event.end || event.start).slice(0, 10) >= from)
        .sort((a, b) => a.start.localeCompare(b.start))
        .map(listed);
      return HttpResponse.json({ data: { events, notes: [], truncated: false }, revision: '1' });
    }),
    http.get(`*${API_BASE}/calendar/events/:calendar/:uid`, ({ params }) => {
      scenario();
      const event = find(String(params.uid));
      if (!event) return refusal(404, 'not_found', 'There is no such event.');
      return HttpResponse.json(
        { data: event, revision: event.revision },
        { headers: { ETag: `W/"${event.revision}"` } },
      );
    }),
    http.post(`*${API_BASE}/calendar/events`, async ({ request }) => {
      const state = scenario();
      record(request);
      const body = (await request.json()) as Partial<CalendarEvent> & { calendar?: string };
      const refused = await guard(state, body.calendar ?? '');
      if (refused) return refused;
      if (!body.title?.trim() || !body.start) return refusal(422, 'invalid', 'title and start are required');
      const whole = body.all_day === true;
      const event: CalendarEvent = {
        uid: `evt-new-${nextUid++}`,
        calendar: body.calendar === 'google' ? 'google' : 'Home',
        title: body.title.trim(),
        all_day: whole,
        start: whole ? body.start : `${body.start.slice(0, 16)}:00${ZONE}`,
        end: whole ? (body.end ?? shift(body.start, 1)) : `${(body.end ?? body.start).slice(0, 16)}:00${ZONE}`,
        location: body.location ?? '',
        notes: body.notes ?? '',
        recurring: false,
        revision: '1',
      };
      store.push(event);
      const ids = effect(() => {
        store = store.filter((item) => item !== event);
      });
      return HttpResponse.json({ data: event, effect_ids: ids, undoable: true }, { status: 201 });
    }),
    http.patch(`*${API_BASE}/calendar/events/:calendar/:uid`, async ({ request, params }) => {
      const state = scenario();
      record(request);
      const refused = await guard(state, String(params.calendar));
      if (refused) return refused;
      const event = find(String(params.uid));
      if (!event) return refusal(404, 'not_found', 'There is no such event.');
      const header = request.headers.get('If-Match');
      if (!header) return refusal(422, 'invalid', 'If-Match is required', { field: 'If-Match' });
      const stale = precondition(state, event, header);
      if (stale) return stale;
      const body = (await request.json()) as Partial<CalendarEvent>;
      const before = { ...event };
      const whole = body.all_day ?? event.all_day;
      if (body.title !== undefined) event.title = body.title;
      if (body.location !== undefined) event.location = body.location;
      if (body.notes !== undefined) event.notes = body.notes;
      event.all_day = whole;
      if (body.start) event.start = whole ? body.start : `${body.start.slice(0, 16)}:00${ZONE}`;
      if (body.end) event.end = whole ? body.end : `${body.end.slice(0, 16)}:00${ZONE}`;
      bump(event);
      const ids = effect(() => {
        Object.assign(event, before);
        bump(event);
      });
      return HttpResponse.json({ data: event, effect_ids: ids, undoable: true });
    }),
    http.delete(`*${API_BASE}/calendar/events/:calendar/:uid`, async ({ request, params }) => {
      const state = scenario();
      record(request);
      const refused = await guard(state, String(params.calendar));
      if (refused) return refused;
      const event = find(String(params.uid));
      if (!event) return refusal(404, 'not_found', 'There is no such event.');
      const stale = precondition(state, event, request.headers.get('If-Match'));
      if (stale) return stale;
      store = store.filter((item) => item !== event);
      const ids = effect(() => {
        store.push(event);
      });
      return HttpResponse.json({
        data: { deleted: event.uid, calendar: String(params.calendar) },
        effect_ids: ids,
        undoable: true,
      });
    }),
    http.post(`*${API_BASE}/effects/:id/undo`, ({ params, request }) => {
      const found = effects.get(Number(params.id));
      if (!found) return undefined;
      record(request);
      if (found.done) return refusal(409, 'not_undoable', 'Nothing is left to undo.');
      found.done = true;
      found.undo();
      return HttpResponse.json({ undone: true });
    }),
  ];
}
