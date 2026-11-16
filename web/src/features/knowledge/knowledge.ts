import { createInfiniteQuery, createQuery, type QueryClient } from '@tanstack/svelte-query';
import {
  ApiError,
  type ChangeKnowledgeChanged,
  type ChangeKnowledgeLink,
  type KnowledgeLink,
  type KnowledgeLinkRequestRelation,
  type KnowledgeLinkRequestTargetKind,
  type KnowledgeSubjectsKind,
  type KnowledgeSubjectsScope,
  knowledgeConfirm,
  knowledgeDetail,
  knowledgeEdit,
  knowledgeInstructions,
  knowledgeLink,
  knowledgeLinks,
  knowledgeRestore,
  knowledgeRetract,
  knowledgeSubject,
  knowledgeSubjects,
  knowledgeUnlink,
  ok,
  reviewUndoEffect,
  searchAll,
} from '../../contract';
import type { ListOption } from '../../shared/ui/Listbox.svelte';

export const TOPICS = ['knowledge'];
const LINK_TOPICS = ['knowledge', 'tasks'];
const PAGE = 50;

export const KINDS: Record<string, string> = { fact: 'Fact', lesson: 'Lesson', wiki: 'Page' };

export const RELATIONS: readonly { value: KnowledgeLinkRequestRelation; label: string }[] = [
  { value: 'about', label: 'About' },
  { value: 'part_of', label: 'Part of' },
  { value: 'works_with', label: 'Works with' },
  { value: 'depends_on', label: 'Depends on' },
  { value: 'see_also', label: 'See also' },
];

export interface SubjectFilter {
  q: string;
  kind: '' | KnowledgeSubjectsKind;
  scope: '' | KnowledgeSubjectsScope;
}

export function relationLabel(relation: string): string {
  return RELATIONS.find((item) => item.value === relation)?.label ?? relation.replaceAll('_', ' ');
}

export function writerLabel(writer: string): string {
  if (writer === 'operator') return 'you';
  if (writer === 'assistant') return 'the assistant';
  return writer;
}

export function isStale(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 409 || error.status === 412);
}

export function useSubjects(filter: () => SubjectFilter) {
  return createInfiniteQuery(() => {
    const { q, kind, scope } = filter();
    return {
      queryKey: ['knowledge', 'subjects', q, kind, scope],
      queryFn: async ({ pageParam, signal }) =>
        ok(
          await knowledgeSubjects(
            {
              q: q || undefined,
              kind: kind || undefined,
              scope: scope || undefined,
              cursor: pageParam || undefined,
              limit: PAGE,
            },
            { signal },
          ),
        ).data,
      initialPageParam: '',
      getNextPageParam: (last) => last.next_cursor ?? undefined,
      meta: { topics: TOPICS },
    };
  });
}

export function useInstructions() {
  return createQuery(() => ({
    queryKey: ['knowledge', 'instructions'],
    queryFn: async ({ signal }) => ok(await knowledgeInstructions({ signal })).data,
    meta: { topics: TOPICS },
  }));
}

export function useSubject(id: () => number | undefined) {
  return createQuery(() => {
    const record = id();
    return {
      queryKey: ['knowledge', 'page', record],
      queryFn: async ({ signal }) => ok(await knowledgeSubject(record as number, { signal })).data,
      enabled: record !== undefined,
      meta: { topics: TOPICS },
    };
  });
}

export function useLinks(id: () => number | undefined) {
  return createQuery(() => {
    const record = id();
    return {
      queryKey: ['knowledge', 'links', record],
      queryFn: async ({ signal }) => ok(await knowledgeLinks(record as number, { signal })).data,
      enabled: record !== undefined,
      meta: { topics: LINK_TOPICS },
    };
  });
}

export function useHistory(id: () => number | undefined) {
  return createQuery(() => {
    const record = id();
    return {
      queryKey: ['knowledge', 'detail', record],
      queryFn: async ({ signal }) => ok(await knowledgeDetail(record as number, { signal })).data,
      enabled: record !== undefined,
      meta: { topics: TOPICS },
    };
  });
}

export async function findTargets(query: string, signal: AbortSignal): Promise<ListOption[]> {
  const text = query.trim();
  if (!text) return [];
  const found = ok(await searchAll({ q: text, types: 'record,task', limit: 10 }, { signal })).data;
  return found.groups.flatMap((group) =>
    group.items.map((hit) => ({
      value: `${hit.type === 'task' ? 'task' : 'record'}:${hit.id}`,
      label: hit.title || hit.snippet,
      detail: hit.type === 'task' ? 'Task' : 'Knowledge',
    })),
  );
}

function oneGesture(extra: Record<string, string> = {}): RequestInit {
  return { headers: { 'Idempotency-Key': crypto.randomUUID(), ...extra } };
}

export function liveLinks(links: readonly KnowledgeLink[] | undefined): KnowledgeLink[] {
  return (links ?? []).filter((link) => !link.removed);
}

export async function handleSaveEdit(
  id: number,
  revision: string,
  fields: { title: string; body: string },
): Promise<ChangeKnowledgeChanged> {
  return ok(await knowledgeEdit(id, fields, oneGesture({ 'If-Match': revision })));
}

export async function handleConfirm(id: number): Promise<void> {
  ok(await knowledgeConfirm(id, oneGesture()));
}

export async function handleRetract(id: number): Promise<ChangeKnowledgeChanged> {
  return ok(await knowledgeRetract(id, oneGesture()));
}

export async function handleRestore(id: number): Promise<ChangeKnowledgeChanged> {
  return ok(await knowledgeRestore(id, oneGesture()));
}

export async function handleAddLink(
  id: number,
  target: { kind: KnowledgeLinkRequestTargetKind; id: string },
  relation: KnowledgeLinkRequestRelation,
): Promise<ChangeKnowledgeLink> {
  return ok(await knowledgeLink(id, { target_kind: target.kind, target_id: target.id, relation }, oneGesture()));
}

export async function handleRemoveLink(linkId: number): Promise<ChangeKnowledgeLink> {
  return ok(await knowledgeUnlink(linkId, oneGesture()));
}

export async function handleUndoEffects(effects: readonly number[]): Promise<void> {
  for (const effect of effects) ok(await reviewUndoEffect(effect, oneGesture()));
}

export function refreshKnowledge(client: QueryClient): Promise<void> {
  return client.invalidateQueries({ queryKey: ['knowledge'] });
}
