'use strict';

const $ = (id) => document.getElementById(id);

const state = {
  data: null,      // what the server has
  fields: [],      // field paths the template reads, as shapes
  skeleton: {},    // data skeleton built from those fields
  dirty: false,    // the panel has edits that have not been applied
};

const VIEW_TABS = [
  { tab: 'tab-preview', panel: 'panel-preview' },
  { tab: 'tab-source', panel: 'panel-source' },
];
const MODE_TABS = [
  { tab: 'tab-list', panel: 'panel-list' },
  { tab: 'tab-json', panel: 'json' },
];

function selectTab(group, id) {
  for (const t of group) {
    const on = t.tab === id;
    $(t.tab).setAttribute('aria-selected', String(on));
    $(t.panel).hidden = !on;
  }
}

function say(message, bad) {
  const el = $('status');
  el.textContent = message || '';
  el.hidden = !message;
  el.dataset.variant = bad ? 'danger' : '';
}

/* ---------- field paths -------------------------------------------------- */

// Shapes that only contain other shapes hold no value of their own.
function leafShapes(fields) {
  return fields.filter((f) => !f.includes('*')
    && !fields.some((o) => o !== f && (o.startsWith(`${f}.`) || o.startsWith(`${f}[`))));
}

// Expand a shape against the data into the concrete paths it covers, so
// ".items[].name" becomes ".items[0].name", ".items[1].name".
function expand(shape, data) {
  let cur = [{ path: '', node: data }];
  for (const seg of shape.replace(/^\./, '').split('.')) {
    const isList = seg.endsWith('[]');
    const key = isList ? seg.slice(0, -2) : seg;
    const next = [];
    for (const c of cur) {
      const obj = c.node;
      const val = obj && typeof obj === 'object' && !Array.isArray(obj) ? obj[key] : undefined;
      const path = `${c.path}.${key}`;
      if (!isList) {
        next.push({ path, node: val });
      } else if (Array.isArray(val) && val.length) {
        val.forEach((el, i) => next.push({ path: `${path}[${i}]`, node: el }));
      } else {
        next.push({ path: `${path}[0]`, node: undefined });
      }
    }
    cur = next;
  }
  return cur;
}

function parsePath(path) {
  const segs = [];
  for (const seg of path.replace(/^\./, '').split('.')) {
    segs.push(seg.replace(/\[\d+\]/g, ''));
    for (const idx of seg.match(/\[\d+\]/g) || []) segs.push(Number(idx.slice(1, -1)));
  }
  return segs;
}

function getAt(obj, segs) {
  let cur = obj;
  for (const s of segs) {
    if (cur === null || typeof cur !== 'object') return undefined;
    cur = cur[s];
  }
  return cur;
}

function setAt(obj, segs, value) {
  let cur = obj;
  for (let i = 0; i < segs.length - 1; i++) {
    const key = segs[i];
    if (cur[key] === null || typeof cur[key] !== 'object') {
      cur[key] = typeof segs[i + 1] === 'number' ? [] : {};
    }
    cur = cur[key];
  }
  cur[segs[segs.length - 1]] = value;
}

// Keep the type the value already had: a template doing `printf "%.2f"` fails
// if a number comes back from the editor as a string.
function coerce(text, previous) {
  const t = text.trim();
  if (typeof previous === 'number') {
    return t !== '' && !Number.isNaN(Number(t)) ? Number(t) : text;
  }
  if (typeof previous === 'boolean') return t === 'true';
  if (previous === undefined) {
    if (t === '') return undefined;
    if (/^-?\d+(\.\d+)?$/.test(t)) return Number(t);
    if (t === 'true' || t === 'false') return t === 'true';
  }
  return text;
}

/* ---------- rendering ---------------------------------------------------- */

function buildRows() {
  const rows = [];
  for (const shape of leafShapes(state.fields)) {
    for (const { path, node } of expand(shape, state.data)) {
      const unset = node === undefined || node === null;
      rows.push({ path, unset, value: unset ? '' : String(node) });
    }
  }
  rows.sort((a, b) => a.path.localeCompare(b.path, undefined, { numeric: true }));
  return rows;
}

function renderRows() {
  const rows = buildRows();
  const unset = rows.filter((r) => r.unset).length;
  const body = $('row-body');
  const frag = document.createDocumentFragment();

  for (const [i, row] of rows.entries()) {
    const tr = document.createElement('tr');
    const th = document.createElement('th');
    const label = document.createElement('label');
    const td = document.createElement('td');
    const input = document.createElement('input');

    th.scope = 'row';
    label.htmlFor = `field-${i}`;
    label.textContent = row.path;
    th.append(label);

    input.type = 'text';
    input.id = `field-${i}`;
    input.value = row.value;
    input.dataset.path = row.path;
    if (row.unset) {
      input.placeholder = 'not provided';
      input.dataset.variant = 'warning';
    }
    input.addEventListener('input', () => { state.dirty = true; say(''); });
    td.append(input);

    tr.append(th, td);
    frag.append(tr);
  }
  body.replaceChildren(frag);

  const hasData = state.data !== null && state.data !== undefined;
  $('count-fields').textContent = `${rows.length} field${rows.length === 1 ? '' : 's'}`;
  $('count-unset').textContent = `${unset} unset`;
  $('count-unset').hidden = unset === 0;
  $('empty').hidden = hasData;
  $('panel-list').hidden = !hasData || $('tab-list').getAttribute('aria-selected') !== 'true';
  $('empty-count').textContent = `${rows.length} field${rows.length === 1 ? '' : 's'}`;
  $('json').value = hasData ? JSON.stringify(state.data, null, 2) : '';
}

