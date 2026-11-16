import type { Query, QueryClient } from '@tanstack/svelte-query';
import type { AppNotice } from '../contract';

declare module '@tanstack/svelte-query' {
  interface Register {
    queryMeta: { topics?: readonly string[]; persist?: boolean };
  }
}

export function topicInvalidator(client: QueryClient): (notice: AppNotice) => void {
  const revisions = new Map<string, number>();
  const invalidate = (topic: string): void => {
    const covers = (query: Query): boolean => query.meta?.topics?.includes(topic) ?? false;
    // A refetch joins a first load still in flight instead of restarting it,
    // so that load could answer from before the change. Cancel those loads
    // first (back to pending, no error), then refetch.
    void client
      .cancelQueries({
        predicate: (query) => covers(query) && query.state.fetchStatus === 'fetching' && query.state.data === undefined,
      })
      .then(() => client.invalidateQueries({ predicate: covers }));
  };
  return (notice) => {
    if (notice.kind === 'change') {
      const known = revisions.get(notice.topic);
      if (known !== undefined && notice.revision <= known) return;
      revisions.set(notice.topic, notice.revision);
      invalidate(notice.topic);
      return;
    }
    for (const { topic, revision } of notice.topics) {
      const known = revisions.get(topic);
      revisions.set(topic, revision);
      if (known !== undefined && known !== revision) invalidate(topic);
    }
  };
}
