import { webHref } from '../../shared/assistant/format';

export type Inline =
  | { t: 'text'; v: string }
  | { t: 'code'; v: string }
  | { t: 'br' }
  | { t: 'strong' | 'em' | 'del'; c: Inline[] }
  | { t: 'link'; href: string; c: Inline[] };

export type Align = 'left' | 'center' | 'right' | null;

export type Block =
  | { t: 'p'; c: Inline[] }
  | { t: 'h'; level: number; c: Inline[] }
  | { t: 'code'; lang: string; v: string }
  | { t: 'quote'; c: Block[] }
  | { t: 'list'; ordered: boolean; start: number; items: Block[][] }
  | { t: 'hr' }
  | { t: 'table'; align: Align[]; head: Inline[][]; rows: Inline[][][] };

const fence = /^ {0,3}(`{3,}|~{3,})\s*([^`\s]*)/;
const heading = /^ {0,3}(#{1,6})(?:\s+(.*?))?(?:\s+#+)?\s*$/;
const rule = /^ {0,3}([-*_])(?:\s*\1){2,}\s*$/;
const quote = /^ {0,3}> ?(.*)$/;
const bullet = /^( {0,3})([-*+]|\d{1,9}[.)])(\s+(.*)|$)/;
const delimiter = /^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$/;
const punctuation = /[!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]/;

function cells(line: string): string[] {
  let body = line.trim();
  if (body.startsWith('|')) body = body.slice(1);
  if (body.endsWith('|') && !body.endsWith('\\|')) body = body.slice(0, -1);
  const out: string[] = [];
  let current = '';
  for (let index = 0; index < body.length; index++) {
    const char = body[index];
    if (char === '\\' && body[index + 1] === '|') {
      current += '|';
      index++;
    } else if (char === '|') {
      out.push(current.trim());
      current = '';
    } else {
      current += char;
    }
  }
  out.push(current.trim());
  return out;
}

function interrupts(line: string): boolean {
  return fence.test(line) || heading.test(line) || rule.test(line) || quote.test(line) || bullet.test(line);
}

export function parse(source: string): Block[] {
  return blocks(source.replace(/\r\n?/g, '\n').split('\n'));
}

function blocks(lines: string[]): Block[] {
  const out: Block[] = [];
  let index = 0;
  while (index < lines.length) {
    const line = lines[index] ?? '';
    if (!line.trim()) {
      index++;
      continue;
    }
    const open = fence.exec(line);
    if (open) {
      const marker = open[1] ?? '```';
      const body: string[] = [];
      index++;
      while (index < lines.length && !(lines[index] ?? '').trim().startsWith(marker)) body.push(lines[index++] ?? '');
      index++;
      out.push({ t: 'code', lang: open[2] ?? '', v: body.join('\n') });
      continue;
    }
    const head = heading.exec(line);
    if (head) {
      out.push({ t: 'h', level: head[1]?.length ?? 1, c: inline(head[2] ?? '') });
      index++;
      continue;
    }
    if (rule.test(line)) {
      out.push({ t: 'hr' });
      index++;
      continue;
    }
    if (quote.test(line)) {
      const body: string[] = [];
      while (index < lines.length && (lines[index] ?? '').trim()) {
        const match = quote.exec(lines[index] ?? '');
        body.push(match ? (match[1] ?? '') : (lines[index] ?? ''));
        index++;
      }
      out.push({ t: 'quote', c: blocks(body) });
      continue;
    }
    const item = bullet.exec(line);
    if (item) {
      index = list(lines, index, out);
      continue;
    }
    const next = lines[index + 1] ?? '';
    if (line.includes('|') && delimiter.test(next)) {
      const align = cells(next).map((cell): Align => {
        const left = cell.startsWith(':');
        const right = cell.endsWith(':');
        return left && right ? 'center' : right ? 'right' : left ? 'left' : null;
      });
      const head = cells(line).map(inline);
      const rows: Inline[][][] = [];
      index += 2;
      while (index < lines.length && (lines[index] ?? '').includes('|') && (lines[index] ?? '').trim()) {
        rows.push(cells(lines[index] ?? '').map(inline));
        index++;
      }
      out.push({ t: 'table', align, head, rows });
      continue;
    }
    const body = [line];
    index++;
    while (index < lines.length && (lines[index] ?? '').trim() && !interrupts(lines[index] ?? '')) {
      body.push(lines[index++] ?? '');
    }
    out.push({ t: 'p', c: inline(body.map((entry) => entry.replace(/^\s+/, '')).join('\n')) });
  }
  return out;
}

