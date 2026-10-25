// copy-clients.ts: every string of S52's Console pieces: the Clients page (the Agency plan), the
// This week card on Overview and the weekly email setting. Calm operator voice; plain names: Check,
// record, Matches / Doesn't match / Can't check. The card's lines come from the server's summary
// (internal/weekly) word for word; this file only labels them.

export const clientsCopy = {
  title: 'Clients',
  lede: 'Every org you look after, this week. Each one’s data stays in its own org.',
  empty: 'You look after one org. Add a client by creating its org: deedglass org create …, then invite yourself.',
  columns: { client: 'Client', actions: 'Actions', doesntMatch: 'Doesn’t match', caught: 'Caught', waiting: 'Waiting', state: 'State' },
  open: 'Open',
  noActivity: 'No activity this week',
  stopped: 'Stopped',
  chain: { intact: 'Chain intact', broken: 'Chain broken', not_checked: 'Not checked', no_records: 'No records yet' } as Record<string, string>,
  caughtChip: '{n} caught',
  more: 'Showing the first {n}. deedglass clients --json lists them all.',
  reportsTitle: 'Monthly reports for every client',
  reportsLine: 'deedglass report --all-clients --month {month} --brand brand.yaml --out reports/',
  separation: 'Each row is read inside that client’s own org, after checking you still belong to it. No read spans two clients.',
  proTitle: 'Clients needs a licence on this build',
  proBody: 'One view of every org you look after, and a monthly report for each client with your name on it. Each client’s own Deedglass, its report and its Monday email stay free.',
  proFree: 'Each client’s own report stays free: deedglass report --org <client>',
  proLink: 'See pricing',
  proURL: 'https://deedglass.com/pricing',
  switchFailed: 'Couldn’t open that org. You may have lost access to it. Reload to see your clients now.',
};

export const weekCopy = {
  title: 'This week',
  lastWeek: 'Last week',
  open: 'Full report',
  reportLine: 'Print or send it: deedglass report --format html --out week.html',
  nothing: 'Nothing caught this week.',
  emailOn: 'The Monday email is on for you.',
  emailOnNotOwner: 'Only owners get the Monday email.',
  emailOff: 'The Monday email is off for you.',
  emailNoMailer: 'This server has no mailer, so this card is the summary.',
  turnOff: 'Turn it off',
  turnOn: 'Turn it on',
  error: 'Couldn’t load the summary: {message}',
};
