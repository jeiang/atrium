import { onlineManager, QueryClient } from '@tanstack/svelte-query';
import { ApiError } from '../contract';

function retry(failures: number, error: unknown): boolean {
  if (error instanceof ApiError && error.status < 500 && error.status !== 429) return false;
  return failures < 2;
}

onlineManager.setEventListener(() => undefined);

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 15_000, retry },
    mutations: { retry: false, networkMode: 'always' },
  },
});
