#!/usr/bin/env node
// sync-docs.mjs: the build-time half of the docs site (Node only; no Go needed, so Cloudflare
// Pages can run it). It writes pages that are never committed (see docs-site/.gitignore):
//
//   1. Imported pages: the repo's engineering specs and references in docs/*.md stay the one
//      source (S33 §3.1) and are copied in with frontmatter, their repo links rewritten to site
//      paths. The table IMPORTS is the split (also in docs/README.md and ADR-064).
//   2. Generated reference: /errors/<code>/ for every catalog code and the /errors/ index,
//      /reference/cli/<group>/, /reference/config/ and /security/attack-suite/, from src/data
//      (which gen-data.mjs writes from the Go sources).
//
// Run by `npm run build` and `npm run dev`. --list prints the inventory and writes nothing.
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, normalize, posix, relative } from 'node:path';
import { fileURLToPath } from 'node:url';
import yaml from 'js-yaml';
import { gitDates, walkFiles } from './seo.mjs';

const site = join(dirname(fileURLToPath(import.meta.url)), '..');
const repo = join(site, '..');
const content = join(site, 'src', 'content', 'docs');
const data = (f) => JSON.parse(readFileSync(join(site, 'src', 'data', f), 'utf8'));

// ---- 1. the split: every docs/*.md is imported (rendered from the repo file), moved (the repo
// file is a one-line pointer; the page is hand-written in src/content/docs), a generator source,
// or repo-only (internal engineering notes nobody outside the repo needs). Nothing is orphaned:
// check-inventory in scripts/test fails on a docs/*.md file missing from this table.
export const IMPORTS = {
  'docs/INSTALL.md': 'get-started/install-deedglass',
  // the record spec (TY-12): its own versioned directory, CC-BY-4.0
  'spec/v2/DEED_SPEC.md': 'reference/deed-spec',
  'spec/v1/BUNDLE_FORMAT.md': 'reference/engineering/bundle-format',
  'docs/INTENTS.md': 'reference/intents',
  'docs/POLICY_LANGUAGE.md': 'reference/policy-language',
  'docs/IDS.md': 'reference/ids',
  'docs/MCP_CLIENTS.md': 'reference/mcp-compatibility',
  'docs/api/versions.md': 'reference/api-versions',
  'docs/INIT_AND_DOCTOR.md': 'reference/doctor-checks',
  'docs/CONNECTORS.md': 'reference/connectors/overview',
  'docs/POSTGRES.md': 'reference/connectors/postgres',
  'docs/connectors/github.md': 'reference/connectors/github',
  'docs/connectors/slack.md': 'reference/connectors/slack',
  'docs/connectors/stripe.md': 'reference/connectors/stripe',
  'docs/connectors/gmail.md': 'reference/connectors/gmail',
  'docs/connectors/notion.md': 'reference/connectors/notion',
  'docs/connectors/fhir.md': 'reference/connectors/fhir',
  'docs/connectors/outbound_mail.md': 'reference/connectors/outbound-mail',
  'docs/sdk/typescript.md': 'sdks/typescript',
  'docs/sdk/adapters/ai-sdk.md': 'sdks/adapters/ai-sdk',
  'docs/sdk/adapters/openai-agents.md': 'sdks/adapters/openai-agents',
  'docs/sdk/adapters/langchain.md': 'sdks/adapters/langchain',
  'docs/sdk/adapters/claude-agent-sdk.md': 'sdks/adapters/claude-agent-sdk',
  'docs/SDK_PYTHON.md': 'sdks/python',
  'docs/THREAT_MODEL.md': 'security/threat-model',
  'docs/LIMITATIONS.md': 'security/limitations',
  'docs/SECURITY_REVIEW_2026-10.md': 'security/review',
  'docs/guides/supabase.md': 'guides/supabase',
  'docs/guides/neon.md': 'guides/neon',
  'docs/guides/observe-beside-another-gate.md': 'guides/observe-beside-another-gate',
  'docs/guides/fhir-server.md': 'guides/fhir-server',
  'docs/guides/healthcare-fhir-pack.md': 'guides/healthcare-fhir-pack',
  'docs/healthcare/INDIA.md': 'guides/india',
  'docs/integrations/medplum.md': 'guides/medplum',
  // the framework recipes (adoption-v1 item 4): one page each, the runnable files in examples/frameworks
  'docs/integrations/vercel-ai-sdk.md': 'frameworks/vercel-ai-sdk',
  'docs/integrations/claude-agent-sdk.md': 'frameworks/claude-agent-sdk',
  'docs/integrations/openai-agents.md': 'frameworks/openai-agents',
  'docs/integrations/langchain.md': 'frameworks/langchain',
  'docs/integrations/mastra.md': 'frameworks/mastra',
  'docs/integrations/pydantic-ai.md': 'frameworks/pydantic-ai',
  'docs/integrations/google-adk.md': 'frameworks/google-adk',
  'docs/integrations/crewai.md': 'frameworks/crewai',
  'docs/integrations/llamaindex.md': 'frameworks/llamaindex',
  'docs/OBSERVE.md': 'reference/observe',
  'docs/PHI_MODE.md': 'reference/phi-mode',
  'docs/LOCKDOWN.md': 'reference/lockdown',
  'docs/EXPORTS.md': 'reference/exports',
  'docs/exports/otel-genai.md': 'reference/exports/otel-genai',
  'docs/exports/ocsf.md': 'reference/exports/ocsf',
  'docs/exports/prov-json.md': 'reference/exports/prov-json',
  'docs/SHELL_GUARD.md': 'reference/shell-guard',
  'docs/A2A.md': 'reference/a2a',
  'docs/WITNESS_DEBEZIUM.md': 'reference/witness-debezium',
  'docs/SECURITY_PACK.md': 'reference/security-pack',
  'docs/WEEKLY.md': 'reference/weekly',
  'docs/AGENCIES.md': 'reference/agencies',
  // engineering notes: imported as they are, under Reference › Engineering notes
  ...Object.fromEntries(
    [
      'AGENTS', 'API', 'APPROVALS', 'ARCHITECTURE', 'BUDGETS', 'CLI', 'CLOUD_COMPATIBILITY', 'CONFIG', 'CONSOLE',
      'DATABASE_SETUP', 'DATA_MODEL', 'DECODER', 'EMAIL', 'EVENTS', 'GATE', 'IDENTITY', 'INSIGHTS', 'INSIGHTS_PRIVACY', 'LEDGER',
      'MCP', 'MCP_AUTHORIZATION', 'MIGRATIONS', 'NETWORK_EGRESS', 'OBSERVABILITY', 'OBSERVATION_BOUNDARY', 'OVERVIEW_METRICS',
      'POLICIES_GUIDE', 'POLICY_COOKBOOK', 'POLICY_EDITOR', 'POLICY_ERRORS', 'POSTGRES_DEPLOYMENT', 'REPLAY', 'RETENTION', 'SCAN',
      'SCHEDULED_ACTIONS', 'SEAL_KEYS', 'SEALING_FILES', 'SELF_HOSTING', 'SIMULATOR', 'SQLGUARD', 'STOP', 'SUGGESTIONS', 'TIMESTAMPS', 'UNDO', 'VAULT', 'VERIFICATION_MODEL',
      'VERIFYING_DEEDS', 'WEBHOOKS', 'WITNESS', 'BENCHMARKS', 'LICENSING', 'PLAYGROUND', 'SHARING', 'INSTALL_CLIENTS',
    ].map((n) => [`docs/${n}.md`, `reference/engineering/${n.toLowerCase().replace(/_/g, '-')}`]),
  ),
  'docs/witness/REASONS.md': 'reference/engineering/witness-reasons',
  'docs/witness/DIFFS.md': 'reference/engineering/witness-diffs',
};
// Moved: the page is hand-written; the repo file is a pointer.
export const MOVED = { 'docs/QUICKSTART.md': 'quickstart', 'docs/FAQ.md': 'faq' };
// Generator sources: rendered through src/data, not imported as a page.
export const SOURCES = { 'docs/api/errors.md': 'errors', 'docs/ATTACK_SUITE.md': 'security/attack-suite' };
// Repo-only: build and process notes for people working in the repo.
export const REPO_ONLY = [
  'docs/RELEASING.md', 'docs/DEMO_SCRIPT.md', 'docs/design/CLI_STYLE.md', 'docs/PUBLIC_REPO_SWITCH.md', 'docs/AMENDMENTS.md', 'docs/COPY_CATALOG.md', 'docs/DESIGN_SYSTEM.md', 'docs/DEV_LOOP.md', 'docs/NAMING.md', 'docs/REPO_MAP.md',
  'docs/SECURITY_BACKLOG.md', 'docs/README.md', 'docs/adr/README.md', 'docs/v0/EVALUATION_RUNBOOK.md', 'docs/v0/OBSERVATION_BOUNDARY.md',
  'docs/v0/OPERATION_BUNDLE.md', 'docs/v0/STATEMENT_SPEC.md', 'docs/v0/VERIFICATION_MODEL.md',
];

