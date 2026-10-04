// A throwaway npm registry for scripts/npx_cold.sh (S32): serves the packed tarballs in a
// directory as packuments, on 127.0.0.1 only. Never a publish: it only answers GETs.
import { createHash } from 'node:crypto';
import { readFileSync, readdirSync } from 'node:fs';
import { createServer } from 'node:http';
import { join } from 'node:path';

const dir = process.argv[2];
const port = Number(process.argv[3] || 0);
const pkgs = new Map();
for (const f of readdirSync(dir).filter((f) => f.endsWith('.tgz'))) {
  const meta = JSON.parse(readFileSync(join(dir, f + '.json'), 'utf8'));
  const data = readFileSync(join(dir, f));
  pkgs.set(meta.name, { meta, file: f, data, shasum: createHash('sha1').update(data).digest('hex'),
    integrity: 'sha512-' + createHash('sha512').update(data).digest('base64') });
}
const srv = createServer((req, res) => {
  const url = decodeURIComponent(req.url.split('?')[0]).replace(/^\//, '');
  for (const [name, p] of pkgs) {
    if (url === name) {
      const base = `http://127.0.0.1:${srv.address().port}/`;
      const v = { ...p.meta, dist: { tarball: base + '-/' + p.file, shasum: p.shasum, integrity: p.integrity } };
      res.writeHead(200, { 'content-type': 'application/json' });
      return res.end(JSON.stringify({ name, 'dist-tags': { latest: p.meta.version }, versions: { [p.meta.version]: v } }));
    }
    if (url === '-/' + p.file) {
      res.writeHead(200, { 'content-type': 'application/octet-stream' });
      return res.end(p.data);
    }
  }
  res.writeHead(404, { 'content-type': 'application/json' });
  res.end('{"error":"not found"}');
});
srv.listen(port, '127.0.0.1', () => console.log(srv.address().port));
