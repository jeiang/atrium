import { delay, HttpResponse, http } from 'msw';
import type { Token } from '../contract';
import { API_BASE } from '../contract';

export interface TokensScenario {
  fail?: boolean;
  empty?: boolean;
  slow?: boolean;
  lose?: boolean;
}

export interface TokensRequest {
  method: string;
  path: string;
  key: string | null;
  body: unknown;
}

const log = globalThis as typeof globalThis & { atriumTokenRequests?: TokensRequest[] };
const DAY = 86_400_000;
function at(offset: number): string {
  return new Date(Date.now() + offset).toISOString();
}

function seed(state: TokensScenario): { tokens: Token[]; next: number; serial: number } {
  if (state.empty) return { tokens: [], next: 1, serial: 0 };
  return {
    next: 5,
    serial: 0,
    tokens: [
      {
        id: 1,
        kind: 'local',
        name: 'Shortcuts on my phone',
        subject: '',
        created_at: at(-40 * DAY),
        expires_at: null,
        last_used_at: at(-2 * 3_600_000),
        revoked_at: null,
        agent: false,
      },
      {
        id: 2,
        kind: 'local',
        name: 'Garden sensor bridge',
        subject: '',
        created_at: at(-12 * DAY),
        expires_at: at(60 * DAY),
        last_used_at: null,
        revoked_at: null,
        agent: true,
      },
      {
        id: 3,
        kind: 'oidc',
        name: 'OIDC access token',
        subject: 'sam@example.test',
        created_at: at(-3 * DAY),
        expires_at: at(DAY),
        last_used_at: at(-DAY),
        revoked_at: null,
        agent: false,
      },
      {
        id: 4,
        kind: 'local',
        name: 'Old laptop',
        subject: '',
        created_at: at(-200 * DAY),
        expires_at: null,
        last_used_at: at(-90 * DAY),
        revoked_at: at(-80 * DAY),
        agent: false,
      },
    ],
  };
}

let store = seed({});
let scenarioText = '';

function scenario(read: () => { tokens?: TokensScenario }): TokensScenario {
  const state = read().tokens ?? {};
  const text = JSON.stringify(state);
  if (text !== scenarioText) {
    scenarioText = text;
    store = seed(state);
  }
  return state;
}

async function record(request: Request): Promise<unknown> {
  const text = await request.clone().text();
  const body = text ? JSON.parse(text) : null;
  log.atriumTokenRequests = [
    ...(log.atriumTokenRequests ?? []),
    {
      method: request.method,
      path: new URL(request.url).pathname,
      key: request.headers.get('Idempotency-Key'),
      body,
    },
  ];
  return body;
}

function refusal(status: number, code: string, message: string): Response {
  return HttpResponse.json({ error: { code, message } }, { status });
}

function secret(id: number, serial: number): string {
  return `atr_mock${String(id).padStart(4, '0')}${'x'.repeat(30)}${serial}`;
}

export function tokenHandlers(read: () => { tokens?: TokensScenario }) {
  return [
    http.get(`*${API_BASE}/auth/tokens`, async () => {
      const state = scenario(read);
      if (state.slow) await delay(1500);
      if (state.fail) return refusal(500, 'internal', 'The token store did not answer.');
      return HttpResponse.json(
        { revision: String(store.serial), data: { tokens: store.tokens } },
        { headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.post(`*${API_BASE}/auth/tokens`, async ({ request }) => {
      const state = scenario(read);
      const body = ((await record(request)) ?? {}) as { name?: string; expires_at?: string; agent?: boolean };
      await delay(200);
      const name = (body.name ?? '').trim();
      if (name === '' || [...name].length > 100) {
        return refusal(422, 'invalid', 'A name of at most 100 characters is required.');
      }
      const expires = body.expires_at ? Date.parse(body.expires_at) : undefined;
      if (expires !== undefined && !(expires > Date.now())) {
        return refusal(422, 'invalid', 'The expiry must be in the future.');
      }
      const token: Token = {
        id: store.next,
        kind: 'local',
        name,
        subject: '',
        created_at: new Date().toISOString(),
        expires_at: expires === undefined ? null : new Date(expires).toISOString(),
        last_used_at: null,
        revoked_at: null,
        agent: body.agent ?? false,
      };
      store.next += 1;
      store.serial += 1;
      store.tokens = [...store.tokens, token];
      if (state.lose) return HttpResponse.error();
      return HttpResponse.json(
        { data: { token, secret: secret(token.id, store.serial) }, audit_id: store.serial },
        { status: 201, headers: { 'Cache-Control': 'no-store' } },
      );
    }),
    http.delete(`*${API_BASE}/auth/tokens/:token`, async ({ params, request }) => {
      const state = scenario(read);
      await record(request);
      await delay(200);
      const id = Number(params.token);
      const found = store.tokens.find((token) => token.id === id);
      if (!found) return refusal(404, 'not_found', `no token ${id}`);
      if (!found.revoked_at) {
        store.serial += 1;
        store.tokens = store.tokens.map((token) =>
          token.id === id ? { ...token, revoked_at: new Date().toISOString() } : token,
        );
      }
      if (state.lose) return HttpResponse.error();
      return new HttpResponse(null, { status: 204 });
    }),
  ];
}