// docs/LIMITATIONS.md is one file in the repo (release notes read its first section) and, on the site,
// a short index at /security/limitations/ plus one page per part (DOC-05: the single page was over 8,000
// words). Every "## " section of the file belongs to exactly one part; a new section that isn't listed
// here fails the build, so nothing is dropped. Each section keeps its heading, so its anchor is the same
// on the part's page as it was on the single page, and the index keeps an element with the old id.
export const LIMITS_FILE = 'docs/LIMITATIONS.md';
export const LIMITS_SLUG = 'security/limitations';
/** Sections that stay on the index page. */
export const LIMITS_INDEX = ['Known limits in v1', 'Already running another gate?', 'What this page does not do'];
export const LIMITS_PARTS = [
  {
    slug: 'postgres',
    label: 'Postgres, the check and undo',
    title: 'Limits: Postgres, the check and undo',
    description: "What Deedglass can't see or do in Postgres: the database, the Postgres connector, the change-log check, the decoder, FHIR servers, foreign feeds and undo.",
    sections: ['Database limits', 'The Postgres connector', 'The Witness', 'The decoder', 'The L2 witness', 'Proven for FHIR servers', 'Foreign change feeds', 'Undo'],
  },
  {
    slug: 'records',
    label: 'Records, verify and the Ledger',
    title: 'Limits: records, verify and the Ledger',
    description: 'What a sealed record, deedglass verify and the Ledger chain check show, and what they leave out.',
    sections: ['Deed limits', 'Verifier limits', 'Ledger limits', 'Head timestamps', 'PHI mode'],
  },
  {
    slug: 'identity',
    label: 'Sign-in, agent keys, Stop and intents',
    title: 'Limits: sign-in, agent keys, Stop and intents',
    description: 'The limits of sign-in, agent and admin keys, Stop and signed intents, including where rate limits live and what Stop cannot undo.',
    sections: ['Identity limits', 'Agents, keys and Stop limits', 'Signed intents, on-behalf-of and MCP limits'],
  },
  {
    slug: 'policies',
    label: 'Policies, rules and approvals',
    title: 'Limits: policies, rules and approvals',
    description: 'The limits of policies, sqlguard, budgets, policy versions, the rules that run each call, approvals, lockdown mode, webhooks and scheduled actions.',
    sections: [
      'Policy limits',
      'Lockdown mode',
      'Policy evaluation, sqlguard and budgets',
      'Policy versions, activation and the registry',
      'Gate limits',
      'Approvals and webhooks',
      'Scheduled actions and outbound mail',
    ],
  },
  {
    slug: 'mcp',
    label: 'The MCP wrapper, gateway, A2A and demo',
    title: 'Limits: the MCP wrapper, gateway, A2A and demo',
    description: 'The limits of deedglass mcp wrap, the MCP HTTP gateway, /mcp/native, mcp serve, deedglass a2a proxy and the Acme demo stack.',
    sections: ['The MCP stdio wrapper', 'The MCP gateway, /mcp/native and mcp serve', 'The Acme demo stack', 'The shell guard in other coding agents', 'A2A delegations'],
  },
  {
    slug: 'connectors',
    label: 'Connectors, the vault and egress',
    title: 'Limits: connectors, the vault and egress',
    description: 'The limits of connectors, the credential vault and network egress, and of the GitHub, Slack, Stripe and Gmail connectors.',
    sections: ['Connectors, the vault and egress', 'GitHub, Slack and approvals in Slack', 'Stripe and Gmail'],
  },
  {
    slug: 'console-and-insights',
    label: 'The Console, Insights and the SDK',
    title: 'Limits: the Console, Insights and the SDK',
    description: "The limits of the Console's control screens, Insights and its suggestions, the weekly digest and the TypeScript SDK.",
    sections: ["The Console's control screens", 'Insights', 'Suggestions, phrasing and the digest', 'The TypeScript SDK'],
  },
  {
    slug: 'testing',
    label: 'The attack suite and security pass',
    title: 'Limits: the attack suite and security pass',
    description: "What the attack suite and the October 2026 security pass cover, and what they don't.",
    sections: ['The attack suite', 'Security pass 2'],
  },
];
/** A section heading without its session tag: "Undo (S23)" → "Undo"; curly apostrophes made straight. */
export const sectionName = (h) => h.replace(/\s*\(S\d+[A-Z]?\)\s*$/, '').replace(/’/g, "'").trim();
/** The id Astro gives a heading (github-slugger's rule for these headings). */
export const headingId = (h) => h.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, '').replace(/\s/g, '-');
/** The part slug for a heading id of docs/LIMITATIONS.md, or null (an index section or unknown). */
export function limitsPartFor(anchor, md = readFileSync(join(repo, LIMITS_FILE), 'utf8')) {
  for (const m of md.matchAll(/^## (.+)$/gm)) {
    if (headingId(m[1]) !== anchor) continue;
    return LIMITS_PARTS.find((p) => p.sections.includes(sectionName(m[1])))?.slug ?? null;
  }
  return null;
}

/** The site path for a repo docs file, or null. */
export function sitePath(repoRel) {
  const r = repoRel.replace(/\\/g, '/');
  const slug = IMPORTS[r] ?? MOVED[r] ?? SOURCES[r];
  if (slug) return `/${slug}/`;
  if (r === 'README.md') return '/';
  return null;
}

/** Rewrites Markdown links in a file that lives at srcRel (repo-relative). */
export function rewriteLinks(md, srcRel) {
  const dir = posix.dirname(srcRel);
  return md.replace(/(!?)\[([^\]\n]*)\]\(([^)\s]+)(\s+"[^"]*")?\)/g, (all, bang, text, href, title) => {
    if (/^(https?:|mailto:)/.test(href)) return all;
    if (href.startsWith('#')) return `${bang}[${text}](${href.toLowerCase()}${title ?? ''})`;
    const [path, anchor] = href.split('#');
    const target = posix.normalize(posix.join(dir, path));
    if (bang) {
      if (target.startsWith('docs/images/')) return `![${text}](/images/${posix.basename(target)})`;
      return text;
    }
    if (target === 'docs/api/errors.md' && anchor && /^[a-z][a-z0-9_]*$/.test(anchor)) return `[${text}](/errors/${anchor}/${title ?? ''})`;
    const part = target === LIMITS_FILE && anchor ? limitsPartFor(anchor.toLowerCase()) : null;
    if (part) return `[${text}](/${LIMITS_SLUG}/${part}/#${anchor.toLowerCase()}${title ?? ''})`;
    const sp = sitePath(target);
    if (sp) return `[${text}](${sp}${anchor ? '#' + anchor.toLowerCase() : ''}${title ?? ''})`;
    // a source file or a repo-only note: keep the words, drop the link (the repo isn't public yet)
    return text;
  });
}

