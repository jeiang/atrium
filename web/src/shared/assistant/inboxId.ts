export type InboxType = 'proposal' | 'draft' | 'attention' | 'ask';

export interface InboxId {
  type: InboxType;
  id: string;
}

const types: Record<InboxType, true> = { proposal: true, draft: true, attention: true, ask: true };

function isType(value: string): value is InboxType {
  return Object.hasOwn(types, value);
}

export function inboxPath(type: InboxType, id: number | string): string {
  return `/inbox/${type}:${id}`;
}

export function parseInboxId(segment: string): InboxId | undefined {
  const at = segment.indexOf(':');
  if (at <= 0 || at === segment.length - 1) return undefined;
  const type = segment.slice(0, at);
  return isType(type) ? { type, id: segment.slice(at + 1) } : undefined;
}
