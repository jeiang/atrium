import { flushSync } from 'svelte';
import type {
  RunStreamEvent,
  RunStreamEventDone,
  RunStreamEventRunDone,
  RunStreamEventRunError,
  RunStreamEventText,
  RunStreamEventThinking,
  RunStreamEventToolStart,
  RunStreamEventUser,
} from '../../contract';
import { webFetch } from '../../contract/fetcher';
import { readEventStream } from '../../contract/sse';
import { applyRunEvent } from '../../shared/assistant/toolcalls';
import type { ToolCall } from '../../shared/assistant/types';

export type Segment =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string }
  | { kind: 'tools'; ids: string[] }
  | { kind: 'user'; text: string };

export type StreamStatus =
  | 'connecting'
  | 'live'
  | 'retrying'
  | 'busy'
  | 'parked'
  | 'interrupted'
  | 'done'
  | 'failed'
  | 'stopped'
  | 'expired';

export const APPLY_MEASURE = 'atrium-run-apply';
const MAX_WAIT = 15_000;

export class RunStream {
  segments = $state<Segment[]>([]);
  calls = $state.raw<ToolCall[]>([]);
  status = $state<StreamStatus>('connecting');
  error = $state<string>();
  readonly thread: number;
  readonly run: number;
  #lastId = 0;
  #retry = 1000;
  #failures = 0;
  #queue: RunStreamEvent[] = [];
  #frame = 0;
  #follow = 0;
  #timer = 0;
  #abort: AbortController | undefined;
  #ended = $state(false);
  #onapply: () => void;
  #onend: (status: StreamStatus) => void;

  constructor(thread: number, run: number, handlers: { onapply: () => void; onend: (status: StreamStatus) => void }) {
    this.thread = thread;
    this.run = run;
    this.#onapply = handlers.onapply;
    this.#onend = handlers.onend;
  }

  get active(): boolean {
    return !this.#ended;
  }

  get resumable(): boolean {
    return this.#ended && (this.status === 'parked' || this.status === 'interrupted');
  }

  get phase(): string {
    if (this.status === 'parked') return 'Waiting for you';
    const running = this.calls.findLast((call) => call.state === 'running');
    if (running) return `Running ${running.name}`;
    return this.segments.at(-1)?.kind === 'text' ? 'Writing' : 'Thinking';
  }

  start(): void {
    this.#ended = false;
    this.status = 'connecting';
    void this.#connect();
  }

  dispose(): void {
    this.#abort?.abort();
    clearTimeout(this.#timer);
    cancelAnimationFrame(this.#frame);
    this.#frame = 0;
  }

  close(status?: StreamStatus): void {
    this.dispose();
    if (this.#queue.length > 0) this.#flush();
    if (status) this.status = status;
    this.#ended = true;
  }

  async #connect(): Promise<void> {
    const abort = new AbortController();
    this.#abort = abort;
    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    if (this.#lastId > 0) headers['Last-Event-ID'] = String(this.#lastId);
    let terminal = false;
    try {
      const res = await webFetch(`/threads/${this.thread}/runs/${this.run}/events`, {
        headers,
        signal: abort.signal,
      });
      if (this.#ended || abort.signal.aborted) return;
      if (res.status === 204) {
        this.#end('done');
        return;
      }
      if (res.status === 429) {
        this.status = 'busy';
        this.#again();
        return;
      }
      if (res.status === 410) {
        this.#end('expired');
        return;
      }
      if (!res.ok || !res.body) {
        const body = (await res.json().catch(() => undefined)) as { error?: { message?: string } } | undefined;
        this.error = body?.error?.message ?? 'The run could not be loaded.';
        this.#end('failed');
        return;
      }
      this.status = 'live';
      await readEventStream(res.body, {
        onRetry: (ms) => {
          this.#retry = ms;
        },
        onEvent: ({ event, data, id }) => {
          let parsed: unknown;
          try {
            parsed = JSON.parse(data);
          } catch {
            return;
          }
          if (id !== undefined) {
            const value = Number(id);
            if (value <= this.#lastId) return;
            this.#lastId = value;
          }
          this.#failures = 0;
          const next = { event, data: parsed, id } as RunStreamEvent;
          if (event === 'done') terminal = true;
          this.#queue.push(next);
          if (!this.#frame) this.#frame = requestAnimationFrame(() => this.#flush());
        },
      });
    } catch {
      if (this.#ended || abort.signal.aborted) return;
    }
    if (this.#ended || terminal || abort.signal.aborted) return;
    this.status = 'retrying';
    this.#again();
  }

  #again(): void {
    const wait = Math.min(MAX_WAIT, this.#retry * 2 ** this.#failures);
    this.#failures++;
    this.#timer = window.setTimeout(() => void this.#connect(), wait);
  }

  #end(status: StreamStatus): void {
    if (this.#ended) return;
    this.close(status);
    this.#onend(status);
  }

  #flush(): void {
    this.#frame = 0;
    const started = performance.now();
    const events = this.#queue;
    this.#queue = [];
    let calls = this.calls;
    let ended: StreamStatus | undefined;
    for (const event of events) {
      calls = applyRunEvent(calls, event, this.run);
      const data = event.data as { run_id?: number };
      if (data.run_id !== undefined && data.run_id !== this.run) continue;
      switch (event.event) {
        case 'text':
          this.#append('text', (event as RunStreamEventText).data.text);
          break;
        case 'thinking':
          this.#append('thinking', (event as RunStreamEventThinking).data.text);
          break;
        case 'tool_start':
          this.#tool((event as RunStreamEventToolStart).data.id);
          break;
        case 'user':
          this.segments.push({ kind: 'user', text: (event as RunStreamEventUser).data.text });
          break;
        case 'run_error':
          this.error = (event as RunStreamEventRunError).data.problem.message;
          break;
        case 'run_done': {
          const { state, error } = (event as RunStreamEventRunDone).data;
          if (state === 'failed' && error) this.error = error;
          break;
        }
        case 'done': {
          const { state } = (event as RunStreamEventDone).data;
          ended = state === 'parked' || state === 'interrupted' || state === 'done' ? state : 'failed';
          break;
        }
      }
    }
    this.calls = calls;
    flushSync();
    performance.measure(APPLY_MEASURE, { start: started, end: performance.now() });
    if (!this.#follow) {
      this.#follow = requestAnimationFrame(() => {
        this.#follow = 0;
        this.#onapply();
      });
    }
    if (ended) this.#end(ended);
  }

  #append(kind: 'text' | 'thinking', chunk: string): void {
    const last = this.segments.at(-1);
    if (last?.kind === kind) last.text += chunk;
    else this.segments.push({ kind, text: chunk });
  }

  #tool(id: string): void {
    const last = this.segments.at(-1);
    if (last?.kind === 'tools') {
      if (!last.ids.includes(id)) last.ids.push(id);
    } else {
      this.segments.push({ kind: 'tools', ids: [id] });
    }
  }
}
