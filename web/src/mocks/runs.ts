import type { RunStreamEvent } from '../contract';

export interface RunScript {
  thread: number;
  run: number;
  label: string;
  events: RunStreamEvent[];
  parked: boolean;
}

type Draft = RunStreamEvent extends infer E ? (E extends unknown ? Omit<E, 'id'> : never) : never;

function numbered(events: Draft[]): RunStreamEvent[] {
  let next = 0;
  return events.map((event) =>
    event.event === 'tool_progress' ? (event as RunStreamEvent) : ({ ...event, id: String(++next) } as RunStreamEvent),
  );
}

function start(thread: number, run: number, title: string): Draft {
  return {
    event: 'run_start',
    data: {
      run_id: run,
      thread_id: thread,
      thread_kind: 'chat',
      role: null,
      title,
      effort: 'high',
      parent_run_id: null,
    },
  };
}

export const runScripts: readonly RunScript[] = [
  {
    thread: 7,
    run: 101,
    label: 'Finishes',
    parked: false,
    events: numbered([
      start(7, 101, 'Plan the garden shed order'),
      { event: 'thinking', data: { run_id: 101, text: 'Checking the saved supplier list.' } },
      {
        event: 'plan',
        data: {
          run_id: 101,
          items: [
            { text: 'Read the supplier list', done: false },
            { text: 'Draft the order', done: false },
          ],
        },
      },
      {
        event: 'tool_start',
        data: { run_id: 101, id: 'call-1', name: 'search_web', args: '{"query":"cedar board prices Hillfield"}' },
      },
      { event: 'tool_progress', data: { run_id: 101, id: 'call-1', text: 'Searching the web for cedar board prices' } },
      { event: 'tool_progress', data: { run_id: 101, id: 'call-1', text: 'Reading 3 supplier pages' } },
      {
        event: 'tool_done',
        data: {
          run_id: 101,
          id: 'call-1',
          ok: true,
          preview: 'Found prices from 3 suppliers',
          results: [
            { title: 'Cedar & Pine Yard: board prices', url: 'https://cedar-pine.example/boards' },
            { title: '', url: 'https://hillfield-timber.example/price-list' },
            { title: 'Unsafe link that must stay text', url: 'javascript:alert(1)' },
          ],
        },
      },
      { event: 'text', data: { run_id: 101, text: 'The order needs **twelve** cedar boards ' } },
      { event: 'text', data: { run_id: 101, text: 'and one box of brass screws.' } },
      { event: 'run_done', data: { run_id: 101, state: 'done', error: null } },
      { event: 'complete', data: { run_id: 101, state: 'done', thread_id: 7 } },
    ]),
  },
  {
    thread: 7,
    run: 102,
    label: 'Fails',
    parked: false,
    events: numbered([
      start(7, 102, 'Refresh the weather card'),
      {
        event: 'tool_start',
        data: { run_id: 102, id: 'call-1', name: 'weather.today', args: '{"place":"Hillfield"}' },
      },
      { event: 'tool_done', data: { run_id: 102, id: 'call-1', ok: false, preview: 'The weather service timed out' } },
      { event: 'run_done', data: { run_id: 102, state: 'failed', error: 'The weather service timed out' } },
      {
        event: 'run_error',
        data: {
          run_id: 102,
          state: 'failed',
          problem: { code: 'tool_failed', message: 'The weather service timed out' },
          effect_ids: [],
        },
      },
    ]),
  },
  {
    thread: 8,
    run: 103,
    label: 'Parks',
    parked: true,
    events: numbered([
      start(8, 103, 'Send the book club reminder'),
      { event: 'text', data: { run_id: 103, text: 'The reminder is ready to send to 6 people.' } },
      {
        event: 'tool_start',
        data: { run_id: 103, id: 'call-1', name: 'mail.send', args: '{"to":"book-club@example.test"}' },
      },
    ]),
  },
];
