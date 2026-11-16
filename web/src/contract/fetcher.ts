import type { WebSignIn } from './gen/model';

export const API_BASE = '/web/v1';

const UNSAFE: Record<string, true> = { POST: true, PUT: true, PATCH: true, DELETE: true };
const NON_JSON_SUCCESS = ['application/octet-stream', 'text/event-stream'];

let csrfToken: string | undefined;
let unauthorized: ((signIn: WebSignIn | undefined) => void) | undefined;
let reached: ((ok: boolean) => void) | undefined;
let reachable = true;

function reach(ok: boolean): void {
  reachable = ok;
  reached?.(ok);
}

export const OFFLINE_MESSAGE = 'You are offline. Nothing was sent.';

export function setCsrfToken(token: string | undefined): void {
  csrfToken = token;
}

export function onUnauthorized(handler: (signIn: WebSignIn | undefined) => void): void {
  unauthorized = handler;
}

export function onReachability(handler: (ok: boolean) => void): void {
  reached = handler;
}

function signInOf(body: unknown): WebSignIn | undefined {
  if (typeof body !== 'object' || body === null || !('sign_in' in body)) return undefined;
  const signIn = body.sign_in;
  return typeof signIn === 'object' && signIn !== null ? (signIn as WebSignIn) : undefined;
}

export async function webFetch(url: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set('Atrium-Client', 'web');
  if (!headers.has('Accept')) headers.set('Accept', 'application/json');
  if (typeof init.body === 'string' && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
  const method = (init.method ?? 'GET').toUpperCase();
  if (UNSAFE[method]) {
    if (!reachable) throw new ApiError(0, { error: { code: 'offline', message: OFFLINE_MESSAGE } });
    if (!headers.has('Idempotency-Key')) headers.set('Idempotency-Key', crypto.randomUUID());
    if (csrfToken) headers.set('X-CSRF-Token', csrfToken);
  }
  let res: Response;
  try {
    res = await fetch(API_BASE + url, { ...init, headers, credentials: 'same-origin' });
  } catch (error) {
    if (!init.signal?.aborted) reach(false);
    throw error;
  }
  reach(true);
  if (res.status === 401 && !url.startsWith('/session') && unauthorized) {
    const type = res.headers.get('Content-Type') ?? '';
    const body = type.includes('json')
      ? await res
          .clone()
          .json()
          .catch(() => undefined)
      : undefined;
    unauthorized(signInOf(body));
  }
  return res;
}

export async function atriumFetch<T>(url: string, init: RequestInit = {}): Promise<T> {
  const res = await webFetch(url, init);
  const type = res.headers.get('Content-Type') ?? '';
  let data: unknown;
  if (res.status === 204 || res.status === 304) {
    data = undefined;
  } else if (type.includes('json')) {
    const text = await res.text();
    data = text ? JSON.parse(text) : undefined;
  } else if (!res.ok || NON_JSON_SUCCESS.some((t) => type.startsWith(t))) {
    data = await res.blob();
  } else {
    throw new ApiError(res.status, {
      error: {
        code: 'unexpected_content_type',
        message: `Expected JSON from ${url}, got ${type || 'no content type'}.`,
      },
    });
  }
  return { data, status: res.status, headers: res.headers } as T;
}

export interface ApiErrorBody {
  error: { code: string; message: string; retry_after?: number; effect_ids?: number[] };
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly body: unknown;
  constructor(status: number, body: unknown) {
    const env =
      typeof body === 'object' && body !== null && 'error' in body
        ? (body.error as Partial<ApiErrorBody['error']> | undefined)
        : undefined;
    super(env?.message ?? `HTTP ${status}`);
    this.name = 'ApiError';
    this.status = status;
    this.code = env?.code ?? `http_${status}`;
    this.body = body;
  }
}

type Success<R> = R extends { status: 200 | 201 | 202 | 204; data: infer D } ? D : never;

export function ok<R extends { status: number; data: unknown }>(res: R): Success<R> {
  if (res.status >= 200 && res.status < 300) return res.data as Success<R>;
  throw new ApiError(res.status, res.data);
}