function list(lines: string[], from: number, out: Block[]): number {
  const first = bullet.exec(lines[from] ?? '');
  const ordered = /\d/.test(first?.[2] ?? '');
  const start = ordered ? Number.parseInt(first?.[2] ?? '1', 10) : 1;
  const items: Block[][] = [];
  let index = from;
  while (index < lines.length) {
    const match = bullet.exec(lines[index] ?? '');
    if (!match || /\d/.test(match[2] ?? '') !== ordered) break;
    const indent = (match[1]?.length ?? 0) + (match[2]?.length ?? 0) + 1;
    const body = [match[4] ?? ''];
    index++;
    while (index < lines.length) {
      const line = lines[index] ?? '';
      if (!line.trim()) {
        const after = lines[index + 1] ?? '';
        if (after.trim() && /^\s+/.test(after) && (after.match(/^\s*/)?.[0].length ?? 0) >= indent) {
          body.push('');
          index++;
          continue;
        }
        break;
      }
      const lead = line.match(/^\s*/)?.[0].length ?? 0;
      if (lead >= indent) body.push(line.slice(indent));
      else if (!interrupts(line)) body.push(line.trim());
      else break;
      index++;
    }
    items.push(blocks(body));
    if (!(lines[index] ?? '').trim() && bullet.test(lines[index + 1] ?? '')) index++;
  }
  out.push({ t: 'list', ordered, start, items });
  return index;
}

function closing(source: string, marker: string, from: number): number {
  for (let index = from; index <= source.length - marker.length; index++) {
    if (source[index] === '\\') {
      index++;
      continue;
    }
    if (source[index] === '`') {
      const run = /^`+/.exec(source.slice(index))?.[0] ?? '`';
      const end = source.indexOf(run, index + run.length);
      if (end > 0) index = end + run.length - 1;
      continue;
    }
    if (source.startsWith(marker, index) && index > from && !/\s/.test(source[index - 1] ?? '')) return index;
  }
  return -1;
}

function link(href: string, children: Inline[]): Inline[] {
  const safe = webHref(href);
  return safe ? [{ t: 'link', href: safe, c: children }] : children;
}

export function inline(source: string): Inline[] {
  const out: Inline[] = [];
  let text = '';
  const flush = (): void => {
    if (text) out.push({ t: 'text', v: text });
    text = '';
  };
  let index = 0;
  while (index < source.length) {
    const char = source[index] ?? '';
    const rest = source.slice(index);
    if (char === '\\' && punctuation.test(source[index + 1] ?? '')) {
      text += source[index + 1];
      index += 2;
      continue;
    }
    if ((char === '\\' && source[index + 1] === '\n') || rest.startsWith('  \n')) {
      flush();
      out.push({ t: 'br' });
      index = source.indexOf('\n', index) + 1;
      continue;
    }
    if (char === '`') {
      const run = /^`+/.exec(rest)?.[0] ?? '`';
      const end = source.indexOf(run, index + run.length);
      if (end > 0) {
        flush();
        out.push({
          t: 'code',
          v:
            source
              .slice(index + run.length, end)
              .replace(/\n/g, ' ')
              .trim() || ' ',
        });
        index = end + run.length;
        continue;
      }
      text += run;
      index += run.length;
      continue;
    }
    const pair = rest.startsWith('**') || rest.startsWith('__') ? rest.slice(0, 2) : rest.startsWith('~~') ? '~~' : '';
    if (pair && !/\s/.test(source[index + 2] ?? ' ')) {
      const end = closing(source, pair, index + 2);
      if (end > 0) {
        flush();
        out.push({ t: pair === '~~' ? 'del' : 'strong', c: inline(source.slice(index + 2, end)) });
        index = end + 2;
        continue;
      }
    }
    if ((char === '*' || char === '_') && !/\s/.test(source[index + 1] ?? ' ')) {
      const wordy = char === '_' && /\w/.test(source[index - 1] ?? '');
      const end = wordy ? -1 : closing(source, char, index + 1);
      if (end > 0 && !(char === '_' && /\w/.test(source[end + 1] ?? ''))) {
        flush();
        out.push({ t: 'em', c: inline(source.slice(index + 1, end)) });
        index = end + 1;
        continue;
      }
    }
    if (char === '[') {
      const match = /^\[((?:[^\]\\]|\\.)*)\]\(\s*<?([^\s)>]+)>?(?:\s+"[^"]*")?\s*\)/.exec(rest);
      if (match) {
        flush();
        out.push(...link(match[2] ?? '', inline(match[1] ?? '')));
        index += match[0].length;
        continue;
      }
    }
    if (char === '<') {
      const match = /^<(https?:\/\/[^\s<>]+)>/.exec(rest);
      if (match) {
        flush();
        out.push(...link(match[1] ?? '', [{ t: 'text', v: match[1] ?? '' }]));
        index += match[0].length;
        continue;
      }
    }
    if ((char === 'h' || char === 'w') && !/\w/.test(source[index - 1] ?? '')) {
      const match = /^(?:https?:\/\/|www\.)[^\s<]+/.exec(rest);
      if (match) {
        const url = match[0].replace(/[.,:;!?'")\]*_~]+$/, '');
        flush();
        out.push(...link(url.startsWith('www.') ? `https://${url}` : url, [{ t: 'text', v: url }]));
        index += url.length;
        continue;
      }
    }
    text += char;
    index++;
  }
  flush();
  return out;
}
