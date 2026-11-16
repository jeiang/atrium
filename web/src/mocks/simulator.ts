import { HttpResponse, http } from 'msw';
import { API_BASE } from '../contract';
import { sseFrame } from './g3';
import { extraRunScripts, type RunScript, runScripts } from './runs';

const encoder = new TextEncoder();
const STEP_MS = 400;

function problem(status: number, code: string, message: string, extra: Record<string, unknown> = {}): Response {
  return HttpResponse.json({ error: { code, message }, ...extra }, { status });
}

function terminalState(script: RunScript): string {
  const last = script.events.at(-1);
  if (last?.event === 'complete') return 'done';
  if (last?.event === 'run_error') return 'failed';
  return 'parked';
}

function parseCursor(value: string | null): number | undefined {
  if (value === null) return 0;
  return /^(0|[1-9][0-9]*)$/.test(value) ? Number(value) : undefined;
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  const { promise, resolve } = Promise.withResolvers<void>();
  if (ms <= 0 || signal.aborted) {
    resolve();
    return promise;
  }
  const handle = setTimeout(resolve, ms);
  signal.addEventListener(
    'abort',
    () => {
      clearTimeout(handle);
      resolve();
    },
    { once: true },
  );
  return promise;
}

export function findRunScript(thread: number, run: number): RunScript | undefined {
  return [...runScripts, ...extraRunScripts].find((script) => script.thread === thread && script.run === run);
}

export function simulateRun(request: Request, thread: number, run: number): Response {
  const script = findRunScript(thread, run);
  if (!script) return problem(404, 'not_found', 'This run does not exist.');

  const url = new URL(request.url);
  const fault = url.searchParams.get('fault') ?? '';
  if (fault === 'busy') return problem(429, 'rate_limited', 'Too many open streams.');
  if (fault === 'expired') {
    return problem(410, 'stream_expired', 'This stream is no longer kept.', { run_id: script.run });
  }

  const header = request.headers.get('Last-Event-ID');
  const cursor = parseCursor(header ?? url.searchParams.get('after'));
  const total = script.events.filter((event) => 'id' in event && event.id !== undefined).length;
  if (cursor === undefined || cursor > total) return problem(400, 'bad_request', 'The cursor is not valid.');
  if (!script.parked && cursor >= total) return new HttpResponse(null, { status: 204 });

  const speed = Number(url.searchParams.get('speed') ?? '1');
  const step = speed > 0 ? STEP_MS / speed : 0;
  const drop = /^drop:([1-9][0-9]*)$/.exec(fault);
  const dropAfter = drop ? Number(drop[1]) : Number.POSITIVE_INFINITY;
  const from = cursor === 0 ? 0 : script.events.findIndex((event) => 'id' in event && event.id === String(cursor)) + 1;
  const pending = script.events.slice(from);

  let sent = -1;
  const body = new ReadableStream<Uint8Array>(
    {
      async pull(controller) {
        if (sent === -1) {
          controller.enqueue(encoder.encode('retry: 1000\n\n'));
          sent = 0;
          return;
        }
        const event = pending[sent];
        if (!event) {
          controller.enqueue(encoder.encode(sseFrame({ event: 'done', data: { state: terminalState(script) } })));
          controller.close();
          return;
        }
        await sleep(step, request.signal);
        if (request.signal.aborted) return;
        if (sent >= dropAfter) {
          controller.close();
          return;
        }
        controller.enqueue(encoder.encode(sseFrame(event)));
        sent++;
      },
    },
    { highWaterMark: 0 },
  );

  return new HttpResponse(body, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' },
  });
}

export const simulatorHandlers = [
  http.get(`*${API_BASE}/threads/:thread/runs/:run/events`, ({ request, params }) =>
    simulateRun(request, Number(params.thread), Number(params.run)),
  ),
];
