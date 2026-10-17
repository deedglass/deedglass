#!/usr/bin/env node
// scripts/npm-publish.mjs: the seven npm packages of one release, staged, checked and published in
// dependency order by release.yml (npm-stage, then publish-npm; it replaces the founder's local
// publish script of DL-090, so a release needs no npm login and no per-package two-factor prompt):
//   1. @deedglass/cli-{darwin-arm64,darwin-x64,linux-arm64,linux-x64,win32-x64}  one binary each
//   2. deedglass        the wrapper: its optionalDependencies pin the five at the same version
//   3. @deedglass/sdk   the tarball sdk-build packed from the tagged commit
//
//   node scripts/npm-publish.mjs stage --version=X.Y.Z[-rc.N] --archives=DIR --sdk=FILE.tgz --out=DIR
//                                      [--archive-version=V] [--root=REPO]
//   node scripts/npm-publish.mjs publish --out=DIR [--dry-run] [--provenance=true|false]
//
// stage never talks to the registry. Every package.json names the version, the wrapper pins the five
// and carries server.json's mcpName, no package has an install script, each package's repository is
// this repository (npm provenance needs that), each GoReleaser archive matches checksums.txt, each
// binary's header names the right OS and CPU (ELF, Mach-O or PE), then npm pack and a check of each
// tarball's files and modes. It writes DIR/plan.json and DIR/tgz/*.tgz. --archive-version takes a
// snapshot's archives (make npm-dry-run); in CI the archives must carry the release version.
// publish works through plan.json in order and stops at the first failure, so the wrapper never
// reaches npm before all five binaries. It skips a package@version npm already has (a stopped run
// can simply be re-run) and publishes the rest over trusted publishing (GitHub OIDC, no token) with
// --provenance (release.yml passes --provenance=false only while the repository is private, where
// npm can't record provenance): a prerelease goes to the "next" dist-tag and never moves "latest".
// --dry-run prints the commands and touches nothing, the registry included. A real publish runs
// only in GitHub Actions, with npm 11.5.1 or later (the first with trusted publishing).
// The unit tests are scripts/npm-publish.test.mjs (node --test; in make workflows-check and CI);
// make npm-dry-run stages a local GoReleaser snapshot (make release-check) the same way.
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import {
  chmodSync, closeSync, copyFileSync, cpSync, existsSync, lstatSync, mkdirSync, openSync, readFileSync,
  readSync, readdirSync, writeFileSync,
} from 'node:fs';
import { basename, dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const REGISTRY = 'https://registry.npmjs.org';
export const REPOSITORY = 'deedglass/deedglass';
export const MIN_NPM = '11.5.1';
const VERSION_RE = /^\d+\.\d+\.\d+(-rc\.\d+)?$/;

// npm's platform names, GoReleaser's, and the binary header each one must carry.
export const PLATFORMS = [
  { id: 'darwin-arm64', os: 'darwin', cpu: 'arm64', goos: 'darwin', goarch: 'arm64' },
  { id: 'darwin-x64', os: 'darwin', cpu: 'x64', goos: 'darwin', goarch: 'amd64' },
  { id: 'linux-arm64', os: 'linux', cpu: 'arm64', goos: 'linux', goarch: 'arm64' },
  { id: 'linux-x64', os: 'linux', cpu: 'x64', goos: 'linux', goarch: 'amd64' },
  { id: 'win32-x64', os: 'win32', cpu: 'x64', goos: 'windows', goarch: 'amd64' },
].map((p) => ({
  ...p,
  name: `@deedglass/cli-${p.id}`,
  exe: p.os === 'win32' ? 'deedglass.exe' : 'deedglass',
  ext: p.os === 'win32' ? 'zip' : 'tar.gz',
}));
const INSTALL_SCRIPTS = ['preinstall', 'install', 'postinstall'];

export const distTag = (version) => (version.includes('-') ? 'next' : 'latest');
export const archiveName = (p, archiveVersion) => `deedglass_${archiveVersion}_${p.goos}_${p.goarch}.${p.ext}`;
const sha256 = (file) => createHash('sha256').update(readFileSync(file)).digest('hex');
const sha512 = (file) => 'sha512-' + createHash('sha512').update(readFileSync(file)).digest('base64');
const readJSON = (file) => JSON.parse(readFileSync(file, 'utf8'));
const say = (s) => console.log(s);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function run(cmd, args, opts = {}) {
  const r = spawnSync(cmd, args, { encoding: 'utf8', maxBuffer: 64 << 20, ...opts });
  if (r.error) throw new Error(`${cmd} didn't start: ${r.error.message}`);
  return r;
}

// The OS and CPU a binary was built for, from its header: { os, arch } in Go's names, or null.
export function binaryTarget(buf) {
  if (buf.length >= 20 && buf.readUInt32BE(0) === 0x7f454c46) {
    if (buf[4] !== 2 || buf[5] !== 1) return { os: 'linux', arch: 'not 64-bit little-endian' };
    const m = buf.readUInt16LE(18);
    return { os: 'linux', arch: m === 0x3e ? 'amd64' : m === 0xb7 ? 'arm64' : `e_machine 0x${m.toString(16)}` };
  }
  if (buf.length >= 8 && buf.readUInt32LE(0) === 0xfeedfacf) {
    const c = buf.readUInt32LE(4);
    return { os: 'darwin', arch: c === 0x01000007 ? 'amd64' : c === 0x0100000c ? 'arm64' : `cputype 0x${c.toString(16)}` };
  }
  if (buf.length >= 0x40 && buf.toString('latin1', 0, 2) === 'MZ') {
    const pe = buf.readUInt32LE(0x3c);
    if (pe + 6 <= buf.length && buf.toString('latin1', pe, pe + 4) === 'PE\0\0') {
      const m = buf.readUInt16LE(pe + 4);
      return { os: 'windows', arch: m === 0x8664 ? 'amd64' : m === 0xaa64 ? 'arm64' : `machine 0x${m.toString(16)}` };
    }
  }
  return null;
}

function header(file) {
  const fd = openSync(file, 'r');
  try {
    const buf = Buffer.alloc(64 << 10);
    return buf.subarray(0, readSync(fd, buf, 0, buf.length, 0));
  } finally {
    closeSync(fd);
  }
}

// package/package.json from a packed tarball.
export function tarballManifest(tgz) {
  const r = run('tar', ['-xzOf', tgz, 'package/package.json']);
  if (r.status !== 0) throw new Error(`${basename(tgz)} has no package/package.json (${r.stderr.trim()})`);
  return JSON.parse(r.stdout);
}

// Every problem with the seven manifests for this version, as sentences (empty when all is well).
export function checkManifests(root, version, sdk, repo = REPOSITORY) {
  const bad = [];
  const wantRepo = `git+https://github.com/${repo}.git`;
  const common = (where, m, name) => {
    if (m.name !== name) bad.push(`${where} is named ${m.name}, not ${name}`);
    if (m.version !== version) bad.push(`${where} is ${m.version}, not ${version}`);
    const s = Object.keys(m.scripts || {}).filter((k) => INSTALL_SCRIPTS.includes(k));
    if (s.length) bad.push(`${where} has an install script (${s.join(', ')})`);
    const url = typeof m.repository === 'string' ? m.repository : m.repository?.url;
    if (url !== wantRepo) bad.push(`${where} names repository ${url || 'none'}, not ${wantRepo} (npm provenance checks it)`);
  };
  for (const p of PLATFORMS) {
    const where = `npm/cli-${p.id}`;
    const m = readJSON(join(root, where, 'package.json'));
    common(where, m, p.name);
    if (JSON.stringify(m.os) !== JSON.stringify([p.os])) bad.push(`${where} has os ${JSON.stringify(m.os)}, not ["${p.os}"]`);
    if (JSON.stringify(m.cpu) !== JSON.stringify([p.cpu])) bad.push(`${where} has cpu ${JSON.stringify(m.cpu)}, not ["${p.cpu}"]`);
    if (!(m.files || []).includes(p.exe)) bad.push(`${where} doesn't list ${p.exe} in files`);
  }
  const w = readJSON(join(root, 'npm/deedglass/package.json'));
  common('npm/deedglass', w, 'deedglass');
  const deps = w.optionalDependencies || {};
  const want = PLATFORMS.map((p) => p.name).sort();
  if (JSON.stringify(Object.keys(deps).sort()) !== JSON.stringify(want)) {
    bad.push(`npm/deedglass's optionalDependencies are ${Object.keys(deps).join(', ') || 'empty'}, not the five platform packages`);
  }
  for (const [n, v] of Object.entries(deps)) if (v !== version) bad.push(`npm/deedglass pins ${n}@${v}, not ${version}`);
  if (w.bin?.deedglass !== 'bin/deedglass.js' || !existsSync(join(root, 'npm/deedglass/bin/deedglass.js'))) {
    bad.push('npm/deedglass has no bin/deedglass.js');
  }
  const serverJSON = join(root, 'server.json');
  if (existsSync(serverJSON)) {
    const name = readJSON(serverJSON).name;
    if (w.mcpName !== name) bad.push(`npm/deedglass's mcpName is ${w.mcpName || 'missing'}, not server.json's ${name}`);
  }
  common('the SDK tarball', sdk, '@deedglass/sdk');
  return bad;
}

// Each platform's archive checked against checksums.txt; returns { id: path }.
export function verifyArchives(dir, archiveVersion) {
  const sumsFile = join(dir, 'checksums.txt');
  if (!existsSync(sumsFile)) throw new Error(`${dir} has no checksums.txt`);
  const sums = new Map();
  for (const line of readFileSync(sumsFile, 'utf8').split('\n')) {
    const m = /^([0-9a-f]{64}) [ *](.+)$/.exec(line.trim());
    if (m) sums.set(m[2], m[1]);
  }
  const out = {};
  const bad = [];
  for (const p of PLATFORMS) {
    const name = archiveName(p, archiveVersion);
    const file = join(dir, name);
    if (!sums.has(name)) bad.push(`checksums.txt doesn't list ${name}`);
    else if (!existsSync(file)) bad.push(`${name} is missing`);
    else if (sha256(file) !== sums.get(name)) bad.push(`${name} doesn't match checksums.txt`);
    else out[p.id] = file;
  }
  if (bad.length) throw new Error(bad.join('; '));
  return out;
}

// The binary out of one archive, into dir; returns its path.
export function extractBinary(p, archive, dir) {
  mkdirSync(dir, { recursive: true });
  const r = p.ext === 'zip'
    ? run('unzip', ['-q', '-o', archive, p.exe, '-d', dir])
    : run('tar', ['-xzf', archive, '-C', dir, p.exe]);
  const exe = join(dir, p.exe);
  if (r.status !== 0 || !existsSync(exe)) throw new Error(`no ${p.exe} in ${basename(archive)} (${(r.stderr || '').trim()})`);
  const st = lstatSync(exe);
  if (!st.isFile() || st.size === 0) throw new Error(`${p.exe} in ${basename(archive)} isn't a regular, non-empty file`);
  const t = binaryTarget(header(exe));
  if (!t || t.os !== p.goos || t.arch !== p.goarch) {
    throw new Error(`${basename(archive)} holds a ${t ? `${t.os}/${t.arch}` : 'unrecognised'} binary, not ${p.goos}/${p.goarch}`);
  }
  return exe;
}

function npmPack(dir, dest) {
  const r = run('npm', ['pack', '--json', '--ignore-scripts', '--pack-destination', dest], { cwd: dir });
  if (r.status !== 0) throw new Error(`npm pack failed in ${basename(dir)}: ${r.stderr.trim()}`);
  const [info] = JSON.parse(r.stdout);
  return info;
}

function expectFiles(info, want) {
  const got = info.files.map((f) => f.path).sort();
  if (JSON.stringify(got) !== JSON.stringify([...want].sort())) {
    throw new Error(`${info.filename} holds ${got.join(', ')}, not ${[...want].sort().join(', ')}`);
  }
}

export function stage({ root, version, archives, sdk, out, archiveVersion = version }) {
  if (!VERSION_RE.test(version)) throw new Error(`"${version}" isn't X.Y.Z or X.Y.Z-rc.N`);
  if (existsSync(out) && readdirSync(out).length) throw new Error(`${out} isn't empty`);
  if (!existsSync(sdk)) throw new Error(`no SDK tarball at ${sdk}`);
  const sdkManifest = tarballManifest(sdk);
  const bad = checkManifests(root, version, sdkManifest);
  if (bad.length) throw new Error(`the packages disagree with ${version}: ${bad.join('; ')}`);
  say(`✓ every package is ${version}, the wrapper pins the five, no install scripts`);

  const files = verifyArchives(archives, archiveVersion);
  say(`✓ the five archives match checksums.txt (${archives})`);

  const tgz = join(out, 'tgz');
  mkdirSync(tgz, { recursive: true });
  const packages = [];
  for (const p of PLATFORMS) {
    const exe = extractBinary(p, files[p.id], join(out, 'x', p.id));
    const dir = join(out, 'stage', `cli-${p.id}`);
    cpSync(join(root, 'npm', `cli-${p.id}`), dir, { recursive: true });
    copyFileSync(exe, join(dir, p.exe));
    chmodSync(join(dir, p.exe), 0o755);
    const info = npmPack(dir, tgz);
    expectFiles(info, ['package.json', p.exe]);
    const f = info.files.find((x) => x.path === p.exe);
    if ((f.mode & 0o111) !== 0o111) throw new Error(`${info.filename}: ${p.exe} isn't executable (mode ${f.mode.toString(8)})`);
    if (f.size !== lstatSync(exe).size) throw new Error(`${info.filename}: ${p.exe} isn't the archive's binary`);
    packages.push({ name: info.name, version: info.version, file: `tgz/${info.filename}`, integrity: info.integrity });
    say(`✓ ${info.name}: ${p.goos}/${p.goarch} binary, executable`);
  }
  const wdir = join(out, 'stage', 'deedglass');
  cpSync(join(root, 'npm', 'deedglass'), wdir, { recursive: true });
  const w = npmPack(wdir, tgz);
  expectFiles(w, ['package.json', 'bin/deedglass.js', 'README.md', 'LICENSE']);
  packages.push({ name: w.name, version: w.version, file: `tgz/${w.filename}`, integrity: w.integrity });
  say(`✓ deedglass: pins the five at ${version}`);
  const sdkFile = join(tgz, basename(sdk));
  copyFileSync(sdk, sdkFile);
  packages.push({ name: sdkManifest.name, version, file: `tgz/${basename(sdk)}`, integrity: sha512(sdkFile) });
  say(`✓ @deedglass/sdk: the tarball sdk-build packed`);

  const plan = { version, dist: distTag(version), packages };
  writeFileSync(join(out, 'plan.json'), JSON.stringify(plan, null, 2) + '\n');
  say(`✓ 7 packages packed in ${tgz}, publish order in plan.json (dist-tag ${plan.dist})`);
  return plan;
}

// a >= b for X.Y.Z versions (a prerelease suffix is ignored).
export function versionAtLeast(a, b) {
  const parse = (v) => String(v).trim().split('-')[0].split('.').map((x) => Number.parseInt(x, 10) || 0);
  const [x, y] = [parse(a), parse(b)];
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] > y[i];
  return true;
}

