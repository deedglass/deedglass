// The kit's S26 section: SchemaForm, SecretField, CheckList (test results and Postgres readiness)
// and Countdown, every state, from small inline samples in the Acme story (never JSON from test/).
// Dev builds only, like the rest of /app/_kit.
//
// The components load with a dynamic import, so the kit page's static JS stays inside the
// per-page budget (scripts/check-dist.mjs follows static imports only).
import { h, maybe } from '../lib/dom.ts';
import type { JSONSchema } from '../lib/schema.ts';
import type { ConnectorTest } from './checklist.ts';

const int = (minimum: number, maximum: number): JSONSchema => ({ type: 'integer', minimum, maximum });
const strings = (description?: string): JSONSchema => ({ type: 'array', items: { type: 'string' }, ...(description ? { description } : {}) });

// S17's postgres config_schema, as GET /v1/connector-types serves it.
const postgresSchema: JSONSchema = {
  type: 'object',
  required: ['allowed_schemas'],
  properties: {
    mode: { type: 'string', enum: ['sql', 'controlled_functions'] },
    allowed_schemas: strings('the read scope and search_path, like [public]'),
    writable_tables: strings('schema.table the agent may INSERT, UPDATE and DELETE'),
    row_limit: int(1, 10000),
    max_result_bytes: int(1024, 16777216),
    max_rows_affected: int(1, 1000000),
    statement_timeout_ms: int(100, 60000),
    lock_timeout_ms: int(100, 60000),
    max_connections: int(1, 16),
    witness: {
      type: 'object',
      properties: {
        enabled: { type: 'boolean' },
        watched_tables: strings(),
        sensitive_columns: strings(),
        auto_columns: { type: 'object', description: 'table → columns a trigger maintains, like {public.orders: [updated_at]}' },
        l2_timeout: { type: 'string', description: "how long an action waits for Postgres's change log, 1s to 60s (10s)" },
        diff_rows: int(1, 100),
      },
    },
    functions: { type: 'array', items: { type: 'object', required: ['name'] } },
    throwaway: { type: 'boolean' },
  },
};

const postgresConfig = {
  mode: 'sql',
  allowed_schemas: ['public'],
  writable_tables: ['public.orders', 'public.refunds'],
  row_limit: 500,
  statement_timeout_ms: 5000,
  max_connections: 4,
  witness: { enabled: true, watched_tables: ['public.customers', 'public.orders', 'public.refunds'], sensitive_columns: ['public.customers.email'] },
  functions: [{ name: 'public.ship_order', args: [{ name: 'p_order_id', type: 'bigint' }], side_effect: 'write' }],
  throwaway: true,
};

// S15's mcp_http config_schema.
const mcpSchema: JSONSchema = {
  type: 'object',
  required: ['url'],
  properties: {
    url: { type: 'string', description: 'https://mcp.example.com/mcp (http in dev only)' },
    auth: { type: 'object', properties: { kind: { type: 'string', enum: ['bearer', 'header', 'none'] }, header: { type: 'string' } } },
    timeout_ms: int(100, 600000),
    tools_ttl_ms: int(0, 3600000),
  },
};

// S17's readiness checks on the demo database (trimmed), plus one synthetic failure.
const doc = (a: string) => `https://docs.deedglass.com/connectors/postgres#${a}`;
const readiness: ConnectorTest = {
  status: 'warn',
  duration_ms: 184,
  checks: [
    { id: 'connection', title: 'connection', status: 'pass', detail: 'deedglass_agent@127.0.0.1:55469/acme · local' },
    { id: 'wal_level', title: 'wal_level', status: 'pass', detail: 'logical' },
    { id: 'slot_capacity', title: 'slot capacity', status: 'pass', detail: '1 of 10 slots used · 0 of 10 senders' },
    { id: 'wal_keep_cap', title: 'wal keep cap', status: 'pass', detail: '1 GB' },
    { id: 'schema', title: 'schema deedglass', status: 'pass', detail: 'ledger · RLS forced · replica identity full' },
    { id: 'agent_role', title: 'agent role', status: 'pass', detail: 'least privilege · 2 writable tables · no ledger access', doc_url: doc('agent-role') },
    { id: 'witness.watched_tables', title: 'watched tables', status: 'pass', detail: '3 of 3 · primary keys · replica identity full', doc_url: doc('watched-tables') },
    { id: 'slot', title: 'slot', status: 'warn', detail: 'deedglass_01m42fmxj177kh3ve33xqv934g · inactive · WAL is kept until the witness reconnects', doc_url: doc('slot') },
    { id: 'tls', title: 'tls', status: 'warn', detail: 'off · local host', fix: "Use sslmode=verify-full for any database that isn't on this machine.", doc_url: doc('tls') },
    { id: 'pooler', title: 'pooler', status: 'skip', detail: 'not checked: no pooler in front of this database' },
    {
      id: 'emit_message_acl', title: 'marker ACL', status: 'warn',
      detail: 'PUBLIC may call pg_logical_emit_message (the default) · a forged marker has no ledger row, so the witness flags it',
      fix: 'Optional hardening, as a superuser: revoke it from PUBLIC and grant it to deedglass_owner.',
      fix_sql: 'REVOKE EXECUTE ON FUNCTION pg_catalog.pg_logical_emit_message(boolean, text, text, boolean) FROM PUBLIC;\nGRANT EXECUTE ON FUNCTION pg_catalog.pg_logical_emit_message(boolean, text, text, boolean) TO deedglass_owner, deedglass_witness;',
      doc_url: doc('marker-acl'),
    },
  ],
};
const failing: ConnectorTest = {
  status: 'fail',
  duration_ms: 212,
  checks: [
    { id: 'connection', title: 'connection', status: 'pass', detail: 'deedglass_agent@db.acme.internal:5432/acme · verify-full' },
    {
      id: 'wal_level', title: 'wal_level', status: 'fail', detail: 'replica · L2 needs logical',
      fix: 'Set wal_level to logical, then restart Postgres. The setting is read only at start.',
      fix_sql: 'ALTER SYSTEM SET wal_level = logical;',
      doc_url: doc('wal-level'),
    },
    { id: 'slot_capacity', title: 'slot capacity', status: 'pass', detail: '0 of 10 slots used · 0 of 10 senders' },
    { id: 'publication', title: 'publication', status: 'fail', detail: 'deedglass_pub is missing', fix: 'Run the setup plan again as a superuser to create it.', doc_url: doc('publication') },
    { id: 'tls', title: 'tls', status: 'pass', detail: 'verify-full · TLS 1.3' },
  ],
};
const allPass: ConnectorTest = {
  status: 'pass',
  duration_ms: 184,
  checks: ['base_url', 'destination', 'credential', 'tls', 'test_call'].map((id) => ({ id, title: id.replace('_', ' '), status: 'pass' as const })),
};
const restFail: ConnectorTest = {
  status: 'fail',
  duration_ms: 96,
  checks: [
    { id: 'base_url', title: 'base_url', status: 'pass' },
    { id: 'destination', title: 'destination', status: 'fail', detail: 'api.acme.internal resolves to 10.0.4.12, a private address', fix: 'Use a public host. Deedglass refuses private and metadata addresses outside development.' },
    { id: 'credential', title: 'credential', status: 'skip' },
    { id: 'tls', title: 'tls', status: 'skip' },
    { id: 'test_call', title: 'test call', status: 'skip' },
  ],
};

