import { ApiError, apiUrl } from '../contract';

export interface StreamFrame {
  id?: string;
  event: string;
  data: string;
}

export type StreamEnd =
  | { kind: 'finished'; lastEventId: string | undefined }
  | { kind: 'expired'; lastEventId: string | undefined }
  | { kind: 'aborted'; lastEventId: string | undefined }
  | { kind: 'error'; error: unknown; lastEventId: string | undefined };

export type StreamStatus = 'connecting' | 'open' | 'reconnecting' | 'closed';

export interface EventStreamOptions {
  path: string;
  after?: string;
  onFrame(frame: StreamFrame): boolean | undefined;
  onStatus?(status: StreamStatus): void;
  signal?: AbortSignal;
  maxRetries?: number;
  idleTimeoutMs?: number;
}

export interface EventStreamHandle {
  readonly done: Promise<StreamEnd>;
  lastEventId(): string | undefined;
  close(): void;
}

export function createSseParser(onFrame: (frame: StreamFrame) => void, onRetry: (ms: number) => void) {
  let buffer = '';
  let data: string[] = [];
  let event = '';
  let id: string | undefined;
  let first = true;
  const line = (raw: string) => {
    if (raw === '') {
      if (data.length > 0) onFrame({ id, event: event || 'message', data: data.join('\n') });
      data = [];
      event = '';
      id = undefined;
      return;
    }
    if (raw.startsWith(':')) return;
    const colon = raw.indexOf(':');
    const field = colon === -1 ? raw : raw.slice(0, colon);
    let value = colon === -1 ? '' : raw.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'data') data.push(value);
    else if (field === 'event') event = value;
    else if (field === 'id' && !value.includes('\0')) id = value;
    else if (field === 'retry' && /^\d+$/.test(value)) onRetry(Number(value));
  };
  return {
    push(chunk: string) {
      buffer += chunk;
      if (first && buffer.length > 0) {
        if (buffer.charCodeAt(0) === 0xfeff) buffer = buffer.slice(1);
        first = false;
      }
      let start = 0;
      for (let i = 0; i < buffer.length; i++) {
        const c = buffer[i];
        if (c !== '\n' && c !== '\r') continue;
        if (c === '\r' && i === buffer.length - 1) break;
        line(buffer.slice(start, i));
        if (c === '\r' && buffer[i + 1] === '\n') i++;
        start = i + 1;
      }
      buffer = buffer.slice(start);
    },
  };
}

export function readEventStream(opts: EventStreamOptions): EventStreamHandle {
  const maxRetries = opts.maxRetries ?? 5;
  const idleTimeoutMs = opts.idleTimeoutMs ?? 45_000;
  const outer = new AbortController();
  const abortOuter = () => outer.abort();
  opts.signal?.addEventListener('abort', abortOuter, { once: true });
  if (opts.signal?.aborted) outer.abort();

  let last = opts.after;
  let retryMs = 1000;
  let failures = 0;
  const url = apiUrl(opts.path);

  const sleep = (ms: number) => {
    const { promise, resolve } = Promise.withResolvers<void>();
    const timer = setTimeout(resolve, ms);
    const stop = () => {
      clearTimeout(timer);
      resolve();
    };
    outer.signal.addEventListener('abort', stop, { once: true });
    return promise;
  };

  async function connectOnce(): Promise<StreamEnd | 'retry'> {
    const attempt = new AbortController();
    const abortAttempt = () => attempt.abort();
    outer.signal.addEventListener('abort', abortAttempt, { once: true });
    let idle: ReturnType<typeof setTimeout> | undefined;
    const arm = () => {
      clearTimeout(idle);
      idle = setTimeout(abortAttempt, idleTimeoutMs);
    };
    try {
      const headers: Record<string, string> = { Accept: 'text/event-stream', 'Atrium-Client': 'web' };
      if (last !== undefined) headers['Last-Event-ID'] = last;
      arm();
      const res = await fetch(url, { headers, credentials: 'same-origin', cache: 'no-store', signal: attempt.signal });
      if (res.status === 204) return { kind: 'finished', lastEventId: last };
      if (res.status === 410) return { kind: 'expired', lastEventId: last };
      if (res.status === 429 || res.status === 503) {
        const after = Number(res.headers.get('Retry-After'));
        if (Number.isFinite(after) && after > 0) retryMs = after * 1000;
        return 'retry';
      }
      if (!res.ok || !res.body) {
        const body: unknown = await res.json().catch(() => null);
        return { kind: 'error', error: new ApiError(res.status, body), lastEventId: last };
      }
      opts.onStatus?.('open');
      let finished = false;
      const parser = createSseParser(
        (frame) => {
          if (finished) return;
          if (frame.id !== undefined) last = frame.id;
          failures = 0;
          if (opts.onFrame(frame)) finished = true;
        },
        (ms) => {
          retryMs = ms;
        },
      );
      const reader = res.body.pipeThrough(new TextDecoderStream()).getReader();
      while (!finished) {
        const { value, done } = await reader.read();
        if (done) break;
        arm();
        parser.push(value);
      }
      if (!finished) return 'retry';
      reader.cancel().catch(() => {});
      return { kind: 'finished', lastEventId: last };
    } catch (error) {
      if (outer.signal.aborted) return { kind: 'aborted', lastEventId: last };
      if (attempt.signal.aborted) return 'retry';
      return { kind: 'error', error, lastEventId: last };
    } finally {
      clearTimeout(idle);
      outer.signal.removeEventListener('abort', abortAttempt);
    }
  }

  const done = (async (): Promise<StreamEnd> => {
    opts.onStatus?.('connecting');
    try {
      for (;;) {
        const result = await connectOnce();
        if (outer.signal.aborted) return { kind: 'aborted', lastEventId: last };
        if (result !== 'retry') return result;
        failures++;
        if (failures > maxRetries) {
          return { kind: 'error', error: new Error('event stream: retries exhausted'), lastEventId: last };
        }
        opts.onStatus?.('reconnecting');
        await sleep(retryMs);
      }
    } finally {
      opts.signal?.removeEventListener('abort', abortOuter);
      opts.onStatus?.('closed');
    }
  })();

  return { done, lastEventId: () => last, close: abortOuter };
}
