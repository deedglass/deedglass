// The docs sidebar (S33 §3.3). Every page a stranger needs, in reading order. Draft pages (owner
// S47/S48) stay listed here: Starlight hides drafts from production builds, and `npm run dev`
// shows them.
import { existsSync, readFileSync } from 'node:fs';

// Drafts (draft: true, owner S47/S48) are left out of production builds, so their sidebar entries
// go too; `astro dev` keeps them.
const building = process.argv.includes('build');
const isDraft = (slug) => {
  for (const ext of ['.mdx', '.md']) {
    const f = new URL(`./src/content/docs/${slug}${ext}`, import.meta.url);
    if (existsSync(f)) return /^draft:\s*true\s*$/m.test(readFileSync(f, 'utf8').split('\n---')[0]);
  }
  return false;
};

const entry = (slug, label) => (label ? { slug, label } : { slug });
const p = (slug, label) => (building && isDraft(slug) ? null : entry(slug, label));
const clean = (items) => items.filter(Boolean).map((i) => (i.items ? { ...i, items: clean(i.items) } : i));

export const sidebar = clean([
  {
    label: 'Get started',
    items: [
      p('quickstart', 'Quickstart (5 min)'),
      p('get-started/install-deedglass', 'Install Deedglass'),
      p('get-started/scan', 'Scan in 60 seconds'),
      p('get-started/claude-code', 'Connect Claude Code'),
      p('get-started/claude-desktop', 'Connect Claude Desktop'),
      p('get-started/cursor-vscode-windsurf', 'Connect Cursor, VS Code and Windsurf'),
      p('get-started/govern-an-mcp-server', 'Govern an MCP server'),
      p('get-started/python-sdk', 'Use the Python SDK'),
      p('get-started/typescript-sdk', 'Use the TypeScript SDK'),
      p('get-started/postgres', 'Put Deedglass in front of Postgres'),
      p('get-started/first-policy', 'Your first policy'),
      p('get-started/install', 'Install in any client'),
      p('get-started/playground', 'The playground'),
    ],
  },
  {
    label: 'Concepts',
    items: [
      p('concepts/how-it-works', 'How Deedglass works'),
      p('concepts/action-lifecycle', 'The action lifecycle'),
      p('concepts/identity', 'Agents, keys and identity'),
      p('concepts/policies', 'Policies'),
      p('concepts/approvals', 'Approvals'),
      p('concepts/verification-levels', 'Verification levels'),
      p('concepts/what-verified-means', 'What VERIFIED means'),
      p('concepts/deeds', 'Deeds and the deed chain'),
      p('concepts/ledger', 'The Ledger'),
      p('concepts/stop', 'Stop'),
      p('concepts/insights', 'Insights'),
      p('concepts/modes', 'Gateway and proxy modes'),
    ],
  },
  {
    label: 'Guides',
    items: [
      p('guides/block-destructive-sql', 'Block destructive SQL'),
      p('guides/catch-a-mismatch', 'Catch "1 row reported, 301 committed"'),
      p('guides/undo', 'Undo a bad write'),
      p('guides/stripe-refunds', 'Stripe refunds with approval'),
      p('guides/github', 'GitHub agent guardrails'),
      p('guides/slack-approvals', 'Slack approvals'),
      p('guides/phone-approvals', 'Approvals on your phone'),
      p('guides/verify-as-an-auditor', 'Verify deeds as an auditor'),
      p('guides/rotate-keys', 'Rotate seal keys and API keys'),
      p('guides/replay', 'Test a policy against last week'),
      p('guides/production', 'Run in production on one VM'),
      p('guides/multi-agent', 'Multi-agent teams'),
      p('guides/troubleshooting', 'Troubleshooting with doctor'),
      p('guides/badge', 'Add the badge'),
      p('guides/scan-every-pull-request', 'Scan every pull request'),
      p('guides/policy-packs', 'Policy packs'),
    ],
  },
  {
    label: 'Reference',
    items: [
      { label: 'CLI', link: '/reference/cli/' },
      {
        label: 'REST API',
        collapsed: true,
        items: [
          p('reference/http/overview', 'Overview'),
          p('reference/http/authentication', 'Authentication'),
          p('reference/http/versioning', 'Versioning'),
          p('reference/http/idempotency', 'Idempotency'),
          p('reference/http/pagination', 'Pagination'),
          p('reference/http/events-and-webhooks', 'Events and webhooks'),
          p('reference/http/endpoints', 'Endpoints'),
        ],
      },
      { label: 'Errors', link: '/errors/' },
      p('reference/policy-language', 'Policy language'),
      p('reference/deed-spec', 'Deed spec'),
      p('reference/intents', 'Signed intents spec'),
      {
        label: 'Connectors',
        collapsed: true,
        items: [
          p('reference/connectors/overview', 'Overview'),
          p('reference/connectors/rest', 'REST'),
          p('reference/connectors/postgres', 'Postgres'),
          p('reference/connectors/github', 'GitHub'),
          p('reference/connectors/slack', 'Slack'),
          p('reference/connectors/stripe', 'Stripe (test mode)'),
          p('reference/connectors/gmail', 'Gmail drafts'),
          p('reference/connectors/notion', 'Notion'),
          p('reference/connectors/mcp-http', 'Remote MCP servers'),
          p('reference/connectors/fake', 'fake (dev only)'),
        ],
      },
      { label: 'Configuration', link: '/reference/config/' },
      p('reference/ids', 'Ids'),
      p('reference/mcp-compatibility', 'MCP compatibility'),
      p('reference/api-versions', 'API versions'),
      p('reference/doctor-checks', 'Doctor checks'),
      p('reference/pro-and-licences', 'Pro and licences'),
      { label: 'Engineering notes', link: '/reference/engineering/' },
    ],
  },
  {
    label: 'SDKs',
    items: [
      p('sdks/typescript', 'TypeScript'),
      { label: 'TypeScript adapters', collapsed: true, items: [p('sdks/adapters/ai-sdk', 'Vercel AI SDK'), p('sdks/adapters/openai-agents', 'OpenAI Agents'), p('sdks/adapters/langchain', 'LangChain.js'), p('sdks/adapters/claude-agent-sdk', 'Claude Agent SDK')] },
      p('sdks/python', 'Python'),
      p('sdks/go', 'Go'),
    ],
  },
  {
    label: 'Security',
    items: [
      p('security/overview', 'Overview'),
      p('security/threat-model', 'Threat model'),
      p('security/limitations', 'Limitations'),
      { label: 'Attack suite results', link: '/security/attack-suite/' },
      p('security/review', 'Security review'),
      p('security/data-handling', 'Data handling and privacy'),
      p('security/disclosure', 'Responsible disclosure'),
    ],
  },
  { label: 'FAQ', items: [p('faq', 'FAQ')] },
]);
