import type { Query, QueryClient } from '@tanstack/svelte-query';
import type { AppNotice } from '../contract';

declare module '@tanstack/svelte-query' {
  interface Register {
    queryMeta: { topics?: readonly string[]; persist?: boolean };
  }
}

const TOPIC_SPACING_MS = 2_000;

export function topicInvalidator(client: QueryClient): (notice: AppNotice) => void {
  const revisions = new Map<string, number>();
  const behind = new Set<string>();
  const timers = new Map<string, number>();

  const schedule = (query: Query): void => {
    if (query.state.fetchStatus !== 'idle') {
      behind.add(query.queryHash);
      return;
    }
    if (timers.has(query.queryHash)) return;
    const settled = Math.max(query.state.dataUpdatedAt, query.state.errorUpdatedAt);
    const wait = Math.max(0, settled + TOPIC_SPACING_MS - Date.now());
    timers.set(
      query.queryHash,
      window.setTimeout(() => {
        timers.delete(query.queryHash);
        void client.invalidateQueries({ queryKey: query.queryKey, exact: true }, { cancelRefetch: false });
      }, wait),
    );
  };

  client.getQueryCache().subscribe((event) => {
    if (event.type !== 'updated' || event.query.state.fetchStatus !== 'idle') return;
    if (behind.delete(event.query.queryHash)) schedule(event.query);
  });

  const invalidate = (topic: string): void => {
    for (const query of client.getQueryCache().findAll({
      predicate: (query) => query.meta?.topics?.includes(topic) ?? false,
    })) {
      if (query.isActive()) schedule(query);
      else query.invalidate();
    }
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
