#!/usr/bin/env node
// gen-data.mjs: writes the generated inputs of the docs site from their one source in the repo
// (S33 §3.4). Never edit a file this writes; run `npm run gen` and commit the result.
//
//   src/data/errors.json         go run ./internal/httpx/cmd/gendocs errors     (the error catalog)
//   src/data/errors-prose.json   docs/api/errors.md, one entry per "## <code>" section
//   src/data/cli-errors.json     go run ./internal/httpx/cmd/gendocs cli-errors (the CLI's own codes, from cmd/deedglass)
//   src/data/sdk-errors.json     sdk/errors.json (the SDK class of every code)
//   src/data/cli.json            deedglass help --json (the command_tree)
//   src/data/config.json         go run ./internal/config/cmd/genconfigdoc
//   src/data/attack.json         go run ./attack/cmd/attack-report -docs-json (attack/results/latest.json)
//   src/data/lifecycle.json      the state diagram in docs/GATE.md (kept equal to gate/lifecycle by TestGateDocDiagram)
//   src/data/score-formula.txt   the formula block in docs/SCAN.md (S32 §3.13.6)
//   src/data/samples.json        the first deed of testdata/deeds/chains/genesis-20.jsonl, its test keys, and a copy with one digit changed
//   src/data/install-links.json  the one-click install links (installLinks below, until S47's CLI)
//   src/styles/tokens.gen.css    a byte copy of web/src/styles/tokens.css
//
// --check compares instead of writing and exits 1 naming each stale file ("Run npm run gen").
import { execFileSync } from 'node:child_process';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const site = join(dirname(fileURLToPath(import.meta.url)), '..');
const repo = join(site, '..');
const check = process.argv.includes('--check');
const go = process.env.GO ?? 'go';

const run = (args, opts = {}) =>
  execFileSync(go, args, { cwd: repo, encoding: 'utf8', maxBuffer: 64 << 20, stdio: ['ignore', 'pipe', 'pipe'], ...opts });
const read = (p) => readFileSync(join(repo, p), 'utf8');
const json = (v) => JSON.stringify(v, null, 2) + '\n';

