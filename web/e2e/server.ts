import { once } from 'node:events';
import { readFile, stat } from 'node:fs/promises';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { extname, join, normalize, resolve } from 'node:path';
import { brotliCompressSync, constants } from 'node:zlib';

export const policy = [
  "default-src 'self'",
  "script-src 'self'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:",
  "frame-ancestors 'none'",
  "form-action 'self'",
  "worker-src 'self'",
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

export async function serve(dist: string, brotli = false): Promise<{ url: string; server: Server }> {
  const root = resolve(dist);
  const packed = new Map<string, Buffer>();
  const server = createServer(async (req, res) => {
    const pathname = new URL(req.url ?? '/', 'http://localhost').pathname;
    const path = await file(root, pathname);
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
