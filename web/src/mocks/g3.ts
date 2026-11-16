import type { RunStreamEvent } from '../contract';

export const G3_THREAD = 50;
export const G3_RUN = 900;
export const G3_EVENTS = 2000;
export const G3_PER_SECOND = 100;
export const G3_STREAM_PATH = `/web/v1/threads/${G3_THREAD}/runs/${G3_RUN}/events`;

type Unnumbered = Omit<RunStreamEvent, 'id'>;

function event(index: number, call: number): Unnumbered {
  if (index % 50 === 1) {
    return {
      event: 'tool_start',
      data: { run_id: G3_RUN, id: `call-${call}`, name: 'search_web', args: `{"query":"winter tyre quote ${call}"}` },
    } as Unnumbered;
  }
  if (index % 50 === 6) {
    return {
      event: 'tool_done',
      data: { run_id: G3_RUN, id: `call-${call}`, ok: true, preview: `Read quote ${call}` },
    } as Unnumbered;
  }
  if (index % 25 === 0) return { event: 'text', data: { run_id: G3_RUN, text: `\n\n## Garage ${index / 25}\n\n` } };
  if (index % 7 === 0) {
    return { event: 'text', data: { run_id: G3_RUN, text: `\n- set ${index} costs **${index % 97} units** ` } };
  }
  return { event: 'text', data: { run_id: G3_RUN, text: `word${index} ` } };
}

export function g3Events(): RunStreamEvent[] {
  const events: Unnumbered[] = [
    {
      event: 'run_start',
      data: {
        run_id: G3_RUN,
        thread_id: G3_THREAD,
        thread_kind: 'chat',
        role: null,
        title: 'Compare the winter tyre quotes',
        effort: 'high',
        parent_run_id: null,
      },
    } as Unnumbered,
  ];
  let call = 0;
  while (events.length < G3_EVENTS - 2) {
    const index = events.length;
    if (index % 50 === 1) call++;
    events.push(event(index, call));
  }
  events.push({ event: 'run_done', data: { run_id: G3_RUN, state: 'done', error: null } } as Unnumbered);
  events.push({ event: 'complete', data: { run_id: G3_RUN, state: 'done', thread_id: G3_THREAD } } as Unnumbered);
  return events.map((entry, index) => ({ ...entry, id: String(index + 1) }) as RunStreamEvent);
}

export function sseFrame(entry: { event: string; data: unknown; id?: string }): string {
  const id = entry.id !== undefined ? `id: ${entry.id}\n` : '';
  return `${id}event: ${entry.event}\ndata: ${JSON.stringify(entry.data)}\n\n`;
}
