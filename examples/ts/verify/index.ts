// Check deeds offline with @deedglass/sdk/verify: two actions, their deeds, the server's seal keys,
// then every seal checked on this machine.
//   node examples/ts/verify/index.ts
import { Deedglass } from '@deedglass/sdk';
import { fetchKeys, verifyDeed } from '@deedglass/sdk/verify';
import { no, ok } from '../_lib/print.ts';

const dg = new Deedglass();
const ids: string[] = [];
for (const message of ['first', 'second']) {
  const a = await dg.act('fake.echo', { message });
  if (a.deed) ids.push(a.deed);
}
const { keys } = await fetchKeys(dg.baseUrl + '/.well-known/deedglass/keys.json');
const kids = new Set<string>();
let bad = 0;
for (const id of ids) {
  const d = await dg.deeds.get(id);
  try {
    await verifyDeed(JSON.stringify(d), keys);
    kids.add(d.seal_key_id);
  } catch (e) {
    bad++;
    no(`${id} · ${(e as Error).message}`);
  }
}
if (bad === 0) ok(`${ids.length} deeds · seals intact · chain not checked · ${kids.size} seal key${kids.size === 1 ? '' : 's'}   (checked offline with @deedglass/sdk/verify)`);
else process.exit(1);
