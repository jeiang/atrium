import { once } from 'node:events';
import { readFile, stat } from 'node:fs/promises';
import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { extname, join, normalize, resolve } from 'node:path';

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

export async function serve(dist: string): Promise<{ url: string; server: Server }> {
  const root = resolve(dist);
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
    res.writeHead(200, headers).end(await readFile(path));
  });
  await once(server.listen(0, '127.0.0.1'), 'listening');
  const { port } = server.address() as AddressInfo;
  return { url: `http://127.0.0.1:${port}`, server };
}
