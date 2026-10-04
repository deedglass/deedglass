// The quickstart: one connector tool through Deedglass (gateway mode), decided, run and sealed.
//   DEEDGLASS_API_KEY=dg_test_… node examples/ts/quickstart/index.ts
import { Deedglass } from '@deedglass/sdk';
import { line, ok } from '../_lib/print.ts';

const dg = new Deedglass(); // reads DEEDGLASS_API_KEY and DEEDGLASS_SERVER
const a = await dg.act('fake.echo', { message: 'hello from @deedglass/sdk' });
ok(line(a));
