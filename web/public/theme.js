/* Deedglass Console: apply the saved theme before first paint (S24 §3.7). External and blocking on
   purpose: the CSP allows no inline script. Without JavaScript, or with nothing saved, the page is
   dark. Keep in step with web/src/lib/theme.ts. */
(function () {
  var t = 'dark';
  try {
    var v = window.localStorage.getItem('deedglass.theme');
    if (v === 'dark' || v === 'light' || v === 'system') t = v;
  } catch (e) { /* storage blocked: dark */ }
  document.documentElement.setAttribute('data-theme', t);
  // S29: this browser last showed a simulated org, so the shell will draw its banner on boot: the
  // page waits for that first pass (app.css). Real orgs never wait. Keep in step with theme.ts.
  try {
    if (window.localStorage.getItem('deedglass.simulated') === '1') document.documentElement.setAttribute('data-sim-hint', '');
  } catch (e) { /* storage blocked: no hint */ }
  // DL-143: the saved view (Simple or Detailed). A browser that chose Simple opens /app as the Simple
  // view, before first paint. Keep in step with theme.ts (VIEW_KEY).
  try {
    var view = window.localStorage.getItem('deedglass.view');
    if (view === 'simple' || view === 'detailed') document.documentElement.setAttribute('data-view', view);
    if (view === 'simple' && /^\/app\/?$/.test(window.location.pathname) && !window.location.search) window.location.replace('/app/simple');
  } catch (e) { /* storage blocked: Detailed */ }
})();
