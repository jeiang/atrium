import type { KnowledgeHistory, KnowledgeRecord } from '../../contract';

export interface Version {
  key: string;
  text: string;
  at: string;
  by: string;
  current: boolean;
  undone: boolean;
}

export interface DiffLine {
  kind: 'same' | 'add' | 'del';
  text: string;
}

const DIFF_CELLS = 1_000_000;

export function versions(record: KnowledgeRecord, history: readonly KnowledgeHistory[]): Version[] {
  const out: Version[] = [];
  let after = record.body;
  history.forEach((entry, index) => {
    const before = entry.previous_body ?? undefined;
    const oldest = index === history.length - 1;
    const wrote = before === undefined ? oldest : before !== after;
    if (!wrote) return;
    out.push({
      key: String(entry.id),
      text: after,
      at: entry.at,
      by: entry.by,
      current: out.length === 0 && after === record.body,
      undone: Boolean(entry.undone_at),
    });
    if (before !== undefined) after = before;
  });
  if (out.length === 0 || out[0]?.text !== record.body) {
    out.unshift({
      key: 'current',
      text: record.body,
      at: record.updated_at,
      by: record.writer,
      current: true,
      undone: false,
    });
  }
  return out;
}

export function lineDiff(from: string, to: string): DiffLine[] | undefined {
  const a = from.split('\n');
  const b = to.split('\n');
  const n = a.length;
  const m = b.length;
  const width = m + 1;
  if ((n + 1) * width > DIFF_CELLS) return undefined;
  const table = new Uint32Array((n + 1) * width);
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      table[i * width + j] =
        a[i] === b[j]
          ? (table[(i + 1) * width + j + 1] ?? 0) + 1
          : Math.max(table[(i + 1) * width + j] ?? 0, table[i * width + j + 1] ?? 0);
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      out.push({ kind: 'same', text: a[i] ?? '' });
      i++;
      j++;
    } else if ((table[(i + 1) * width + j] ?? 0) >= (table[i * width + j + 1] ?? 0)) {
      out.push({ kind: 'del', text: a[i] ?? '' });
      i++;
    } else {
      out.push({ kind: 'add', text: b[j] ?? '' });
      j++;
    }
  }
  for (; i < n; i++) out.push({ kind: 'del', text: a[i] ?? '' });
  for (; j < m; j++) out.push({ kind: 'add', text: b[j] ?? '' });
  return out;
}
