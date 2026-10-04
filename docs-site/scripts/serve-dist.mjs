// A tiny static server for dist/ (screenshots, Lighthouse, the dist link check): 127.0.0.1 only,
// directory index.html, the 404 page for anything missing, and the headers from public/_headers.
import { createServer } from 'node:http';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';

const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css', '.js': 'text/javascript', '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.txt': 'text/plain; charset=utf-8', '.xml': 'application/xml', '.md': 'text/markdown; charset=utf-8', '.pf_meta': 'application/octet-stream' };

export function headersFor(dist) {
  const p = join(dist, '_headers');
  const out = {};
  if (!existsSync(p)) return out;
  for (const line of readFileSync(p, 'utf8').split('\n')) {
    const m = /^\s+([A-Za-z-]+):\s*(.+)$/.exec(line);
    if (m && m[1] !== 'Strict-Transport-Security') out[m[1]] = m[2];
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
    res.writeHead(status, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream', ...(extname(file) === '.html' ? extra : {}) });
    res.end(readFileSync(file));
  });
  return new Promise((r) => server.listen(port, '127.0.0.1', () => r(server)));
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const dist = join(fileURLToPath(new URL('..', import.meta.url)), 'dist');
  const s = await serve(dist, Number(process.argv[2] ?? 4322));
  console.log(`serving dist on http://127.0.0.1:${s.address().port}`);
}
