#!/usr/bin/env node
// The deedglass npm wrapper (S32, Biome's pattern): npm installs the one platform package that
// matches this machine (an optionalDependency with os and cpu set); this file finds its binary
// and runs it. No install script, so `npm install --ignore-scripts` and `npx` work the same.
'use strict';
const { spawnSync } = require('node:child_process');
const path = require('node:path');

const pkg = `@deedglass/cli-${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'deedglass.exe' : 'deedglass';
let bin;
try {
  bin = require.resolve(`${pkg}/${exe}`);
} catch {
  process.stderr.write(
    `✕ No deedglass binary for ${process.platform}/${process.arch} came with this install (${pkg}).\n` +
      '  Install it another way: go install github.com/deedglass/deedglass/cmd/deedglass@latest\n' +
      '  or download a release from https://github.com/deedglass/deedglass/releases.\n',
  );
  process.exit(1);
}
const r = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit', windowsHide: true });
if (r.error) {
  process.stderr.write(`✕ ${path.basename(bin)} didn't start (${r.error.code || r.error.message}). Reinstall deedglass.\n`);
  process.exit(1);
}
process.exit(r.status === null ? 1 : r.status);
