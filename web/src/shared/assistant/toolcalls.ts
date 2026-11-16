import type {
  RunStreamEvent,
  RunStreamEventDone,
  RunStreamEventRunDone,
  RunStreamEventToolDone,
  RunStreamEventToolProgress,
  RunStreamEventToolStart,
} from '../../contract';
import type { ToolCall } from './types';

function own(event: RunStreamEvent, run: number): boolean {
  const data = event.data as { run_id?: unknown };
  return data.run_id === undefined || data.run_id === run;
}

export function applyRunEvent(calls: ToolCall[], event: RunStreamEvent, run: number): ToolCall[] {
  if (!own(event, run)) return calls;
  switch (event.event) {
    case 'tool_start': {
      const { id, name, args } = (event as RunStreamEventToolStart).data;
      if (calls.some((call) => call.id === id)) return calls;
      return [...calls, { id, name, args, state: 'running' }];
    }
    case 'tool_progress': {
      const { id, text } = (event as RunStreamEventToolProgress).data;
      return calls.map((call) => (call.id === id && call.state === 'running' ? { ...call, progress: text } : call));
    }
    case 'tool_done': {
      const { id, ok, preview, results } = (event as RunStreamEventToolDone).data;
      return calls.map((call) =>
        call.id === id
          ? {
              id: call.id,
              name: call.name,
              args: call.args,
              state: ok ? 'done' : 'failed',
              preview,
              results: results?.map((item) => ({ title: item.title || item.url, url: item.url })),
            }
          : call,
      );
    }
    case 'run_done': {
      const { state } = (event as RunStreamEventRunDone).data;
      if (state !== 'failed') return calls;
      return calls.map((call) => (call.state === 'running' ? { ...call, state: 'failed', progress: undefined } : call));
    }
    case 'done': {
      const { state } = (event as RunStreamEventDone).data;
      if (state !== 'parked') return calls;
      return calls.map((call) =>
        call.state === 'running' ? { ...call, state: 'waiting', progress: undefined } : call,
      );
    }
    default:
      return calls;
  }
}
