// The Medplum example's setup (run.sh calls it; examples/medplum/README.md walks through it by hand).
//
// On Medplum: the made-up super admin logs in, creates an AccessPolicy and a ClientApplication for
// the agent's connector, and a synthetic Patient. The AccessPolicy marks Observation.note read-only,
// a setting real projects use; Medplum then accepts a write that sets a note, answers 201 and
// stores the Observation without it. Scenario (b) in agent.ts runs into exactly that.
//
// On Deedglass: a fhir connector named medplum (bearer token of that client, Proven on when
// MEDPLUM_WITNESS_DSN is set), an agent and its key. Every value printed is an id, never a secret;
// the keys go to examples/medplum/.env.keys (0600, git-ignored).
//
//   MEDPLUM_URL=… MEDPLUM_ADMIN_EMAIL=… MEDPLUM_ADMIN_PASSWORD=… DEEDGLASS_SERVER=… DEEDGLASS_ADMIN_KEY=… \
//   [MEDPLUM_WITNESS_DSN=…] node examples/medplum/setup.ts
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

function need(name: string): string {
  const v = process.env[name];
  if (!v) {
    console.error(`✕ ${name} isn't set. Run examples/medplum/run.sh, or set it and run this again.`);
    process.exit(2);
  }
  return v;
}

const medplum = need('MEDPLUM_URL').replace(/\/+$/, '');
const server = need('DEEDGLASS_SERVER').replace(/\/+$/, '');
const adminKey = need('DEEDGLASS_ADMIN_KEY');
const witnessDSN = process.env.MEDPLUM_WITNESS_DSN ?? '';

type Json = Record<string, any>;

async function call(base: string, method: string, path: string, body?: Json | URLSearchParams, token?: string): Promise<Json> {
  const headers: Record<string, string> = { Accept: 'application/json' };
  let payload: string | undefined;
  if (body instanceof URLSearchParams) {
    headers['Content-Type'] = 'application/x-www-form-urlencoded';
    payload = body.toString();
  } else if (body) {
    headers['Content-Type'] = base === medplum && path.startsWith('/fhir/') ? 'application/fhir+json' : 'application/json';
    payload = JSON.stringify(body);
  }
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(base + path, { method, headers, body: payload });
  const text = await res.text();
  const out = text ? JSON.parse(text) : {};
  if (res.status >= 300) {
    // Never echo the body of an auth answer: it can hold a token.
    const what = path.startsWith('/oauth2/') || path.startsWith('/auth/') ? `HTTP ${res.status}` : `HTTP ${res.status} ${text.slice(0, 300)}`;
    throw new Error(`${method} ${path}: ${what}`);
  }
  return out;
}

// --- Medplum ---
const login = await call(medplum, 'POST', '/auth/login', {
  email: need('MEDPLUM_ADMIN_EMAIL'), password: need('MEDPLUM_ADMIN_PASSWORD'),
  scope: 'openid', codeChallenge: 'deedglass-example', codeChallengeMethod: 'plain',
});
const session = await call(medplum, 'POST', '/oauth2/token', new URLSearchParams({ grant_type: 'authorization_code', code: login.code, code_verifier: 'deedglass-example' }));
const admin = session.access_token as string;
const project = String(session.project.reference).replace('Project/', '');

const policy = await call(medplum, 'POST', '/fhir/R4/AccessPolicy', {
  resourceType: 'AccessPolicy',
  name: 'charting-agent',
  resource: [{ resourceType: 'Patient' }, { resourceType: 'Observation', readonlyFields: ['note'] }],
}, admin);
const client = await call(medplum, 'POST', `/admin/projects/${project}/client`, {
  name: 'deedglass-charting-agent',
  description: 'The Deedglass fhir connector of the Medplum example.',
  accessPolicy: { reference: `AccessPolicy/${policy.id}` },
}, admin);
const cc = await call(medplum, 'POST', '/oauth2/token', new URLSearchParams({ grant_type: 'client_credentials', client_id: client.id, client_secret: client.secret }));
const clientToken = cc.access_token as string;
const patient = await call(medplum, 'POST', '/fhir/R4/Patient', { resourceType: 'Patient', name: [{ family: 'Synthetic', given: ['Example'] }] }, clientToken);
console.log(`✓ Medplum · project ${project} · AccessPolicy/${policy.id} (Observation.note read-only) · ClientApplication/${client.id} · Patient/${patient.id}`);

// --- Deedglass ---
const config: Json = {
  base_url: `${medplum}/fhir/R4`,
  auth: { kind: 'bearer' },
  allowed_resource_types: ['Observation', 'Patient'],
  allowed_interactions: ['read', 'vread', 'search', 'history', 'create', 'update', 'patch'],
  if_match: 'when_known',
};
const secret: Json = { token: clientToken };
if (witnessDSN) {
  config.witness = { enabled: true, server: 'medplum', author: `ClientApplication/${client.id}` };
  secret.witness_dsn = witnessDSN;
}
const connector = await call(server, 'POST', '/v1/connectors', { type: 'fhir', name: 'medplum', description: 'Medplum (examples/medplum). Synthetic data only.', config }, adminKey);
await call(server, 'PUT', `/v1/connectors/${connector.id}/credentials`, { kind: 'bearer', secret }, adminKey);
if (witnessDSN) {
  const deadline = Date.now() + 60_000;
  for (;;) {
    const d = await call(server, 'GET', `/v1/connectors/${connector.id}/decoder`, undefined, adminKey);
    if (d.status === 'running') break;
    if (Date.now() > deadline) throw new Error(`the medplum decoder isn't running: ${d.status}`);
    await new Promise((r) => setTimeout(r, 250));
  }
}
const agent = await call(server, 'POST', '/v1/agents', { name: 'charting-agent' }, adminKey);
const key = await call(server, 'POST', `/v1/agents/${agent.id}/keys`, {}, adminKey);
console.log(`✓ Deedglass · connector medplum (${connector.id}) · ${witnessDSN ? 'Proven on' : 'Checked only'} · agent charting-agent (${agent.id})`);

const here = dirname(fileURLToPath(import.meta.url));
writeFileSync(join(here, '.env.keys'), [
  `DEEDGLASS_AGENT_KEY=${key.secret}`,
  `MEDPLUM_PATIENT_ID=${patient.id}`,
  `MEDPLUM_CLIENT_ID=${client.id}`,
  '',
].join('\n'), { mode: 0o600 });
