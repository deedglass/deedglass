/* Deedglass /approve (S22 §5.3). The token is read from the URL fragment (#t=…), which never
   reaches a server or a log, then removed from the address bar. Opening the page decides nothing:
   preview doesn't consume the link; only the Approve and Deny buttons POST a decision. Every
   user-visible string is in COPY; text from the API is only ever set with textContent. */
(function () {
  'use strict';

  const COPY = {
    loading: 'Opening the approval…',
    reviewTitle: 'Countersign this action?',
    waiting: 'Waiting for you',
    waitingOthers: 'Waiting for others',
    approve: 'Approve and run',
    approving: 'Approving…',
    deny: 'Deny',
    denying: 'Denying…',
    expires: '{clock} · {in}',
    rows: 'About {n} (planner estimate)',
    rowOne: '1 row',
    rowMany: '{n} rows',
    fourEyes: '{done} of {need} · {who}',
    fourEyesAt: '{name} at {clock}',
    runningTitle: 'Countersigned',
    runningBody: 'Deedglass is running it now.',
    runningChip: 'executing',
    authorizedBody: 'Approved. The agent runs it now, and Deedglass records what happens.',
    sealedTitle: 'Sealed',
    sealedMismatchTitle: 'Sealed with a mismatch',
    sealedUnverifiableTitle: 'Sealed',
    sealedFallback: 'The action ran and its deed is sealed.',
    partialTitle: 'Countersigned ({done} of {need})',
    partialBody: 'It runs when one more person approves.',
    partialBodyMany: 'It runs when {n} more people approve.',
    deniedTitle: 'Denied',
    deniedBody: 'Nothing ran. The agent sees your reason.',
    decidedTitle: 'Already decided',
    decidedApproved: '{name} approved this at {clock}.',
    decidedDenied: '{name} denied this at {clock}.',
    decidedCancelled: 'The agent cancelled this request at {clock}. Nothing ran.',
    decidedSystem: 'This approval ended at {clock}.',
    expiredTitle: 'This approval expired',
    expiredBody: 'Nobody approved it before {clock}, so nothing ran.',
    selfTitle: "You can't countersign this one",
    selfBody: "It runs on your behalf, so another approver has to. You can still deny it.",
    notEligibleTitle: "You're not an approver for this rule",
    notEligibleBody: 'Your role changed since the email was sent. Nothing was decided.',
    reviewOnlyTitle: 'Review this action',
    reviewOnlyBody: 'Your org decides approvals in the Console.',
    stoppedBanner: '{agent} is stopped. Your countersign is recorded, but nothing runs while it is stopped.',
    stoppedAfter: 'A person approved this, but {agent} is stopped, so nothing ran.',
    invalidTitle: 'This link has expired or was already used',
    invalidBody: 'Approval links work once, for one person, for up to 24 hours. Console › Approvals has the latest.',
    openConsole: 'Open in Console',
    stale: 'What you reviewed is out of date. Review it again below.',
    reasonEmpty: 'A reason is required to deny. The agent sees it.',
    reasonCount: '{n} / 280',
    offline: "Deedglass couldn't reach the server. Check your connection and try again.",
    limited: 'Too many requests for this link. Try again in a minute.',
    copied: 'Deed id copied.',
    copy: 'Copy',
    recentWhen: '{ago}',
    agoNow: 'just now',
    agoMin: '{n} min ago',
    agoHour: '{n} h ago',
    agoDay: '{n} d ago',
    inNow: 'now',
    inSec: 'in {n} s',
    inMin: 'in {n} min',
    inHour: 'in {h} h {m} min',
    inHourExact: 'in {h} h',
    // S40: the email_preview section.
    emailWindow: '{local} ({ist})',
    emailChips: '{words} words · {links} · {questions} · lints {lint} · judge {judge}',
    emailLinksNone: '0 links',
    emailLinksOne: '1 link',
    emailLinksMany: '{n} links',
    emailQOne: '1 question',
    emailQMany: '{n} questions',
    approveEmail: 'Approve',
    // Ward Light: the clinical_preview section.
    approveClinical: 'Approve order',
    approveCommit: 'Sign & Commit',
    // S23: the undo section (internal/witness/undo/present.go holds the CLI and API strings).
    undoHead: '{rows} in {tables}',
    undoUpdates: '{n}\u00a0updates',
    undoUpdate: '1\u00a0update',
    undoInserts: '{n}\u00a0inserts',
    undoInsert: '1\u00a0insert',
    undoDeletes: '{n}\u00a0deletes',
    undoDelete: '1\u00a0delete',
    undoMore: 'And {n} more steps in the plan.',
    undoMoreOne: 'And 1 more step in the plan.',
    undoTriggers: 'Triggers on these tables fire again during this undo. The witness shows what they did.',
    undoAbsent: 'absent',
    undoPresent: 'present',
    undoPutBack: 'put back',
    undoRemoved: 'removed',
    undoNull: 'NULL',
  };

  const $ = (id) => document.getElementById(id);
  const show = (el, on) => { if (el) el.hidden = !on; };
  const fmt = (tpl, v) => tpl.replace(/\{(\w+)\}/g, (m, k) => (k in v ? String(v[k]) : m));
  const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // ---------- the token: fragment → memory (and this tab's session storage, for a reload) ----------
  const KEY = 'deedglass.approve.t';
  let token = '';
  const frag = new URLSearchParams(location.hash.slice(1)).get('t');
  if (frag) {
    token = frag;
    try { sessionStorage.setItem(KEY, frag); } catch (e) { /* storage may be off: memory only */ }
  } else {
    try { token = sessionStorage.getItem(KEY) || ''; } catch (e) { token = ''; }
  }
  if (location.hash || location.search) history.replaceState(null, '', location.pathname);

  // ---------- elements ----------
  const card = $('card');
  const titleEl = $('title');
  const ledeEl = $('lede');
  const chip = $('chip');
  const chipText = $('chip-text');
  const banner = $('banner');
  const alertEl = $('alert');
  const retry = $('retry');
  const statusEl = $('status');
  const approveBtn = $('approve');
  const denyOpen = $('deny-open');
  const sheet = $('deny-sheet');
  const reasonEl = $('reason');
  const denyBtn = $('deny');
  const consoleLink = $('console');
  const seal = $('seal');

  let preview = null; // the last approval_link_preview
  let retryFn = null;
  let pollTimer = 0;
  let pollUntil = 0;

  // ---------- API ----------
  async function post(path, body) {
    let res;
    try {
      res = await fetch(path, {
        method: 'POST', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer',
        headers: { 'Content-Type': 'application/json', Accept: 'application/json' }, body: JSON.stringify(body),
      });
    } catch (e) {
      return { network: true };
    }
    let data = null;
    try { data = await res.json(); } catch (e) { data = null; }
    return { status: res.status, data, error: data && data.error ? data.error : null };
  }

  // ---------- time ----------
  function clock(iso, tz) {
    // The server sends clocks already formatted in the org's zone (Go's "15:04 MST") when it can.
    if (!iso) return '';
    try {
      return new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false, timeZone: tz || 'UTC', timeZoneName: 'short' })
        .format(new Date(iso)).replace(',', '');
    } catch (e) { return iso; }
  }
  function until(iso) {
    const s = Math.round((new Date(iso).getTime() - Date.now()) / 1000);
    if (s <= 0) return COPY.inNow;
    if (s < 60) return fmt(COPY.inSec, { n: s });
    if (s < 3600) return fmt(COPY.inMin, { n: Math.floor(s / 60) });
    const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60);
    return m ? fmt(COPY.inHour, { h, m }) : fmt(COPY.inHourExact, { h });
  }
  // Plain names (DL-075; the same words as internal/witness/plain.go and web/src/lib/copy.ts). Wire
  // values stay in the data; only the text a person reads changes.
  function plainLevel(l) {
    return { L0: 'Claimed', L1: 'Checked', L2: 'Proven' }[l] || l;
  }
  function plainVerdict(v) {
    return { VERIFIED: 'Matches', MISMATCH: "Doesn't match", UNVERIFIABLE: "Can't check" }[v] || v;
  }

  function ago(iso) {
    const s = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (s < 60) return COPY.agoNow;
    if (s < 3600) return fmt(COPY.agoMin, { n: Math.floor(s / 60) });
    if (s < 86400) return fmt(COPY.agoHour, { n: Math.floor(s / 3600) });
    return fmt(COPY.agoDay, { n: Math.floor(s / 86400) });
  }

  // ---------- rendering ----------
  function setAlert(msg, again) {
    alertEl.textContent = msg || '';
    retryFn = again || null;
    show(retry, Boolean(again));
  }
  function setChip(kind, text) {
    if (!kind) { show(chip, false); return; }
    chip.className = 'chip chip-' + kind;
    const icon = { pending: '◐', verified: '✓', mismatch: '✕', denied: '✕', unverifiable: '○' }[kind] || '';
    chip.firstElementChild.textContent = icon;
    chip.firstElementChild.hidden = !icon;
    chipText.textContent = text;
    show(chip, true);
  }
  function setTitle(title, lede, focus) {
    titleEl.textContent = title;
    ledeEl.textContent = lede || '';
    show(ledeEl, Boolean(lede));
    document.title = title + ' · Deedglass';
    if (focus) titleEl.focus({ preventScroll: false });
  }
  function done() {
    card.setAttribute('aria-busy', 'false');
    show($('skeleton'), false);
  }
  function actions(primary, deny) {
    show($('actions'), primary || deny);
    show(approveBtn, primary);
    show(denyOpen, deny);
    show(sheet, false);
    denyOpen.setAttribute('aria-expanded', 'false');
  }
  // quiet: while the decision buttons show, the Console link is a plain text link below them.
  function consoleAt(url, quiet) {
    if (url) consoleLink.href = url;
    consoleLink.textContent = COPY.openConsole;
    consoleLink.className = quiet ? 'link-quiet' : 'btn btn-secondary';
    show(consoleLink, true);
  }
  function hideContext() {
    show($('context'), false);
    show($('recent-wrap'), false);
  }
  function rowsText(n) { return n === 1 ? COPY.rowOne : fmt(COPY.rowMany, { n }); }

  function renderContext(p) {
    const a = p.approval;
    const ctx = a.context || {};
    $('f-agent').textContent = a.agent.name;
    $('f-tool').textContent = a.tool;
    $('f-rule').textContent = a.rule_id || '';
    $('f-reason').textContent = a.reason || '';
    const est = ctx.sql && ctx.sql.estimate;
    const hasRows = est && typeof est.rows === 'number';
    show($('f-rows-label'), hasRows);
    show($('f-rows'), hasRows);
    if (hasRows) $('f-rows').textContent = fmt(COPY.rows, { n: rowsText(est.rows) });
    $('f-expires').textContent = fmt(COPY.expires, { clock: p.expires_clock || clock(a.expires_at, p.time_zone), in: until(a.expires_at) });
    const cs = Array.isArray(a.countersigned) ? a.countersigned : [];
    const four = a.required > 1 || cs.length > 0;
    show($('f-four-label'), four);
    show($('f-four'), four);
    if (four) {
      const who = cs.map((c, i) => fmt(COPY.fourEyesAt, { name: c.user.name, clock: (p.countersign_clocks || [])[i] || clock(c.at, p.time_zone) })).join(', ');
      $('f-four').textContent = fmt(COPY.fourEyes, { done: cs.length, need: a.required, who: who || '-' }).replace(/ · -$/, '');
    }
    renderUndo(ctx.sections && ctx.sections.undo);
    renderEmail(ctx.sections && ctx.sections.email_preview);
    renderClinical(ctx.sections && ctx.sections.clinical_preview);
    $('sql-text').textContent = p.sql || '';
    show($('sql'), Boolean(p.sql));
    $('args-note').textContent = ctx.args_note || '';
    show($('args-note'), Boolean(ctx.args_note));
    const list = $('recent');
    list.textContent = '';
    const recent = ((ctx.agent && ctx.agent.recent) || []).slice(0, 5);
    for (const r of recent) {
      const li = document.createElement('li');
      const tool = document.createElement('span');
      tool.className = 'tool';
      tool.translate = false;
      tool.textContent = r.tool;
      const when = document.createElement('span');
      when.className = 'when';
      when.textContent = ago(r.at);
      const c = document.createElement('span');
      const v = r.verdict || '';
      c.className = 'chip chip-' + (v === 'VERIFIED' ? 'verified' : v === 'MISMATCH' ? 'mismatch' : v === 'UNVERIFIABLE' ? 'unverifiable' : r.status === 'denied' ? 'denied' : 'neutral');
      c.textContent = v ? (r.level ? plainLevel(r.level) + ' · ' + plainVerdict(v) : plainVerdict(v)) : r.status;
      li.append(tool, c, when);
      list.append(li);
    }
    show($('recent-wrap'), recent.length > 0);
    show($('context'), true);
  }

  // S40: an email exactly as it will leave: From, To, the recipient's window, the signal it answers,
  // the subject and the whole plain-text body (text only, line breaks kept), then the checks. The
  // primary button is just "Approve": one tap per email, no batch control anywhere on this page.
  function renderEmail(e) {
    const on = Boolean(e && typeof e === 'object');
    show($('email'), on);
    show($('sql'), !on && Boolean($('sql-text').textContent));
    if (!on) return;
    $('e-from').textContent = e.from || '';
    $('e-to').textContent = e.to || '';
    const w = e.window;
    show($('e-window-label'), Boolean(w));
    show($('e-window'), Boolean(w));
    if (w) $('e-window').textContent = fmt(COPY.emailWindow, { local: w.local, ist: w.ist });
    const r = e.review || {};
    const sig = r.signal_summary || '';
    show($('e-signal-label'), Boolean(sig));
    show($('e-signal'), Boolean(sig));
    $('e-signal').textContent = '';
    if (sig) {
      $('e-signal').append(document.createTextNode(sig + (r.signal_date ? ' · ' + r.signal_date : '')));
      if (r.signal_url && r.signal_url.indexOf('https://') === 0) {
        const a = document.createElement('a');
        a.href = r.signal_url;
        a.rel = 'noopener noreferrer';
        a.target = '_blank';
        a.textContent = ' ' + r.signal_url.replace(/^https:\/\//, '');
        $('e-signal').append(a);
      }
    }
    $('email-subject').textContent = e.subject || '';
    $('email-body').textContent = e.body_text || '';
    const links = e.links === 0 ? COPY.emailLinksNone : e.links === 1 ? COPY.emailLinksOne : fmt(COPY.emailLinksMany, { n: e.links });
    const qs = e.questions === 1 ? COPY.emailQOne : fmt(COPY.emailQMany, { n: e.questions || 0 });
    $('email-chips').textContent = fmt(COPY.emailChips, { words: e.words || 0, links, questions: qs, lint: r.lint_summary || '-', judge: r.judge_summary || '-' });
    $('approve').textContent = COPY.approveEmail;
  }

  // Ward Light: the clinical_preview section (DIRECTION_HEALTH.md). Shows patient chart card,
  // ambient AI scribe transcript anchor, structured clinical fields (Medicine, Dose, Route,
  // Frequency, Duration), inline "Edit order" (Edit & Commit) and direct EHR deep-link (CH-05).
  let editingClinical = false;
  let clinicalData = null;
  let originalFields = [];
  let sessionSecondsLeft = 300;
  let sessionInterval = null;

  function startSessionTimer(totalSeconds, role) {
    if (sessionInterval) clearInterval(sessionInterval);
    sessionSecondsLeft = totalSeconds > 0 ? totalSeconds : 300;
    $('cl-practitioner').textContent = role || 'Attending Clinician';
    updateSessionDisplay();
    show($('cl-session-expired'), false);
    approveBtn.disabled = false;

    sessionInterval = setInterval(() => {
      sessionSecondsLeft -= 1;
      updateSessionDisplay();
      if (sessionSecondsLeft <= 0) {
        clearInterval(sessionInterval);
        sessionInterval = null;
        show($('cl-session-expired'), true);
        approveBtn.disabled = true;
      }
    }, 1000);
  }

  function updateSessionDisplay() {
    const m = Math.floor(Math.max(0, sessionSecondsLeft) / 60);
    const s = Math.max(0, sessionSecondsLeft) % 60;
    const timerStr = (m < 10 ? '0' : '') + m + ':' + (s < 10 ? '0' : '') + s;
    $('cl-session-timer').textContent = timerStr;
  }

  function computeDigestHex(fields) {
    let s = '';
    for (const f of fields) {
      s += f.key + ':' + (f.value || '') + '\n';
    }
    let h1 = 0xdeadbeef, h2 = 0x41c6ce57;
    for (let i = 0; i < s.length; i++) {
      const ch = s.charCodeAt(i);
      h1 = Math.imul(h1 ^ ch, 2654435761);
      h2 = Math.imul(h2 ^ ch, 1597334677);
    }
    h1 = ((h1 ^ (h1 >>> 16)) >>> 0).toString(16).padStart(8, '0');
    h2 = ((h2 ^ (h2 >>> 16)) >>> 0).toString(16).padStart(8, '0');
    return (h1 + h2 + h1 + h2).substring(0, 16);
  }

  function renderDiffSummary(diffs) {
    const wrap = $('cl-diff-wrap');
    if (!diffs || diffs.length === 0) {
      show(wrap, false);
      return;
    }
    show(wrap, true);
    const list = $('cl-diff-list');
    list.textContent = '';
    for (const d of diffs) {
      const row = document.createElement('div');
      row.className = 'diff-item';
      const keySpan = document.createElement('span');
      keySpan.className = 'diff-key';
      keySpan.textContent = d.label + ':';
      const oldSpan = document.createElement('span');
      oldSpan.className = 'diff-old';
      oldSpan.textContent = d.oldValue;
      const arrSpan = document.createElement('span');
      arrSpan.textContent = ' \u2192 ';
      const newSpan = document.createElement('span');
      newSpan.className = 'diff-new';
      newSpan.textContent = d.newValue;
      row.append(keySpan, oldSpan, arrSpan, newSpan);
      list.append(row);
    }
    const aiHash = clinicalData && clinicalData.ai_proposed_digest ? clinicalData.ai_proposed_digest.substring(0, 16) : computeDigestHex(originalFields);
    const finalHash = computeDigestHex(clinicalData.fields || []);
    $('cl-digest-ai').textContent = 'AI Draft Digest: ' + aiHash;
    $('cl-digest-final').textContent = 'Clinician Final: ' + finalHash;
  }

  function renderClinical(c) {
    const on = Boolean(c && typeof c === 'object');
    show($('clinical'), on);
    if (!on) return;
    clinicalData = c;
    editingClinical = false;
    originalFields = JSON.parse(JSON.stringify(c.fields || []));

    // Patient badge & ward label
    $('cl-patient').textContent = c.patient_display || c.patient_ref || 'Patient';
    $('cl-bed').textContent = c.action_label || 'Ward Chart';

    // Environment pill (CH-35)
    const envEl = $('cl-tag-env');
    if (c.environment) {
      envEl.textContent = c.environment.toUpperCase();
      show(envEl, true);
    } else {
      show(envEl, false);
    }

    // Preliminary draft tag & alert (CH-36)
    const hasPrelim = Boolean(c.preliminary_warning);
    show($('cl-tag-prelim'), hasPrelim);
    show($('cl-preliminary-banner'), hasPrelim);
    if (hasPrelim) {
      $('cl-preliminary-text').textContent = c.preliminary_warning;
    }

    // Concurrency collision warning (CH-32)
    const hasCollision = Boolean(c.collision_warning);
    show($('cl-collision-banner'), hasCollision);
    if (hasCollision) {
      $('cl-collision-text').textContent = c.collision_warning;
    }

    // Ungrounded draft alert (CH-34)
    const hasUngrounded = Boolean(c.ungrounded_notice);
    show($('cl-ungrounded-banner'), hasUngrounded);
    if (hasUngrounded) {
      $('cl-ungrounded-text').textContent = c.ungrounded_notice;
    }

    // Offline edge queue split-brain conflict (CH-38)
    const hasEdgeQueue = Boolean(c.edge_queue_conflict);
    show($('cl-edgequeue-banner'), hasEdgeQueue);
    if (hasEdgeQueue) {
      $('cl-edgequeue-text').textContent = c.edge_queue_conflict;
    }

    // Secondary prompt injection risk alert (CH-39)
    const hasInjection = Boolean(c.prompt_injection_alert);
    show($('cl-injection-banner'), hasInjection);
    if (hasInjection) {
      $('cl-injection-text').textContent = c.prompt_injection_alert;
    }

    // Emergency break-glass banner (CH-07)
    const isEmer = Boolean(c.is_break_glass);
    show($('cl-breakglass-banner'), isEmer);
    if (isEmer) {
      $('cl-breakglass-text').textContent = c.audit_notice || 'Action initiated under emergency break-glass override.';
    }

    // Da Vinci PAS / Pre-Authorization details
    const isPA = Boolean(c.is_prior_auth && c.prior_auth_details);
    show($('cl-pa-wrap'), isPA);
    if (isPA) {
      const pa = c.prior_auth_details;
      $('cl-pa-payer').textContent = pa.payer_name || 'Payer';
      $('cl-pa-npi').textContent = pa.provider_npi || '-';
      $('cl-pa-amount').textContent = pa.total_amount || '-';
      $('cl-pa-service').textContent = pa.service_type || '-';
      $('cl-pa-urgency').textContent = pa.urgency || 'STANDARD';

      const hasDiags = Array.isArray(pa.diagnosis_codes) && pa.diagnosis_codes.length > 0;
      show($('cl-pa-diag-wrap'), hasDiags);
      if (hasDiags) $('cl-pa-diag').textContent = pa.diagnosis_codes.join(', ');

      const hasReqs = Array.isArray(pa.service_requests) && pa.service_requests.length > 0;
      show($('cl-pa-req-wrap'), hasReqs);
      if (hasReqs) $('cl-pa-req').textContent = pa.service_requests.join(', ');
    }

    // Transcript source anchor
    const hasTranscript = Boolean(c.transcript_anchor);
    show($('cl-transcript-wrap'), hasTranscript);
    if (hasTranscript) {
      $('cl-transcript').textContent = c.transcript_anchor;
    }

    // Table of clinical fields
    renderClinicalFields(c.fields || []);
    renderDiffSummary([]);

    // Session Inactivity Guard (CH-37)
    startSessionTimer(c.session_timeout_sec || 300, c.practitioner_role);

    // EHR deep-link (CH-05)
    const hasEHR = Boolean(c.ehr_deep_link);
    show($('cl-ehr-link-wrap'), hasEHR);
    if (hasEHR) {
      $('cl-ehr-link').href = c.ehr_deep_link;
      $('cl-ehr-link').textContent = '\u2197 Open patient record in EHR';
    }

    // Button label
    $('approve').textContent = COPY.approveClinical;
  }

  function renderClinicalFields(fields) {
    const tbody = $('cl-fields');
    tbody.textContent = '';
    const hasAnyPrior = fields.some(f => Boolean(f.prior));
    show($('cl-th-prior'), hasAnyPrior);

    for (const f of fields) {
      const tr = document.createElement('tr');
      const tdLabel = document.createElement('td');
      tdLabel.className = 'cl-k';
      tdLabel.textContent = f.label || f.key;

      let tdPrior = null;
      if (hasAnyPrior) {
        tdPrior = document.createElement('td');
        tdPrior.className = 'cl-prior mono';
        tdPrior.textContent = f.prior || '-';
      }

      const tdVal = document.createElement('td');
      tdVal.className = 'cl-v mono';
      if (f.prior && f.prior !== f.value) {
        tdVal.classList.add('cl-diff-changed');
      }

      if (editingClinical && f.editable) {
        const inp = document.createElement('input');
        inp.className = 'cl-edit-input';
        inp.type = 'text';
        inp.value = f.value || '';
        inp.dataset.key = f.key;
        tdVal.append(inp);
      } else {
        tdVal.textContent = f.value || '-';
      }

      if (hasAnyPrior) {
        tr.append(tdLabel, tdPrior, tdVal);
      } else {
        tr.append(tdLabel, tdVal);
      }
      tbody.append(tr);
    }
  }

  // S23: an undo's plan, as the person who countersigns it reads it: what is put back, and the first
  // steps as Now → After undo. Values are text only (textContent); sensitive ones arrive as •••.
  // Two rows on a phone keep the decision above the fold; six where there is room.
  const maxUndoRows = () => (window.matchMedia && window.matchMedia('(min-width: 441px)').matches ? 6 : 2);
  const nf = (n) => Number(n).toLocaleString('en-US');
  function plural(n, one, many) { return n === 1 ? one : fmt(many, { n: nf(n) }); }
  function cell(text, cls) {
    const td = document.createElement('td');
    if (cls) td.className = cls;
    td.translate = false;
    td.textContent = text;
    td.title = text; // the full value when the cell cuts it
    return td;
  }
  function renderUndo(u) {
    const on = Boolean(u && typeof u === 'object');
    show($('undo'), on);
    for (const id of ['f-why-label', 'f-why']) show($(id), on);
    if (!on) return;
    $('f-why').textContent = u.reason || '';
    show($('f-why-label'), Boolean(u.reason));
    show($('f-why'), Boolean(u.reason));
    const c = u.counts || {};
    const parts = [fmt(COPY.undoHead, { rows: plural(u.rows || 0, COPY.rowOne, COPY.rowMany), tables: (u.tables || []).join(', ') })];
    if (c.update) parts.push(plural(c.update, COPY.undoUpdate, COPY.undoUpdates));
    if (c.insert) parts.push(plural(c.insert, COPY.undoInsert, COPY.undoInserts));
    if (c.delete) parts.push(plural(c.delete, COPY.undoDelete, COPY.undoDeletes));
    $('undo-head').textContent = parts.join(' · ');
    $('undo-triggers').textContent = u.allow_triggers ? COPY.undoTriggers : '';
    show($('undo-triggers'), Boolean(u.allow_triggers));
    const body = $('undo-rows');
    body.textContent = '';
    const steps = Array.isArray(u.steps) ? u.steps : [];
    let shownSteps = 0;
    let lines = 0;
    const MAX_UNDO_ROWS = maxUndoRows();
    for (const st of steps) {
      if (lines >= MAX_UNDO_ROWS) break;
      shownSteps++;
      // With several tables, each row names its table (without the schema).
      const many = (u.tables || []).length > 1;
      const key = (many ? String(st.table || '').split('.').pop() + ' ' : '') +
        Object.keys(st.key || {}).sort().map((k) => k + ' ' + st.key[k]).join(' ');
      const val = (v) => (v === null || v === undefined ? COPY.undoNull : String(v));
      if (st.op === 'update') {
        for (const col of Object.keys(st.set || {}).sort()) {
          if (lines >= MAX_UNDO_ROWS) break;
          const tr = document.createElement('tr');
          tr.append(cell(key + ' · ' + col, 'k'), cell(val((st.expect || {})[col]), 'now'), cell(val(st.set[col]), 'after'));
          body.append(tr);
          lines++;
        }
      } else {
        const tr = document.createElement('tr');
        const ins = st.op === 'insert';
        tr.append(cell(key, 'k'), cell(ins ? COPY.undoAbsent : COPY.undoPresent, 'now'), cell(ins ? COPY.undoPutBack : COPY.undoRemoved, 'after'));
        body.append(tr);
        lines++;
      }
    }
    const more = Math.max(0, (u.steps_more || 0) + steps.length - shownSteps);
    show($('undo-wrap'), lines > 0);
    $('undo-more').textContent = more ? fmt(more === 1 ? COPY.undoMoreOne : COPY.undoMore, { n: nf(more) }) : '';
    $('undo-more').title = u.plan_digest || '';
    show($('undo-more'), more > 0);
  }

  function renderPreview(p, focus) {
    preview = p;
    done();
    setAlert('');
    const a = p.approval;
    const you = a.you || {};
    const tz = p.time_zone;
    show($('fine'), false);
    show(banner, false);
    show(seal, false);
    show($('deed'), false);
    show(consoleLink, false);
    if (a.status !== 'pending') return renderDecided(p, focus);
    if (new Date(a.expires_at).getTime() <= Date.now()) {
      setChip('neutral', 'expired');
      setTitle(COPY.expiredTitle, fmt(COPY.expiredBody, { clock: p.expires_clock || clock(a.expires_at, tz) }), focus);
      actions(false, false);
      hideContext();
      consoleAt(p.console_url);
      return;
    }
    renderContext(p);
    if (p.agent_stopped) {
      banner.textContent = fmt(COPY.stoppedBanner, { agent: a.agent.name });
      show(banner, true);
    }
    if (p.review_only) {
      setChip('pending', COPY.waiting);
      setTitle(COPY.reviewOnlyTitle, a.summary + ' ' + COPY.reviewOnlyBody, focus);
      actions(false, false);
      consoleAt(p.console_url);
      return;
    }
    if (!you.eligible) {
      setChip(null);
      setTitle(COPY.notEligibleTitle, COPY.notEligibleBody, focus);
      actions(false, false);
      consoleAt(p.console_url);
      return;
    }
    if (!you.can_decide) {
      const cs = Array.isArray(a.countersigned) ? a.countersigned : [];
      setChip('pending', COPY.waitingOthers);
      setTitle(fmt(COPY.partialTitle, { done: cs.length, need: a.required }), partialBody(a), focus);
      actions(false, false);
      consoleAt(p.console_url, true);
      return;
    }
    if (you.why_not) {
      setChip('pending', COPY.waiting);
      setTitle(COPY.selfTitle, COPY.selfBody, focus);
      actions(false, true);
      consoleAt(p.console_url, true);
      return;
    }
    setChip('pending', COPY.waiting);
    setTitle(COPY.reviewTitle, a.summary, focus);
    actions(true, true);
    consoleAt(p.console_url, true);
    show($('fine'), true);
  }

  function partialBody(a) {
    const cs = Array.isArray(a.countersigned) ? a.countersigned.length : 0;
    const left = Math.max(a.required - cs, 1);
    return left === 1 ? COPY.partialBody : fmt(COPY.partialBodyMany, { n: left });
  }

  function renderDecided(p, focus) {
    const a = p.approval;
    const tz = p.time_zone;
    const at = p.decided_clock || clock(a.decided_at, tz);
    actions(false, false);
    hideContext();
    if (a.status === 'expired') {
      setChip('neutral', 'expired');
      setTitle(COPY.expiredTitle, fmt(COPY.expiredBody, { clock: p.expires_clock || clock(a.expires_at, tz) }), focus);
      consoleAt(p.console_url);
      return;
    }
    let body = fmt(COPY.decidedSystem, { clock: at });
    const cs = Array.isArray(a.countersigned) ? a.countersigned : [];
    if (a.status === 'approved' && cs.length) body = fmt(COPY.decidedApproved, { name: cs[cs.length - 1].user.name, clock: at });
    if (a.status === 'denied' && a.denied_by) body = fmt(COPY.decidedDenied, { name: a.denied_by.name, clock: at });
    if (a.status === 'cancelled') body = fmt(COPY.decidedCancelled, { clock: at });
    setChip(a.status === 'denied' ? 'denied' : 'neutral', a.status);
    setTitle(COPY.decidedTitle, body, focus);
    consoleAt(p.console_url);
  }

  // ---------- after a decision: follow the action until it is sealed ----------
  function renderAction(st) {
    const a = st.approval;
    const act = st.action || {};
    if (a.status === 'pending') {
      const cs = Array.isArray(a.countersigned) ? a.countersigned.length : 0;
      setChip('pending', COPY.waitingOthers);
      setTitle(fmt(COPY.partialTitle, { done: cs, need: a.required }), partialBody(a), true);
      return true;
    }
    if (a.status === 'denied') {
      setChip('denied', 'denied');
      setTitle(COPY.deniedTitle, COPY.deniedBody, true);
      return true;
    }
    if (act.state === 'sealed' || act.state === 'denied') {
      if (act.status === 'denied') {
        setChip('denied', 'denied');
        setTitle(COPY.deniedTitle, fmt(COPY.stoppedAfter, { agent: a.agent.name }), true);
        return true;
      }
      const w = act.witness || null;
      const verdict = w ? w.verdict : '';
      const kind = verdict === 'VERIFIED' ? 'verified' : verdict === 'MISMATCH' ? 'mismatch' : verdict ? 'unverifiable' : 'neutral';
      const line = (w && w.summary) || COPY.sealedFallback; // the chip already names the level and verdict
      setChip(kind, w ? plainLevel(w.level) + ' · ' + plainVerdict(w.verdict) : 'sealed');
      setTitle(kind === 'mismatch' ? COPY.sealedMismatchTitle : COPY.sealedTitle, line, true);
      if (act.deed) {
        $('deed-id').textContent = act.deed;
        show($('deed'), true);
      }
      playSeal(kind);
      consoleAt(preview && preview.console_url);
      return true;
    }
    if (act.status === 'authorized') {
      setChip('pending', 'authorized');
      setTitle(COPY.runningTitle, COPY.authorizedBody, false);
      return false;
    }
    setChip('pending', COPY.runningChip);
    setTitle(COPY.runningTitle, COPY.runningBody, false);
    return false;
  }

  function playSeal(kind) {
    show(seal, true);
    seal.classList.remove('closed', 'short', 'verified');
    const apply = () => {
      if (kind === 'mismatch') seal.classList.add('short');
      else seal.classList.add('closed');
      if (kind === 'verified') seal.classList.add('verified');
    };
    if (reduceMotion) apply();
    else requestAnimationFrame(() => requestAnimationFrame(apply));
  }

  async function poll() {
    clearTimeout(pollTimer);
    const r = await post('/v1/approval-links/status', { token });
    if (r.network) {
      statusEl.textContent = COPY.offline;
    } else if (r.status === 200 && r.data) {
      statusEl.textContent = '';
      if (renderAction(r.data)) return;
    } else {
      return;
    }
    if (Date.now() < pollUntil) pollTimer = setTimeout(poll, 1500);
  }

  // ---------- load ----------
  async function load(focus) {
    setAlert('');
    if (!token) return invalid();
    const r = await post('/v1/approval-links/preview', { token });
    if (r.network) {
      done();
      setTitle(COPY.loading.replace('…', ''), '', false);
      return setAlert(COPY.offline, () => load(true));
    }
    if (r.status === 200 && r.data) return renderPreview(r.data, focus);
    if (r.status === 429) { done(); return setAlert(COPY.limited, () => load(true)); }
    if (r.status === 401) return invalidOrDecided();
    done();
    return setAlert(r.error && r.error.message ? r.error.message : COPY.offline, () => load(true));
  }

  // A link that no longer previews may belong to an approval someone else just decided.
  async function invalidOrDecided() {
    const r = await post('/v1/approval-links/status', { token });
    if (r.status === 200 && r.data && r.data.approval && r.data.approval.status !== 'pending') {
      done();
      if (preview) return renderDecided(Object.assign({}, preview, { approval: r.data.approval }), true);
      return renderDecided({ approval: r.data.approval, time_zone: 'UTC' }, true);
    }
    invalid();
  }

  function invalid() {
    done();
    try { sessionStorage.removeItem(KEY); } catch (e) { /* nothing to clear */ }
    setChip(null);
    hideContext();
    actions(false, false);
    show($('fine'), false);
    setTitle(COPY.invalidTitle, COPY.invalidBody, true);
    consoleAt('/app/');
  }

  async function decide(vote, reason) {
    const btn = vote === 'approve' ? approveBtn : denyBtn;
    const label = btn.textContent;
    btn.disabled = true;
    btn.setAttribute('aria-busy', 'true');
    btn.textContent = vote === 'approve' ? COPY.approving : COPY.denying;
    setAlert('');
    const r = await post('/v1/approval-links/decide', { token, vote, reason: reason || '', context_digest: preview.approval.context_digest });
    btn.disabled = false;
    btn.removeAttribute('aria-busy');
    btn.textContent = label;
    if (r.network) return setAlert(COPY.offline, () => decide(vote, reason));
    if (r.status === 200 && r.data) {
      actions(false, false);
      show($('fine'), false);
      hideContext();
      show(banner, false);
      pollUntil = Date.now() + 15 * 60 * 1000;
      const a = r.data;
      if (a.status === 'denied') return renderAction({ approval: a, action: {} });
      if (a.status === 'pending') return renderAction({ approval: a, action: {} });
      setChip('pending', COPY.runningChip);
      setTitle(COPY.runningTitle, COPY.runningBody, true);
      pollTimer = setTimeout(poll, 600);
      return;
    }
    const code = r.error ? r.error.code : '';
    if (code === 'context_changed') { await load(false); return setAlert(COPY.stale); }
    if (code === 'validation_failed' && r.error.param === 'reason') return reasonError(r.error.message);
    if (r.status === 401) return invalidOrDecided();
    if (code === 'approval_decided' || code === 'approval_expired' || code === 'approver_not_eligible' || code === 'self_approval' ||
        code === 'approval_link_review_only' || code === 'already_countersigned') {
      await load(true);
      if (code !== 'approval_decided' && code !== 'approval_expired') setAlert(r.error.message);
      return;
    }
    if (r.status === 429) return setAlert(COPY.limited);
    setAlert(r.error && r.error.message ? r.error.message : COPY.offline);
  }

  // ---------- events ----------
  retry.addEventListener('click', () => { if (retryFn) retryFn(); });
  approveBtn.addEventListener('click', () => decide('approve'));
  denyOpen.addEventListener('click', () => {
    show($('actions'), false);
    show(sheet, true);
    denyOpen.setAttribute('aria-expanded', 'true');
    reasonEl.focus();
  });
  $('deny-back').addEventListener('click', () => {
    show(sheet, false);
    reasonOK();
    renderPreview(preview, false);
    (approveBtn.hidden ? denyOpen : approveBtn).focus();
  });
  // A reason error sits under the field it is about (and is read with it), not in the page alert.
  const reasonHelp = $('reason-help');
  const reasonHelpText = reasonHelp.textContent;
  function reasonError(msg) {
    reasonEl.setAttribute('aria-invalid', 'true');
    reasonHelp.textContent = msg;
    reasonHelp.classList.add('help-error');
    reasonEl.focus();
  }
  function reasonOK() {
    reasonEl.removeAttribute('aria-invalid');
    reasonHelp.textContent = reasonHelpText;
    reasonHelp.classList.remove('help-error');
  }
  reasonEl.addEventListener('input', () => {
    $('reason-count').textContent = fmt(COPY.reasonCount, { n: [...reasonEl.value].length });
    if (reasonEl.value.trim()) reasonOK();
  });
  sheet.addEventListener('submit', (e) => {
    e.preventDefault();
    const reason = reasonEl.value.trim();
    if (!reason) return reasonError(COPY.reasonEmpty);
    decide('deny', reason);
  });
  $('copy').addEventListener('click', async () => {
    const id = $('deed-id').textContent;
    try { await navigator.clipboard.writeText(id); statusEl.textContent = COPY.copied; } catch (e) { /* nothing to report */ }
  });
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden && pollUntil && Date.now() < pollUntil && !pollTimer) poll();
  });

  const clEditBtn = $('cl-btn-edit');
  if (clEditBtn) {
    clEditBtn.addEventListener('click', () => {
      if (!clinicalData) return;
      editingClinical = !editingClinical;
      clEditBtn.textContent = editingClinical ? 'Confirm edits' : 'Edit order';
      if (!editingClinical) {
        const inputs = $('cl-fields').querySelectorAll('input.cl-edit-input');
        const diffs = [];
        for (const inp of inputs) {
          const k = inp.dataset.key;
          for (const f of (clinicalData.fields || [])) {
            if (f.key === k) {
              const orig = originalFields.find((o) => o.key === k);
              const origVal = orig ? orig.value : '';
              if (origVal !== inp.value) {
                f.prior = origVal;
                diffs.push({ label: f.label || f.key, oldValue: origVal, newValue: inp.value });
              }
              f.value = inp.value;
            }
          }
        }
        if (diffs.length > 0) {
          $('approve').textContent = COPY.approveCommit;
          renderDiffSummary(diffs);
        } else {
          renderDiffSummary([]);
        }
      }
      renderClinicalFields(clinicalData.fields || []);
    });
  }

  const clReauthBtn = $('cl-btn-reauth');
  if (clReauthBtn) {
    clReauthBtn.addEventListener('click', () => {
      startSessionTimer(300, clinicalData ? clinicalData.practitioner_role : 'Attending Clinician');
    });
  }

  load(false);
})();
