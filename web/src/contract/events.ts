import { webFetch } from './fetcher';
import { readEventStream, type SseEvent } from './sse';

export type AppNotice =
  | { kind: 'sync'; topics: { topic: string; revision: number }[] }
  | { kind: 'change'; topic: string; revision: number };

export interface AppEventsHandlers {
  onNotice(notice: AppNotice): void;
}

const NAME = 'atrium-app-events';
const MIN_DELAY_MS = 1_000;
const MAX_DELAY_MS = 30_000;
const BUSY_DELAY_MS = 15_000;

function revisionOf(value: unknown): { topic: string; revision: number } | undefined {
  if (typeof value !== 'object' || value === null) return undefined;
  const { topic, revision } = value as Record<string, unknown>;
  return typeof topic === 'string' && typeof revision === 'number' ? { topic, revision } : undefined;
}

function parse(event: SseEvent): AppNotice | undefined {
  let data: unknown;
  try {
    data = JSON.parse(event.data);
  } catch {
    return undefined;
  }
  if (event.event === 'change') {
    const change = revisionOf(data);
    return change ? { kind: 'change', ...change } : undefined;
  }
  if (event.event === 'sync' && typeof data === 'object' && data !== null && 'topics' in data) {
    const list = Array.isArray(data.topics) ? data.topics : [];
    return { kind: 'sync', topics: list.map(revisionOf).filter((item) => item !== undefined) };
  }
  return undefined;
}

function sleep(ms: number, signal: AbortSignal): Promise<boolean> {
  const { promise, resolve } = Promise.withResolvers<boolean>();
  const done = new AbortController();
  const finish = (online: boolean): void => {
    clearTimeout(timer);
    done.abort();
    resolve(online);
  };
  const timer = navigator.onLine ? setTimeout(() => finish(false), ms) : undefined;
  addEventListener('online', () => finish(true), { once: true, signal: done.signal });
  signal.addEventListener('abort', () => finish(false), { once: true, signal: done.signal });
  return promise;
}

async function stream(signal: AbortSignal, emit: (notice: AppNotice) => void): Promise<void> {
  let delay = MIN_DELAY_MS;
  while (!signal.aborted) {
    if (!navigator.onLine) {
      await sleep(0, signal);
      delay = MIN_DELAY_MS;
      continue;
    }
    let synced = false;
    let busy = false;
    try {
      const res = await webFetch('/events', { headers: { Accept: 'text/event-stream' }, signal });
      if (res.status === 401) return;
      if (res.ok && res.body) {
        await readEventStream(res.body, {
          onEvent(event) {
            const notice = parse(event);
            if (!notice) return;
            if (notice.kind === 'sync') synced = true;
            emit(notice);
          },
        });
      } else {
        busy = res.status === 429;
        await res.body?.cancel();
      }
    } catch {
      if (signal.aborted) return;
    }
    if (synced) delay = MIN_DELAY_MS;
    const wait = busy ? Math.max(delay, BUSY_DELAY_MS) : delay;
    if (await sleep(wait * (0.8 + Math.random() * 0.4), signal)) delay = MIN_DELAY_MS;
    else delay = Math.min(delay * 2, MAX_DELAY_MS);
  }
}

export function startAppEvents(handlers: AppEventsHandlers): () => void {
  const stop = new AbortController();
  const channel = typeof BroadcastChannel === 'function' ? new BroadcastChannel(NAME) : undefined;
  channel?.addEventListener(
    'message',
    (event: MessageEvent<AppNotice>) => {
      if (!stop.signal.aborted) handlers.onNotice(event.data);
    },
    { signal: stop.signal },
  );
  const lead = (): Promise<void> =>
    stream(stop.signal, (notice) => {
      if (stop.signal.aborted) return;
      handlers.onNotice(notice);
      channel?.postMessage(notice);
    });
  if (channel && 'locks' in navigator) {
    navigator.locks.request(NAME, { signal: stop.signal }, lead).catch(() => undefined);
  } else {
    void lead();
  }
  const end = (): void => {
    if (stop.signal.aborted) return;
    stop.abort();
    channel?.close();
  };
  addEventListener('pagehide', end, { signal: stop.signal });
  return end;
}
