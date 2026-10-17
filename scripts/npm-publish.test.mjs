// node --test scripts/npm-publish.test.mjs: scripts/npm-publish.mjs on fixtures, with no network.
// Fake GoReleaser archives carry tiny binaries whose headers name each OS and CPU; a copy of npm/ and
// server.json stands in for the repository; a stand-in registry on 127.0.0.1 and a stand-in npm
// record what publish would do. In make workflows-check and the CI lint-unit job.
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  appendFileSync, chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync,
} from 'node:fs';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { after, before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';
import {
  PLATFORMS, archiveName, binaryTarget, checkManifests, distTag, publish, stage, tarballManifest, versionAtLeast,
} from './npm-publish.mjs';

const repo = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const V = '0.9.0-rc.2';
const tmp = mkdtempSync(join(tmpdir(), 'npm-publish-test-'));
after(() => rmSync(tmp, { recursive: true, force: true }));
let n = 0;
const fresh = (label) => join(tmp, `${label}-${++n}`);
const sh = (cmd, args, opts) => {
  const r = spawnSync(cmd, args, { encoding: 'utf8', env: { ...process.env, COPYFILE_DISABLE: '1' }, ...opts });
  assert.equal(r.status, 0, `${cmd} ${args.join(' ')}: ${r.stderr}`);
  return r.stdout;
};

// ── binaries with real headers ───────────────────────────────────────────────────────────────────
function fakeBinary(goos, goarch) {
  const b = Buffer.alloc(512, 0x2e);
  if (goos === 'linux') {
    b.writeUInt32BE(0x7f454c46, 0);
    b[4] = 2; b[5] = 1; b[6] = 1;
    b.writeUInt16LE(goarch === 'amd64' ? 0x3e : 0xb7, 18);
  } else if (goos === 'darwin') {
    b.writeUInt32LE(0xfeedfacf, 0);
    b.writeUInt32LE(goarch === 'amd64' ? 0x01000007 : 0x0100000c, 4);
  } else {
    b.write('MZ', 0, 'latin1');
    b.writeUInt32LE(0x80, 0x3c);
    b.write('PE\0\0', 0x80, 'latin1');
    b.writeUInt16LE(goarch === 'amd64' ? 0x8664 : 0xaa64, 0x84);
  }
  b.write(`fake deedglass for ${goos}/${goarch}`, 256, 'latin1');
  return b;
}

// A minimal stored (uncompressed) zip, enough for unzip.
const CRC = Array.from({ length: 256 }, (_, k) => {
  let c = k;
  for (let i = 0; i < 8; i++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});
const crc32 = (buf) => {
  let c = 0xffffffff;
  for (const x of buf) c = CRC[(c ^ x) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
};
function writeZip(file, entries) {
  const locals = [];
  const centrals = [];
  let offset = 0;
  for (const [name, data, mode] of entries) {
    const nm = Buffer.from(name);
    const crc = crc32(data);
    const lh = Buffer.alloc(30);
    lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt32LE(crc, 14);
    lh.writeUInt32LE(data.length, 18); lh.writeUInt32LE(data.length, 22); lh.writeUInt16LE(nm.length, 26);
    const ch = Buffer.alloc(46);
    ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(0x031e, 4); ch.writeUInt16LE(20, 6); ch.writeUInt32LE(crc, 16);
    ch.writeUInt32LE(data.length, 20); ch.writeUInt32LE(data.length, 24); ch.writeUInt16LE(nm.length, 28);
    ch.writeUInt32LE(((0o100000 | mode) << 16) >>> 0, 38); ch.writeUInt32LE(offset, 42);
    locals.push(lh, nm, data);
    centrals.push(ch, nm);
    offset += lh.length + nm.length + data.length;
  }
  const cd = Buffer.concat(centrals);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(entries.length, 8); end.writeUInt16LE(entries.length, 10);
  end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(offset, 16);
  writeFileSync(file, Buffer.concat([...locals, cd, end]));
}

const sha = (f) => createHash('sha256').update(readFileSync(f)).digest('hex');
function writeSums(dir, names) {
  writeFileSync(join(dir, 'checksums.txt'), names.map((f) => `${sha(join(dir, f))}  ${f}\n`).join(''));
}

// GoReleaser's five archives and checksums.txt; binaries[id] overrides one platform's binary.
function fakeArchives(version, binaries = {}) {
  const dir = fresh('dist');
  const work = join(dir, '.work');
  mkdirSync(work, { recursive: true });
  const names = [];
  for (const p of PLATFORMS) {
    const bin = binaries[p.id] || fakeBinary(p.goos, p.goarch);
    const name = archiveName(p, version);
    if (p.ext === 'zip') {
      writeZip(join(dir, name), [['LICENSE', Buffer.from('license\n'), 0o644], [p.exe, bin, 0o755]]);
    } else {
      const d = join(work, p.id);
      mkdirSync(d);
      writeFileSync(join(d, 'LICENSE'), 'license\n');
      writeFileSync(join(d, p.exe), bin, { mode: 0o755 });
      sh('tar', ['-czf', join(dir, name), '-C', d, 'LICENSE', p.exe]);
    }
    names.push(name, `${name}.sbom.json`);
    writeFileSync(join(dir, `${name}.sbom.json`), '{}\n');
  }
  rmSync(work, { recursive: true });
  writeSums(dir, names);
  return dir;
}

// A copy of the repository's npm/ and server.json at version v; edit(root) may break it.
function fakeRoot(v = V, edit = () => {}) {
  const root = fresh('root');
  cpSync(join(repo, 'npm'), join(root, 'npm'), { recursive: true });
  cpSync(join(repo, 'server.json'), join(root, 'server.json'));
  for (const id of [...PLATFORMS.map((p) => `cli-${p.id}`), 'deedglass']) {
    const f = join(root, 'npm', id, 'package.json');
    const m = JSON.parse(readFileSync(f, 'utf8'));
    m.version = v;
    if (m.optionalDependencies) for (const k of Object.keys(m.optionalDependencies)) m.optionalDependencies[k] = v;
    writeFileSync(f, JSON.stringify(m, null, 2) + '\n');
  }
  edit(root);
  return root;
}
const editJSON = (f, fn) => writeFileSync(f, JSON.stringify(fn(JSON.parse(readFileSync(f, 'utf8'))), null, 2) + '\n');

function fakeSDK(v = V, extra = {}) {
  const d = fresh('sdk');
  mkdirSync(join(d, 'package'), { recursive: true });
  const repository = { type: 'git', url: 'git+https://github.com/deedglass/deedglass.git', directory: 'sdk/typescript' };
  writeFileSync(join(d, 'package', 'package.json'), JSON.stringify({ name: '@deedglass/sdk', version: v, repository, ...extra }));
  const tgz = join(d, `deedglass-sdk-${v}.tgz`);
  sh('tar', ['-czf', tgz, '-C', d, 'package']);
  return tgz;
}

const quiet = (fn) => {
  const log = console.log;
  console.log = () => {};
  try { return fn(); } finally { console.log = log; }
};
const quietAsync = async (fn) => {
  const log = console.log;
  console.log = () => {};
  try { return await fn(); } finally { console.log = log; }
};
const stageWith = (o = {}) => quiet(() => stage({
  root: o.root || fakeRoot(), version: o.version || V, archives: o.archives || fakeArchives(o.archiveVersion || V),
  sdk: o.sdk || fakeSDK(), out: o.out || fresh('out'), archiveVersion: o.archiveVersion,
}));

// ── pure parts ───────────────────────────────────────────────────────────────────────────────────
test('binaryTarget reads ELF, Mach-O and PE headers', () => {
  for (const [os, arch] of [['linux', 'amd64'], ['linux', 'arm64'], ['darwin', 'amd64'], ['darwin', 'arm64'], ['windows', 'amd64'], ['windows', 'arm64']]) {
    assert.deepEqual(binaryTarget(fakeBinary(os, arch)), { os, arch });
  }
  assert.equal(binaryTarget(Buffer.from('#!/bin/sh\necho hi\n')), null);
  assert.equal(binaryTarget(Buffer.alloc(0)), null);
});

test('a prerelease goes to next, a release to latest', () => {
  assert.equal(distTag('0.2.0-rc.1'), 'next');
  assert.equal(distTag('0.2.0'), 'latest');
});

test('versionAtLeast compares npm versions', () => {
  assert.ok(versionAtLeast('11.21.0\n', '11.5.1'));
  assert.ok(versionAtLeast('11.5.1', '11.5.1'));
  assert.ok(versionAtLeast('12.0.0-pre.1', '11.5.1'));
  assert.ok(!versionAtLeast('11.5.0', '11.5.1'));
  assert.ok(!versionAtLeast('10.9.3', '11.5.1'));
  assert.ok(!versionAtLeast('', '11.5.1'));
});

test("the repository's own manifests agree with each other and with server.json", () => {
  const v = JSON.parse(readFileSync(join(repo, 'npm/deedglass/package.json'), 'utf8')).version;
  const sdk = JSON.parse(readFileSync(join(repo, 'sdk/typescript/package.json'), 'utf8'));
  assert.deepEqual(checkManifests(repo, v, sdk, 'deedglass/deedglass'), []);
});

// ── stage ────────────────────────────────────────────────────────────────────────────────────────
describe('stage', () => {
  test('packs the seven packages in dependency order with each binary executable', () => {
    const out = fresh('out');
    const plan = stageWith({ out });
    assert.equal(plan.dist, 'next');
    assert.deepEqual(plan.packages.map((p) => p.name), [...PLATFORMS.map((p) => p.name), 'deedglass', '@deedglass/sdk']);
    assert.deepEqual(JSON.parse(readFileSync(join(out, 'plan.json'), 'utf8')), plan);
    for (const p of plan.packages) assert.ok(existsSync(join(out, p.file)), p.file);
    for (const p of PLATFORMS) {
      const tgz = join(out, plan.packages.find((x) => x.name === p.name).file);
      const listing = sh('tar', ['-tvzf', tgz]).trim().split('\n');
      const exe = listing.find((l) => l.endsWith(`package/${p.exe}`));
      assert.match(exe, /^-rwxr-xr-x/, `${p.name}: ${exe}`);
      assert.equal(listing.length, 2, listing.join('\n'));
      const bin = spawnSync('tar', ['-xzOf', tgz, `package/${p.exe}`]).stdout;
      assert.deepEqual(binaryTarget(bin), { os: p.goos, arch: p.goarch });
      assert.equal(tarballManifest(tgz).version, V);
    }
    const w = tarballManifest(join(out, plan.packages[5].file));
    assert.equal(w.mcpName, 'io.github.deedglass/deedglass');
  });

  test('a snapshot dry run takes the archives of another version with --archive-version', () => {
    const plan = stageWith({ archiveVersion: '0.0.0-SNAPSHOT-abc1234' });
    assert.equal(plan.packages.length, 7);
  });

  test('a release takes only archives that carry its version', () => {
    assert.throws(() => stageWith({ archives: fakeArchives('0.0.0-SNAPSHOT-abc1234') }), /checksums\.txt doesn't list deedglass_0\.9\.0-rc\.2_darwin_arm64/);
  });

  test('refuses an archive that differs from checksums.txt', () => {
    const archives = fakeArchives(V);
    appendFileSync(join(archives, archiveName(PLATFORMS[3], V)), 'x');
    assert.throws(() => stageWith({ archives }), /deedglass_0\.9\.0-rc\.2_linux_amd64\.tar\.gz doesn't match checksums\.txt/);
  });

  test('refuses a missing archive', () => {
    const archives = fakeArchives(V);
    rmSync(join(archives, archiveName(PLATFORMS[4], V)));
    assert.throws(() => stageWith({ archives }), /windows_amd64\.zip is missing/);
  });

  test('refuses a binary built for another CPU, even when its checksum matches', () => {
    const archives = fakeArchives(V, { 'linux-x64': fakeBinary('linux', 'arm64') });
    assert.throws(() => stageWith({ archives }), /linux_amd64\.tar\.gz holds a linux\/arm64 binary, not linux\/amd64/);
  });

  test('refuses a binary built for another OS', () => {
    const archives = fakeArchives(V, { 'win32-x64': fakeBinary('linux', 'amd64') });
    assert.throws(() => stageWith({ archives }), /windows_amd64\.zip holds a linux\/amd64 binary, not windows\/amd64/);
  });

  test('refuses an archive without the binary', () => {
    const archives = fakeArchives(V);
    const name = archiveName(PLATFORMS[2], V);
    const d = fresh('empty');
    mkdirSync(d);
    writeFileSync(join(d, 'LICENSE'), 'license\n');
    sh('tar', ['-czf', join(archives, name), '-C', d, 'LICENSE']);
    writeSums(archives, PLATFORMS.map((p) => archiveName(p, V)));
    assert.throws(() => stageWith({ archives }), /no deedglass in deedglass_0\.9\.0-rc\.2_linux_arm64\.tar\.gz/);
  });

  test('refuses packages that disagree with the version', () => {
    assert.throws(() => stageWith({ version: '0.9.1' }), /npm\/cli-darwin-arm64 is 0\.9\.0-rc\.2, not 0\.9\.1/);
  });

  test('refuses a version that is not X.Y.Z or X.Y.Z-rc.N', () => {
    assert.throws(() => stageWith({ version: 'v0.9.0' }), /isn't X\.Y\.Z/);
    assert.throws(() => stageWith({ version: '0.9.0-beta.1' }), /isn't X\.Y\.Z/);
  });

  test('refuses a wrapper that pins another version of a platform package', () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/deedglass/package.json'), (m) => {
      m.optionalDependencies['@deedglass/cli-linux-x64'] = '0.8.0';
      return m;
    }));
    assert.throws(() => stageWith({ root }), /npm\/deedglass pins @deedglass\/cli-linux-x64@0\.8\.0/);
  });

  test('refuses a wrapper that drops a platform package', () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/deedglass/package.json'), (m) => {
      delete m.optionalDependencies['@deedglass/cli-win32-x64'];
      return m;
    }));
    assert.throws(() => stageWith({ root }), /optionalDependencies are .*, not the five platform packages/);
  });

  test("refuses a wrapper whose mcpName isn't server.json's name", () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/deedglass/package.json'), (m) => ({ ...m, mcpName: 'io.github.other/x' })));
    assert.throws(() => stageWith({ root }), /mcpName is io\.github\.other\/x, not server\.json's io\.github\.deedglass\/deedglass/);
  });

  test('refuses an install script in a platform package', () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/cli-linux-arm64/package.json'), (m) => ({ ...m, scripts: { postinstall: 'node x.js' } })));
    assert.throws(() => stageWith({ root }), /npm\/cli-linux-arm64 has an install script \(postinstall\)/);
  });

  test('refuses a platform package with the wrong os or cpu', () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/cli-darwin-x64/package.json'), (m) => ({ ...m, cpu: ['arm64'] })));
    assert.throws(() => stageWith({ root }), /npm\/cli-darwin-x64 has cpu \["arm64"\], not \["x64"\]/);
  });

  test('refuses a package whose repository npm provenance would reject', () => {
    const root = fakeRoot(V, (r) => editJSON(join(r, 'npm/cli-linux-x64/package.json'), (m) => ({ ...m, repository: 'github:someone/fork' })));
    assert.throws(() => stageWith({ root }), /npm\/cli-linux-x64 names repository github:someone\/fork/);
  });

  test('refuses an SDK tarball of another version, or with an install script', () => {
    assert.throws(() => stageWith({ sdk: fakeSDK('0.8.0') }), /the SDK tarball is 0\.8\.0, not 0\.9\.0-rc\.2/);
    assert.throws(() => stageWith({ sdk: fakeSDK(V, { scripts: { preinstall: 'x' } }) }), /the SDK tarball has an install script/);
  });

  test("refuses an --out directory that isn't empty", () => {
    const out = fresh('out');
    mkdirSync(out);
    writeFileSync(join(out, 'old.tgz'), '');
    assert.throws(() => stageWith({ out }), /isn't empty/);
  });
});

