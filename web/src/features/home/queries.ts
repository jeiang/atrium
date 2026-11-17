import { assistantHome, ok, reviewPending } from '../../contract';

const HOME_TOPICS = ['assistant', 'settings', 'calendar', 'tasks', 'attention', 'mail', 'knowledge'] as const;
const PENDING_TOPICS = [
  'review',
  'effects',
  'mail',
  'calendar',
  'tasks',
  'assistant',
  'settings',
  'connections',
] as const;

export const homeQuery = {
  queryKey: ['home'],
  queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await assistantHome(undefined, { signal })),
  meta: { topics: HOME_TOPICS },
};

export const pendingQuery = {
  queryKey: ['pending'],
  queryFn: async ({ signal }: { signal: AbortSignal }) => ok(await reviewPending({ signal })),
  meta: { topics: PENDING_TOPICS },
};