/** Parses docs/api/errors.md into {code: {status, type, when, todo, example, related}}. */
export function parseErrorsProse(md) {
  const out = {};
  const parts = md.split(/^## /m).slice(1);
  for (const part of parts) {
    const nl = part.indexOf('\n');
    const code = part.slice(0, nl).trim();
    if (!/^[a-z][a-z0-9_]*$/.test(code)) continue; // "What this doesn't do" and other prose sections
    const body = part.slice(nl + 1);
    const head = /^\*\*(\d{3}) · ([a-z_]+)\*\*/m.exec(body);
    const when = /\*\*When it happens\.\*\*\s*([\s\S]*?)(?=\n\*\*What to do\.\*\*)/.exec(body);
    const todo = /\*\*What to do\.\*\*\s*([\s\S]*?)(?=\n```json|\n## |$)/.exec(body);
    const ex = /```json\n([\s\S]*?)\n```/.exec(body);
    if (!head || !when || !todo) throw new Error(`docs/api/errors.md: the ${code} section needs "**<status> · <type>**", "**When it happens.**" and "**What to do.**"`);
    let example = null;
    if (ex) {
      try {
        example = JSON.parse(ex[1]);
      } catch (e) {
        throw new Error(`docs/api/errors.md: the ${code} example isn't JSON: ${e.message}`);
      }
    }
    const related = [...new Set([...body.matchAll(/\]\(#([a-z][a-z0-9_]*)\)/g)].map((m) => m[1]).filter((c) => c !== code))];
    out[code] = { status: Number(head[1]), type: head[2], when: when[1].trim(), todo: todo[1].trim(), example, related };
  }
  return out;
}

/** The stateDiagram edges in docs/GATE.md, in order. */
export function parseLifecycle(md) {
  const m = /```mermaid\nstateDiagram-v2\n([\s\S]*?)\n```/.exec(md);
  if (!m) throw new Error('docs/GATE.md: no stateDiagram-v2 block');
  const edges = [];
  for (const line of m[1].split('\n')) {
    const e = /^\s*(\[\*\]|[a-z_]+) --> (\[\*\]|[a-z_]+)\s*$/.exec(line);
    if (e && e[1] !== '[*]' && e[2] !== '[*]') edges.push([e[1], e[2]]);
  }
  return { states: [...new Set(edges.flat())], edges };
}

/** The formula block under "## The score" in docs/SCAN.md. */
export function parseFormula(md) {
  const m = /Formula v1[^\n]*\n\n```\n([\s\S]*?)\n```/.exec(md);
  if (!m) throw new Error('docs/SCAN.md: no "Formula v1" block');
  return m[1] + '\n';
}

/** One digit of the body changed, so the seal no longer matches (the DeedCard's tampered sample). */
export function tamper(line) {
  const out = line.replace('"latency_ms":41', '"latency_ms":42');
  if (out === line) throw new Error('samples: the first sample deed no longer has "latency_ms":41');
  return out;
}

// The one-click install links of "Install in any client" (12_VIRAL_SELFSERVE.md A5). Until S47 ships
// `deedglass mcp install-links`, this function makes them from the one server object; S47 swaps
// it for the CLI's output. scripts/check-install-links.mjs decodes every link back.
export const SERVER = { command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] };
// internal/installlinks.Description (docs/description.json's tagline): the Goose link shows it, and deedglass.com/install
// carries the Go-generated link, so both say the same line.
const DESCRIPTION = JSON.parse(read('docs/description.json')).tagline;
export function installLinks(server = SERVER) {
  const json = JSON.stringify(server);
  const b64 = Buffer.from(json, 'utf8').toString('base64');
  const enc = encodeURIComponent;
  const named = JSON.stringify({ name: 'deedglass', ...server });
  const goose = ['cmd=' + enc(server.command), ...server.args.map((a) => 'arg=' + enc(a)), 'id=deedglass', 'name=Deedglass', 'description=' + enc(DESCRIPTION)].join('&');
  return {
    object: 'install_links',
    generator: 'docs-site/scripts/gen-data.mjs (until S47: deedglass mcp install-links)',
    server,
    clients: [
      { client: 'Cursor', link: `cursor://anysphere.cursor-deeplink/mcp/install?name=deedglass&config=${enc(b64)}`, web: `https://cursor.com/en/install-mcp?name=deedglass&config=${enc(b64)}`, source: 'https://cursor.com/docs/mcp/install-links' },
      { client: 'VS Code', link: `vscode:mcp/install?${enc(named)}`, web: `https://vscode.dev/redirect/mcp/install?name=deedglass&config=${enc(json)}`, command: `code --add-mcp '${named}'`, source: 'https://code.visualstudio.com/api/extension-guides/ai/mcp' },
      { client: 'LM Studio', link: `lmstudio://add_mcp?name=deedglass&config=${enc(b64)}`, source: 'https://lmstudio.ai/docs/app/mcp/deeplink' },
      { client: 'Goose', link: `goose://extension?${goose}`, source: 'https://goose-docs.ai/docs/getting-started/using-extensions/' },
      { client: 'Claude Code', command: `claude mcp add --scope user deedglass -- ${server.command} ${server.args.join(' ')}`, source: 'https://code.claude.com/docs/en/mcp' },
      // The .mcpb bundles aren't attached to a release yet (internal/installlinks.MCPBOnRelease): link the by-hand page.
      { client: 'Claude Desktop', guide: '/get-started/claude-desktop/', source: 'https://github.com/modelcontextprotocol/mcpb' },
    ],
  };
}

function generate() {
  const files = {};
  files['src/data/errors.json'] = run(['run', './internal/httpx/cmd/gendocs', 'errors']);
  files['src/data/errors-prose.json'] = json(parseErrorsProse(read('docs/api/errors.md')));
  files['src/data/cli-errors.json'] = run(['run', './internal/httpx/cmd/gendocs', 'cli-errors']);
  files['src/data/sdk-errors.json'] = read('sdk/errors.json');
  // freshness-1019: stamped with the released version (npm/deedglass/package.json, bumped by release prep), so
  // the CLI reference says 0.2.1 rather than the dev build's 0.1.0-dev.
  const released = JSON.parse(read('npm/deedglass/package.json')).version;
  files['src/data/cli.json'] = run(['run', '-ldflags', `-X github.com/deedglass/deedglass/internal/buildinfo.Version=${released}`, './cmd/deedglass', 'help', '--json'], { env: { ...process.env, NO_COLOR: '1', DEEDGLASS_NO_BANNER: '1' } });
  files['src/data/config.json'] = run(['run', './internal/config/cmd/genconfigdoc']);
  try {
    files['src/data/attack.json'] = run(['run', './attack/cmd/attack-report', '-docs-json']);
  } catch (e) {
    throw new Error('attack/results/latest.json is missing or unreadable. Run make attack first.\n' + (e.stderr ?? ''));
  }
  files['src/data/lifecycle.json'] = json(parseLifecycle(read('docs/GATE.md')));
  files['src/data/score-formula.txt'] = parseFormula(read('docs/SCAN.md'));
  const first = read('testdata/deeds/chains/genesis-20.jsonl').split('\n')[0];
  files['src/data/samples.json'] = json({ deed: first, tampered: tamper(first), keys: read('testdata/deeds/keys/test-keys.json') });
  files['src/styles/tokens.gen.css'] = read('web/src/styles/tokens.css');
  files['src/data/install-links.json'] = json(installLinks());
  return files;
}

/** The generated files that differ from what is on disk under root (the --check answer). */
export function stale(files, root = site) {
  return Object.entries(files).filter(([rel, content]) => {
    const p = join(root, rel);
    return !existsSync(p) || readFileSync(p, 'utf8') !== content;
  }).map(([rel]) => rel);
}

function main() {
  let files;
  try {
    files = generate();
  } catch (e) {
    console.error(`✕ gen-data: ${e.message}`);
    process.exit(1);
  }
  const stale = [];
  for (const [rel, content] of Object.entries(files)) {
    const p = join(site, rel);
    const cur = existsSync(p) ? readFileSync(p, 'utf8') : null;
    if (cur === content) continue;
    if (check) {
      stale.push(rel);
      continue;
    }
    mkdirSync(dirname(p), { recursive: true });
    writeFileSync(p, content);
    console.log(`wrote ${rel}`);
  }
  if (check && stale.length) {
    console.error(`✕ generated data is stale: ${stale.join(', ')}\n  Next: Run npm run gen in docs-site and commit the result.`);
    process.exit(1);
  }
  if (check) console.log(`✓ generated data fresh · ${Object.keys(files).length} files`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) main();
