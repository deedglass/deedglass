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
})();
