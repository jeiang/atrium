import { createInfiniteQuery, createQuery } from '@tanstack/svelte-query';
import {
  API_BASE,
  assistantFileLibrary,
  assistantFileThreadList,
  assistantFileThreadPreview,
  getAssistantFileThreadDownloadGetUrl,
  getKnowledgeSpaceFileDownloadGetUrl,
  knowledgeSpaceFilePreview,
  knowledgeSpaceFiles,
  type LibraryEntry,
  ok,
} from '../../contract';
import { p } from '../../shell/router';

const PAGE = 50;

export interface FileOwner {
  kind: 'thread' | 'space';
  id: number;
}

export function sizeText(bytes: number): string {
  if (bytes < 1024) return `${bytes} bytes`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

export function contentUrl(owner: FileOwner, file: number): string {
  return (
    API_BASE +
    (owner.kind === 'thread'
      ? getAssistantFileThreadDownloadGetUrl(owner.id, file)
      : getKnowledgeSpaceFileDownloadGetUrl(owner.id, file))
  );
}

export function previewHref(owner: FileOwner, file: number): string {
  return owner.kind === 'thread'
    ? p('/chat/:id/files/:file', { params: { id: String(owner.id), file: String(file) } })
    : p('/knowledge/spaces/:space/files/:file', { params: { space: String(owner.id), file: String(file) } });
}

export function libraryOwner(entry: LibraryEntry): FileOwner | undefined {
  if (entry.thread_id !== undefined) return { kind: 'thread', id: entry.thread_id };
  if (entry.project_id !== undefined) return { kind: 'space', id: entry.project_id };
  return undefined;
}

export function useLibrary() {
  return createInfiniteQuery(() => ({
    queryKey: ['knowledge', 'files', 'library'],
    queryFn: async ({ pageParam, signal }) =>
      ok(await assistantFileLibrary({ cursor: pageParam || undefined, limit: PAGE }, { signal })),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    meta: { topics: ['knowledge', 'assistant'] },
  }));
}

export function useOwnerFiles(owner: () => FileOwner | undefined) {
  return createInfiniteQuery(() => {
    const at = owner();
    return {
      queryKey: ['knowledge', 'files', at?.kind, at?.id],
      queryFn: async ({ pageParam, signal }: { pageParam: string; signal: AbortSignal }) => {
        const params = { cursor: pageParam || undefined, limit: PAGE };
        const found = at as FileOwner;
        return found.kind === 'thread'
          ? ok(await assistantFileThreadList(found.id, params, { signal }))
          : ok(await knowledgeSpaceFiles(found.id, params, { signal }));
      },
      initialPageParam: '',
      getNextPageParam: (last: { next_cursor?: string | null }) => last.next_cursor ?? undefined,
      enabled: at !== undefined,
      meta: { topics: ['knowledge', 'assistant'] },
    };
  });
}

export function usePreview(owner: () => FileOwner | undefined, file: () => number | undefined) {
  return createQuery(() => {
    const at = owner();
    const id = file();
    return {
      queryKey: ['knowledge', 'files', at?.kind, at?.id, 'preview', id],
      queryFn: async ({ signal }: { signal: AbortSignal }) => {
        const found = at as FileOwner;
        return found.kind === 'thread'
          ? ok(await assistantFileThreadPreview(found.id, id as number, { signal }))
          : ok(await knowledgeSpaceFilePreview(found.id, id as number, { signal }));
      },
      enabled: at !== undefined && id !== undefined,
      meta: { topics: ['knowledge', 'assistant'] },
    };
  });
}
