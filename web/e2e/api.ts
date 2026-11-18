import type { ServerResponse } from 'node:http';
import type { Draft, Home, PendingBody } from '../src/contract/index.ts';
import type { Handler } from './server.ts';

export const CSRF = 'csrf-fictional-7f3a91';

export interface Api {
  subject: string;
  signedIn: boolean;
  needsYou: number;
  logouts: number;
  calls: string[];
  drafts?: Draft[];
  loginUrl?: string;
}

export const names: Record<string, string> = { 'op-1': 'Ada Operator', 'op-2': 'Grace Operator' };

export function signedInApi(): Api {
  return { subject: 'op-1', signedIn: true, needsYou: 3, logouts: 0, calls: [] };
}

function shell(needsYou: number): unknown {
  return {
    brand_url: '/',
    footer: [],
    groups: [],
    sections: [],
    settings: [],
    settings_url: '/settings',
    needs_you: needsYou,
    notify_failing: [],
    mail: { mode: 'live', paused: false },
    pause: { paused: false },
    modules: ['mail', 'calendar', 'notion', 'media', 'assistant', 'notifications'].map((name) => ({
      name,
      state: 'enabled',
    })),
    version: '0.0.0-fake',
  };
}

const home: Home = {
  threads: [],
  q: '',
  view: '',
  welcome: false,
  date: 'Monday, November 16',
  digest: [
    { text: 'Ilse replied about the December book club' },
    { text: 'Dentist visit moved to Friday 09:30' },
    { text: 'Cedar & Pine shipped order 4471' },
  ],
  agenda: true,
  about: null,
  defaults: {
    model: 'fake-model',
    effort: 'medium',
    context_limit: 0,
    context_override: false,
    thinking: 'off',
    web_search: true,
  },
  composer: {
    models: [],
    model: 'fake-model',
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
  },
  weather: {
    state: 'ok',
    fetched_at: 1_795_075_200,
    failure: null,
    location: 'Hillfield',
    timezone: 'Europe/Amsterdam',
    current: {
      time: '2026-11-19T09:00',
      temperature_c: 6.4,
      apparent_c: 3.1,
      humidity: 82,
      precipitation_mm: 0,
      weather_code: 3,
      condition: 'Overcast',
      wind_kmh: 18,
      wind_direction: 240,
      is_day: true,
    },
    hours: [],
    today: {
      date: '2026-11-19',
      high_c: 8.2,
      low_c: 2.7,
      precipitation_probability: 60,
      weather_code: 61,
      condition: 'Light rain',
      sunrise: '08:04',
      sunset: '16:42',
    },
  },
};

const pending: PendingBody = {
  approvals: [],
  blocked: [],
  drafts: [],
  counts: { approvals: 0, blocked: 0, drafts: 0, unknown: 0 },
};

function json(res: ServerResponse, status: number, body?: unknown): true {
  if (body === undefined) {
    res.writeHead(status).end();
  } else {
    res
      .writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' })
      .end(JSON.stringify(body));
  }
  return true;
}

export function fakeApi(api: Api): Handler {
  return (req, res, pathname) => {
    const method = req.method ?? 'GET';
    if (pathname === '/login' || pathname.startsWith('/auth/')) {
      const title = pathname === '/login' ? 'Sign in' : 'Callback';
      res
        .writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
        .end(`<!doctype html><title>${title}</title><h1>${title}</h1>`);
      return true;
    }
    if (!pathname.startsWith('/web/v1/')) return false;
    const path = pathname.slice('/web/v1'.length);
    api.calls.push(`${method} ${path}`);
    if (path === '/session' && method === 'GET') {
      if (!api.signedIn) {
        const signIn = api.loginUrl ? { modes: ['oidc'], login_url: api.loginUrl, csrf_token: CSRF } : undefined;
        return json(res, 401, {
          error: { code: 'unauthenticated', message: 'Sign in to continue.' },
          ...(signIn ? { sign_in: signIn } : {}),
        });
      }
      return json(res, 200, {
        auth_mode: 'oidc',
        csrf_token: CSRF,
        identity: {
          subject: api.subject,
          name: names[api.subject],
          email: `${api.subject}@example.test`,
          agent: false,
        },
        sign_out: { method: 'POST', url: '/web/v1/session/logout' },
      });
    }
    if (path === '/session/logout' && method === 'POST') {
      api.logouts += 1;
      api.signedIn = false;
      return json(res, 204);
    }
    if (path === '/shell') return json(res, 200, shell(api.needsYou));
    if (path === '/home') return json(res, 200, home);
    if (path === '/pending') return json(res, 200, pending);
    if (path === '/apps') return json(res, 200, { data: { apps: [] }, revision: '1' });
    if (path === '/queue' && api.drafts) {
      return json(res, 200, {
        data: { items: [], total: 0, counts: { ask: 0, proposal: 0, draft: 0, attention: 0 } },
        revision: 1,
      });
    }
    const draft = /^\/drafts\/(\d+)$/.exec(path);
    const found = draft && api.drafts?.find((item) => item.id === Number(draft[1]));
    if (found) return json(res, 200, found);
    if (path === '/events') {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' }).end('event: sync\ndata: {"topics":[]}\n\n');
      return true;
    }
    return json(res, 404, { error: { code: 'not_found', message: 'Not found.' } });
  };
}