export function mountKitForms(): void {
  const host = maybe('kit-s26-forms');
  if (!host) return;
  void Promise.all([import('./checklist.ts'), import('./countdown.ts'), import('./schemaform.ts'), import('./secretfield.ts')]).then(([cl, cd, sf, sec]) => render(host, cl, cd, sf, sec));
}

function render(
  host: HTMLElement,
  { checkList, readinessLine, readinessSummary, testSummary }: typeof import('./checklist.ts'),
  { Countdown }: typeof import('./countdown.ts'),
  { schemaForm }: typeof import('./schemaform.ts'),
  { secretField }: typeof import('./secretfield.ts'),
): void {
  const label = (t: string) => h('p', { class: 'kit-label' }, t);
  const card = (...c: (Node | null)[]) => h('div', { class: 'card card-pad grid gap-4' }, ...c);

  const pg = schemaForm(postgresSchema, { idPrefix: 'kit-sf-pg', initial: postgresConfig });
  const pgRo = schemaForm(postgresSchema, { idPrefix: 'kit-sf-pgro', initial: postgresConfig, readOnly: true });
  const mcp = schemaForm(mcpSchema, { idPrefix: 'kit-sf-mcp' });
  // The server's own words under the field, as POST /v1/connectors returns them; focus stays put in the kit.
  const errs = () => mcp.setErrors([
    { param: 'config.url', message: 'url resolves to 169.254.169.254, a cloud metadata address. Use the server’s public address.' },
    { param: 'config.timeout_ms', message: 'timeout_ms must be at most 600000.' },
  ], { focus: false });

  const hidden = secretField({ id: 'kit-sec-1', label: 'dsn' });
  const shown = secretField({ id: 'kit-sec-2', label: 'token', help: 'The bearer token the MCP server expects. Deedglass adds it only when the Gate opens a session.' });
  const toggle = shown.el.querySelector<HTMLButtonElement>('.secret-toggle');
  const sample = shown.el.querySelector<HTMLInputElement>('input');
  if (sample) sample.value = 'kit-sample-not-a-secret';
  toggle?.click();
  const errored = secretField({ id: 'kit-sec-3', label: 'password' });
  errored.setError('The credential was refused: password authentication failed for user "deedglass_agent". Check the password, then save again.');

  const min = 60_000;
  const t0 = Date.now();
  const cds = [9 * min, 4 * min, 90_000, 0].map((ms) => Countdown(t0 + ms));

  host.replaceChildren(
    label('SchemaForm · postgres (editing, from the connector config)'), card(pg.el),
    label('SchemaForm · mcp_http (adding; two server errors mapped to their fields)'), card(mcp.el),
    label('SchemaForm · read-only (viewer)'), card(pgRo.el),
    label('SecretField · hidden · shown · server error'), card(hidden.el, shown.el, errored.el),
    label('CheckList · test summaries'),
    card(...[allPass, readiness, restFail].map((t) => h('p', { class: 'readiness' }, testSummary(t)))),
    label('CheckList · REST test with a failure and skips'), card(checkList(restFail, { idPrefix: 'kit-cl-rest' })),
    label('Postgres readiness · ready with notes'), card(readinessLine(readiness), checkList(readiness, { db: 'acme', idPrefix: 'kit-cl-ready' })),
    label('Postgres readiness · not ready'), card(readinessLine(failing), checkList(failing, { db: 'acme', title: readinessSummary(failing), idPrefix: 'kit-cl-fail' })),
    label('Countdown · 9 min · 4 min (urgent) · 90 s · expired'),
    card(h('div', { class: 'flex flex-wrap gap-6' }, ...cds.map((c) => h('span', { class: 't-dense' }, c.el)))),
  );
  errs(); // without moving focus: the kit page must not scroll on load
}