/* ---------- server ------------------------------------------------------- */

async function getJSON(url) {
  const res = await fetch(url);
  const body = await res.json();
  if (!res.ok) throw new Error(body.error || res.statusText);
  return body.data;
}

async function putJSON(url, payload) {
  const res = await fetch(url, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const body = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(body.error || res.statusText);
}

async function refreshFields() {
  try {
    const d = await getJSON('/fields');
    state.fields = d.fields || [];
    state.skeleton = d.skeleton || {};
  } catch (err) {
    say(String(err.message || err), true);
  }
}

async function refreshData() {
  try {
    state.data = await getJSON('/data');
    state.dirty = false;
  } catch (err) {
    say(String(err.message || err), true);
  }
}

async function refreshSource() {
  const res = await fetch('/source');
  $('source-body').textContent = await res.text();
}

async function refreshTemplates() {
  try {
    const d = await getJSON('/templates');
    const list = d.templates || [];
    $('cmd').textContent = d.entry || '';
    $('source-name').textContent = d.entry || '';

    // Always shown, even for a single template, so the toolbar does not
    // change shape depending on what is being served.
    const select = $('entry');
    select.replaceChildren(...list.map((path) => {
      const option = document.createElement('option');
      option.value = path;
      option.textContent = path.split('/').pop();
      option.selected = path === d.entry;
      return option;
    }));
  } catch (err) {
    say(String(err.message || err), true);
  }
}

async function apply() {
  let next;
  if ($('tab-json').getAttribute('aria-selected') === 'true') {
    try {
      next = JSON.parse($('json').value);
      $('json').removeAttribute('aria-invalid');
    } catch (err) {
      $('json').setAttribute('aria-invalid', 'true');
      say(`Invalid JSON: ${err.message}`, true);
      return;
    }
  } else {
    // Patch a copy so keys the template never reads are preserved.
    next = structuredClone(state.data ?? {});
    for (const input of document.querySelectorAll('#row-body input')) {
      const segs = parsePath(input.dataset.path);
      const value = coerce(input.value, getAt(next, segs));
      if (value === undefined) continue;
      setAt(next, segs, value);
    }
  }

  try {
    await putJSON('/data', next);
    state.data = next;
    state.dirty = false;
    renderRows();
    say('applied');
  } catch (err) {
    say(String(err.message || err), true);
  }
}

/* ---------- wiring ------------------------------------------------------- */

for (const t of VIEW_TABS) {
  $(t.tab).addEventListener('click', () => {
    selectTab(VIEW_TABS, t.tab);
    if (t.tab === 'tab-source') refreshSource();
  });
}

for (const t of MODE_TABS) {
  $(t.tab).addEventListener('click', () => {
    selectTab(MODE_TABS, t.tab);
    renderRows();
  });
}

function setWidth(mobile) {
  $('w-desktop').setAttribute('aria-pressed', String(!mobile));
  $('w-mobile').setAttribute('aria-pressed', String(mobile));
  $('out').style.width = mobile ? '390px' : '100%';
  // Report what is actually being rendered, not the setting.
  $('width').textContent = `${$('out').clientWidth} px`;
}
$('w-desktop').addEventListener('click', () => setWidth(false));
$('w-mobile').addEventListener('click', () => setWidth(true));
addEventListener('resize', () => setWidth($('w-mobile').getAttribute('aria-pressed') === 'true'));

$('theme').addEventListener('click', () => {
  const root = document.documentElement;
  const dark = getComputedStyle(root).colorScheme.includes('dark');
  const next = dark ? 'light' : 'dark';
  root.style.colorScheme = next;
  try { localStorage.setItem('gotp-theme', next); } catch (e) {}
});

$('entry').addEventListener('change', async (e) => {
  try {
    await putJSON('/entry', { entry: e.target.value });
  } catch (err) {
    say(String(err.message || err), true);
  }
});

$('apply').addEventListener('click', apply);

$('json').addEventListener('input', () => { state.dirty = true; say(''); });

$('autofill').addEventListener('click', async () => {
  try {
    await putJSON('/data', state.skeleton);
    state.data = state.skeleton;
    renderRows();
  } catch (err) {
    say(String(err.message || err), true);
  }
});

const events = new EventSource('/events');
events.onopen = () => {
  $('watching').textContent = 'live';
  $('watching').dataset.variant = 'success';
};
events.onerror = () => {
  $('watching').textContent = 'disconnected';
  $('watching').dataset.variant = 'danger';
};
events.onmessage = async () => {
  $('out').contentWindow.location.reload();
  await Promise.all([refreshFields(), refreshTemplates()]);
  if (!$('panel-source').hidden) refreshSource();
  // Do not clobber edits that have not been applied yet.
  if (!state.dirty) {
    await refreshData();
    renderRows();
  }
};

(async function start() {
  await Promise.all([refreshFields(), refreshData(), refreshTemplates()]);
  renderRows();
  setWidth(false);
})();
