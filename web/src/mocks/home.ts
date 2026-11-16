import { delay, HttpResponse, http } from 'msw';
import type {
  Approval,
  ComposerView,
  Draft,
  Home,
  HomeWeather,
  HomeWeatherState,
  NewThread,
  PendingBody,
} from '../contract';
import { API_BASE } from '../contract';
import { digestLines, weather } from '../gallery/assistant-fixtures';

export interface HomeScenario {
  weather?: HomeWeatherState;
  digest?: number;
  delayMs?: number;
  fail?: boolean;
  startFails?: boolean;
}

export interface StartedRecord {
  body: NewThread;
  idempotencyKey: string | null;
  csrf: string | null;
}

export const STALE_HOURS = 3;

const mockGlobal = globalThis as typeof globalThis & { atriumMockStarted?: StartedRecord[] };

const extraDigest = [
  'Moved the quarterly backup check to Monday',
  'Filed three receipts under Household',
  'Renewed the library hold on the atlas',
  'Archived 14 newsletters',
  'Marked the bike service as done',
];

const titles = [
  'Send the reply to Ilse about the book club',
  'Move the dentist visit to Friday 09:30',
  'File order 4471 from Cedar & Pine',
  'Add the allotment work day to the calendar',
  'Pay the water bill from the household account',
  'Archive the old hosting newsletters',
  'Share the trip notes with Bram',
  'Reschedule the boiler service',
  'Unsubscribe from the garden centre list',
  'Rename the Receipts folder to Household',
  'Confirm the choir rehearsal time',
  'Forward the parcel notice to the neighbour',
];

const kinds = ['Send mail', 'Change event', 'File mail', 'Add event', 'Pay bill', 'Archive mail'];

function approval(index: number, blocked: boolean): Approval {
  return {
    id: 100 + index,
    kind: blocked ? 'mail.send' : 'calendar.update',
    kind_label: kinds[index % kinds.length] ?? 'Change',
    module: blocked ? 'mail' : 'calendar',
    approval: true,
    status: blocked ? 'failed' : 'pending',
    title: titles[index % titles.length] ?? `Item ${index + 1}`,
    created_at: 1_763_280_000 + index * 60,
    undoable: true,
    blocked,
    blocked_effect: blocked,
    blocked_undo: false,
    args: [],
    preview: null,
    result: null,
  };
}

const draft: Draft = {
  id: 7,
  account: 'home',
  to: 'ilse@example.test',
  cc: '',
  subject: 'Re: December book club',
  body: 'Thank you, I will be there.',
  status: 'draft',
  revision: 1,
  created_at: 1_763_280_000,
  updated_at: 1_763_280_000,
  unexpected_recipients: [],
  account_configured: true,
};

export function pendingBody(count: number): PendingBody {
  const drafts = count >= 2 ? [draft] : [];
  const blocked = count >= 3 ? [approval(count - 1, true)] : [];
  const approvals = Array.from({ length: count - drafts.length - blocked.length }, (_, i) => approval(i, false));
  return {
    approvals,
    blocked,
    drafts,
    counts: { approvals: approvals.length, blocked: blocked.length, drafts: drafts.length, unknown: 0 },
  };
}

const composer: ComposerView = {
  models: [],
  model: 'mock-model',
  known: true,
  efforts: [],
  effort: 'medium',
  context_limits: [],
  context: 0,
  thinking: 'off',
  thinking_options: [],
  effort_fixed: false,
  web_search: true,
  cliff: false,
  coding: false,
  live: 0,
};

function homeBody(state: HomeScenario): Home {
  const lines = [...digestLines, ...extraDigest.map((text) => ({ text }))].slice(0, state.digest ?? 7);
  return {
    threads: [],
    q: '',
    view: '',
    welcome: false,
    date: 'Monday, November 16',
    digest: lines.length > 0 ? lines : null,
    agenda: true,
    about: null,
    defaults: { model: 'mock-model', effort: 'medium', context_limit: 0, thinking: 'off', web_search: true },
    composer,
    weather: weatherBody(state.weather ?? 'ok'),
  };
}

function weatherBody(state: HomeWeatherState): HomeWeather {
  const now = Math.floor(Date.now() / 1000);
  const empty = {
    location: null,
    timezone: null,
    current: null,
    today: null,
    hours: [],
    fetched_at: null,
    failure: null,
  };
  switch (state) {
    case 'off':
    case 'unconfigured':
      return { ...empty, state };
    case 'loading':
      return { ...empty, state, location: weather.location, timezone: weather.timezone };
    case 'failed':
      return { ...empty, state, location: weather.location, timezone: weather.timezone, failure: 'timeout' };
    case 'stale':
      return { ...weather, state, fetched_at: now - STALE_HOURS * 3600 };
    default:
      return { ...weather, state: 'ok', fetched_at: now };
  }
}

export function homeHandlers(scenario: () => { home?: HomeScenario; needsYou?: number }) {
  return [
    http.get(`*${API_BASE}/home`, async () => {
      const state = scenario().home ?? {};
      if (state.delayMs) await delay(state.delayMs);
      if (state.fail) {
        return HttpResponse.json(
          { error: { code: 'unavailable', message: 'Home is not available right now.' } },
          { status: 503 },
        );
      }
      return HttpResponse.json(homeBody(state));
    }),
    http.get(`*${API_BASE}/pending`, () => {
      const count = scenario().needsYou;
      return count === undefined ? undefined : HttpResponse.json(pendingBody(count));
    }),
    http.post(`*${API_BASE}/threads`, async ({ request }) => {
      if (scenario().home?.startFails) {
        return HttpResponse.json(
          { error: { code: 'busy', message: 'The assistant is busy. Try again in a moment.' } },
          { status: 503 },
        );
      }
      const body = (await request.clone().json()) as NewThread;
      mockGlobal.atriumMockStarted = [
        ...(mockGlobal.atriumMockStarted ?? []),
        {
          body,
          idempotencyKey: request.headers.get('Idempotency-Key'),
          csrf: request.headers.get('X-CSRF-Token'),
        },
      ];
      return undefined;
    }),
  ];
}
