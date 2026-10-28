// copy-simple-nav.ts: the DL-143 navigation words (the Simple view's destinations and the Simple /
// Detailed switch). Apart from copy-simple.ts so every page's shell bundle carries only these few
// strings; copy-simple.ts re-exports them. test/checks.test.ts walks this file too.

export const simpleNav = {
  nav: {
    label: 'Deedglass',
    activity: 'What agents did',
    waiting: 'Waiting for you',
    records: 'Locked records',
    connect: 'Connect',
    activityShort: 'Activity',
    waitingShort: 'Waiting',
    recordsShort: 'Records',
  },

  view: {
    label: 'View',
    simple: 'Simple',
    detailed: 'Detailed',
  },

};
