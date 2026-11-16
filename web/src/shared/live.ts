import type { QueryClient } from '@tanstack/svelte-query';
import type { AppNotice } from '../contract';

declare module '@tanstack/svelte-query' {
  interface Register {
    queryMeta: { topics?: readonly string[]; persist?: boolean };
  }
}

export function topicInvalidator(client: QueryClient): (notice: AppNotice) => void {
  const revisions = new Map<string, number>();
  const invalidate = (topic: string): void => {
    void client.invalidateQueries({ predicate: (query) => query.meta?.topics?.includes(topic) ?? false });
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