// The registry's document for name@version, or null when npm doesn't have it; throws on anything else.
export async function registryVersion(registry, name, version, tries = 3) {
  const url = `${registry}/${name.replace('/', '%2f')}/${encodeURIComponent(version)}`;
  for (let i = 1; ; i++) {
    let why;
    try {
      const r = await fetch(url, { headers: { accept: 'application/json' } });
      if (r.status === 200) return await r.json();
      await r.arrayBuffer();
      if (r.status === 404) return null;
      why = `HTTP ${r.status}`;
    } catch (e) {
      why = e.message;
    }
    if (i >= tries) throw new Error(`the registry didn't answer for ${name}@${version} (${why})`);
    await sleep(1000 * i);
  }
}

export async function publish({
  out, dryRun = false, provenance = true, npm = 'npm', registry = REGISTRY, waitTries = 24, waitMs = 5000, tries = 3,
}) {
  const plan = readJSON(join(out, 'plan.json'));
  const { version, dist } = plan;
  if (plan.packages.length !== 7) throw new Error(`plan.json lists ${plan.packages.length} packages, not 7`);
  if (!dryRun) {
    const v = run(npm, ['--version']);
    if (v.status !== 0 || !versionAtLeast(v.stdout, MIN_NPM)) {
      throw new Error(`trusted publishing needs npm ${MIN_NPM} or later; this is npm ${(v.stdout || '').trim() || 'unknown'}`);
    }
  }
  const done = [];
  for (const p of plan.packages) {
    const file = join(out, p.file);
    const args = ['publish', file, '--access', 'public', '--tag', dist, `--provenance=${provenance}`, '--registry', `${registry}/`];
    if (dryRun) {
      say(`○ [dry-run] npm ${args.join(' ').replace(file, p.file)}`);
      continue;
    }
    const have = await registryVersion(registry, p.name, version, tries);
    if (have) {
      say(`✓ ${p.name}@${version} is already on npm`);
      if (have.dist?.integrity && have.dist.integrity !== p.integrity) {
        say(`::warning::${p.name}@${version} on npm isn't this run's tarball (npm versions can't be replaced; it stays as it is)`);
      }
      done.push(p);
      continue;
    }
    const r = run(npm, args);
    process.stdout.write(r.stdout || '');
    process.stderr.write(r.stderr || '');
    if (r.status !== 0) {
      // Another run got there first, or the registry's 404 was a cached one: npm has it, so skip it.
      if (/cannot publish over|previously published version/i.test(`${r.stdout}${r.stderr}`)
        && (await registryVersion(registry, p.name, version, tries))) {
        say(`✓ ${p.name}@${version} is already on npm`);
        done.push(p);
        continue;
      }
      const auth = /E401|E403|E404|ENEEDAUTH|OIDC|trusted publish/i.test(`${r.stdout}${r.stderr}`)
        ? `. If npm refused the login: on npmjs.com, ${p.name} › Settings › Trusted publishing needs GitHub Actions with repository ${REPOSITORY}, workflow release.yml and environment npm`
        : '';
      throw new Error(`npm publish failed for ${p.name}@${version}; nothing after it was published${auth}`);
    }
    say(`✓ published ${p.name}@${version} (dist-tag ${dist})`);
    done.push(p);
  }
  if (dryRun) {
    say(`✓ dry run: 7 packages in order, dist-tag ${dist}; nothing was published`);
    return;
  }
  for (let i = 0; i < waitTries; i++) {
    const missing = [];
    for (const p of done) if (!(await registryVersion(registry, p.name, version, tries))) missing.push(p.name);
    if (!missing.length) {
      say(`✓ npm serves all 7 at ${version} (dist-tag ${dist}${dist === 'next' ? '; latest is unchanged' : ''})`);
      return;
    }
    if (i === waitTries - 1) {
      say(`::warning::npm accepted every publish but doesn't serve ${missing.join(', ')} at ${version} yet; check npmjs.com in a few minutes`);
      return;
    }
    await sleep(waitMs);
  }
}

