#!/usr/bin/env node
// check-workflows.mjs (S33 §3.6, §3.8): every ```yaml workflow block in the docs (drafts too)
// passes actionlint and zizmor with 0 findings, never triggers on pull_request_target, never checks
// out code in a workflow_run workflow, and gives every job an explicit permissions block.
// Tools run pinned and without a global install: actionlint through `go run …@v1.7.12`, zizmor
// 1.30.1 from a throwaway venv in .cache/ (uvx when it exists). `<full-commit-sha>` placeholders are
// replaced with a dummy 40-hex SHA for the linters (S47 pins the real ones).
// --offline skips the two tools (the structural checks still run).
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import yaml from 'js-yaml';
import { pages, site } from './lib-pages.mjs';

export const ACTIONLINT = 'github.com/rhysd/actionlint/cmd/actionlint@v1.7.12';
export const ZIZMOR = '1.30.1';
const DUMMY_SHA = '0123456789abcdef0123456789abcdef01234567';

export function workflowBlocks(list = pages()) {
  const out = [];
  for (const p of list) {
    for (const m of p.body.matchAll(/```yaml workflow(?: title="([^"]+)")?\n([\s\S]*?)\n```/g)) out.push({ page: p.rel, title: m[1] ?? 'workflow.yml', text: m[2] });
  }
  return out;
}

/** Structural rules, independent of the linters. Returns the problems. */
export function structural(text) {
  const wf = yaml.load(text);
  const probs = [];
  const on = wf.on ?? wf.true;
  const triggers = typeof on === 'string' ? [on] : Array.isArray(on) ? on : Object.keys(on ?? {});
  if (triggers.includes('pull_request_target')) probs.push('triggers on pull_request_target');
  for (const [name, job] of Object.entries(wf.jobs ?? {})) {
    if (!job.permissions) probs.push(`job ${name} has no permissions block`);
    if (triggers.includes('workflow_run')) for (const st of job.steps ?? []) if (String(st.uses ?? '').startsWith('actions/checkout')) probs.push(`job ${name} checks out code in a workflow_run workflow`);
  }
  if (!wf.permissions) probs.push('no top-level permissions');
  return probs;
}

function zizmorCmd() {
  try {
    execFileSync('uvx', ['--version'], { stdio: 'ignore' });
    return ['uvx', [`zizmor@${ZIZMOR}`]];
  } catch {}
  const venv = join(site, '.cache', `zizmor-${ZIZMOR}`);
  if (!existsSync(join(venv, 'bin', 'zizmor'))) {
    mkdirSync(join(site, '.cache'), { recursive: true });
    execFileSync('python3', ['-m', 'venv', venv], { stdio: 'ignore' });
    execFileSync(join(venv, 'bin', 'pip'), ['install', '--quiet', `zizmor==${ZIZMOR}`], { stdio: 'ignore' });
  }
  return [join(venv, 'bin', 'zizmor'), []];
}

function main() {
  const offline = process.argv.includes('--offline');
  const blocks = workflowBlocks();
  const fails = [];
  for (const b of blocks) for (const p of structural(b.text)) fails.push(`${b.page} ${b.title}: ${p}`);
  let al = 'skipped', zz = 'skipped';
  if (!offline && blocks.length) {
    const dir = mkdtempSync(join(tmpdir(), 'dg-wf-'));
    mkdirSync(join(dir, '.github', 'workflows'), { recursive: true });
    blocks.forEach((b, i) => writeFileSync(join(dir, '.github', 'workflows', `${i}-${b.title.split('/').pop()}`), b.text.replaceAll('<full-commit-sha>', DUMMY_SHA) + '\n'));
    const wfFiles = blocks.map((b, i) => join(dir, '.github', 'workflows', `${i}-${b.title.split('/').pop()}`));
    try {
      execFileSync('go', ['run', ACTIONLINT, '-no-color', ...wfFiles], { cwd: site, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
      al = '0';
    } catch (e) {
      fails.push('actionlint:\n' + (e.stdout || e.stderr || e.message));
      al = 'findings';
    }
    try {
      const [cmd, pre] = zizmorCmd();
      execFileSync(cmd, [...pre, '--offline', '--no-progress', '--min-severity', 'informational', join(dir, '.github', 'workflows')], { encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] });
      zz = '0';
    } catch (e) {
      fails.push('zizmor:\n' + (e.stdout || '') + (e.stderr || e.message));
      zz = 'findings';
    }
    rmSync(dir, { recursive: true, force: true });
  }
  if (fails.length) {
    for (const f of fails) console.error('✕ ' + f);
    process.exit(1);
  }
  console.log(`✓ ${blocks.length} workflows · actionlint ${al} · zizmor ${zz} · no pull_request_target trigger`);
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) main();
