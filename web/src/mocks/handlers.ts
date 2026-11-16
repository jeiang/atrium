import { HttpResponse, http } from 'msw';
import type { ModuleStateState, Shell, WebSession, WebSignIn } from '../contract';
import { API_BASE } from '../contract';
import { decideElsewhere, type InboxScenario, inboxCount, inboxHandlers } from './inbox';
import { simulatorHandlers } from './simulator';

export interface MockScenario {
  session?: 'signed-in' | 'signed-out' | 'signed-out-proxy' | 'unavailable';
  signOut?: boolean;
  expireOn?: 'shell';
  needsYou?: number;
  modules?: Record<string, ModuleStateState>;
  mail?: { mode: string; paused: boolean };
  inbox?: InboxScenario;
}

export const SCENARIO_KEY = 'atrium.mock';
export const MOCK_EVENTS = 'atrium-mock-events';
const CSRF = 'mock-csrf-token';
const MODULES = ['mail', 'calendar', 'notion', 'media', 'assistant', 'notifications'];
const TOPICS = ['review', 'attention', 'effects', 'mail', 'settings', 'notify', 'connections'];
const encoder = new TextEncoder();

function scenario(): MockScenario {
  try {
    return JSON.parse(localStorage.getItem(SCENARIO_KEY) ?? '{}') as MockScenario;
  } catch {
    return {};
  }
}

function update(patch: MockScenario): void {
  localStorage.setItem(SCENARIO_KEY, JSON.stringify({ ...scenario(), ...patch }));
}

function signIn(proxy: boolean): WebSignIn {
  return proxy
    ? { modes: ['proxy'], csrf_token: CSRF }
    : { modes: ['oidc'], login_url: '/login?reason=expired', csrf_token: CSRF };
}

function unauthenticated(proxy = false): Response {
  return HttpResponse.json(
    { error: { code: 'unauthenticated', message: 'Sign in to continue.' }, sign_in: signIn(proxy) },
    { status: 401 },
  );
}

function session(state: MockScenario): WebSession {
  return {
    auth_mode: state.signOut === false ? 'proxy' : 'oidc',
    identity: { subject: 'operator', email: 'operator@example.test', name: 'Operator' },
    csrf_token: CSRF,
    sign_out: state.signOut === false ? null : { method: 'POST', url: `${API_BASE}/session/logout` },
  };
}

function shell(state: MockScenario): Shell {
  return {
    brand_url: '/',
    footer: [],
    groups: [],
    sections: [],
    settings: [],
    settings_url: '/settings',
    needs_you: state.needsYou ?? inboxCount(),
    notify_failing: [],
    mail: state.mail ?? { mode: 'live', paused: false },
    modules: MODULES.map((name) => ({ name, state: state.modules?.[name] ?? 'enabled' })),
    version: '0.0.0-mock',
  };
}

const revisions = new Map(TOPICS.map((topic) => [topic, 1]));
const counter = globalThis as typeof globalThis & { atriumMockStreams?: number };

export const handlers = [
  http.get(`*${API_BASE}/session`, () => {
    const state = scenario();
    if (state.session === 'unavailable') {
      return HttpResponse.json(
        { error: { code: 'auth_unavailable', message: 'Sign-in is unavailable.' } },
        { status: 503 },
      );
    }
    if (state.session === 'signed-out' || state.session === 'signed-out-proxy') {
      return unauthenticated(state.session === 'signed-out-proxy');
    }
    return HttpResponse.json(session(state));
  }),
  http.post(`*${API_BASE}/session/logout`, ({ request }) => {
    if (request.headers.get('X-CSRF-Token') !== CSRF) {
      return HttpResponse.json({ error: { code: 'csrf_failed', message: 'The form expired.' } }, { status: 403 });
    }
    update({ session: 'signed-out' });
    return new HttpResponse(null, { status: 204 });
  }),
  http.get(`*${API_BASE}/shell`, () => {
    const state = scenario();
    if (state.session && state.session !== 'signed-in') return unauthenticated();
    if (state.expireOn === 'shell') return unauthenticated();
    return HttpResponse.json(shell(state));
  }),
  http.get(`*${API_BASE}/events`, ({ request }) => {
    const channel = new BroadcastChannel(MOCK_EVENTS);
    let closed = false;
    const end = (): void => {
      if (closed) return;
      closed = true;
      channel.close();
    };
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        counter.atriumMockStreams = (counter.atriumMockStreams ?? 0) + 1;
        const send = (text: string): void => {
          if (!closed) controller.enqueue(encoder.encode(text));
        };
        const topics = [...revisions].map(([topic, revision]) => ({ topic, revision }));
        send(`retry: 1000\n\nevent: sync\ndata: ${JSON.stringify({ topics })}\n\n`);
        channel.addEventListener('message', (event: MessageEvent<{ topic: string; decide?: string }>) => {
          if (event.data.decide) decideElsewhere(event.data.decide);
          const revision = (revisions.get(event.data.topic) ?? 0) + 1;
          revisions.set(event.data.topic, revision);
          send(`event: change\ndata: ${JSON.stringify({ topic: event.data.topic, revision })}\n\n`);
        });
        request.signal.addEventListener('abort', end, { once: true });
        addEventListener('pagehide', end, { once: true });
      },
      cancel: end,
    });
    return new HttpResponse(stream, {
      headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' },
    });
  }),
  ...simulatorHandlers,
  ...inboxHandlers,
];
