import { once } from 'node:events';
import { readFile, stat } from 'node:fs/promises';
import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http';
import type { AddressInfo } from 'node:net';
import { extname, join, normalize, resolve } from 'node:path';
import { brotliCompressSync, constants } from 'node:zlib';
import { G3_PER_SECOND, G3_STREAM_PATH, g3Events, sseFrame } from '../src/mocks/g3.ts';

export const policy = [
  "default-src 'none'",
  "script-src 'self'",
  "style-src 'self'",
  "img-src 'self' data: blob:",
  "font-src 'self'",
  "connect-src 'self'",
  "media-src 'self' blob:",
  "manifest-src 'self'",
  "worker-src 'self'",
  "frame-ancestors 'none'",
  "form-action 'self'",
  "base-uri 'none'",
  "require-trusted-types-for 'script'",
  'trusted-types atrium-md atrium-sw svelte-trusted-html default',
].join('; ');

const types: Record<string, string> = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.json': 'application/json',
  '.svg': 'image/svg+xml',
  '.woff2': 'font/woff2',
  '.txt': 'text/plain; charset=utf-8',
  '.webmanifest': 'application/manifest+json',
};

async function file(root: string, pathname: string): Promise<string | undefined> {
  const path = join(root, normalize(decodeURIComponent(pathname)).replace(/^(\.\.[/\\])+/, ''));
  if (!path.startsWith(root)) return undefined;
  const info = await stat(path).catch(() => undefined);
  if (info?.isFile()) return path;
  if (extname(pathname) === '') return join(root, 'index.html');
  return undefined;
}

function streamG3(req: IncomingMessage, res: ServerResponse): void {
  const cursor = Number(
    req.headers['last-event-id'] ?? new URL(req.url ?? '/', 'http://localhost').searchParams.get('after') ?? 0,
  );
  const events = g3Events().filter((entry) => 'id' in entry && Number(entry.id) > cursor);
  res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' });
  res.write('retry: 1000\n\n');
  const started = performance.now();
  let sent = 0;
  const timer = setInterval(() => {
    const due = Math.min(events.length, Math.floor(((performance.now() - started) * G3_PER_SECOND) / 1000));
    for (; sent < due; sent++) {
      const entry = events[sent];
      if (entry) res.write(sseFrame(entry));
    }
    if (sent < events.length) return;
    clearInterval(timer);
    res.end(sseFrame({ event: 'done', data: { state: 'done' } }));
  }, 1000 / G3_PER_SECOND);
  req.on('close', () => clearInterval(timer));
}

export async function serve(
  dist: string,
  { brotli = false, base = '/' }: { brotli?: boolean; base?: string } = {},
): Promise<{ url: string; server: Server }> {
  const root = resolve(dist);
  const packed = new Map<string, Buffer>();
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url ?? '/', 'http://localhost').pathname;
    if (pathname === G3_STREAM_PATH) {
      streamG3(req, res);
      return;
    }
    const path = pathname.startsWith(base) ? await file(root, `/${pathname.slice(base.length)}`) : undefined;
    if (!path) {
      res.writeHead(404, { 'Content-Type': 'text/plain' }).end('not found');
      return;
    }
    const type = types[extname(path)] ?? 'application/octet-stream';
    const headers: Record<string, string> = { 'Content-Type': type, 'Cache-Control': 'no-cache' };
    if (type.startsWith('text/html')) headers['Content-Security-Policy'] = policy;
    let body: Buffer = await readFile(path);
    if (brotli && !type.startsWith('font/') && /\bbr\b/.test(req.headers['accept-encoding'] ?? '')) {
      const encoded =
        packed.get(path) ?? brotliCompressSync(body, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } });
      packed.set(path, encoded);
      if (encoded.length < body.length) {
        body = encoded;
        headers['Content-Encoding'] = 'br';
        headers.Vary = 'Accept-Encoding';
      }
    }
    res.writeHead(200, headers).end(body);
  });
  await once(server.listen(0, '127.0.0.1'), 'listening');
  const { port } = server.address() as AddressInfo;
  return { url: `http://127.0.0.1:${port}`, server };
}