function parseArgs(argv) {
  const [cmd, ...rest] = argv;
  const o = {};
  for (const a of rest) {
    const m = /^--([a-z-]+)(?:=(.*))?$/.exec(a);
    if (!m) throw new Error(`unknown argument ${a}`);
    o[m[1]] = m[2] === undefined ? true : m[2];
  }
  return { cmd, o };
}

async function main() {
  const { cmd, o } = parseArgs(process.argv.slice(2));
  const known = { stage: ['version', 'archives', 'sdk', 'out', 'archive-version', 'root'], publish: ['out', 'dry-run', 'provenance'] };
  if (!known[cmd]) {
    console.error(readFileSync(fileURLToPath(import.meta.url), 'utf8').split('\n').slice(1, 27).map((l) => l.replace(/^\/\/ ?/, '')).join('\n'));
    process.exit(2);
  }
  for (const k of Object.keys(o)) if (!known[cmd].includes(k)) throw new Error(`${cmd} doesn't take --${k}`);
  if (cmd === 'stage') {
    for (const k of ['version', 'archives', 'sdk', 'out']) if (typeof o[k] !== 'string') throw new Error(`stage needs --${k}=…`);
    stage({
      root: resolve(o.root || join(dirname(fileURLToPath(import.meta.url)), '..')),
      version: o.version,
      archives: resolve(o.archives),
      sdk: resolve(o.sdk),
      out: resolve(o.out),
      archiveVersion: o['archive-version'] || o.version,
    });
  } else {
    if (typeof o.out !== 'string') throw new Error('publish needs --out=…');
    if (!o['dry-run'] && process.env.GITHUB_ACTIONS !== 'true') {
      throw new Error("a real publish runs in release.yml's publish-npm job (trusted publishing over GitHub OIDC); here, add --dry-run");
    }
    const prov = o.provenance === undefined || o.provenance === true ? 'true' : o.provenance;
    if (prov !== 'true' && prov !== 'false') throw new Error('--provenance is true or false');
    await publish({ out: resolve(o.out), dryRun: Boolean(o['dry-run']), provenance: prov === 'true' });
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  main().catch((e) => {
    console.error(`✕ ${e.message}`);
    console.error('  next  Fix the cause above and run it again; a package@version already on npm is skipped.');
    process.exit(1);
  });
}
