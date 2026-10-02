// copy.ts: every Console string (S24 §3.9). Calm, exact, sentence case, a next step on every error,
// no exclamation marks, no emoji, no em dashes (brand §4, Taste §9.G). Strings marked "lifted" come
// verbatim from docs/COPY_CATALOG.md (S08, S09, S16, S17, S20); test/copy.test.ts asserts each one
// against that file and runs the banned-word list over this file. COPY_CATALOG.md points here.

export type Vars = Record<string, string | number>;

/** fill replaces {name} placeholders. Unknown names are left as written, so a missing value shows. */
export function fill(tpl: string, vars: Vars = {}): string {
  return tpl.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m));
}

export const copy = {
  product: 'Deedglass',
  wordmark: 'deedglass',

  nav: {
    label: 'Console',
    overview: 'Overview',
    actions: 'Actions',
    approvals: 'Approvals',
    agents: 'Agents',
    policies: 'Policies',
    connectors: 'Connectors',
    insights: 'Insights',
    deeds: 'Deeds',
    settings: 'Settings',
    more: 'More',
    docs: 'Docs',
    skip: 'Skip to content',
    approvalsBadge: '{n} approvals waiting',
    approvalsBadgeOne: '1 approval waiting',
  },

  topbar: {
    search: 'Search or jump to…',
    searchShortcut: '⌘K',
    searchLabel: 'Search or jump to an id',
    org: 'Org',
    switchOrg: 'Switch org',
    theme: 'Theme',
    themeDark: 'Dark',
    themeLight: 'Light',
    themeSystem: 'System',
    account: 'Account',
    signOut: 'Sign out',
    signOutEverywhere: 'Sign out everywhere',
    role: { owner: 'Owner', admin: 'Admin', viewer: 'Viewer' } as Record<string, string>,
    stopAll: 'Stop all agents',
    startAll: 'Start all agents',
    menu: 'Menu',
  },

  connection: {
    connecting: 'Connecting',
    live: 'Live',
    reconnecting: 'Reconnecting · retry in {s}s',
    offline: 'Offline · Deedglass will reconnect when your network is back.',
    offlineShort: 'Offline',
    ended: 'Not live',
  },

  // Stop all and Start all: S08 §5.2, lifted.
  stop: {
    allTitle: 'Stop all agents in {org}?',
    allBody: 'Deedglass will refuse every agent call in {org} within 2 seconds. Only an owner can start them again.',
    allConfirm: 'Type {slug} to confirm',
    reasonOptional: 'Reason (optional, recorded in the deed)',
    allButton: 'Stop all agents',
    cancel: 'Cancel',
    viewer: 'Stopping every agent needs the Admin role. Ask an admin or owner in {org}.',
    stoppedBanner: 'All agents in {org} are stopped',
    stoppedSince: 'since {time}',
    stoppedBy: 'by {who}',
    startTitle: 'Start all agents in {org}?',
    startBody: 'Agents that were stopped one by one stay stopped. Everything else can act again as soon as you confirm.',
    startReason: 'Why is it safe to start again?',
    startButton: 'Start all agents',
    startOwnerOnly: 'Only an owner can start all agents again.',
    stoppedToast: 'All agents in {org} stopped · deed sealed',
    startedToast: 'All agents in {org} can act again · deed sealed',
    viewDeed: 'View deed',
    agentTitle: 'Stop {name}?',
    agentBody: 'Deedglass will refuse every call from {name}, on every instance, within 2 seconds. Calls already running at a tool finish; nothing new starts.',
    agentConfirm: 'Type {name} to confirm',
    agentButton: 'Stop agent',
    agentToast: '{name} stopped · deed sealed',
    mismatchHint: 'Type it exactly, including case.',
  },

  // S08 agents strings, lifted.
  agents: {
    empty: 'No agents yet. Connect your first agent in about two minutes.',
    create: 'Create agent',
    error: "Deedglass couldn't load agents. Retry, and share request {req} if it keeps happening.",
    noPermission: 'Stopping agents needs Admin. Ask an owner to change your role.',
    newKeyTitle: 'Copy your new key',
    newKeyBody: "This is the only time Deedglass shows it. Deedglass stores only a hash, so it can't show it again.",
    newKeyStored: "I've stored this key in a secret manager",
    done: 'Done',
    active: 'Active',
    suspended: 'Suspended',
    stopped: 'Stopped',
    test: 'Test',
  },

  // S09, lifted.
  intents: {
    title: 'Signed intents',
    keysEmpty: 'No intent keys yet. Generate one on the machine that runs the agent: deedglass agent intent keygen support-bot',
  },

  // S16, lifted.
  connectors: {
    empty: 'No connectors yet. Add your first API in about two minutes: a base URL, one tool, one credential.',
    credentialsNone: "No credentials yet. Agents' calls fail as credentials_missing until you store one.",
    credentialsNote: 'Agents never see this value. Deedglass adds it only when the Gate runs a call.',
  },

  // S17 and S20, lifted.
  postgres: {
    ready: 'Ready for L2',
    empty: 'Connect a Postgres database to see what your agents actually changed.',
    witnessEmpty: "This connector isn't witnessed yet. Turn on Witness to see what Postgres actually committed for every agent write. It needs wal_level=logical and a publication; the check below walks you through it.",
  },

  pages: {
    overview: { title: 'Overview', lede: 'What your agents did, as Postgres and your tools recorded it.' },
    actions: { title: 'Actions', lede: 'Every call an agent made through the Gate, newest first.', empty: 'No actions yet. When an agent calls a tool through the Gate, it appears here within a second.' },
    approvals: { title: 'Approvals', lede: 'Risky actions wait here for a person to countersign.', empty: 'Nothing waiting for you. Risky actions will appear here for a countersign.' },
    agents: { title: 'Agents', lede: 'The agents that may call tools in this org, and their keys.' },
    policies: { title: 'Policies', lede: 'The rules the Gate applies to every call, as signed versions.', empty: 'No policies beyond default-deny yet. Start from a starter policy and test it before you activate it.' },
    connectors: { title: 'Connectors', lede: 'The tools and databases agents reach through the Gate.' },
    insights: { title: 'Insights', lede: 'Patterns in what your agents did, and suggestions a person approves.', empty: 'Insights appear after about 50 actions, and every suggestion waits for your approval.' },
    deeds: { title: 'Deeds', lede: 'The sealed record of every action and admin change, in chain order.', empty: 'No deeds yet. Every action and admin change is sealed here as it happens.' },
    settings: { title: 'Settings', lede: 'Your org, its people, its keys and how Deedglass reaches you.' },
    detail: { loading: 'Loading…', backTo: 'Back to {screen}' },
    notFound: { title: "This page doesn't exist.", action: 'Go to Overview' },
  },

  live: {
    title: 'Live events',
    lede: 'The last 20 events from this org, as Postgres committed them.',
    empty: 'Waiting for the next event. To watch them arrive in a dev build, run',
    emptyCommand: 'deedglass dev fake-action --n 30',
    region: 'Live events',
    count: '{n} events',
  },

  states: {
    loading: 'Loading…',
    errorTitle: "Deedglass couldn't load this",
    errorBody: 'Retry. If it keeps happening, share this request id:',
    retry: 'Retry',
    permissionTitle: "You can't see this yet",
    permissionBody: 'This needs the {role} role in {org}. Ask an owner to change your role.',
    viewerBanner: "You're a Viewer in {org}. You can see everything; changes need an Admin.",
    offlineBanner: 'Offline · Deedglass will reconnect when your network is back.',
    sessionEnded: 'Your session ended. Sign in again to continue.',
    signIn: 'Sign in',
    resync: 'Some live updates may have been missed. The list was refreshed.',
  },

  toast: {
    copied: 'Copied',
    copiedValue: '{value} copied',
    dismiss: 'Dismiss',
    requestId: 'Request {req}',
    copyRequestId: 'Copy request id',
    networkError: "Deedglass couldn't reach the server. Check your connection and try again.",
    rateLimited: 'Deedglass is busy. Retrying in {s} seconds.',
  },

  palette: {
    title: 'Command palette',
    placeholder: 'Search or paste an id',
    placeholderHint: 'Search or paste an id like act_…',
    empty: 'No matches. Paste an id like act_… to jump straight to it.',
    groups: { jump: 'Jump to id', goto: 'Go to', actions: 'Actions', agents: 'Agents', recent: 'Recent' } as Record<string, string>,
    jumpTo: 'Open {id}',
    createAgent: 'Create agent',
    stopAgent: 'Stop an agent…',
    stopAll: 'Stop all agents',
    toggleTheme: 'Toggle theme',
    copyOrgId: 'Copy org id',
    signOut: 'Sign out',
    hint: '↑↓ to move · Enter to open · Esc to close',
  },

  shortcuts: {
    title: 'Keyboard shortcuts',
    open: 'Open the command palette',
    help: 'Show this list',
    goTo: 'Go to {screen}',
    move: 'Move down / up in a list',
    enter: 'Open the selected row',
    esc: 'Close a sheet, menu or palette',
    note: 'Shortcuts never change anything on their own. Anything that changes state asks first.',
  },

  chips: {
    verified: 'VERIFIED',
    mismatch: 'MISMATCH',
    unverifiable: 'UNVERIFIABLE',
    allowed: 'Allowed',
    denied: 'Denied',
    approval: 'Approval',
    pending: 'Pending',
    level: { L0: 'Reported', L1: 'Confirmed', L2: 'Witnessed' } as Record<string, string>,
  },

  meter: {
    label: '{used} of {limit}',
    near: '{pct}% used · near the limit',
    at: 'At the limit · new calls are refused',
  },

  kit: {
    title: 'Component kit',
    lede: 'Every Console component in every state, built only from the generated tokens. Dev builds only.',
    contrast: 'Contrast',
    contrastLede: 'Every declared pair, both themes, checked by scripts/contrast.mjs.',
    playSealed: 'Play Sealed',
    playMismatch: 'Play Mismatch',
    reset: 'Reset',
  },
} as const;

/** Copy that test/copy.test.ts finds verbatim in docs/COPY_CATALOG.md. */
export const lifted: readonly string[] = [
  copy.agents.empty,
  copy.agents.newKeyTitle,
  copy.agents.newKeyBody,
  copy.agents.newKeyStored,
  copy.stop.reasonOptional,
  copy.stop.startBody,
  copy.stop.startReason,
  copy.stop.agentButton,
  copy.stop.allButton,
  copy.intents.title,
  copy.intents.keysEmpty,
  copy.connectors.empty,
  copy.connectors.credentialsNone,
  copy.connectors.credentialsNote,
  copy.postgres.ready,
  copy.postgres.empty,
  copy.postgres.witnessEmpty,
];
