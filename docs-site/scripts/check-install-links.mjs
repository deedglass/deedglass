#!/usr/bin/env node
// check-install-links.mjs (S33 §3.6): every one-click link on "Install in any client" decodes back
// to the one server object, and the Cursor link stays under 10,000 characters.
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { site } from './lib-pages.mjs';

export function decode(client, c) {
  const u = c.link ? new URL(c.link) : null;
  switch (client) {
    case 'Cursor':
    case 'LM Studio':
      return JSON.parse(Buffer.from(u.searchParams.get('config'), 'base64').toString('utf8'));
    case 'VS Code': {
      const { name, ...server } = JSON.parse(decodeURIComponent(c.link.slice(c.link.indexOf('?') + 1)));
      if (name !== 'deedglass') throw new Error('name is not deedglass');
      return server;
    }
    case 'Goose':
      return { command: u.searchParams.get('cmd'), args: u.searchParams.getAll('arg') };
    case 'Claude Code': {
      const m = /^claude mcp add (?:--scope user )?deedglass -- (\S+) (.+)$/.exec(c.command);
      return { command: m[1], args: m[2].split(' ') };
    }
    default:
      return null;
  }
}

export function check(data) {
  const want = JSON.stringify(data.server);
  const fails = [];
  for (const c of data.clients) {
    if (c.client === 'Claude Desktop') continue;
    let got;
    try {
      got = JSON.stringify(decode(c.client, c));
    } catch (e) {
      fails.push(`${c.client}: ${e.message}`);
      continue;
    }
    if (got !== want) fails.push(`${c.client}: decodes to ${got}, not ${want}`);
    if (c.client === 'Cursor' && c.link.length > 10000) fails.push(`Cursor: the link is ${c.link.length} characters (at most 10,000)`);
  }
  if (want !== JSON.stringify({ command: 'npx', args: ['-y', 'deedglass', 'mcp', 'serve'] })) fails.push('the server object changed');
  return fails;
}

if (process.argv[1] && import.meta.url.endsWith(process.argv[1].split('/').pop())) {
  const data = JSON.parse(readFileSync(join(site, 'src/data/install-links.json'), 'utf8'));
  const fails = check(data);
  if (fails.length) {
    for (const f of fails) console.error('✕ ' + f);
    process.exit(1);
  }
  console.log(`✓ ${data.clients.length} clients · every link decodes to the same server object · Cursor URL ≤ 10,000`);
}
