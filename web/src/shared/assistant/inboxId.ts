import { p } from '../../shell/router';

export interface InboxId {
  type: string;
  id: string;
}

export function inboxPath(type: string, id: number | string): string {
  return p('/inbox/:item', { params: { item: `${type}:${id}` } });
}

export function parseInboxId(segment: string): InboxId | undefined {
  let text: string;
  try {
    text = decodeURIComponent(segment);
  } catch {
    return undefined;
  }
  const match = /^([a-z][a-z_]{0,39}):([A-Za-z0-9][A-Za-z0-9._@+-]{0,199})$/.exec(text);
  if (!match?.[1] || !match[2]) return undefined;
  const [, type, id] = match;
  if ((type === 'proposal' || type === 'draft') && !/^[1-9][0-9]{0,15}$/.test(id)) return undefined;
  return { type, id };
}