// ── publish ──────────────────────────────────────────────────────────────────────────────────────
describe('publish', () => {
  let srv;
  let registry;
  let hits = 0;
  let fail500 = false;
  const published = fresh('registry'); // one tarball per name@version npm "has"
  const key = (name, v) => encodeURIComponent(`${name}@${v}`);
  const has = (name, v) => existsSync(join(published, key(name, v)));
  // npm "has" name@version, as the tarball file (a staged one, or another build's).
  const give = (name, v, file) => cpSync(file, join(published, key(name, v)));
  const lie = new Map(); // key → fields the registry's dist answers with instead of the real ones
  const npmLog = fresh('npm-log');
  const fakeNpm = join(tmp, 'fake-npm.cjs');

  before(async () => {
    mkdirSync(published);
    // GET /<name with %2f>/<version>: 200 with dist.integrity and dist.tarball when published, else
    // 404; GET /-/<key> serves the tarball. Every answer closes its connection: a kept-alive socket
    // the server times out between two tests made fetch flaky.
    srv = createServer((req, res) => {
      hits++;
      res.setHeader('connection', 'close');
      if (fail500) { res.writeHead(500); return res.end(); }
      const t = /^\/-\/([^/]+)$/.exec(req.url);
      if (t && existsSync(join(published, t[1]))) {
        res.writeHead(200, { 'content-type': 'application/octet-stream' });
        return res.end(readFileSync(join(published, t[1])));
      }
      const [, name, v] = /^\/(.+)\/([^/]+)$/.exec(req.url) || [];
      const nm = decodeURIComponent(name || '');
      if (name && !t && has(nm, decodeURIComponent(v))) {
        const k = key(nm, decodeURIComponent(v));
        const integrity = 'sha512-' + createHash('sha512').update(readFileSync(join(published, k))).digest('base64');
        const dist = { integrity, tarball: `${registry}/-/${k}`, ...lie.get(k) };
        res.writeHead(200, { 'content-type': 'application/json' });
        return res.end(JSON.stringify({ name: nm, version: decodeURIComponent(v), dist }));
      }
      res.writeHead(404, { 'content-type': 'application/json' });
      res.end('{"error":"not found"}');
    });
    await new Promise((r) => srv.listen(0, '127.0.0.1', r));
    registry = `http://127.0.0.1:${srv.address().port}`;
    // The stand-in npm (CommonJS, so require works whatever the package type): answers --version
    // with FAKE_NPM_VERSION and records each publish (the tarball lands in the stand-in registry);
    // FAKE_NPM_RACE=<name> answers E403 as if another run won (with FAKE_NPM_RACE_FILE's tarball,
    // when set), FAKE_NPM_FAIL=<name> fails as npm does without a trusted publisher.
    writeFileSync(fakeNpm, `#!/usr/bin/env node
const { appendFileSync, copyFileSync } = require('node:fs');
const { execFileSync } = require('node:child_process');
const args = process.argv.slice(2);
if (args[0] === '--version') { console.log(process.env.FAKE_NPM_VERSION || '11.21.0'); process.exit(0); }
const m = JSON.parse(execFileSync('tar', ['-xzOf', args[1], 'package/package.json'], { encoding: 'utf8' }));
appendFileSync(process.env.FAKE_NPM_LOG, JSON.stringify({ name: m.name, args }) + '\\n');
const dest = process.env.FAKE_NPM_REGISTRY + '/' + encodeURIComponent(m.name + '@' + m.version);
if (process.env.FAKE_NPM_RACE === m.name) {
  copyFileSync(process.env.FAKE_NPM_RACE_FILE || args[1], dest);
  console.error('npm error 403 You cannot publish over the previously published versions: ' + m.version + '.');
  process.exit(1);
}
if (process.env.FAKE_NPM_FAIL === m.name) { console.error('npm error code E404\\nnpm error 404 Not Found - PUT ' + m.name); process.exit(1); }
copyFileSync(args[1], dest);
console.log('+ ' + m.name + '@' + m.version);
`.replace("#!/usr/bin/env node\n", `#!${process.execPath}\n`));
    chmodSync(fakeNpm, 0o755);
  });
  after(() => srv.close());

  const runPublish = async (out, env = {}, o = {}) => {
    rmSync(npmLog, { force: true });
    writeFileSync(npmLog, '');
    const set = {
      FAKE_NPM_LOG: npmLog, FAKE_NPM_REGISTRY: published, FAKE_NPM_RACE: '', FAKE_NPM_RACE_FILE: '', FAKE_NPM_FAIL: '', FAKE_NPM_VERSION: '', ...env,
    };
    const saved = Object.fromEntries(Object.keys(set).map((k) => [k, process.env[k]]));
    Object.assign(process.env, set);
    try {
      await quietAsync(() => publish({ out, npm: fakeNpm, registry, waitTries: 2, waitMs: 10, tries: 1, ...o }));
    } finally {
      for (const [k, v] of Object.entries(saved)) if (v === undefined) delete process.env[k]; else process.env[k] = v;
    }
    return readFileSync(npmLog, 'utf8').trim().split('\n').filter(Boolean).map((l) => JSON.parse(l));
  };
  const names = (calls) => calls.map((c) => c.name);
  const stageAt = (v, binaries) => {
    const out = fresh('out');
    const plan = quiet(() => stage({ root: fakeRoot(v), version: v, archives: fakeArchives(v, binaries), sdk: fakeSDK(v), out }));
    const file = (name) => join(out, plan.packages.find((p) => p.name === name).file);
    return { out, plan, file };
  };
  // The same platform, rebuilt: same OS and CPU, other bytes.
  const rebuilt = (id) => {
    const p = PLATFORMS.find((x) => x.id === id);
    const b = fakeBinary(p.goos, p.goarch);
    b.write('rebuilt from another commit', 320, 'latin1');
    return b;
  };

  test('publishes in order with provenance and the next dist-tag, skipping what npm has', async () => {
    const { out, file } = stageAt(V);
    give('@deedglass/cli-darwin-arm64', V, file('@deedglass/cli-darwin-arm64'));
    const calls = await runPublish(out);
    assert.deepEqual(names(calls), [
      '@deedglass/cli-darwin-x64', '@deedglass/cli-linux-arm64', '@deedglass/cli-linux-x64', '@deedglass/cli-win32-x64',
      'deedglass', '@deedglass/sdk',
    ]);
    for (const c of calls) {
      assert.equal(c.args[0], 'publish');
      assert.match(c.args[1], /\.tgz$/);
      assert.deepEqual(c.args.slice(2), ['--access', 'public', '--tag', 'next', '--provenance=true', '--registry', `${registry}/`]);
    }
    // A second run finds all seven on npm and publishes nothing.
    assert.deepEqual(await runPublish(out), []);
  });

  test('a release goes to latest', async () => {
    const { out } = stageAt('0.9.0');
    const calls = await runPublish(out);
    assert.equal(calls.length, 7);
    for (const c of calls) assert.equal(c.args[c.args.indexOf('--tag') + 1], 'latest');
  });

  test('a version another run published first counts as published', async () => {
    const { out } = stageAt('0.9.1');
    const calls = await runPublish(out, { FAKE_NPM_RACE: '@deedglass/cli-linux-x64' });
    assert.equal(calls.length, 7);
    assert.ok(has('@deedglass/sdk', '0.9.1'));
  });

  test('stops at the first failure, so the wrapper never goes out before its binaries', async () => {
    const { out } = stageAt('0.9.2');
    await assert.rejects(
      runPublish(out, { FAKE_NPM_FAIL: '@deedglass/cli-win32-x64' }),
      /npm publish failed for @deedglass\/cli-win32-x64@0\.9\.2; nothing after it was published\. If npm refused the login: .*Trusted publishing .*workflow release\.yml, environment npm and the allowed action npm publish/,
    );
    assert.ok(has('@deedglass/cli-linux-x64', '0.9.2'));
    assert.ok(!has('deedglass', '0.9.2'));
    assert.ok(!has('@deedglass/sdk', '0.9.2'));
    // Fixed and re-run: only what's missing goes out.
    assert.deepEqual(names(await runPublish(out)), ['@deedglass/cli-win32-x64', 'deedglass', '@deedglass/sdk']);
  });

  test("publishes nothing when npm already has a platform package with another build's binary", async () => {
    // A run of an earlier commit published linux-x64 at this version and stopped; the tag moved.
    const earlier = stageAt('0.9.7', { 'linux-x64': rebuilt('linux-x64') });
    const { out } = stageAt('0.9.7');
    give('@deedglass/cli-linux-x64', '0.9.7', earlier.file('@deedglass/cli-linux-x64'));
    await assert.rejects(
      runPublish(out),
      /npm already has @deedglass\/cli-linux-x64@0\.9\.7 with another build's deedglass \(sha256 [0-9a-f]{12}…; this run's is [0-9a-f]{12}…\)\. .*bump the version/,
    );
    for (const p of PLATFORMS) if (p.id !== 'linux-x64') assert.ok(!has(p.name, '0.9.7'), p.name);
    assert.ok(!has('deedglass', '0.9.7'));
  });

  test('a platform package npm has from another pack of the same binary counts as published', async () => {
    const { out, plan, file } = stageAt('0.9.8');
    // Another pack of the same files: npm's integrity differs, the binary inside is this run's.
    const d = fresh('repack');
    mkdirSync(d);
    sh('tar', ['-xzf', file('@deedglass/cli-darwin-x64'), '-C', d]);
    sh('tar', ['-czf', join(d, 'repacked.tgz'), '-C', d, 'package']);
    const integrity = 'sha512-' + createHash('sha512').update(readFileSync(join(d, 'repacked.tgz'))).digest('base64');
    assert.notEqual(integrity, plan.packages.find((p) => p.name === '@deedglass/cli-darwin-x64').integrity);
    give('@deedglass/cli-darwin-x64', '0.9.8', join(d, 'repacked.tgz'));
    const calls = await runPublish(out);
    assert.equal(calls.length, 6);
    assert.ok(!names(calls).includes('@deedglass/cli-darwin-x64'));
  });

  test("a race lost to another build's binary stops before the wrapper", async () => {
    const earlier = stageAt('0.9.9', { 'win32-x64': rebuilt('win32-x64') });
    const { out } = stageAt('0.9.9');
    await assert.rejects(
      runPublish(out, { FAKE_NPM_RACE: '@deedglass/cli-win32-x64', FAKE_NPM_RACE_FILE: earlier.file('@deedglass/cli-win32-x64') }),
      /npm already has @deedglass\/cli-win32-x64@0\.9\.9 with another build's deedglass\.exe/,
    );
    assert.ok(has('@deedglass/cli-linux-x64', '0.9.9'));
    assert.ok(!has('deedglass', '0.9.9'));
  });

  test("won't fetch a tarball from anywhere but the registry", async () => {
    const earlier = stageAt('0.9.10', { 'darwin-arm64': rebuilt('darwin-arm64') });
    const { out } = stageAt('0.9.10');
    give('@deedglass/cli-darwin-arm64', '0.9.10', earlier.file('@deedglass/cli-darwin-arm64'));
    lie.set(key('@deedglass/cli-darwin-arm64', '0.9.10'), { tarball: 'https://example.invalid/x.tgz' });
    try {
      await assert.rejects(runPublish(out), /names its tarball at https:\/\/example\.invalid\/x\.tgz, not on http:\/\/127\.0\.0\.1/);
    } finally {
      lie.clear();
    }
  });

  test("refuses a staged tarball that doesn't match plan.json, before npm or the registry", async () => {
    const { out, file } = stageAt('0.9.11');
    appendFileSync(file('deedglass'), 'x');
    const before = hits;
    await assert.rejects(runPublish(out), /tgz\/deedglass-0\.9\.11\.tgz doesn't match plan\.json/);
    assert.equal(hits, before);
  });

  test("doesn't publish when the registry can't say what it has", async () => {
    const { out } = stageAt('0.9.3');
    fail500 = true;
    try {
      await assert.rejects(runPublish(out), /the registry didn't answer for @deedglass\/cli-darwin-arm64@0\.9\.3 \(HTTP 500\)/);
    } finally {
      fail500 = false;
    }
    assert.ok(!has('@deedglass/cli-darwin-arm64', '0.9.3'));
  });

  test('--provenance=false while the repository is private', async () => {
    const { out } = stageAt('0.9.5');
    const calls = await runPublish(out, {}, { provenance: false });
    assert.equal(calls.length, 7);
    for (const c of calls) assert.ok(c.args.includes('--provenance=false'), c.args.join(' '));
  });

  test('refuses an npm older than trusted publishing, before any publish', async () => {
    const { out } = stageAt('0.9.6');
    await assert.rejects(runPublish(out, { FAKE_NPM_VERSION: '10.9.3' }), /trusted publishing needs npm 11\.5\.1 or later; this is npm 10\.9\.3/);
    assert.ok(!has('@deedglass/cli-darwin-arm64', '0.9.6'));
  });

  test('a dry run calls neither npm nor the registry', async () => {
    const { out } = stageAt('0.9.4');
    const before = hits;
    assert.deepEqual(await runPublish(out, {}, { dryRun: true }), []);
    assert.equal(hits, before);
  });
});
