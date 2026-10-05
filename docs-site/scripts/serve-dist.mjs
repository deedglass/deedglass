// A tiny static server for dist/ (screenshots, Lighthouse, the dist link check): 127.0.0.1 only,
// directory index.html, the 404 page for anything missing, and the headers from public/_headers.
import { createServer } from 'node:http';
import { gzipSync } from 'node:zlib';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.js': 'text/javascript', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.txt': 'text/plain; charset=utf-8', '.xml': 'application/xml', '.md': 'text/markdown; charset=utf-8', '.pf_meta': 'application/octet-stream' };

export function headersFor(dist) {
  const p = join(dist, '_headers');
  const out = {};
  if (!existsSync(p)) return out;
  // Only the "/*" block applies to every path (S34: path blocks such as /.well-known/security.txt
  // or /_astro/* set their own headers on Cloudflare and are left out here).
  let block = '';
  for (const line of readFileSync(p, 'utf8').split('\n')) {
    if (/^\S/.test(line)) {
      block = line.trim();
      continue;
    }
    const m = /^\s+([A-Za-z-]+):\s*(.+)$/.exec(line);
    if (m && block === '/*' && m[1] !== 'Strict-Transport-Security') out[m[1]] = m[2];
  }
  return out;
}

export function serve(dist, port = 0) {
  const extra = headersFor(dist);
  const server = createServer((req, res) => {
    let path = decodeURIComponent(new URL(req.url, 'http://x').pathname);
    let file = normalize(join(dist, path));
    if (!file.startsWith(dist)) return res.writeHead(400).end();
    if (existsSync(file) && statSync(file).isDirectory()) file = join(file, 'index.html');
    let status = 200;
    if (!existsSync(file)) {
      if (!path.endsWith('/') && existsSync(join(dist, path, 'index.html'))) return res.writeHead(308, { Location: path + '/' }).end();
      file = join(dist, '404.html');
      status = 404;
    }
    // Text is gzipped, as Cloudflare compresses it, so Lighthouse sees realistic transfer sizes (S36).
    const type = types[extname(file)] ?? 'application/octet-stream';
    let body = readFileSync(file);
    const enc = {};
    if (/^(text\/|application\/(json|xml)|image\/svg)/.test(type) && /gzip/.test(req.headers['accept-encoding'] ?? '')) {
      body = gzipSync(body);
      enc['Content-Encoding'] = 'gzip';
      enc.Vary = 'Accept-Encoding';
    }
    res.writeHead(status, { 'Content-Type': type, ...enc, ...(extname(file) === '.html' ? extra : {}) });
    res.end(body);
  });
  return new Promise((r) => server.listen(port, '127.0.0.1', () => r(server)));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dist = join(fileURLToPath(new URL('..', import.meta.url)), 'dist');
  const s = await serve(dist, Number(process.argv[2] ?? 4322));
  console.log(`serving dist on http://127.0.0.1:${s.address().port}`);
}
