import { p } from '../../shell/router';

export const inboxTypes = ['proposal', 'draft', 'attention', 'ask'] as const;

export type InboxType = (typeof inboxTypes)[number];

export interface InboxId {
  type: InboxType;
  id: number;
}

export function inboxPath(type: InboxType, id: number): string {
  return p('/inbox/:item', { params: { item: `${type}:${id}` } });
}

export function parseInboxId(segment: string): InboxId | undefined {
  let text: string;
  try {
    text = decodeURIComponent(segment);
  } catch {
    return undefined;
  }
  const match = /^([a-z]+):([1-9][0-9]{0,15})$/.exec(text);
  if (!match) return undefined;
  const type = inboxTypes.find((item) => item === match[1]);
  const id = Number(match[2]);
  if (!type || !Number.isSafeInteger(id)) return undefined;
  return { type, id };
}
