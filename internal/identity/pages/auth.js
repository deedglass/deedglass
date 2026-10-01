/* Deedglass sign-in pages: one script for /login, /auth/confirm and /app/.
   It switches on <body data-page="login|confirm|app">. Every user-visible string is in COPY
   (the static HTML repeats the initial ones). Text from the API is only ever set with textContent. */
(function () {
  'use strict';

  const COPY = {
    // /login
    loginTitle: 'Sign in to Deedglass',
    loginBody: "We'll email you a link and a code. No password.",
    emailLabel: 'Work email',
    continue: 'Continue',
    sentTitle: 'Check your email',
    sentBody: 'If {email} has an account, a link and a 6-digit code are on their way. The code works for {minutes}.',
    codeLabel: '6-digit code',
    signIn: 'Sign in',
    differentEmail: 'Use a different email',
    emailEmpty: 'Enter your work email to continue.',
    emailInvalid: '"{value}" isn\'t a complete email address. Use the form name@company.com.',
    codeIncomplete: 'Enter the 6-digit code from the email.',
    codeWrong: "That code doesn't match, or it has expired. Check the latest email, or get a new code.",
    // /auth/confirm
    checkingLink: 'Checking your link…',
    confirmTitle: 'Sign in to {org}',
    confirmBody: "You're signing in as {email}.",
    inviteTitle: 'Join {org} on Deedglass',
    inviteBody: '{inviter} invited you as {role}.',
    inviteBodyNoInviter: "You're invited as {role}.",
    acceptInvite: 'Accept invite',
    notYou: "Not you, or didn't ask for this? Close this tab. Nothing happens.",
    invalidTitle: 'This link has expired or was already used',
    invalidBody: 'Sign-in links work once and expire after 15 minutes.',
    newLink: 'Get a new link',
    // /app/
    checkingSession: 'Checking your session…',
    appTitle: 'Signed in to {org}',
    appNote: 'The Console arrives in a later release. Until then, this page shows who you are signed in as.',
    signOut: 'Sign out',
    fields: {
      name: 'Name', email: 'Email', org: 'Org', role: 'Role', timezone: 'Time zone',
      sessionEnds: 'Session ends', userId: 'User ID', orgId: 'Org ID',
      membershipId: 'Membership ID', sessionId: 'Session ID',
    },
    notSet: 'Not set',
    // Shared
    limited: 'Too many attempts from this network. Try again in {minutes}.',
    offline: "Deedglass couldn't reach the server. Check your connection and try again.",
    tryAgain: 'Try again',
    noscript: 'Turn on JavaScript to finish signing in, or type the 6-digit code from the email at deedglass.com/login.',
    minuteOne: '1 minute',
    minuteMany: '{n} minutes',
    roleVowel: 'an {role}',
    roleConsonant: 'a {role}',
  };

  // ---------- helpers ----------
  const $ = (id) => document.getElementById(id);
  const show = (el, on) => { if (el) el.hidden = !on; };
  const fmt = (tpl, vars) => tpl.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
  const minutes = (n) => (n === 1 ? COPY.minuteOne : fmt(COPY.minuteMany, { n }));
  const EMAIL_RE = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

  function titleCase(s) {
    const t = String(s || '').replace(/_/g, ' ');
    return t.charAt(0).toUpperCase() + t.slice(1);
  }
  function roleWithArticle(role) {
    const r = titleCase(role);
    return fmt(/^[aeiou]/i.test(r) ? COPY.roleVowel : COPY.roleConsonant, { role: r });
  }
  function invalid(el, on) {
    if (on) el.setAttribute('aria-invalid', 'true'); else el.removeAttribute('aria-invalid');
  }

  // ---------- the alert (role="alert") and its Try again button ----------
  const alertEl = $('alert');
  const retryBtn = $('retry');
  let retryFn = null;
  let alertSeq = 0;

  function say(text, retry) {
    const n = ++alertSeq;
    retryFn = retry || null;
    show(retryBtn, !!retry);
    if (alertEl.textContent === text) {
      // Same words again: clear first so screen readers announce the repeat.
      alertEl.textContent = '';
      setTimeout(() => { if (n === alertSeq) alertEl.textContent = text; }, 50);
    } else {
      alertEl.textContent = text;
    }
  }
  function quiet() {
    alertSeq++;
    alertEl.textContent = '';
    retryFn = null;
    show(retryBtn, false);
  }
  retryBtn.addEventListener('click', () => {
    const fn = retryFn;
    quiet();
    if (fn) fn();
  });

  // ---------- API ----------
  // Resolves to { ok, status, data, err, retryAfter }; status 0 means the request never got an answer.
  async function api(method, url, body, headers) {
    const init = {
      method,
      credentials: 'same-origin',
      cache: 'no-store',
      headers: Object.assign({ Accept: 'application/json' }, headers),
    };
    if (body !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }
    let res;
    try {
      res = await fetch(url, init);
    } catch (e) {
      return { ok: false, status: 0 };
    }
    let data = null;
    try { data = await res.json(); } catch (e) { /* empty or not JSON */ }
    return {
      ok: res.ok,
      status: res.status,
      data,
      err: (data && data.error) || null,
      retryAfter: res.headers.get('Retry-After'),
    };
  }

  function limitedText(r) {
    const h = (r.retryAfter || '').trim();
    const secs = /^\d+$/.test(h) ? Number(h) : (h ? (Date.parse(h) - Date.now()) / 1000 : NaN);
    if (!Number.isFinite(secs)) return (r.err && r.err.message) || fmt(COPY.limited, { minutes: minutes(15) });
    return fmt(COPY.limited, { minutes: minutes(Math.max(1, Math.ceil(secs / 60))) });
  }

  // Shows the right alert for a failed request. No answer, or one we can't read, offers Try again.
  function report(r, retry) {
    if (r.status === 429) return say(limitedText(r));
    if (r.status && r.err && typeof r.err.message === 'string' && r.err.message) return say(r.err.message);
    say(COPY.offline, retry);
  }

  // Runs one request at a time behind a button. The task returns true when the page is navigating
  // (the button stays disabled), or an element to focus once the button is usable again.
  let inFlight = false;
  async function run(btn, task) {
    if (inFlight) return;
    inFlight = true;
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    let after = null;
    try {
      after = await task();
    } finally {
      if (after !== true) {
        inFlight = false;
        btn.disabled = false;
        btn.removeAttribute('aria-busy');
      }
    }
    if (after && after !== true && typeof after.focus === 'function') after.focus();
  }

  // Skeleton and aria-busy on the card while a page loads its data.
  function loading(on) {
    show($('skeleton'), on);
    if (on) $('card').setAttribute('aria-busy', 'true'); else $('card').removeAttribute('aria-busy');
  }

  function fail(field, text) {
    invalid(field, true);
    say(text);
    field.focus();
  }

  // ---------- /login ----------
  function initLogin() {
    const [title, lede, emailForm, email, emailBtn, codeForm, code, codeBtn] =
      ['title', 'lede', 'email-form', 'email', 'email-submit', 'code-form', 'code', 'code-submit'].map($);
    let sentTo = '';

    function startState() {
      title.textContent = COPY.loginTitle;
      lede.textContent = COPY.loginBody;
      show(codeForm, false);
      show(emailForm, true);
      quiet();
      email.focus();
    }

    function sentState(addr, expiresIn) {
      sentTo = addr;
      const m = expiresIn > 0 ? Math.max(1, Math.round(expiresIn / 60)) : 15;
      title.textContent = COPY.sentTitle;
      lede.textContent = fmt(COPY.sentBody, { email: addr, minutes: minutes(m) });
      show(emailForm, false);
      show(codeForm, true);
      code.value = '';
      invalid(code, false);
      quiet();
      return code;
    }

    emailForm.addEventListener('submit', (e) => {
      e.preventDefault();
      const value = email.value.trim();
      if (!value) return fail(email, COPY.emailEmpty);
      if (!EMAIL_RE.test(value)) return fail(email, fmt(COPY.emailInvalid, { value }));
      invalid(email, false);
      const send = () => run(emailBtn, async () => {
        quiet();
        const r = await api('POST', '/v1/auth/login', { email: value });
        if (r.ok) return sentState((r.data && r.data.email) || value, r.data && r.data.expires_in);
        invalid(email, r.status === 400);
        report(r, send);
        return email;
      });
      send();
    });

    codeForm.addEventListener('submit', (e) => {
      e.preventDefault();
      const value = code.value.replace(/\s+/g, '');
      if (!/^\d{6}$/.test(value)) return fail(code, COPY.codeIncomplete);
      invalid(code, false);
      const send = () => run(codeBtn, async () => {
        quiet();
        const r = await api('POST', '/v1/auth/callback', { email: sentTo, code: value });
        if (r.ok) { location.assign('/app/'); return true; }
        if (r.err && r.err.code === 'login_code_invalid') {
          code.value = '';
          invalid(code, true);
          say(COPY.codeWrong);
          return code;
        }
        report(r, send);
        return code;
      });
      send();
    });

    // Back to the start; the email stays in the field.
    $('change-email').addEventListener('click', startState);
    email.addEventListener('input', () => invalid(email, false));
    code.addEventListener('input', () => invalid(code, false));

    startState();
  }

  // ---------- /auth/confirm ----------
  function initConfirm() {
    // Take the token out of the address bar first. Only the fragment is read; a token in the
    // query string is ignored on purpose. The token lives in this closure and nowhere else.
    const frag = location.hash.slice(1);
    history.replaceState(null, '', location.pathname);
    const t = new URLSearchParams(frag).get('t');
    let token = t && /^[A-Za-z0-9_-]{43}$/.test(t) ? t : null;

    const [title, lede, note, btn, newLink] = ['title', 'lede', 'note', 'confirm-submit', 'new-link'].map($);

    function invalidState() {
      token = null;
      loading(false);
      quiet();
      title.textContent = COPY.invalidTitle;
      lede.textContent = COPY.invalidBody;
      show(lede, true);
      show(note, false);
      show(btn, false);
      show(newLink, true);
      title.focus();
    }

    function readyState(p) {
      loading(false);
      quiet();
      const org = (p.org && p.org.name) || '';
      if (p.purpose === 'invite') {
        const by = p.invited_by && (p.invited_by.name || p.invited_by.email);
        const role = roleWithArticle(p.role);
        title.textContent = fmt(COPY.inviteTitle, { org });
        lede.textContent = by ? fmt(COPY.inviteBody, { inviter: by, role }) : fmt(COPY.inviteBodyNoInviter, { role });
        btn.textContent = COPY.acceptInvite;
      } else {
        title.textContent = fmt(COPY.confirmTitle, { org });
        lede.textContent = fmt(COPY.confirmBody, { email: p.email || '' });
        btn.textContent = COPY.signIn;
      }
      show(lede, true);
      show(note, true);
      show(newLink, false);
      show(btn, true);
      btn.focus();
    }

    async function preview() {
      if (!token) return invalidState();
      loading(true);
      const r = await api('POST', '/v1/auth/preview', { token });
      if (r.ok && r.data) return readyState(r.data);
      if (r.status === 400) return invalidState();
      loading(false);
      report(r, preview);
    }

    btn.addEventListener('click', () => {
      const send = () => run(btn, async () => {
        if (!token) { invalidState(); return null; }
        quiet();
        const r = await api('POST', '/v1/auth/callback', { token });
        if (r.ok) { location.assign('/app/'); return true; }
        if (r.status === 400) { invalidState(); return null; }
        report(r, send);
        return btn;
      });
      send();
    });

    preview();
  }

  // ---------- /app/ ----------
  function when(iso, timeZone) {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    const opts = { year: 'numeric', month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', timeZoneName: 'short' };
    try {
      return new Intl.DateTimeFormat('en-GB', Object.assign({ timeZone: timeZone || undefined }, opts)).format(d);
    } catch (e) { // unknown time zone name
      return new Intl.DateTimeFormat('en-GB', opts).format(d);
    }
  }

  function initApp() {
    const [title, facts, note, out] = ['title', 'facts', 'note', 'signout'].map($);
    let csrf = '';

    function render(me) {
      const u = me.user || {};
      const o = me.org || {};
      const m = me.membership || {};
      const s = me.session || {};
      const F = COPY.fields;
      csrf = me.csrf_token || '';
      title.textContent = fmt(COPY.appTitle, { org: o.name || '' });
      const rows = [
        [F.name, u.name],
        [F.email, u.email],
        [F.org, o.name],
        [F.role, m.role && titleCase(m.role)],
        [F.timezone, o.timezone],
        [F.sessionEnds, s.expires_at, 'time'],
        [F.userId, u.id, 'mono'],
        [F.orgId, o.id, 'mono'],
        [F.membershipId, m.id, 'mono'],
        [F.sessionId, s.id, 'mono'],
      ];
      const nodes = [];
      for (const [label, value, kind] of rows) {
        const dt = document.createElement('dt');
        const dd = document.createElement('dd');
        dt.textContent = label;
        if (kind === 'mono') { dt.className = 'full'; dd.className = 'full mono'; }
        if (!value) {
          dd.textContent = COPY.notSet;
        } else if (kind === 'time') {
          const el = document.createElement('time');
          el.dateTime = value;
          el.textContent = when(value, o.timezone);
          dd.append(el);
        } else {
          dd.textContent = value;
        }
        nodes.push(dt, dd);
      }
      facts.replaceChildren(...nodes);
      loading(false);
      show(facts, true);
      show(note, true);
      show(out, true);
    }

    async function load() {
      loading(true);
      const r = await api('GET', '/v1/me');
      if (r.status === 401) { location.assign('/login'); return; }
      if (r.ok && r.data) return render(r.data);
      loading(false);
      report(r, load);
    }

    out.addEventListener('click', () => {
      const send = () => run(out, async () => {
        quiet();
        const r = await api('POST', '/v1/auth/logout', { everywhere: false }, { 'Deedglass-CSRF': csrf });
        if (r.ok || r.status === 401) { location.assign('/login'); return true; }
        report(r, send);
        return out;
      });
      send();
    });

    load();
  }

  // A page restored from the back/forward cache may hold a disabled button; start it fresh.
  window.addEventListener('pageshow', (e) => { if (e.persisted) location.reload(); });

  const page = document.body.dataset.page;
  if (page === 'login') initLogin();
  else if (page === 'confirm') initConfirm();
  else if (page === 'app') initApp();
})();