const stripMd = (s) =>
  s
    .replace(/`([^`]*)`/g, '$1')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/[*_]{1,2}([^*_]+)[*_]{1,2}/g, '$1')
    .replace(/\s+/g, ' ')
    .trim();

export function describe(text, max = 160) {
  const t = stripMd(text);
  if (t.length <= max) return t;
  const cut = t.slice(0, max - 1);
  return cut.slice(0, cut.lastIndexOf(' ')).replace(/[,;:·(]+$/, '') + '…';
}

const yamlStr = (s) => JSON.stringify(s);
// Bare URLs in generated table text become code, so Markdown doesn't autolink a loopback address.
const codeUrls = (s) => s.replace(/(?<!`)(https?:\/\/[^\s),|`]+)/g, '`$1`');
const sessionRefs = (s) => s.replace(/\s*\((?:S\d+[A-Z]?(?:,\s*)?)+\)/g, '').replace(/\s*\(S\d+[^)]*\)/g, '');

function frontmatter(fields) {
  const lines = ['---'];
  for (const [k, v] of Object.entries(fields)) {
    if (v === undefined) continue;
    if (typeof v === 'object') lines.push(`${k}:`, ...Object.entries(v).map(([a, b]) => `  ${a}: ${typeof b === 'string' ? yamlStr(b) : b}`));
    else lines.push(`${k}: ${typeof v === 'string' ? yamlStr(v) : v}`);
  }
  lines.push('---', '');
  return lines.join('\n');
}

function write(rel, body) {
  const p = join(content, rel);
  mkdirSync(dirname(p), { recursive: true });
  writeFileSync(p, body);
  written.push(rel);
}
const written = [];

function importDoc(src, slug) {
  let md = readFileSync(join(repo, src), 'utf8').replace(/\r\n/g, '\n');
  md = md.replace(/^<!--[\s\S]*?-->\s*/, '');
  if (src === LIMITS_FILE) return importLimits(md);
  const h1 = /^# (.+)\n/m.exec(md);
  const title = h1 ? stripMd(h1[1]) : slug;
  if (h1) md = md.slice(0, h1.index) + md.slice(h1.index + h1[0].length);
  const firstPara = md.split(/\n\s*\n/).map((p) => p.trim()).find((p) => p && !/^(#|```|\||>|-|\*\s|<|!\[|\d+\.)/.test(p)) ?? title;
  md = rewriteLinks(md, src);
  const fm = frontmatter({ title, description: describe(firstPara), imported: src, editUrl: false });
  // seo-aeo-v1: the source note goes last, so a page opens with its own first paragraph (the sentence search and
  // answer engines quote), not with where the file lives.
  write(`${slug}.md`, fm + md.trimStart().trimEnd() + `\n\n:::note[Source]\nThis page is \`${src}\` in the Deedglass repo, imported at build time.\n:::\n`);
}

