import { HttpResponse, http } from 'msw';
import type { FileSummary, FileView } from '../contract';
import { API_BASE } from '../contract';

export interface MockFile {
  summary: FileSummary;
  text?: string;
  data?: Uint8Array<ArrayBuffer>;
}

const PREVIEW_CHARS = 200_000;
const IMAGES: Record<string, true> = { 'image/jpeg': true, 'image/png': true, 'image/gif': true, 'image/webp': true };
const PNG = Uint8Array.from(
  atob(
    'iVBORw0KGgoAAAANSUhEUgAAAGAAAAA8CAIAAAAWtijjAAAAd0lEQVR42u3QMREAEAAAQPlkEMTJIIwkUphlsJqchenvPsGHOToHQYEgQYIECRIkSBCCBAkSJEjQpuX4hyBBggQJEiRIkCBBggQJEiRIkCBBggQJEiRIkCBBggS9DCo1cSBIkCBBggQJEiQIQYIECRIkSJAgBN1b6iYHbzMAYt4AAAAASUVORK5CYII=',
  ),
  (char) => char.charCodeAt(0),
);

function secs(day: number, hour: number): number {
  return Date.UTC(2026, 10, day, hour, 15) / 1000;
}

export function mockFile(
  fields: Partial<FileSummary> & Pick<FileSummary, 'id' | 'name' | 'media_type'>,
  content: { text?: string; data?: Uint8Array<ArrayBuffer> },
): MockFile {
  const bytes = content.data?.byteLength ?? new TextEncoder().encode(content.text ?? '').byteLength;
  return {
    summary: {
      bytes,
      created_at: secs(12, 9),
      origin: 'upload',
      tokens: Math.ceil(bytes / 4),
      project_id: null,
      run_id: null,
      thread_id: null,
      ...fields,
    },
    ...content,
  };
}

const SHED_LOG = Array.from(
  { length: 4200 },
  (_, line) => `line ${line + 1}: checked the shed order, nothing to change yet.`,
).join('\n');

const THREAD_FILES: Record<number, MockFile[]> = {
  7: [
    mockFile(
      { id: 701, name: 'shed-quote.md', media_type: 'text/markdown', thread_id: 7, created_at: secs(14, 9) },
      {
        text: '# Shed quote\n\n- Cedar shed, 2.4 m by 1.8 m\n- Delivery on **5 December**\n- Base slab not included\n',
      },
    ),
    mockFile(
      {
        id: 702,
        name: 'timber-sizes.csv',
        media_type: 'text/csv',
        thread_id: 7,
        origin: 'generated',
        run_id: 101,
        created_at: secs(14, 10),
      },
      { text: 'Piece,Length (m),Count\nFloor joist,2.4,6\nWall stud,1.9,14\nRoof rafter,2.1,8\n' },
    ),
    mockFile(
      { id: 703, name: 'shed-sketch.png', media_type: 'image/png', thread_id: 7, created_at: secs(14, 11) },
      { data: PNG },
    ),
    mockFile(
      { id: 704, name: 'site-survey.pdf', media_type: 'application/pdf', thread_id: 7, created_at: secs(14, 12) },
      { data: new TextEncoder().encode('%PDF-1.4\n%fictional survey\n') },
    ),
    mockFile(
      {
        id: 705,
        name: 'order-check.log',
        media_type: 'text/plain',
        thread_id: 7,
        origin: 'generated',
        run_id: 101,
        created_at: secs(14, 13),
      },
      { text: SHED_LOG },
    ),
  ],
  401: [
    mockFile(
      { id: 601, name: 'garden-layout.pdf', media_type: 'application/pdf', thread_id: 401, created_at: secs(9, 9) },
      { data: new TextEncoder().encode('%PDF-1.4\n%fictional layout\n') },
    ),
  ],
};

export const THREAD_TITLES: Record<number, string> = {
  7: 'Plan the garden shed order',
  401: 'Planning the raised beds',
};

export function threadFiles(): { thread: number; file: MockFile }[] {
  return Object.entries(THREAD_FILES).flatMap(([thread, files]) =>
    files.map((file) => ({ thread: Number(thread), file })),
  );
}

function csvRows(text: string): string[][] {
  return text
    .split('\n')
    .filter((line) => line.length > 0)
    .map((line) => line.split(','));
}

export function previewOf(file: MockFile): FileView {
  const view: FileView = { file: file.summary, render: '', body: '', rows: [], truncated: false };
  const media = file.summary.media_type;
  if (IMAGES[media]) return { ...view, render: 'image' };
  if (file.text === undefined) return view;
  const truncated = file.text.length > PREVIEW_CHARS;
  const body = truncated ? file.text.slice(0, PREVIEW_CHARS) : file.text;
  const name = file.summary.name.toLowerCase();
  if (media === 'text/markdown' || name.endsWith('.md')) return { ...view, render: 'markdown', body, truncated };
  if (media === 'text/csv' || name.endsWith('.csv'))
    return { ...view, render: 'table', rows: csvRows(body), truncated };
  return { ...view, render: 'text', body, truncated };
}

export function contentOf(file: MockFile): Response {
  const body = file.data ?? new TextEncoder().encode(file.text ?? '');
  return new HttpResponse(body, {
    headers: {
      'Content-Type': file.summary.media_type,
      'Content-Disposition': `attachment; filename="${file.summary.name}"`,
    },
  });
}

export function pageOf<T>(items: readonly T[], request: Request): { items: T[]; next_cursor: string | null } {
  const url = new URL(request.url);
  const limit = Number(url.searchParams.get('limit') ?? '50');
  const offset = Number(url.searchParams.get('cursor') ?? '0');
  const next = offset + limit < items.length ? String(offset + limit) : null;
  return { items: items.slice(offset, offset + limit), next_cursor: next };
}

function missing(): Response {
  return HttpResponse.json({ error: { code: 'not_found', message: 'unknown file' } }, { status: 404 });
}

function find(params: Record<string, unknown>): MockFile | undefined {
  return THREAD_FILES[Number(params.thread)]?.find((file) => file.summary.id === Number(params.file));
}

export const fileHandlers = [
  http.get(`*${API_BASE}/threads/:thread/files`, ({ params, request }) => {
    const files = THREAD_FILES[Number(params.thread)] ?? [];
    return HttpResponse.json(
      pageOf(
        files.map((file) => file.summary),
        request,
      ),
    );
  }),
  http.get(`*${API_BASE}/threads/:thread/files/:file`, ({ params }) => {
    const file = find(params);
    return file ? HttpResponse.json(previewOf(file)) : missing();
  }),
  http.get(`*${API_BASE}/threads/:thread/files/:file/content`, ({ params }) => {
    const file = find(params);
    return file ? contentOf(file) : missing();
  }),
];
