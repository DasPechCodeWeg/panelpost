// Panelpost UI helpers. No framework, no inline scripts (strict CSP).
(function () {
  'use strict';
  const csrf = () => (document.querySelector('meta[name="csrf"]') || {}).content || '';
  const $ = (sel, root) => (root || document).querySelector(sel);
  const $$ = (sel, root) => Array.from((root || document).querySelectorAll(sel));

  async function getJSON(url) {
    const res = await fetch(url, { credentials: 'same-origin', headers: { 'Accept': 'application/json' } });
    return res.json();
  }
  async function postForm(url, form) {
    const data = new FormData(form);
    const res = await fetch(url, { method: 'POST', body: data, credentials: 'same-origin', headers: { 'X-CSRF-Token': csrf(), 'Accept': 'application/json' } });
    return res.json();
  }
  function setStatus(el, result) {
    if (!el) return;
    el.classList.remove('ok', 'err');
    if (result.error) { el.textContent = result.error; el.classList.add('err'); }
    else { el.textContent = result.ok || 'OK'; el.classList.add('ok'); }
  }

  // Confirmation for destructive forms.
  document.addEventListener('submit', (e) => {
    const msg = e.target.getAttribute('data-confirm');
    if (msg && !window.confirm(msg)) e.preventDefault();
  });

  // Connection test.
  const testBtn = $('[data-test-connection]');
  if (testBtn) {
    testBtn.addEventListener('click', async () => {
      const out = $('[data-test-result]');
      out.textContent = 'Testing…'; out.classList.remove('ok', 'err');
      try { setStatus(out, await postForm('/connections/test', $('[data-connection-form]'))); }
      catch (err) { setStatus(out, { error: String(err) }); }
    });
  }

  // Email test.
  const mailBtn = $('[data-test-email]');
  if (mailBtn) {
    mailBtn.addEventListener('click', async () => {
      const out = $('[data-test-email-result]');
      out.textContent = 'Sending…'; out.classList.remove('ok', 'err');
      try { setStatus(out, await postForm('/settings/email/test', $('[data-email-form]'))); }
      catch (err) { setStatus(out, { error: String(err) }); }
    });
  }

  // Run page: refresh until finished.
  const runCard = $('[data-run]');
  if (runCard && ['queued', 'running'].includes(runCard.dataset.status)) {
    const id = runCard.dataset.run;
    const poll = async () => {
      try {
        const run = await getJSON('/ui/runs/' + encodeURIComponent(id));
        if (!['queued', 'running'].includes(run.status)) { window.location.reload(); return; }
      } catch (e) { /* keep polling */ }
      setTimeout(poll, 1500);
    };
    setTimeout(poll, 1500);
  }

  // Report editor.
  const form = $('[data-report-form]');
  if (!form) return;

  const show = (els, on) => els.forEach((el) => el.classList.toggle('hidden', !on));

  const preset = $('[data-time-preset]', form);
  const syncPreset = () => show($$('[data-custom-time]', form), preset.value === 'custom');
  preset.addEventListener('change', syncPreset); syncPreset();

  const mode = $('[data-mode]', form);
  const syncMode = () => show($$('[data-panels-only]', form), mode.value === 'panels');
  mode.addEventListener('change', syncMode); syncMode();

  const burst = $('[data-burst-toggle]', form);
  const syncBurst = () => show($$('[data-burst]', form), burst && burst.checked);
  if (burst) { burst.addEventListener('change', syncBurst); syncBurst(); }

  // Schedule fields and preview.
  const kind = $('[data-schedule-kind]', form);
  const preview = $('[data-schedule-preview]', form);
  const syncSchedule = () => {
    $$('[data-sched]', form).forEach((el) => el.classList.toggle('hidden', !el.dataset.sched.split(' ').includes(kind.value)));
  };
  let schedTimer;
  const refreshSchedule = () => {
    clearTimeout(schedTimer);
    schedTimer = setTimeout(async () => {
      if (kind.value === 'manual') { preview.textContent = 'Runs only when you start it (Run now, or the API).'; return; }
      const q = new URLSearchParams();
      q.set('kind', kind.value);
      q.set('at', (form.elements.at || {}).value || '');
      q.set('month_day', (form.elements.month_day || {}).value || '');
      q.set('cron', (form.elements.cron || {}).value || '');
      q.set('tz', (form.elements.timezone || {}).value || '');
      $$('input[name="weekdays"]:checked', form).forEach((c) => q.append('weekdays', c.value));
      try {
        const res = await getJSON('/ui/schedule?' + q.toString());
        preview.classList.toggle('status', !!res.error);
        preview.classList.toggle('err', !!res.error);
        preview.textContent = res.error ? res.error : res.describe + ' · next: ' + (res.next || []).join(', ');
      } catch (e) { /* ignore */ }
    }, 250);
  };
  kind.addEventListener('change', () => { syncSchedule(); refreshSchedule(); });
  ['at', 'month_day', 'cron', 'timezone'].forEach((n) => form.elements[n] && form.elements[n].addEventListener('input', refreshSchedule));
  $$('input[name="weekdays"]', form).forEach((c) => c.addEventListener('change', refreshSchedule));
  syncSchedule(); refreshSchedule();

  // Dashboard picker.
  const conn = $('[data-connection]', form);
  const select = $('[data-dashboard-select]', form);
  const uid = $('[data-dashboard-uid]', form);
  const titleInput = $('[data-dashboard-title]', form);
  const status = $('[data-dashboard-status]', form);
  const vars = $('[data-variables]', form);
  const varHint = $('[data-variable-hint]', form);
  const panelHint = $('[data-panel-hint]', form);

  const option = (value, text) => { const o = document.createElement('option'); o.value = value; o.textContent = text; return o; };

  async function loadDashboards() {
    if (!conn || !conn.value) { select.replaceChildren(option('', 'Add a Grafana connection first')); return; }
    select.replaceChildren(option('', 'Loading dashboards…'));
    try {
      const res = await getJSON('/ui/dashboards?connection=' + encodeURIComponent(conn.value));
      if (res.error) { select.replaceChildren(option('', 'Could not list dashboards')); status.textContent = res.error; status.classList.add('status', 'err'); return; }
      const list = res.dashboards || [];
      const opts = [option('', list.length ? 'Choose a dashboard…' : 'No dashboards visible to this token')];
      let found = false;
      list.forEach((d) => {
        const o = option(d.uid, (d.folderTitle ? d.folderTitle + ' / ' : '') + d.title);
        o.dataset.title = d.title;
        if (d.uid === uid.value) { o.selected = true; found = true; }
        opts.push(o);
      });
      if (!found && uid.value) { const o = option(uid.value, (select.dataset.currentTitle || uid.value) + ' (current)'); o.selected = true; opts.push(o); }
      select.replaceChildren(...opts);
      status.classList.remove('status', 'err');
      if (uid.value) loadDashboard(false);
    } catch (e) { select.replaceChildren(option('', 'Could not list dashboards')); }
  }

  async function loadDashboard(fillVariables) {
    if (!uid.value || !conn.value) return;
    try {
      const res = await getJSON('/ui/dashboard?connection=' + encodeURIComponent(conn.value) + '&uid=' + encodeURIComponent(uid.value));
      if (res.error) { status.textContent = res.error; status.classList.add('status', 'err'); return; }
      status.classList.remove('status', 'err');
      status.textContent = 'Dashboard: ' + res.title;
      titleInput.value = res.title || '';
      if (!form.elements.name.value) form.elements.name.value = res.title || '';
      const v = res.variables || [];
      if (fillVariables && v.length && !vars.value.trim()) {
        vars.value = v.map((x) => x.name + ' = ' + ((x.current && x.current.length) ? x.current.join(', ') : '')).join('\n');
      }
      varHint.textContent = v.length
        ? 'Variables on this dashboard: ' + v.map((x) => x.name + (x.options && x.options.length ? ' (' + x.options.slice(0, 6).join(', ') + (x.options.length > 6 ? ', …' : '') + ')' : '')).join(' · ')
        : 'This dashboard has no template variables.';
      if (panelHint) panelHint.textContent = (res.panels || []).length ? 'Panels: ' + res.panels.slice(0, 20).join(' · ') : '';
    } catch (e) { /* ignore */ }
  }

  if (conn) conn.addEventListener('change', () => { uid.value = ''; vars.value = ''; loadDashboards(); });
  select.addEventListener('change', () => {
    if (!select.value) return;
    uid.value = select.value;
    const o = select.selectedOptions[0];
    titleInput.value = (o && o.dataset.title) || '';
    loadDashboard(true);
  });
  uid.addEventListener('change', () => loadDashboard(true));
  loadDashboards();
})();
