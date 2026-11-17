import { infiniteQueryOptions } from '@tanstack/svelte-query';
import { assistantThreadTranscript, ok } from '../../contract';
import { queryClient } from '../../shared/query';

const PAGE = 50;

export const threadQuery = (id: number | undefined) =>
  infiniteQueryOptions({
    queryKey: ['thread', id],
    queryFn: async ({ pageParam, signal }) =>
      ok(await assistantThreadTranscript(id ?? 0, { limit: PAGE, cursor: pageParam || undefined }, { signal })),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    meta: { topics: ['assistant'] },
  });

export const prefetchThread = (id: number): Promise<void> => queryClient.prefetchInfiniteQuery(threadQuery(id));
