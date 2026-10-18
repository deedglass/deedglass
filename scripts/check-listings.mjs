#!/usr/bin/env node
// S47 §3.6a: every listing kit installs the one server object, fits its limits, carries no secret,
// uses copied (not redrawn) images and passes the copy rules. Run in make release-check and CI.
import { readFileSync, existsSync, readdirSync, statSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { join, dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const rd = (p) => readFileSync(join(root, p), 'utf8');
const json = (p) => JSON.parse(rd(p));
const problems = [];
const bad = (m) => problems.push(m);

const gen = json('examples/play/install-links.json');
const server = JSON.stringify(gen.server);
const SECRET = [/dg_(live|test)_/, /DEEDGLASS_API_KEY=/, /Bearer\s/, /[a-z][a-z0-9+.-]*:\/\/[^\s/"'@]+@/i];
const banned = rd('scripts/banned_words.txt').split('\n').map((l) => l.trim()).filter((l) => l && !l.startsWith('#'));
const publicRepo = process.env.DEEDGLASS_REPO_PUBLIC === '1';
// The phrase is banned in this repo until it is public, so it is assembled here rather than written.
const OS_PHRASE = new RegExp(['open', 'source'].join('[- ]'), 'i');

function walk(dir) {
  const out = [];
  for (const f of readdirSync(join(root, dir))) {
    const p = join(dir, f);
    if (statSync(join(root, p)).isDirectory()) out.push(...walk(p));
    else out.push(p);
  }
  return out;
}

function textChecks(p, s) {
  for (const re of SECRET) if (re.test(s)) bad(`${p}: holds a secret pattern (${re})`);
  if (/—/.test(s)) bad(`${p}: has an em-dash`);
  if (!publicRepo && OS_PHRASE.test(s)) bad(`${p}: uses the licence phrase banned until the repo is public (say "public repo")`);
  for (const w of banned) {
    const re = new RegExp(`(^|[^a-z])${w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}([^a-z]|$)`, 'i');
    if (re.test(s)) bad(`${p}: banned word "${w}"`);
  }
}

// claude-code
const plugin = json('integrations/claude-code/.claude-plugin/plugin.json');
for (const k of ['name', 'description', 'version', 'author', 'homepage', 'repository', 'license']) if (!plugin[k]) bad(`plugin.json: missing ${k}`);
if (plugin.name !== 'deedglass' || plugin.version !== gen.version) bad('plugin.json: name or version drifted');
const pmcp = json('integrations/claude-code/.mcp.json');
if (JSON.stringify(pmcp.mcpServers?.deedglass) !== server) bad('claude-code/.mcp.json: not the one server object');
// FEAT-07: the one hook is the shell guard, off unless DEEDGLASS_SHELL_GUARD is ask or deny. Nothing else
// may go in hooks/, the hook only sees Bash, and the script answers nothing (exit 0) when the variable is unset.
const hooksDir = 'integrations/claude-code/hooks';
if (existsSync(join(root, hooksDir))) {
  const files = readdirSync(join(root, hooksDir)).sort().join(',');
  if (files !== 'hooks.json,shell-guard.sh') bad(`claude-code/hooks: only hooks.json and shell-guard.sh belong here (found ${files})`);
  const hk = json(`${hooksDir}/hooks.json`);
  const events = Object.keys(hk.hooks ?? {});
  const groups = hk.hooks?.PreToolUse ?? [];
  const cmds = groups.flatMap((g) => g.hooks ?? []);
  if (events.join() !== 'PreToolUse' || groups.length !== 1 || groups[0].matcher !== 'Bash' || cmds.length !== 1 ||
    cmds[0].type !== 'command' || cmds[0].command !== 'sh "${CLAUDE_PLUGIN_ROOT}/hooks/shell-guard.sh"') {
    bad('claude-code/hooks/hooks.json: must be the one PreToolUse hook on Bash that runs shell-guard.sh');
  }
  // A failed hook must never block a Bash command: on Windows without Git Bash, Claude Code runs a
  // shell-form hook in PowerShell (no sh) and counts its failure as a block unless onFailure says
  // continue. No shell or args either: shell form, so Git Bash, sh and PowerShell all read it the same.
  if (cmds[0]?.onFailure !== 'continue' || 'shell' in (cmds[0] ?? {}) || 'args' in (cmds[0] ?? {})) {
    bad('claude-code/hooks/hooks.json: the hook must set "onFailure": "continue" and no shell or args');
  }
  const sg = rd(`${hooksDir}/shell-guard.sh`);
  if (!/case "\$\{DEEDGLASS_SHELL_GUARD:-off\}" in/.test(sg) || !/\*\)\n\tcat >\/dev\/null\n\texit 0/.test(sg)) bad('claude-code/hooks/shell-guard.sh: must be off unless DEEDGLASS_SHELL_GUARD is set');
  if (/npx|curl|wget|https?:/.test(sg)) bad('claude-code/hooks/shell-guard.sh: must not fetch anything');
  if (/exit\s+2|exec\s/.test(sg)) bad('claude-code/hooks/shell-guard.sh: must never exit 2 or exec (exit 2 blocks the command)');
  textChecks(`${hooksDir}/hooks.json`, rd(`${hooksDir}/hooks.json`));
  textChecks(`${hooksDir}/shell-guard.sh`, sg);
}
if (existsSync(join(root, 'integrations/claude-code/settings.json'))) bad('claude-code: has settings that could change permissions');
if (!/uploads nothing/.test(rd('integrations/claude-code/commands/scan.md'))) bad('commands/scan.md: must say it uploads nothing');
const mk = json('.claude-plugin/marketplace.json');
if (mk.plugins?.[0]?.source !== './integrations/claude-code' || mk.name !== 'deedglass') bad('marketplace.json: source or name drifted');

// cursor
const cur = json('integrations/cursor/listing.json');
const cursorLink = gen.links.find((l) => l.id === 'cursor').url;
if (cur.install !== cursorLink) bad('cursor/listing.json: install link is not the generated one');
if (cur.install.length > 10000) bad('cursor: install link over 10,000 characters');
if ([...cur.tagline].length > 80) bad('cursor: tagline over 80 characters');
if ([...cur.description].length > 300) bad('cursor: description over 300 characters');
const cfg = JSON.stringify(JSON.parse(Buffer.from(new URL(cur.install).searchParams.get('config'), 'base64').toString()));
if (cfg !== server) bad('cursor: the deeplink decodes to a different server');
const icon = join(root, 'integrations/cursor', cur.icon);
if (!existsSync(icon)) bad('cursor: icon missing');
else {
  const sha = createHash('sha256').update(readFileSync(icon)).digest('hex');
  if (sha !== cur.icon_source.sha256) bad('cursor: icon differs from the kit file it was copied from');
  const b = readFileSync(icon);
  if (b.readUInt32BE(16) !== 512 || b.readUInt32BE(20) !== 512) bad('cursor: icon is not 512 x 512');
}
for (const s of cur.screenshots) if (!existsSync(join(root, 'integrations/cursor', s))) bad(`cursor: screenshot ${s} missing`);

// supabase, neon
for (const p of ['supabase', 'neon']) {
  const s = rd(`integrations/${p}/listing.md`);
  const words = (h) => (s.split(`## ${h}`)[1] ?? '').split('\n## ')[0].trim().split(/\s+/).filter(Boolean).length;
  if (words('50 words') > 55) bad(`${p}: the 50-word description has ${words('50 words')} words`);
  if (words('150 words') > 165) bad(`${p}: the 150-word description has ${words('150 words')} words`);
  if (!/## Sources \(checked \d{4}-\d{2}-\d{2}\)/.test(s)) bad(`${p}: no dated sources`);
}

for (const f of [...walk('integrations'), '.claude-plugin/marketplace.json']) {
  if (/\.(png|jpg)$/.test(f)) continue;
  textChecks(f, rd(f));
}
if (problems.length) {
  console.error(problems.map((p) => '✕ ' + p).join('\n'));
  process.exit(1);
}
console.log('✓ claude-code · cursor · supabase · neon · one server object · 0 secrets');
