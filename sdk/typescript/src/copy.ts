// Every client-side sentence the SDK writes (S31 §5). Server messages pass through verbatim.

export const copy = {
  browserNotSupported:
    "Agent keys are secrets, so @deedglass/sdk doesn't run in a browser by default. Call Deedglass from your server, or pass dangerouslyAllowBrowser: true if you accept the risk.",
  invalidApiKey: "The API key isn't valid. It starts with dg_live_ or dg_test_. Create one in Console › Agents.",
  missingApiKey: 'No API key. Set DEEDGLASS_API_KEY, or pass apiKey to new Deedglass().',
  badToolName: (name: string): string =>
    `${JSON.stringify(clip(name, 80))} isn't a Deedglass tool name. Use "connector.tool", for example "billing.refund_customer".`,
  badIntentTool: (name: string): string =>
    `${JSON.stringify(clip(name, 80))} can't be signed: a signed intent's tool is a lowercase connector, a dot and a tool name, like "billing.refund_customer".`,
  badRef: (header: string): string =>
    `${header} must be 1 to 256 characters from A-Z a-z 0-9 . _ ~ : @ / + = -, for example usr_4821.`,
  timeout: (seconds: number, actionId?: string): string =>
    actionId
      ? `Deedglass didn't answer within ${seconds} s. The action may still be running: check ${actionId} in Console › Actions.`
      : `Deedglass didn't answer within ${seconds} s. The action may still be running: check Console › Actions.`,
  connection: (host: string): string => `Couldn't reach Deedglass at ${host}. Check DEEDGLASS_SERVER and that the server is running.`,
  toolExecution: (tool: string): string =>
    `${tool} threw after Deedglass authorized it. The deed records it as UNVERIFIABLE because it may have run. Pass failureMeans: 'not_applied' only if the function fails before any side effect.`,
  toolFailedNotApplied: (tool: string): string =>
    `${tool} threw after Deedglass authorized it, before any side effect (failureMeans: 'not_applied'). The deed records it as failed.`,
  reportLost: (actionId: string): string =>
    `Couldn't report the result of ${actionId} to Deedglass after 5 attempts. Deedglass will seal it as UNVERIFIABLE (edge_lost).`,
  approvalPending: (approvalId: string, expires: string): string =>
    `Waiting for approval ${approvalId}. It expires at ${expires}. Nothing has run yet. Try again after a person approves it.`,
  approvalWaiting: (approvalId: string): string =>
    `Waiting for approval in Console › Approvals (${approvalId}). Try again after someone decides.`,
  approvalTimeout: (approvalId: string): string =>
    `Approval ${approvalId} wasn't decided in time. Nothing has run yet. It stays open in Console › Approvals until it expires.`,
  unreadable: (status: number): string => `Deedglass answered ${status} with something the SDK couldn't read. Check that DEEDGLASS_SERVER points at a Deedglass server.`,
  argsInvalid: (why: string): string => `The arguments can't be sent: ${why}.`,
  bigInt:
    'The arguments hold an integer larger than 2^53 − 1, which JSON numbers can\'t carry exactly. Send it as a string, like "9007199254740993".',
  stopped: 'This agent was stopped in Deedglass by an admin. Nothing was run.',
  nothingRan: 'Nothing was run.',
  outage: "Deedglass isn't reachable, so this tool was not run. Check that deedglass serve is running.",
  noResultObserved: 'The tool was authorized but no result was observed before the agent stopped.',
  intentKeyBad: 'The intent key must be a PKCS#8 PEM Ed25519 private key, like the one deedglass agent intent keygen writes.',
  ed25519Missing: 'This runtime has no Ed25519 in WebCrypto. Use Node 22.12 or newer, Bun, Deno or a current browser.',
  fetchKeys: (host: string, why: string): string => `Couldn't fetch seal keys from ${host} (${why}).`,
  keysURL: 'Seal keys are fetched over https, or http only from localhost or 127.0.0.1.',
} as const;

export function clip(s: string, n: number): string {
  return s.length > n ? s.slice(0, n) + '…' : s;
}