/** Splits a Markdown file at its "## " headings: { head, sections: [{ heading, body }] }. */
export function splitSections(md) {
  const parts = md.split(/^(?=## )/m);
  const head = parts[0].startsWith('## ') ? '' : parts.shift();
  return { head, sections: parts.map((p) => ({ heading: /^## (.+)$/m.exec(p)[1].trim(), body: p })) };
}

/** docs/LIMITATIONS.md → the index at /security/limitations/ and one page per part (LIMITS_PARTS). */
function importLimits(md) {
  const h1 = /^# (.+)\n/m.exec(md);
  if (h1) md = md.slice(0, h1.index) + md.slice(h1.index + h1[0].length);
  const { head, sections } = splitSections(md);
  const byPart = new Map(LIMITS_PARTS.map((p) => [p.slug, []]));
  const index = [];
  for (const sec of sections) {
    const name = sectionName(sec.heading);
    const part = LIMITS_PARTS.find((p) => p.sections.includes(name));
    if (part) byPart.get(part.slug).push(sec);
    else if (LIMITS_INDEX.includes(name)) index.push(sec);
    else throw new Error(`${LIMITS_FILE} has a section "${sec.heading}" that no page holds. Add "${name}" to LIMITS_PARTS (or LIMITS_INDEX) in docs-site/scripts/sync-docs.mjs.`);
  }
  for (const p of LIMITS_PARTS) {
    const missing = p.sections.filter((n) => !byPart.get(p.slug).some((s) => sectionName(s.heading) === n));
    if (missing.length) throw new Error(`LIMITS_PARTS lists "${missing.join('", "')}" under ${p.slug}, but ${LIMITS_FILE} has no such section.`);
  }
  const intro = rewriteLinks(head, LIMITS_FILE).trim();
  const keep = (name) => index.find((s) => sectionName(s.heading) === name);
  // The index: the intro, the limits everyone should read, then every part with its sections. Each
  // section keeps an element with its old id here, and a short script sends an old #anchor link on to
  // the part's page (without script, the link still lands on the right line of this list).
  const moved = {};
  let list = '## The limits by part\n\nEach part of Deedglass has its own page. The sections keep the names and anchors they had on this page.\n';
  for (const p of LIMITS_PARTS) {
    list += `\n### ${p.label}\n\n[${p.title.replace(/^Limits: /, 'Limits of ')}](/${LIMITS_SLUG}/${p.slug}/): ${p.description.charAt(0).toLowerCase() + p.description.slice(1)}\n\n`;
    for (const s of byPart.get(p.slug)) {
      const id = headingId(s.heading);
      moved[id] = p.slug;
      list += `- <a id="${id}"></a>[${sectionName(s.heading)}](/${LIMITS_SLUG}/${p.slug}/#${id})\n`;
    }
  }
  const script = `<script>{const m=${JSON.stringify(moved)};const h=decodeURIComponent(location.hash.slice(1));if(m[h])location.replace("/${LIMITS_SLUG}/"+m[h]+"/#"+h)}</script>`;
  const body = [
    intro,
    ...['Known limits in v1', 'Already running another gate?'].map((n) => rewriteLinks(keep(n).body, LIMITS_FILE).trim()),
    list.trim(),
    rewriteLinks(keep('What this page does not do').body, LIMITS_FILE).trim(),
    `:::note[Source]\nThis page and the ${LIMITS_PARTS.length} pages below it are \`${LIMITS_FILE}\` in the Deedglass repo, split by part and imported at build time.\n:::`,
    script,
  ].join('\n\n');
  const title = h1 ? stripMd(h1[1]) : 'Limitations';
  const firstPara = intro.split(/\n\s*\n/)[0] ?? title;
  write(`${LIMITS_SLUG}.md`, frontmatter({ title, description: describe(firstPara), imported: LIMITS_FILE, editUrl: false }) + body + '\n');
  LIMITS_PARTS.forEach((p, i) => {
    const prev = i > 0 ? LIMITS_PARTS[i - 1] : null;
    const next = LIMITS_PARTS[i + 1] ?? null;
    let page = frontmatter({ title: p.title, description: p.description, imported: LIMITS_FILE, editUrl: false });
    // seo-aeo-v1: the page opens with one plain sentence of what it covers (the part's description), then the limits.
    page += `${p.description}\n\n`;
    page += byPart.get(p.slug).map((s) => rewriteLinks(s.body, LIMITS_FILE).trim()).join('\n\n') + '\n\n';
    page += `## More limits\n\n- [Limitations](/${LIMITS_SLUG}/): the known limits in v1, and every part\n`;
    for (const o of [prev, next].filter(Boolean)) page += `- [${o.title.replace(/^Limits: /, 'Limits of ')}](/${LIMITS_SLUG}/${o.slug}/)\n`;
    page += `\n:::note[Source]\nThis page is part of \`${LIMITS_FILE}\` in the Deedglass repo, imported at build time. [Every limit, by part](/${LIMITS_SLUG}/).\n:::\n`;
    write(`${LIMITS_SLUG}/${p.slug}.md`, page);
  });
}

// ---- 2. generated reference pages ----
const typeNames = {
  invalid_request_error: 'Invalid request',
  authentication_error: 'Authentication',
  permission_error: 'Permission',
  policy_error: 'Policy',
  rate_limit_error: 'Rate limit',
  api_error: 'API',
};

function errorPages() {
  const errors = data('errors.json');
  const prose = data('errors-prose.json');
  const sdk = Object.fromEntries(data('sdk-errors.json').codes.map((c) => [c.code, c]));
  for (const e of errors) {
    const p = prose[e.code];
    if (!p) throw new Error(`docs/api/errors.md has no section for ${e.code}. Every catalog code needs one.`);
    const codes = new Set(errors.map((x) => x.code));
    const local = (md) => md.replace(/\]\(#([a-z][a-z0-9_]*)\)/g, (m, c) => (codes.has(c) ? `](/errors/${c}/)` : m));
    const when = local(rewriteLinks(sessionRefs(p.when), 'docs/api/errors.md'));
    const todo = local(rewriteLinks(sessionRefs(p.todo), 'docs/api/errors.md'));
    const summary = describe(when.split(/(?<=\.)\s/)[0], 200);
    const ex = p.example ?? { type: e.type, code: e.code, message: e.default_message, param: null };
    const env = JSON.stringify({ error: { ...ex, request_id: 'req_01j9zr3m5k8q2w7x4t6v0b1c9d', doc_url: e.doc_url } }, null, 2);
    const retry = e.retryable ? (e.status === 429 ? 'retry after the time in Retry-After' : 'safe to retry') : 'not retryable';
    const cls = sdk[e.code]?.class ?? 'APIError';
    const siblings = errors.filter((x) => x.type === e.type && x.code !== e.code).map((x) => x.code);
    const related = [...new Set([...p.related, ...siblings])].slice(0, 6);
    const body = [
      frontmatter({ title: e.code, description: describe(summary), tableOfContents: false, pagefind: true, sidebar: { hidden: true } }),
      `${summary}\n`,
      `<p class="dg-kv"><span class="dg-num">HTTP ${e.status}</span> · <code>${e.type}</code> · ${retry}</p>\n`,
      `## What happened\n\n${when}\n`,
      `## What to do\n\n${todo}\n`,
      `## Example\n\n\`\`\`json title="HTTP ${e.status} · ${e.code}"\n${env}\n\`\`\`\n`,
      `The message is for a person and can get more exact over time: branch on \`type\` and \`code\`, never on the message.\n`,
      `## In the SDKs\n\n| SDK | Raised as |\n|---|---|\n| TypeScript (\`@deedglass/sdk\`) | \`${cls}\` with \`code === "${e.code}"\` |\n| Python (\`deedglass\`) | \`deedglass.${cls}\` with \`.code == "${e.code}"\` |\n`,
      `## Related\n\n${related.map((c) => `- [\`${c}\`](/errors/${c}/)`).join('\n')}\n- [Every error code](/errors/)\n`,
    ].join('\n');
    write(`errors/${e.code}.md`, body);
  }
  const byType = {};
  for (const e of errors) (byType[e.type] ??= []).push(e);
  let idx = frontmatter({
    title: 'Errors',
    description: `Every error Deedglass returns: ${errors.length} codes in six types, each with what happened, what to do and the exact envelope.`,
    sidebar: { hidden: true },
  });
  idx += `Every error has the same envelope. Branch on \`type\` for broad handling and on \`code\` for exact handling; codes are never renamed. Each \`doc_url\` in an error opens the page for its code below. **${errors.length} codes.**\n\n`;
  idx += '```json title="The error envelope"\n' + JSON.stringify({ error: { type: 'invalid_request_error', code: 'validation_failed', message: '`limit` must be between 1 and 100.', param: 'limit', request_id: 'req_01j9zr3m5k8q2w7x4t6v0b1c9d', doc_url: 'https://docs.deedglass.com/errors/validation_failed' } }, null, 2) + '\n```\n\n';
  for (const t of Object.keys(typeNames)) {
    const list = byType[t] ?? [];
    idx += `## ${typeNames[t]} (\`${t}\`)\n\n| Code | HTTP | Default message |\n|---|---|---|\n`;
    idx += list.map((e) => `| [\`${e.code}\`](/errors/${e.code}/) | ${e.status} | ${e.default_message.replace(/\|/g, '\\|')} |`).join('\n') + '\n\n';
  }
  // the CLI's own codes (doc_url in `deedglass … --json` errors), generated from cmd/deedglass
  const cliErrs = data('cli-errors.json');
  const exitMeaning = { 0: 'ok', 1: 'a failure', 2: 'could not run, or a usage error', 3: 'not authenticated' };
  for (const c of cliErrs) {
    const exits = c.exit.length ? c.exit.map((x) => `exit ${x} (${exitMeaning[x] ?? x})`).join(' or ') : 'a non-zero exit';
    let page = frontmatter({ title: c.code, description: describe(`A deedglass CLI error: ${c.messages?.[0] ?? 'the command stopped and printed what to do next.'}`), tableOfContents: false, sidebar: { hidden: true } });
    page += `A \`deedglass\` command stopped with this code. It comes from the CLI, not the API.\n\n<p class="dg-kv"><span>CLI</span> · <span>${exits}</span></p>\n\n`;
    page += `## What happened\n\nThe command printed one sentence that says what happened. With \`--json\`, the same sentence is \`error.message\` and this page is \`error.doc_url\`.\n\n`;
    if (c.messages?.length) page += `What it says, for example:\n\n${c.messages.map((m) => '- ' + m).join('\n')}\n\n`;
    page += `## What to do\n\nRun the command in the \`next\` line under the message${c.next?.length ? ', for example:\n\n' + c.next.map((n) => '- `' + n.replace(/`/g, '') + '`').join('\n') + '\n\n' : '. '}Add \`-v\` for more detail. If the message names a setting, [Configuration](/reference/config/) explains it; \`deedglass doctor\` checks the whole setup.\n\n`;
    page += `## Related\n\n- [CLI reference](/reference/cli/)\n- [Every error code](/errors/)\n`;
    write(`errors/${c.code}.md`, page);
  }
  idx += `## CLI codes\n\nThese come from the \`deedglass\` CLI (in \`--json\` errors), not from the API.\n\n| Code | Exit |\n|---|---|\n`;
  idx += cliErrs.map((c) => `| [\`${c.code}\`](/errors/${c.code}/) | ${c.exit.join(', ')} |`).join('\n') + '\n';
  write('errors/index.md', idx);
  return errors.length + cliErrs.length;
}

// Policy diagnostics: one page per "## <code>" section of docs/POLICY_ERRORS.md, at
// /policies/errors/<code>/, the doc_url every diagnostic carries.
function policyErrorPages() {
  const md = readFileSync(join(repo, 'docs', 'POLICY_ERRORS.md'), 'utf8');
  const parts = md.split(/^## /m).slice(1);
  const rows = [];
  for (const part of parts) {
    const nl = part.indexOf('\n');
    const code = part.slice(0, nl).trim();
    if (!/^[a-z][a-z0-9_]*$/.test(code)) continue;
    let body = rewriteLinks(part.slice(nl + 1), 'docs/POLICY_ERRORS.md').trim();
    body = body.replace(/\]\(\/reference\/engineering\/policy-errors\/#([a-z0-9_]+)\)/g, '](/policies/errors/$1/)');
    const sev = /\*\*Severity:\*\*\s*([a-z]+)/.exec(body)?.[1] ?? 'error';
    const first = body.replace(/\*\*Severity:\*\*[^\n]*\n+/, '').split(/\n\n/)[0];
    rows.push({ code, sev, first });
    write(`policies/errors/${code}.md`, frontmatter({ title: code, description: describe(first), tableOfContents: false, sidebar: { hidden: true } }) + `A policy diagnostic from \`deedglass policy lint\` and the policy routes.\n\n` + body.replace(/^(#{3,})\s/gm, '## ') + `\n\n## Related\n\n- [Every policy diagnostic](/policies/errors/)\n- [Policy language](/reference/policy-language/)\n`);
  }
  let idx = frontmatter({ title: 'Policy diagnostics', description: `All ${rows.length} codes the policy compiler reports, each with an example and the fix.`, sidebar: { hidden: true } });
  idx += `Every diagnostic from \`deedglass policy lint\` and the policy routes has a stable \`code\` and a \`doc_url\` that opens its page here. An \`error\` means the policy is not accepted; a \`warning\` means it is accepted and you should look at that line.\n\n| Code | Severity | What it means |\n|---|---|---|\n`;
  idx += rows.map((r) => `| [\`${r.code}\`](/policies/errors/${r.code}/) | ${r.sev} | ${describe(r.first, 140).replace(/\|/g, '\\|')} |`).join('\n') + '\n';
  write('policies/errors/index.md', idx);
  return rows.length;
}

const groupSlug = (g) => g.toLowerCase().replace(/[^a-z0-9]+/g, '-');

function cliPages() {
  const cli = data('cli.json');
  const notBuilt = [];
  let index = frontmatter({ title: 'CLI reference', description: `Every deedglass command, generated from deedglass help --json (${cli.version}).` });
  index += `Generated from \`deedglass help --json\` (CLI ${cli.version}). Every command takes \`--json\`. Exit codes: ${Object.entries(cli.exit_codes).map(([k, v]) => `\`${k}\` ${v}`).join(' · ')}.\n\n| Group | Commands |\n|---|---|\n`;
  for (const g of cli.groups) {
    const cmds = g.commands.filter((c) => !c.hidden);
    const built = cmds.filter((c) => c.built);
    notBuilt.push(...cmds.filter((c) => !c.built).map((c) => 'deedglass ' + c.path.join(' ')));
    index += `| [${g.name}](/reference/cli/${groupSlug(g.name)}/) | ${built.map((c) => `\`${c.path.join(' ')}\``).join(', ')} |\n`;
    let page = frontmatter({ title: `CLI: ${g.name}`, description: describe(`The ${g.name} commands: ${built.map((c) => c.path.join(' ')).join(', ')}.`) });
    page += `Generated from \`deedglass help --json\` (CLI ${cli.version}). Global flags and exit codes are on the [CLI reference](/reference/cli/) page.\n\n`;
    for (const c of built) {
      const name = `deedglass ${c.path.join(' ')}`;
      page += `## ${name}\n\n${codeUrls(c.summary)}.\n\n\`\`\`sh\n${name}${c.usage ? ' ' + c.usage : ''}\n\`\`\`\n\n`;
      const flags = (c.flags ?? []).filter((f) => f.name !== 'json');
      if (flags.length) {
        page += `| Flag | Type | Default | What it does |\n|---|---|---|---|\n`;
        page += flags
          .map((f) => `| \`--${f.name}\` | ${f.type} | ${f.default && f.default !== 'false' ? `\`${f.default}\`` : ''} | ${codeUrls(f.usage).replace(/\|/g, '\\|')}${f.values ? ` (${f.values.map((v) => `\`${v}\``).join(', ')})` : ''} |`)
          .join('\n') + '\n\n';
      }
      page += `Add \`--json\` for machine-readable output.\n\n`;
    }
    const unbuilt = cmds.filter((c) => !c.built);
    if (unbuilt.length) page += `:::note[Not in this release]\nThese commands are planned and not in ${cli.version.replace(/-dev$/, '')}: ${unbuilt.map((c) => `\`deedglass ${c.path.join(' ')}\``).join(', ')}.\n:::\n`;
    write(`reference/cli/${groupSlug(g.name)}.md`, page);
  }
  index += `\n## Global flags\n\n| Flag | Type | What it does |\n|---|---|---|\n${cli.global_flags.map((f) => `| \`--${f.name}\` | ${f.type} | ${codeUrls(f.usage)} |`).join('\n')}\n\n`;
  index += `## Exit codes\n\n| Code | Meaning |\n|---|---|\n${Object.entries(cli.exit_codes).map(([k, v]) => `| \`${k}\` | ${v} |`).join('\n')}\n\n`;
  if (notBuilt.length) index += `:::note[Not in this release]\nThese commands are planned and not in ${cli.version.replace(/-dev$/, '')}: ${notBuilt.map((c) => `\`${c}\``).join(', ')}.\n:::\n`;
  write('reference/cli/index.md', index);
  return cli.groups.length;
}

function configPage() {
  const rows = data('config.json');
  const table = (list) =>
    `| Variable | \`deedglass.yaml\` | Type | Default | What it does |\n|---|---|---|---|---|\n` +
    list
      .map((r) => {
        const def = r.secret ? 'none; never logged or printed' : r.default ? `\`${r.default}\`` : '';
        const vals = r.values ? ` Values: ${r.values.map((v) => `\`${v}\``).join(', ')}.` : '';
        return `| \`${r.env}\` | ${r.client ? '' : `\`${r.yaml}\``} | ${r.type}${r.secret ? ', secret' : ''} | ${def} | ${r.doc.replace(/\|/g, '\\|')}${vals} Used by: ${r.commands}. |`;
      })
      .join('\n') + '\n';
  let page = frontmatter({ title: 'Configuration and environment variables', description: `All ${rows.length} DEEDGLASS_ settings, generated from internal/config: type, default, who reads it, and which are secret.` });
  page += `Generated from the settings table in \`internal/config/keys.go\`. Environment variables win over \`deedglass.yaml\`. In prod, an unknown \`DEEDGLASS_*\` name is an error, and secrets are never written to logs or printed by \`deedglass config print\`.\n\n`;
  page += `## Server settings\n\n${table(rows.filter((r) => !r.client && !r.dev_only))}\n## Client settings\n\nRead by the CLI, the MCP commands and the SDKs; \`deedglass serve\` ignores them.\n\n${table(rows.filter((r) => r.client))}\n`;
  const dev = rows.filter((r) => r.dev_only && !r.client);
  if (dev.length) page += `## Dev only\n\nRefused when \`DEEDGLASS_ENV=prod\`.\n\n${table(dev)}\n`;
  page += `## Everything else\n\nTunable behaviour (\`gate.*\`, \`witness.*\`, \`decoder.*\`, \`approvals.*\`, \`webhooks.*\`, \`undo.*\`, \`mcp.*\`) lives in \`deedglass.yaml\`, not in new environment variables. The [configuration engineering note](/reference/engineering/config/) lists every key.\n`;
  write('reference/config.md', page);
  return rows.length;
}

function attackPage() {
  const a = data('attack.json');
  const sym = {
    matched: '<span class="dg-st ok"><span aria-hidden="true">✓</span> matched</span>',
    differs: '<span class="dg-st bad"><span aria-hidden="true">✕</span> differs</span>',
    waiting: '<span class="dg-st gap"><span aria-hidden="true">○</span> waiting</span>',
    not_run: '<span class="dg-st gap"><span aria-hidden="true">○</span> not run</span>',
  };
  let page = frontmatter({ title: 'Attack suite results', description: `${a.summary.matched} matched · ${a.summary.differs} differ · ${a.summary.waiting} waiting. The attack cases Deedglass runs against a real server and Postgres, and what each did.` });
  page += `These cases assume the model has already been manipulated. They test what Deedglass does with the calls that come out of it; they don't measure how often a model falls for an injection. Every expected outcome was written down before its code existed, and a test pins a digest of the expectations, so a case that turns out different shows as differs and is never edited to pass.\n\n`;
  page += `<p class="dg-kv"><strong>${a.summary.matched} matched · ${a.summary.differs} differ · ${a.summary.waiting} waiting</strong> · last run ${a.day} · commit <code>${a.commit}</code> · PostgreSQL ${a.postgres}</p>\n\n`;
  page += `After every case the deed chains of both orgs were **${a.chains}**, the target database was unchanged wherever nothing should have run, every action was sealed, and the planted credential was seen **${a.secrets_seen}** times across the server's logs, every HTTP response and every stored record.\n\n`;
  for (const l of a.layers) {
    page += `## ${l.title}\n\n${sessionRefs(l.intro)}\n\n| Case | Attack | Expected | Actual | Status |\n|---|---|---|---|---|\n`;
    page += l.cases.map((c) => `| ${c.id} | ${c.title.replace(/\|/g, '\\|')} | ${c.expected.replace(/\|/g, '\\|')} | ${c.actual.replace(/\|/g, '\\|')} | ${sym[c.status]} |`).join('\n') + '\n\n';
  }
  page += `## What this suite doesn't cover\n\n${a.not_covered.map((l) => '- ' + sessionRefs(l).replace(/`docs\/WITNESS\.md`/, '[the Witness notes](/reference/engineering/witness/)')).join('\n')}\n`;
  write('security/attack-suite.md', page);
}

// S35 (DL-049 carry-over): the API reference renders a slimmed copy of api/openapi.yaml. Each
// operation's request and response schemas are inlined to SLIM_DEPTH levels; a deeper object
// becomes a one-line pointer to its schema in the full file, which /api/openapi.yaml serves
// unchanged. Every operation, parameter, status and top-level field stays; only deep nesting goes.
const SLIM_DEPTH = 2;
function slimOpenAPI() {
  const spec = yaml.load(readFileSync(join(repo, 'api', 'openapi.yaml'), 'utf8'));
  const schemas = spec.components?.schemas ?? {};
  const deref = (node, depth, seen) => {
    if (Array.isArray(node)) return node.map((n) => deref(n, depth, seen));
    if (!node || typeof node !== 'object') return node;
    if (typeof node.$ref === 'string' && node.$ref.startsWith('#/components/schemas/')) {
      const name = node.$ref.slice('#/components/schemas/'.length);
      const target = schemas[name];
      if (!target) return node;
      if (depth >= SLIM_DEPTH || seen.has(name)) {
        const kind = target.type === 'array' ? 'array' : target.enum ? target.type ?? 'string' : 'object';
        if (target.enum || (target.type && target.type !== 'object' && target.type !== 'array')) return deref(target, depth, seen);
        return { type: kind, description: `${target.description ? target.description.split('\n')[0] + ' ' : ''}The full \`${name}\` schema is in [openapi.yaml](/api/openapi.yaml).` };
      }
      return deref(target, depth, new Set([...seen, name]));
    }
    const out = {};
    for (const [k, v] of Object.entries(node)) {
      const nested = k === 'properties' || k === 'items' || k === 'additionalProperties';
      out[k] = k === 'properties' ? Object.fromEntries(Object.entries(v).map(([pk, pv]) => [pk, deref(pv, depth + 1, seen)])) : deref(v, nested ? depth + 1 : depth, seen);
    }
    return out;
  };
  const out = { ...spec, paths: {} };
  for (const [path, item] of Object.entries(spec.paths ?? {})) {
    out.paths[path] = deref(item, 0, new Set());
  }
  out.components = { ...spec.components, schemas: {} };
  for (const k of ['responses', 'requestBodies', 'parameters']) if (spec.components?.[k]) out.components[k] = deref(spec.components[k], 0, new Set());
  writeFileSync(join(site, 'src', 'data', 'openapi.docs.json'), JSON.stringify(out));
  mkdirSync(join(site, 'public', 'api'), { recursive: true });
  cpSync(join(repo, 'api', 'openapi.yaml'), join(site, 'public', 'api', 'openapi.yaml'));
  return out;
}

// The endpoint index: every operation in api/openapi.yaml, by tag, linked to the OpenAPI plugin's
// page for it (one sidebar link instead of 121, which kept every page's HTML small).
function endpointsPage() {
  const spec = yaml.load(readFileSync(join(repo, 'api', 'openapi.yaml'), 'utf8'));
  const byTag = new Map((spec.tags ?? []).map((t) => [t.name, { desc: t.description ?? '', ops: [] }]));
  let n = 0;
  for (const [path, item] of Object.entries(spec.paths ?? {})) {
    for (const m of ['get', 'post', 'put', 'patch', 'delete']) {
      const op = item[m];
      if (!op?.operationId) continue;
      const tag = op.tags?.[0] ?? 'Other';
      if (!byTag.has(tag)) byTag.set(tag, { desc: '', ops: [] });
      byTag.get(tag).ops.push({ m: m.toUpperCase(), path, id: op.operationId, summary: op.summary ?? '' });
      n++;
    }
  }
  let page = frontmatter({ title: 'Endpoints', description: `All ${n} operations of the Deedglass API, by area, generated from api/openapi.yaml.` });
  page += `Every operation in \`api/openapi.yaml\` (API version ${spec.info.version}), the file a test keeps equal to the server's mounted routes. Each links to its full reference: parameters, request body, responses and examples.\n\n`;
  for (const [tag, t] of byTag) {
    if (!t.ops.length) continue;
    page += `## ${tag}\n\n${t.desc ? sessionRefs(String(t.desc)).trim() + '\n\n' : ''}| Method | Path | What it does |\n|---|---|---|\n`;
    page += t.ops.map((o) => `| ${o.m} | [\`${o.path}\`](/reference/api/operations/${o.id.toLowerCase()}/) | ${o.summary.replace(/\|/g, '\\|')} |`).join('\n') + '\n\n';
  }
  write('reference/http/endpoints.md', page);
  return n;
}

function engineeringIndex() {
  const rows = Object.entries(IMPORTS).filter(([, slug]) => slug.startsWith('reference/engineering/'));
  let page = frontmatter({ title: 'Engineering notes', description: 'The design notes and specs behind each part of Deedglass, imported from the repo at build time.' });
  page += `These are the repo's own engineering notes, imported as they are at build time. They go deeper than the guides and name the sessions and decisions that built each part.\n\n| Note | Source |\n|---|---|\n`;
  for (const [src, slug] of rows) {
    const md = readFileSync(join(repo, src), 'utf8');
    const t = /^# (.+)$/m.exec(md)?.[1] ?? slug;
    page += `| [${stripMd(t).replace(/\|/g, '\\|')}](/${slug}/) | \`${src}\` |\n`;
  }
  write('reference/engineering/index.md', page);
}

// The last commit date of each page's source, for the sitemap's lastmod and the JSON-LD dateModified
// (DOC-05): the hand-written page, the imported docs/*.md, or a generated page's data. One git call;
// without git (or for a file with no commit yet) a page simply has no date.
const GENERATED_SOURCES = [
  [/^errors(\/|$)/, 'docs-site/src/data/errors.json'],
  [/^policies\//, 'docs/POLICY_ERRORS.md'],
  [/^reference\/cli(\/|$)/, 'docs-site/src/data/cli.json'],
  [/^reference\/config$/, 'docs-site/src/data/config.json'],
  [/^security\/attack-suite$/, 'docs-site/src/data/attack.json'],
  [/^reference\/http\/endpoints$/, 'api/openapi.yaml'],
  [/^security\/limitations(\/|$)/, LIMITS_FILE],
];
function pageDates() {
  const dates = gitDates(['docs', 'docs-site/src/content/docs', 'docs-site/src/data', 'api/openapi.yaml']);
  const imported = Object.fromEntries(Object.entries({ ...IMPORTS, ...SOURCES }).map(([src, slug]) => [slug, src]));
  const generated = new Set(written);
  const out = {};
  for (const f of walkFiles(content)) {
    if (!/\.mdx?$/.test(f)) continue;
    const rel = relative(content, f).replace(/\\/g, '/');
    const slug = rel.replace(/\.mdx?$/, '').replace(/(^|\/)index$/, '');
    let src;
    if (generated.has(rel)) src = GENERATED_SOURCES.find(([re]) => re.test(slug))?.[1] ?? imported[slug];
    else src = imported[slug] ?? `docs-site/src/content/docs/${rel}`;
    const d = src && dates.get(src);
    if (d) out[slug ? `/${slug}/` : '/'] = d;
  }
  const api = dates.get('api/openapi.yaml');
  if (api) out['/reference/api/'] = api;
  // The engineering index lists the notes: it changes when the newest of them does.
  const notes = Object.entries(IMPORTS).filter(([, slug]) => slug.startsWith('reference/engineering/')).map(([src]) => dates.get(src)).filter(Boolean).sort();
  if (notes.length) out['/reference/engineering/'] = notes[notes.length - 1];
  const sorted = Object.fromEntries(Object.entries(out).sort(([a], [b]) => (a < b ? -1 : 1)));
  writeFileSync(join(site, 'src', 'data', '.page-dates.json'), JSON.stringify(sorted, null, 1) + '\n');
  return Object.keys(sorted).length;
}

function main() {
  const list = process.argv.includes('--list');
  if (list) {
    for (const [s, d] of Object.entries(IMPORTS)) console.log(`imported  ${s} → /${d}/`);
    for (const [s, d] of Object.entries(MOVED)) console.log(`moved     ${s} → /${d}/`);
    for (const [s, d] of Object.entries(SOURCES)) console.log(`generator ${s} → /${d}/`);
    for (const s of REPO_ONLY) console.log(`repo only ${s}`);
    return;
  }
  for (const d of ['errors', 'policies', 'reference/cli', 'reference/engineering', LIMITS_SLUG]) rmSync(join(content, d), { recursive: true, force: true });
  for (const [src, slug] of Object.entries(IMPORTS)) {
    if (!existsSync(join(repo, src))) throw new Error(`${src} is listed in IMPORTS but doesn't exist`);
    importDoc(src, slug);
  }
  if (existsSync(join(repo, 'docs', 'images'))) cpSync(join(repo, 'docs', 'images'), join(site, 'public', 'images'), { recursive: true });
  const n = errorPages();
  slimOpenAPI();
  const ops = endpointsPage();
  const pol = policyErrorPages();
  engineeringIndex();
  const g = cliPages();
  const k = configPage();
  attackPage();
  writeFileSync(join(site, 'src', 'data', '.generated-pages.json'), JSON.stringify(written.sort(), null, 1) + '\n');
  const dated = pageDates();
  console.log(`✓ sync-docs · ${Object.keys(IMPORTS).length} imported · ${n} error pages · ${g} CLI groups · ${k} settings · attack page · ${ops} endpoints · ${pol} policy diagnostics · ${dated} pages dated`);
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (e) {
    console.error(`✕ sync-docs: ${e.message}`);
    process.exit(1);
  }
}
