// wrapTool: your own function behind Deedglass (proxy mode). With sdk-demo.yaml active, a $20.00
// refund runs, deleting a customer never runs, and a $120.00 refund waits for a person.
//   node examples/ts/wrap/index.ts
import { ApprovalPendingError, Deedglass, PolicyDeniedError, wrapTool } from '@deedglass/sdk';
import { no, ok, short, wait } from '../_lib/print.ts';

const dg = new Deedglass();

// Your functions: these stand in for real billing calls.
async function refundCustomer(args: { customer: string; amount: number }): Promise<{ refunded: number }> {
  return { refunded: args.amount };
}
async function deleteCustomer(_args: { customer: string }): Promise<void> {
  throw new Error('unreachable: policy never lets this run');
}

const refund = wrapTool(refundCustomer, { client: dg, tool: 'billing.refund_customer', onSealed: (a) => ok(`billing.refund_customer · executed · ${a.witness?.level ?? 'L0'} · ${a.witness?.verdict ?? 'pending'} · ${short(a.deed)}`) });
const remove = wrapTool(deleteCustomer, { client: dg, tool: 'billing.delete_customer' });

await refund({ customer: 'cus_123', amount: 2000 });
try {
  await remove({ customer: 'cus_123' });
} catch (e) {
  if (e instanceof PolicyDeniedError) no(`billing.delete_customer · denied · ${e.message} Nothing was run.`);
  else throw e;
}
try {
  await refund({ customer: 'cus_123', amount: 12000 });
} catch (e) {
  if (e instanceof ApprovalPendingError) wait(`billing.refund_customer · pending approval · ${short(e.approvalId)} · a person decides in Console › Approvals`);
  else throw e;
}
