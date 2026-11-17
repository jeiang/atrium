import type { InfiniteData } from '@tanstack/svelte-query';
import { ApiError, assistantThreadArchive, assistantThreadPin, type ListedThread, ok } from '../../contract';
import { outcomeUnknown, refusedOffline } from '../../shared/device.svelte';
import { queryClient } from '../../shared/query';
import { toast } from '../../shared/ui/toast.svelte';

export type Flag = 'pinned' | 'archived';

type ThreadPages = InfiniteData<{ items: ListedThread[]; next_cursor?: string | null }>;

export function failureText(error: unknown): string {
  if (refusedOffline(error) && error instanceof Error) return error.message;
  if (error instanceof TypeError) {
    return 'The outcome is unknown. The connection failed before atrium answered, so this may or may not have happened. atrium did not try again.';
  }
  if (outcomeUnknown(error)) return `The outcome is unknown. ${error.message} atrium did not try again.`;
  if (error instanceof ApiError && error.status === 404) return 'This chat no longer exists.';
  if (error instanceof ApiError && error.status === 403) return 'You can read this but not change it.';
  return error instanceof Error && error.message ? error.message : 'Something went wrong.';
}

export function chatTitle(title: string | null | undefined): string {
  return title || 'Untitled chat';
}

function flagText(flag: Flag, on: boolean, title: string): string {
  if (flag === 'pinned') return `${on ? 'Pinned' : 'Unpinned'} “${title}”.`;
  return `${on ? 'Archived' : 'Restored'} “${title}”.`;
}

function applyOptimistic(id: number, flag: Flag, on: boolean): () => void {
  const saved = queryClient.getQueriesData<ThreadPages>({ queryKey: ['threads'] });
  queryClient.setQueriesData<ThreadPages>({ queryKey: ['threads'] }, (data) =>
    data
      ? {
          ...data,
          pages: data.pages.map((page) => ({
            ...page,
            items:
              flag === 'archived'
                ? page.items.filter((thread) => thread.id !== id)
                : page.items.map((thread) => (thread.id === id ? { ...thread, pinned: on } : thread)),
          })),
        }
      : data,
  );
  return () => {
    for (const [key, data] of saved) queryClient.setQueryData(key, data);
  };
}

export async function handleFlag(
  id: number,
  title: string,
  flag: Flag,
  on: boolean,
  undoable = true,
): Promise<boolean> {
  await queryClient.cancelQueries({ queryKey: ['threads'] });
  const restore = applyOptimistic(id, flag, on);
  try {
    if (flag === 'pinned') ok(await assistantThreadPin(id, { pinned: on }));
    else ok(await assistantThreadArchive(id, { archived: on }));
  } catch (error) {
    restore();
    toast(failureText(error), { tone: 'error' });
    return false;
  }
  void queryClient.invalidateQueries({ queryKey: ['threads'] });
  void queryClient.invalidateQueries({ queryKey: ['thread', id] });
  toast(flagText(flag, on, title), {
    action: undoable ? { label: 'Undo', run: () => void handleFlag(id, title, flag, !on, false) } : undefined,
  });
  return true;
}
