/* lagra settings — plain JS. Talks to the local API the daemon serves.
   Design rule: the essentials first, in plain words; everything technical
   lives behind "Advanced". */
(() => {
  const $ = (sel, el = document) => el.querySelector(sel);
  const h = (tag, attrs = {}, ...children) => {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) {
      if (k === 'class') el.className = v;
      else if (k.startsWith('on')) el.addEventListener(k.slice(2), v);
      else if (v === false || v == null) continue;
      else if (v === true) el.setAttribute(k, '');
      else el.setAttribute(k, v);
    }
    for (const c of children.flat(Infinity)) {
      if (c == null || c === false) continue;
      el.append(c.nodeType ? c : document.createTextNode(String(c)));
    }
    return el;
  };
  const enc = encodeURIComponent;

  // ---------------------------------------------------------------- icons
  // One stroke style for every glyph, drawn on a 24 grid.
  const ICONS = {
    plus: '<path d="M12 5v14M5 12h14"/>',
    chevron: '<path d="m9.5 6 6 6-6 6"/>',
    check: '<path d="m5 12.5 4.5 4.5L19 7.5"/>',
    drive: '<rect x="2.75" y="12.5" width="18.5" height="7.5" rx="2"/><path d="m4.5 12.5 2.6-6.8A2 2 0 0 1 9 4.5h6a2 2 0 0 1 1.9 1.2l2.6 6.8"/><path d="M6.75 16.25h.01M10 16.25h.01"/>',
    bucket: '<ellipse cx="12" cy="6" rx="8" ry="2.5"/><path d="m4 6 1.8 12.3a2.5 2.5 0 0 0 2.5 2.2h7.4a2.5 2.5 0 0 0 2.5-2.2L20 6"/>',
    terminal: '<rect x="3" y="4.5" width="18" height="15" rx="2.5"/><path d="m7.5 9.5 3 2.5-3 2.5M13 15h3.5"/>',
    globe: '<circle cx="12" cy="12" r="8.5"/><path d="M3.5 12h17M12 3.5c2.4 2.3 3.6 5.1 3.6 8.5s-1.2 6.2-3.6 8.5c-2.4-2.3-3.6-5.1-3.6-8.5S9.6 5.8 12 3.5Z"/>',
    server: '<rect x="3.5" y="4" width="17" height="7" rx="2"/><rect x="3.5" y="13" width="17" height="7" rx="2"/><path d="M7.5 7.5h.01M7.5 16.5h.01"/>',
    transfer: '<path d="M7 20V4M3.5 7.5 7 4l3.5 3.5M17 4v16M13.5 16.5 17 20l3.5-3.5"/>',
    cloud: '<path d="M7.5 19h9.75a4.25 4.25 0 0 0 .45-8.48A6 6 0 0 0 6.2 9.2 4.9 4.9 0 0 0 7.5 19Z"/>',
    cloudOff: '<path d="M10 6.1A6 6 0 0 1 17.7 10.5a4.25 4.25 0 0 1 2.2 7.2M16 19H7.5a4.9 4.9 0 0 1-1.3-9.8"/><path d="m3.5 3.5 17 17"/>',
    person: '<circle cx="12" cy="8.5" r="3.75"/><path d="M4.75 20a7.25 7.25 0 0 1 14.5 0"/>',
    sliders: '<path d="M4 7h9M17 7h3M4 17h3M11 17h9"/><circle cx="15" cy="7" r="2"/><circle cx="9" cy="17" r="2"/>',
    help: '<circle cx="12" cy="12" r="8.5"/><path d="M9.6 9.4a2.5 2.5 0 1 1 3.4 2.4c-.6.3-1 .8-1 1.5v.4"/><path d="M12 16.75h.01"/>',
    alert: '<path d="M10.3 4.3 2.9 17.2a2 2 0 0 0 1.7 3h14.8a2 2 0 0 0 1.7-3L13.7 4.3a2 2 0 0 0-3.4 0Z"/><path d="M12 9.5v4M12 16.75h.01"/>',
    info: '<circle cx="12" cy="12" r="8.5"/><path d="M12 11v5.25M12 7.75h.01"/>',
    refresh: '<path d="M19.5 12a7.5 7.5 0 1 1-2.2-5.3l2.2 2.2"/><path d="M19.5 4.5v4.4h-4.4"/>',
    eject: '<path d="M12 4.5 4.5 13h15Z"/><path d="M4.5 18.5h15"/>',
    power: '<path d="M12 3.5v7.5"/><path d="M6.7 6.9a7.5 7.5 0 1 0 10.6 0"/>',
    folder: '<path d="M3.5 7.5a2 2 0 0 1 2-2h3.6l2 2.25h7.4a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2Z"/>',
    trash: '<path d="M4.5 7h15M10 11v5.5M14 11v5.5M6.5 7l.8 11.2a2 2 0 0 0 2 1.8h5.4a2 2 0 0 0 2-1.8L17.5 7M9 7V4.75h6V7"/>',
    lock: '<rect x="5" y="10.5" width="14" height="9.5" rx="2"/><path d="M8.5 10.5V8a3.5 3.5 0 0 1 7 0v2.5"/>',
    shield: '<path d="M12 3.5 5.5 6v5.5c0 4.1 2.7 7.2 6.5 9 3.8-1.8 6.5-4.9 6.5-9V6Z"/><path d="m9.25 12 2 2 3.5-3.75"/>',
    copy: '<rect x="8.5" y="8.5" width="11" height="11" rx="2"/><path d="M15.5 8.5V6.5a2 2 0 0 0-2-2h-7a2 2 0 0 0-2 2v7a2 2 0 0 0 2 2h2"/>',
    file: '<path d="M14 3.5H7.5a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h9a2 2 0 0 0 2-2V8Z"/><path d="M14 3.5V8h4.5M9 12.5h6M9 16h6"/>',
    search: '<circle cx="11" cy="11" r="6"/><path d="m15.5 15.5 4 4"/>',
    pencil: '<path d="M4.5 19.5h3.75L19 8.75a2.65 2.65 0 0 0-3.75-3.75L4.5 15.75Z"/>',
    upload: '<path d="M12 15.5v-10M7.75 9.75 12 5.5l4.25 4.25M5 19h14"/>',
    more: '<circle cx="5.5" cy="12" r="1.4"/><circle cx="12" cy="12" r="1.4"/><circle cx="18.5" cy="12" r="1.4"/>',
  };
  function icon(name, cls = '') {
    const s = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    s.setAttribute('viewBox', '0 0 24 24');
    s.setAttribute('class', ('icon ' + cls).trim());
    s.setAttribute('aria-hidden', 'true');
    s.innerHTML = ICONS[name] || ICONS.cloud;
    return s;
  }

  // ---------------------------------------------------------------- data
  const state = { meta: null, view: null, status: null, current: { kind: 'general' }, optionCache: {}, dirty: false };

  // Changes name the version of the file the page shows (If-Match); the
  // server refuses a save with 412 when the file changed since, and every
  // change answers with the new version.
  async function api(method, path, body) {
    const headers = body ? { 'Content-Type': 'application/json' } : {};
    if (method !== 'GET' && state.view?.file_version) headers['If-Match'] = `"${state.view.file_version}"`;
    const res = await fetch(path, { method, headers, body: body ? JSON.stringify(body) : undefined });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) { const e = new Error(data.error || res.statusText); e.issues = data.issues || []; e.status = res.status; throw e; }
    if (method !== 'GET' && data.file_version && state.view) state.view.file_version = data.file_version;
    return data;
  }
  async function refresh() {
    [state.view, state.status] = await Promise.all([api('GET', '/api/view'), api('GET', '/api/status')]);
    renderSidebar();
  }
  async function options(type) {
    if (!state.optionCache[type]) state.optionCache[type] = await api('GET', '/api/meta/options/' + enc(type));
    return state.optionCache[type];
  }
  const mountStatus = name => (state.status?.mounts || []).find(m => m.name === name);
  const accountByName = name => (state.view?.accounts || []).find(a => a.name === name);
  const tilde = p => p && state.meta?.home && (p === state.meta.home || p.startsWith(state.meta.home + '/')) ? '~' + p.slice(state.meta.home.length) : p;

  const toastEl = h('div', { class: 'toast', role: 'status' });
  document.body.append(toastEl);
  let toastTimer;
  function toast(msg, kind = 'ok', ms) {
    toastEl.replaceChildren(icon({ bad: 'alert', info: 'info' }[kind] || 'check'), h('span', {}, msg));
    toastEl.className = 'toast show ' + kind;
    clearTimeout(toastTimer); toastTimer = setTimeout(() => toastEl.classList.remove('show'), ms || (kind === 'bad' ? 4500 : 2400));
  }

  // Unsaved edits: any input on the page marks it; leaving asks first.
  const setDirty = v => { state.dirty = v; $('#main').classList.toggle('dirty', v); };
  for (const ev of ['input', 'change']) {
    $('#main').addEventListener(ev, e => { if (e.target.closest('.page') && !e.target.closest('[data-transient]')) setDirty(true); });
  }
  document.addEventListener('keydown', e => {
    if (e.metaKey && !e.shiftKey && !e.altKey && e.key.toLowerCase() === 's') {
      e.preventDefault();
      $('#main .actionbar .btn.primary:not(:disabled)')?.click();
    }
  });

  // ------------------------------------------------- what we know about types
  // Curated presentation for the common storage types. Anything not listed
  // still works: it gets a generic form built from rclone's metadata.
  // `fields` and `creds` are the account's (who you are), `where` are each
  // storage's own options (which server), shown next to its share or folder.
  const TYPES = {
    s3: {
      name: 'S3', icon: 'bucket',
      pathLabel: 'Bucket', pathHelp: 'The bucket name. Add a folder to show only part of it, e.g. my-bucket/photos.',
      fields: [
        { opt: 'provider', label: 'Provider', select: true },
        { opt: 'region', label: 'Region' },
        { opt: 'endpoint', label: 'Endpoint', showIf: v => v.provider && v.provider !== 'AWS' },
      ],
      creds: [{ opt: 'access_key_id', label: 'Access Key ID' }, { opt: 'secret_access_key', label: 'Secret Access Key' }],
    },
    sftp: {
      name: 'SFTP', icon: 'terminal',
      pathLabel: 'Folder on the server', pathHelp: 'Absolute path, e.g. /srv/data. Empty means the login folder.',
      where: [
        { opt: 'host', label: 'Server', placeholder: 'server.example.org' },
        { opt: 'port', label: 'Port', placeholder: '22', narrow: true },
      ],
      fields: [
        { opt: 'user', label: 'User name', public: true },
        { opt: 'key_file', label: 'SSH key file', placeholder: '~/.ssh/id_ed25519', help: 'Leave empty to use a password.' },
      ],
      creds: [{ opt: 'pass', label: 'Password' }, { opt: 'key_file_pass', label: 'Key passphrase' }],
    },
    webdav: {
      name: 'WebDAV', icon: 'globe',
      pathLabel: 'Folder', pathHelp: 'Folder below the WebDAV address, or empty.',
      where: [
        { opt: 'url', label: 'Address', placeholder: 'https://cloud.example.org/remote.php/dav/files/me' },
        { opt: 'vendor', label: 'Server type', select: true },
      ],
      fields: [{ opt: 'user', label: 'User name', public: true }],
      creds: [{ opt: 'pass', label: 'Password' }],
    },
    smb: {
      name: 'SMB', icon: 'server',
      pathLabel: 'Share', pathHelp: 'The share name, optionally followed by a folder: share/folder.',
      splitAddress: splitSMBAddress,
      where: [{ opt: 'host', label: 'Server', placeholder: 'server.example.org', help: 'The name only. A pasted smb:// address fills in the share as well.' }],
      fields: [
        { opt: 'user', label: 'User name', public: true },
        { opt: 'domain', label: 'Domain', placeholder: 'WORKGROUP' },
      ],
      creds: [{ opt: 'pass', label: 'Password' }],
    },
    ftp: {
      name: 'FTP', icon: 'transfer',
      pathLabel: 'Folder', pathHelp: 'Folder on the server, or empty.',
      where: [
        { opt: 'host', label: 'Server', placeholder: 'ftp.example.org' },
        { opt: 'port', label: 'Port', placeholder: '21', narrow: true },
        { opt: 'tls', label: 'Use TLS' },
      ],
      fields: [{ opt: 'user', label: 'User name', public: true }],
      creds: [{ opt: 'pass', label: 'Password' }],
    },
    azureblob: {
      name: 'Azure Blob Storage', icon: 'cloud', pathLabel: 'Container',
      fields: [{ opt: 'account', label: 'Storage account', public: true }],
      creds: [{ opt: 'key', label: 'Access key' }],
    },
    b2: {
      name: 'Backblaze B2', icon: 'cloud', pathLabel: 'Bucket',
      fields: [{ opt: 'account', label: 'Key ID', public: true }],
      creds: [{ opt: 'key', label: 'Application key' }],
    },
  };
  // What macOS's “Connect to Server” and Windows show for a share,
  // smb://DOMAIN;user@server/share/folder or \\server\share\folder, taken
  // apart. null for a plain server name.
  function splitSMBAddress(value) {
    let v = value.trim();
    if (v.startsWith('\\\\')) v = 'smb://' + v.slice(2).replace(/\\/g, '/');
    const m = v.match(/^(?:smb|cifs):\/\/(?:([^@/]*)@)?([^/]+)(?:\/(.*))?$/i);
    if (!m) return null;
    const dec = s => { try { return decodeURIComponent(s); } catch (_) { return s; } };
    const login = dec(m[1] || '').split(':')[0]; // a password in the address is left out
    const [domain, user] = login.includes(';') ? login.split(';') : ['', login];
    return { host: m[2].replace(/:445$/, ''), user, domain, path: dec(m[3] || '').replace(/^\/+|\/+$/g, '') };
  }
  const FEATURED = ['s3', 'sftp', 'webdav', 'smb', 'ftp'];
  // Shorter names where rclone's description is a sentence or lowercase.
  const NAMES = { hdfs: 'Hadoop HDFS', koofr: 'Koofr', oracleobjectstorage: 'Oracle Object Storage', seafile: 'Seafile', swift: 'OpenStack Swift' };
  const typeName = t => TYPES[t]?.name || NAMES[t] || (state.meta.backends.find(b => b.name === t)?.description) || t;
  const typeIcon = t => TYPES[t]?.icon || 'cloud';

  // ---------------------------------------------------------------- plain words
  // kind drives the colour everywhere: ok, busy, warn, bad, idle.
  function statusWords(s) {
    if (!s) return { kind: 'idle', glyph: 'power', title: 'Not active', line: 'Lagra picks this storage up once the settings file is valid.' };
    const err = explain(s.last_error);
    switch (s.state) {
      case 'connected': return {
        kind: 'ok', glyph: 'check', title: 'Connected', location: s.mount_point,
        line: s.uploads ? `Uploading ${s.uploads} file${s.uploads > 1 ? 's' : ''}…` : 'Your files are in Finder.',
        hint: s.uploads_stuck ? 'The uploads seem stuck. Check your network, or look at the log.' : '',
      };
      case 'offline': return { kind: 'warn', glyph: 'cloudOff', title: 'Storage unreachable', location: s.mount_point,
        line: 'The volume stays; changes upload once the storage answers again.', hint: err.hint || 'Check your network or the credentials.', tech: err.tech };
      case 'connecting': return { kind: 'busy', glyph: 'refresh', title: 'Connecting…', line: 'One moment.' };
      case 'broken': return { kind: 'warn', glyph: 'refresh', title: 'Reconnecting',
        line: `The volume stopped answering. Lagra rebuilds it${s.next_retry ? ' in ' + Math.max(0, Math.round((new Date(s.next_retry) - Date.now()) / 1000)) + ' s' : ''}.`, hint: err.hint, tech: err.tech };
      case 'failed': return { kind: 'bad', glyph: 'alert', title: 'Not connected',
        line: err.hint || (s.last_error ? 'Lagra could not connect. The details say why.' : 'Something is wrong with this storage.'), tech: err.tech, techOpen: !err.hint };
      case 'disconnected': return s.enabled
        ? { kind: 'idle', glyph: 'eject', title: 'Disconnected', line: s.manual ? 'You disconnected this storage. Press Connect to bring it back.' : 'Press Connect to mount it.' }
        : { kind: 'idle', glyph: 'power', title: 'Off', line: 'Turn on “Connect automatically” or press Connect.' };
    }
    return { kind: 'idle', glyph: 'info', title: s.state, line: '' };
  }
  // A plain-language hint for an error, and the raw text for the details.
  function explain(e) {
    if (!e) return { hint: '', tech: '' };
    const f = friendlyError(e);
    return f === e ? { hint: '', tech: e } : { hint: f, tech: e };
  }
  function friendlyError(e) {
    if (!e) return '';
    if (/in the keychain|keychain item not found/i.test(e)) return 'The account has no credentials yet. Enter them on the account page.';
    if (/rejected the login.*\blocked\b/i.test(e)) return 'The account is locked. Once it is unlocked again, press Connect.';
    if (/rejected the login/i.test(e)) return 'The server rejected the login. Check the user name, the domain and the password. Lagra stopped trying, so the account does not get locked.';
    if (/SignatureDoesNotMatch|InvalidAccessKeyId|403|Forbidden|AccessDenied|permission denied|authentication failed/i.test(e)) return 'The storage rejected the credentials. Check the keys or the password.';
    if (/NoSuchBucket|no such bucket|not found|does not exist/i.test(e)) return 'The bucket, share or folder was not found. Check the name.';
    if (/no such host|connection refused|timeout|timed out|network is unreachable/i.test(e)) return 'The storage cannot be reached. Check the address and your network.';
    if (/occupied by a mount lagra does not manage/i.test(e)) return 'Another volume already uses that folder on this Mac. Choose a different folder.';
    if (/operation not permitted|permission denied.*mount point|EPERM/i.test(e)) return 'macOS is blocking Lagra’s access to the volume. Allow Lagra under System Settings → Privacy & Security → Files and Folders (see Settings → Permissions).';
    return e;
  }
  function pendingWords(p) {
    if (!p) return '';
    const what = { disconnect: 'Disconnects', restart: 'Reconnects with your changes', remove: 'Goes away' }[p.action] || 'Continues';
    return `${what} once ${p.reason === 'in use' ? 'no app uses the volume anymore' : 'all uploads are done'}.`;
  }

  // ---------------------------------------------------------------- sidebar
  function navItem({ active, onclick, glyph, kind, name, sub }) {
    return h('li', {}, h('button', { class: 'nav' + (active ? ' active' : ''), onclick },
      h('span', { class: 'nav-icon' }, icon(glyph), kind ? h('span', { class: 'badge ' + kind }) : null),
      h('span', { class: 'text' }, h('span', { class: 'name' }, name), sub ? h('span', { class: 'sub' }, sub) : null)));
  }
  function renderSidebar() {
    const mounts = state.view?.mounts || [], accounts = state.view?.accounts || [];
    $('#mountlist').replaceChildren(...mounts.map(m => {
      const s = mountStatus(m.name), w = statusWords(s);
      return navItem({ active: isCurrent('mount', m.name), onclick: () => show({ kind: 'mount', name: m.name }), glyph: 'drive', kind: w.kind, name: m.name,
        sub: s ? (s.state === 'connected' && s.uploads ? `Uploading ${s.uploads}…` : w.title) : m.account });
    }), ...(mounts.length ? [] : [h('li', { class: 'empty' }, 'No storages yet')]));
    $('#accountlist').replaceChildren(...accounts.map(a => {
      const n = (a.used_by || []).length, types = accountTypes(a.name);
      return navItem({ active: isCurrent('account', a.name), onclick: () => show({ kind: 'account', name: a.name }), glyph: types.length ? typeIcon(types[0]) : 'person', name: a.name,
        sub: n ? `${types.map(typeName).join(', ')} · ${n} storage${n > 1 ? 's' : ''}` : 'unused' });
    }), ...(accounts.length ? [] : [h('li', { class: 'empty' }, 'No accounts yet')]));
    for (const b of document.querySelectorAll('[data-view]')) b.classList.toggle('active', isCurrent(b.dataset.view));
  }
  const isCurrent = (kind, name) => state.current.kind === kind && (name == null || state.current.name === name);
  for (const b of document.querySelectorAll('[data-view]')) b.addEventListener('click', () => show({ kind: b.dataset.view }));
  for (const el of document.querySelectorAll('[data-icon]')) el.append(icon(el.dataset.icon));

  const hashFor = t => t.kind === 'mount' || t.kind === 'account' ? `#${t.kind}/` + enc(t.name) : '#' + t.kind;
  const fromHash = () => {
    const hsh = decodeURIComponent(location.hash.slice(1));
    if (hsh.startsWith('mount/')) return { kind: 'mount', name: hsh.slice(6) };
    if (hsh.startsWith('account/')) return { kind: 'account', name: hsh.slice(8) };
    if (['general', 'new', 'new-account', 'logs'].includes(hsh)) return { kind: hsh };
    return null;
  };

  // show renders one page: a scrolling page and, for forms, a fixed action
  // bar at the bottom so Save is always in reach.
  async function show(target, { force = false } = {}) {
    const same = hashFor(target) === hashFor(state.current) && target.account === state.current.account;
    if (!force && same && state.view) return;
    if (!force && state.dirty && !confirm('Discard your unsaved changes?')) {
      history.replaceState(null, '', hashFor(state.current));
      return;
    }
    setDirty(false);
    state.current = target; renderSidebar();
    if (location.hash !== hashFor(target)) history.replaceState(null, '', hashFor(target));
    const page = h('div', { class: 'page' });
    const bar = h('footer', { class: 'actionbar' });
    $('#main').replaceChildren(h('div', { class: 'scroller' }, page), bar);
    techOpen = null;
    if (state.view && !state.view.exists && target.kind !== 'logs') return renderMissing(page);
    const problems = configProblems();
    if (problems) page.append(problems);
    switch (target.kind) {
      case 'general': return renderGeneral(page, bar);
      case 'mount': {
        const m = state.view.mounts.find(x => x.name === target.name);
        return m ? renderMount(page, bar, m) : renderGone(page, 'storage');
      }
      case 'new': return renderMount(page, bar, null, target.account);
      case 'account': {
        const a = accountByName(target.name);
        return a ? renderAccount(page, bar, a) : renderGone(page, 'account');
      }
      case 'new-account': return renderAccount(page, bar, null);
      case 'logs': return renderLogs(page);
    }
  }

  // ---------------------------------------------------------------- widgets
  function header(tile, title, sub, aside) {
    return h('header', { class: 'page-head' },
      h('span', { class: 'tile ' + tile.tone }, icon(tile.glyph)),
      h('div', { class: 'titles' }, h('h1', {}, title), sub ? h('p', { class: 'sub' }, sub) : null),
      aside || null);
  }
  const section = (title, ...content) => h('section', { class: 'group' }, title ? h('h2', {}, title) : null, ...content);
  function row(label, control, help, opts = {}) {
    return h('div', { class: 'row', 'data-key': opts.key },
      h('label', { class: 'row-label' }, h('span', {}, label), opts.code ? h('span', { class: 'code' }, opts.code) : null),
      h('div', { class: 'row-control' }, control, help ? h('div', { class: 'help' }, help) : null));
  }
  function toggleRow(label, desc, sw, key) {
    return h('div', { class: 'row toggle-row', 'data-key': key },
      h('div', { class: 'row-label' }, h('span', {}, label), desc ? h('div', { class: 'help' }, desc) : null),
      sw);
  }
  const text = (value, attrs = {}) => h('input', { type: 'text', value: value ?? '', spellcheck: 'false', autocapitalize: 'off', autocorrect: 'off', ...attrs });
  function select(value, choices, attrs = {}) {
    const s = h('select', attrs);
    for (const c of choices) {
      const [v, label] = Array.isArray(c) ? c : [c, c];
      s.append(h('option', { value: v, selected: v === (value ?? '') }, label));
    }
    return s;
  }
  function toggle(checked) {
    const input = h('input', { type: 'checkbox', role: 'switch', checked: !!checked });
    const el = h('label', { class: 'switch' }, input, h('span', { class: 'track' }));
    el.input = input;
    return el;
  }
  function button(label, onclick, { kind = '', glyph, title } = {}) {
    return h('button', { class: ('btn ' + kind).trim(), onclick, title }, glyph ? icon(glyph) : null, label ? h('span', {}, label) : null);
  }
  // Runs an async button action with a busy state; errors become a toast.
  async function busy(btn, fn) {
    btn.disabled = true; btn.classList.add('busy');
    try { await fn(); } catch (e) { toast(e.message, 'bad'); } finally { btn.disabled = false; btn.classList.remove('busy'); }
  }
  function callout(kind, title, ...body) {
    return h('div', { class: 'callout ' + kind, role: kind === 'bad' ? 'alert' : null },
      icon(kind === 'bad' || kind === 'warn' ? 'alert' : 'info'),
      h('div', { class: 'callout-body' }, title ? h('strong', {}, title) : null, ...body));
  }
  const unsaved = () => h('span', { class: 'unsaved' }, h('span', { class: 'dot warn' }), 'Unsaved changes');
  const spacer = () => h('span', { class: 'spacer' });
  const pathChip = (p, title) => h('code', { class: 'path', title: title || p }, tilde(p));

  const plainPath = p => (p || '').replace(/^account\.(?=(mount|account)\b)/, '').replace(/^(mount|account) "[^"]*"\./, '').replace(/^(mount|account)\[\d+\]\./, '');
  function issuesBox(issues, title) {
    if (!issues?.length) return null;
    return callout('bad', title || 'That didn’t work — nothing was saved',
      h('ul', {}, issues.map(i => h('li', {}, (i.path ? plainPath(i.path) + ': ' : '') + i.msg))));
  }
  // Puts each issue next to its field as well, when the field is on the page.
  // Paths come from the validator: "path", "options.region", "account.name"
  // for the account created along with a storage, "defaults.vfs.…", …
  function markIssues(page, issues) {
    for (const i of issues) {
      if (!i.path) continue;
      const inAccount = /^account\./.test(i.path);
      const key = plainPath(i.path).replace(/^account\./, '').replace(/^(general|defaults)\./, '').replace(/^(options|secrets|vfs)\./, '').replace(/\.timeout$/, '');
      const rows = [...page.querySelectorAll('.row[data-key]')].filter(r => r.dataset.key === key && !r.hidden);
      const r = rows.find(x => !!x.closest('[data-scope=account]') === inAccount) || (rows.length === 1 ? rows[0] : null);
      if (!r) continue;
      r.classList.add('invalid');
      const d = r.closest('details'); if (d) d.open = true;
      (r.querySelector('.row-control') || r).append(h('div', { class: 'field-error' }, i.msg));
    }
  }
  function clearIssues(page, host) {
    host.replaceChildren();
    for (const r of page.querySelectorAll('.row.invalid')) r.classList.remove('invalid');
    for (const e of page.querySelectorAll('.field-error')) e.remove();
  }
  function showIssues(page, host, e) {
    if (e.status === 412) {
      host.replaceChildren(callout('bad', 'The config file changed outside this window — nothing was saved',
        'Load the file as it is now, then make your change again.',
        h('div', { class: 'callout-actions' }, button('Load the current file', async () => { setDirty(false); await refresh(); await show(state.current, { force: true }); }, { glyph: 'refresh' }))));
      host.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
      return;
    }
    const issues = e.issues?.length ? e.issues : [{ msg: e.message }];
    host.replaceChildren(issuesBox(issues));
    markIssues(page, issues);
    host.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
  }
  async function save(btn, page, host, fn, after) {
    clearIssues(page, host);
    btn.disabled = true; btn.classList.add('busy');
    try {
      await fn(); setDirty(false); toast('Saved'); await refresh(); await after();
    } catch (e) {
      showIssues(page, host, e);
    } finally { btn.disabled = false; btn.classList.remove('busy'); }
  }
  // Problems in the settings file itself, e.g. after a hand edit.
  function configProblems() {
    const errs = (state.view?.issues || []).filter(i => !i.warning);
    if (!state.view?.exists || !errs.length) return null;
    return callout('bad', 'The settings file has a problem',
      h('ul', {}, errs.map(i => h('li', {}, i.line ? `Line ${i.line}: ` : '', i.path ? plainPath(i.path) + ': ' : '', i.msg))),
      h('div', { class: 'callout-actions' }, button('Open in editor', () => api('POST', '/api/open?what=config'), { glyph: 'file' })));
  }

  // matchProvider applies rclone's provider filter ("AWS,Ceph" or "!AWS").
  function matchProvider(filter, provider) {
    if (!filter) return true;
    const neg = filter.startsWith('!');
    const found = (neg ? filter.slice(1) : filter).split(',').map(s => s.trim()).includes(provider || '');
    return neg ? !found : found;
  }
  function splitCommand(s) {
    const out = []; let cur = '', q = null, has = false;
    for (const ch of s) {
      if (q) { if (ch === q) q = null; else cur += ch; }
      else if (ch === '"' || ch === "'") { q = ch; has = true; }
      else if (/\s/.test(ch)) { if (cur || has) { out.push(cur); cur = ''; has = false; } }
      else cur += ch;
    }
    if (cur || has) out.push(cur);
    return out;
  }
  const joinCommand = arr => (arr || []).map(a => /[\s"']/.test(a) ? JSON.stringify(a) : a).join(' ');

  // ---------------------------------------------------------------- first run
  function renderMissing(page) {
    page.classList.add('narrow-page');
    page.append(h('div', { class: 'hero' },
      h('span', { class: 'app-icon large' }, $('.brand .app-icon svg').cloneNode(true)),
      h('h1', {}, 'Welcome to Lagra'),
      h('p', {}, 'Lagra shows cloud storage as folders in Finder. Let’s create your settings file and add the first storage.'),
      h('div', { class: 'hero-file' }, icon('file'), h('div', {}, h('div', {}, 'Your settings will live at'), pathChip(state.view.path))),
      h('p', { class: 'fine' }, 'Prefer your dotfiles? Symlink that path into your repo first; Lagra writes through it.'),
      h('button', { class: 'btn primary large', onclick: async ev => busy(ev.currentTarget, async () => {
        await api('POST', '/api/config/create'); await refresh(); show({ kind: 'new' }, { force: true });
      }) }, 'Get started')));
  }
  function renderGone(page, what) {
    page.append(h('div', { class: 'hero' },
      h('h1', {}, `This ${what} is gone`),
      h('p', {}, 'It was renamed or removed, perhaps in the settings file.')));
  }

  // ---------------------------------------------------------------- settings
  function renderGeneral(page, bar) {
    const g = state.view.general || {}, d = state.view.defaults || {}, dv = d.vfs || {};
    const launch = toggle(g.launch_at_login === true);
    const level = select(g.log_level || '', [['', 'Normal'], ['debug', 'Detailed (for troubleshooting)'], ['warn', 'Problems only']]);
    const cacheDir = text(g.cache_dir, { placeholder: '~/Library/Caches/lagra' });
    const mountRoot = text(d.mount_root, { placeholder: '~/Volumes' });
    const hideApple = toggle(d.hide_apple_files !== false);
    const cacheSize = text(dv.vfs_cache_max_size, { placeholder: '10G' });
    const writeBack = text(dv.vfs_write_back, { placeholder: '5s' });
    const listRefresh = text(dv.dir_cache_time, { placeholder: '1m' });
    const probeM = text(g.mount_probe_interval, { placeholder: '15s' });
    const probeB = text(g.backend_probe_interval, { placeholder: '60s' });
    const stuck = text(g.upload_stuck_after, { placeholder: '10m' });
    const issueHost = h('div', { class: 'issue-host' });
    page.append(
      header({ tone: 'gray', glyph: 'sliders' }, 'Settings', 'These apply to Lagra as a whole. Each storage has its own page.'),
      issueHost,
      section(null, h('div', { class: 'card' },
        toggleRow('Launch at login', 'Start Lagra when you log in, so your storages are there right away.', launch, 'launch_at_login'),
        toggleRow('Hide Finder helper files', 'Keep Finder’s hidden helper files (.DS_Store, ._*) out of your storages. Recommended.', hideApple, 'hide_apple_files'),
        row('New storages appear in', h('div', { class: 'inline' }, mountRoot, pickButton(mountRoot)), 'Each storage becomes a folder here, named after the storage. You can pick a different folder per storage.', { key: 'mount_root' }))),
      section('Permissions', permissionsCard()),
      section('Settings file', h('div', { class: 'card' }, h('div', { class: 'row file-row' },
        h('div', { class: 'file-line' }, icon('file'), pathChip(state.view.real_path || state.view.path)),
        button('Open in editor', () => api('POST', '/api/open?what=config'))))),
      disclosure('Advanced', 'Uploads, cache, health checks and logging',
        section('Uploads & cache', h('div', { class: 'card' },
          row('Upload delay', writeBack, 'How long Lagra waits after you save a file before uploading it. Short = snappier, longer = fewer uploads while you keep editing.', { key: 'vfs_write_back' }),
          row('Refresh listing every', listRefresh, 'How often Finder picks up changes made elsewhere. Use “Sync now” on a storage to refresh immediately.', { key: 'dir_cache_time' }),
          row('Cache size', cacheSize, 'Space on this Mac for recently used files, per storage.', { key: 'vfs_cache_max_size' }),
          row('Cache folder', cacheDir, 'Changing this needs a restart of Lagra.', { key: 'cache_dir' }),
          row('Notify about stuck uploads after', stuck, null, { key: 'upload_stuck_after' }))),
        section('Health checks', h('div', { class: 'card' },
          row('Check volume every', probeM, 'A local check that the folder still answers.', { key: 'mount_probe_interval' }),
          row('Check storage every', probeB, 'One small request to the storage to tell “connected” from “unreachable”.', { key: 'backend_probe_interval' }))),
        section('Logging', h('div', { class: 'card' }, row('Log detail', level, null, { key: 'log_level' })))));
    bar.append(spacer(), unsaved(), h('button', { class: 'btn primary', onclick: async ev => {
      await save(ev.currentTarget, page, issueHost, async () => {
        await api('PUT', '/api/general', {
          launch_at_login: String(launch.input.checked), log_level: level.value, cache_dir: cacheDir.value.trim(),
          mount_probe_interval: probeM.value.trim(), backend_probe_interval: probeB.value.trim(), upload_stuck_after: stuck.value.trim(),
        });
        await api('PUT', '/api/defaults', {
          fields: { mount_root: mountRoot.value.trim(), hide_apple_files: String(hideApple.input.checked) },
          vfs: { vfs_cache_max_size: cacheSize.value.trim(), vfs_write_back: writeBack.value.trim(), dir_cache_time: listRefresh.value.trim() },
        });
      }, () => show({ kind: 'general' }, { force: true }));
    } }, 'Save'));
  }

  function disclosure(title, desc, ...content) {
    return h('details', { class: 'disclosure' },
      h('summary', {}, icon('chevron', 'chev'), h('span', { class: 'disc-title' }, title), desc ? h('span', { class: 'disc-desc' }, desc) : null),
      h('div', { class: 'disc-body' }, ...content));
  }

  // macOS binds privacy grants (Files and Folders, Network Volumes) to the
  // app's signing identity. Explain the state and open the right pane.
  function permissionsCard() {
    const sg = state.meta.signing || {};
    const open = pane => button(pane === 'network' ? 'Network Volumes…' : 'Files and Folders…', () => api('POST', '/api/open-privacy?pane=' + pane));
    return h('div', { class: 'card pad' },
      h('p', { class: 'para' }, 'When Lagra first touches a volume, macOS asks whether Lagra may access it. Allow it once; the grant lives in System Settings → Privacy & Security and can be reviewed there.'),
      sg.stable
        ? h('div', { class: 'note ok' }, icon('shield'), h('span', {}, `This copy of Lagra is signed as “${sg.identity}”, so the permission survives updates.`))
        : h('div', { class: 'note warn' }, icon('alert'), h('span', {}, 'This copy of Lagra is signed ad hoc: macOS treats every rebuild as a new app and asks again. Build with ', h('kbd', {}, 'make install'), ' to create a stable local signing identity (asks for your password once).')),
      h('div', { class: 'button-row' }, open('files'), open('network')));
  }

  function pickButton(input) {
    return button('Choose…', async ev => busy(ev.currentTarget, async () => {
      const r = await api('POST', '/api/pick-dir?start=' + enc(input.value || state.meta.home));
      if (!r.available) { toast('Type the folder instead: no folder chooser here.', 'bad'); return; }
      if (r.ok && r.path) { input.value = r.path; input.dispatchEvent(new Event('input', { bubbles: true })); }
    }), { kind: 'fixed' });
  }

  // ---------------------------------------------------------------- type chooser
  // Equal tiles for the common kinds; the last one is a menu with the rest.
  let chooserSeq = 0;
  // The kinds beyond the featured ones, sorted by name.
  const otherTypes = () => state.meta.backends.filter(b => !FEATURED.includes(b.name)).map(b => [b.name, typeName(b.name)])
    .sort((a, b) => a[1].localeCompare(b[1]));
  // An account belongs to no kind of storage: these are the kinds its
  // storages use.
  const accountTypes = name => [...new Set((state.view?.mounts || []).filter(m => m.account === name).map(m => m.type).filter(Boolean))];
  // For an account no storage uses yet: the first featured kind that knows
  // all of its options.
  async function guessType(a) {
    const keys = [...Object.keys(a?.options || {}), ...Object.keys(a?.secrets || {})];
    for (const t of FEATURED) {
      const names = new Set((await options(t)).map(o => o.Name));
      if (keys.every(k => names.has(k))) return t;
    }
    return FEATURED[0];
  }
  function typeChooser(initial, onChange, title = 'Kind of storage', help = null) {
    const group = 'type-' + (++chooserSeq);
    const tiles = h('div', { class: 'types', role: 'radiogroup', 'aria-label': 'Kind of storage' });
    const tile = (type, glyph, name, control) => h('label', { class: 'type', 'data-type': type }, control,
      h('span', { class: 'type-icon' }, icon(glyph)), name, h('span', { class: 'type-check' }, icon('check')));
    for (const t of FEATURED) {
      tiles.append(tile(t, TYPES[t].icon, h('span', { class: 't' }, TYPES[t].name),
        h('input', { type: 'radio', name: group, value: t, onchange: () => set(t) })));
    }
    const menu = select('', [['', 'Other…'], ...otherTypes()], { 'aria-label': 'Other kind of storage' });
    menu.addEventListener('change', () => { if (menu.value) set(menu.value); });
    const otherName = h('span', { class: 't' });
    const other = tile('', 'more', otherName, menu);
    tiles.append(other);
    function set(t, quiet) {
      const featured = FEATURED.includes(t);
      for (const c of tiles.children) c.classList.toggle('selected', featured ? c.dataset.type === t : c === other);
      for (const r of tiles.querySelectorAll('input')) r.checked = r.value === t;
      menu.value = featured ? '' : t;
      otherName.textContent = featured ? 'Other…' : typeName(t);
      other.title = featured ? '' : typeName(t);
      if (!quiet) onChange(t);
    }
    set(initial, true);
    return section(title, help ? h('p', { class: 'section-help' }, help) : null, tiles);
  }

  // A free account name built from the provider, server or type: "hetzner",
  // "hetzner-2", …
  function suggestAccountName(base, own) {
    let n = String(base || 'account').toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^[^a-z0-9]+|-+$/g, '') || 'account';
    const taken = new Set((state.view?.accounts || []).map(a => a.name).filter(x => x !== own));
    if (!taken.has(n)) return n;
    for (let i = 2; ; i++) if (!taken.has(`${n}-${i}`)) return `${n}-${i}`;
  }

  // ---------------------------------------------------------------- account
  async function renderAccount(page, bar, a) {
    const isNew = !a;
    // The fields shown follow the kinds of the account's storages; without
    // any, the chooser picks them.
    const used = a ? accountTypes(a.name) : [];
    let chosen = used.length ? null : await guessType(a);
    const issueHost = h('div', { class: 'issue-host' });
    if (isNew) page.append(header({ tone: 'indigo', glyph: 'person' }, 'Add account', 'An account is your login: user name, password or keys. Storages that use it come afterwards — as many as you like, on any server, over SMB, WebDAV, SFTP and the rest alike.'));
    else page.append(header({ tone: 'indigo', glyph: used.length ? typeIcon(used[0]) : 'person' }, a.name, used.length ? used.map(typeName).join(' · ') : 'Not used by any storage yet'), section('Storages using this account', usedByCard(a)));
    page.append(issueHost);
    const formHost = h('div');
    let form;
    const build = async () => { form = await accountForm(a, used.length ? used : [chosen]); formHost.replaceChildren(form.el); };
    if (!used.length) page.append(typeChooser(chosen, t => { chosen = t; build(); }, 'Used with', 'This only decides which fields show here. Each storage picks its own kind, and storages of different kinds can share the account.'));
    page.append(formHost);

    const primary = h('button', { class: 'btn primary', onclick: async ev => {
      const body = form.values();
      await save(ev.currentTarget, page, issueHost, () => isNew ? api('POST', '/api/accounts', body) : api('PUT', `/api/accounts/${enc(a.name)}`, body),
        () => show({ kind: 'account', name: body.name }, { force: true }));
    } }, isNew ? 'Add account' : 'Save');
    if (isNew) {
      bar.append(h('span', { class: 'hint' }, icon('lock'), 'Credentials go into your macOS keychain, never into the settings file.'), spacer(), primary);
    } else {
      const users = a.used_by || [];
      bar.append(button('Remove…', async () => {
        if (users.length) { toast(`Still used by ${users.join(', ')}. Remove those storages first.`, 'bad'); return; }
        if (!confirm(`Remove the account “${a.name}”?`)) return;
        const hasKeychain = Object.values(a.secrets || {}).some(s => s.source === 'keychain');
        const delSecrets = hasKeychain && confirm('Also forget its saved credentials?');
        try {
          await api('DELETE', `/api/accounts/${enc(a.name)}?secrets=${delSecrets ? 1 : 0}`); toast('Removed'); setDirty(false); await refresh();
          show(state.view.accounts.length ? { kind: 'account', name: state.view.accounts[0].name } : { kind: 'new-account' });
        } catch (e) { showIssues(page, issueHost, e); }
      }, { kind: 'danger', glyph: 'trash' }), spacer(), unsaved(), primary);
    }
    await build();
  }

  // The storages that use an account, with their state.
  function usedByCard(a) {
    const list = h('div', { class: 'card list' });
    for (const name of a.used_by || []) {
      const st = mountStatus(name), w = statusWords(st);
      list.append(h('button', { class: 'list-row', onclick: () => show({ kind: 'mount', name }) },
        h('span', { class: 'nav-icon' }, icon('drive'), h('span', { class: 'badge ' + w.kind })),
        h('span', { class: 'list-name' }, name),
        h('span', { class: 'pill ' + w.kind }, w.title),
        icon('chevron', 'chev')));
    }
    if (!(a.used_by || []).length) list.append(h('div', { class: 'list-empty' }, 'No storage uses this account yet.'));
    list.append(h('button', { class: 'list-row add-row', onclick: () => show({ kind: 'new', account: a.name }) },
      h('span', { class: 'nav-icon' }, icon('plus')), h('span', { class: 'list-name' }, 'Add storage with this account')));
    return list;
  }

  // ---------------------------------------------------------------- storage
  const NEW_ACCOUNT = '\u0000new';
  async function renderMount(page, bar, m, preferAccount) {
    const isNew = !m;
    const accounts = state.view.accounts || [];
    if (isNew) {
      const pick = accounts.some(a => a.name === preferAccount) ? preferAccount : accounts.length ? accounts[accounts.length - 1].name : NEW_ACCOUNT;
      m = { name: '', account: pick, type: (pick !== NEW_ACCOUNT && accountTypes(pick)[0]) || FEATURED[0], path: '', mount_point: '', vfs: {} };
    }
    const st = isNew ? null : mountStatus(m.name);
    const issueHost = h('div', { class: 'issue-host' });

    // --- header with status and actions
    if (!isNew) {
      page.append(header({ tone: 'blue', glyph: 'drive' }, m.name,
        [m.type ? typeName(m.type) + ' · ' : '', h('button', { class: 'link', onclick: () => show({ kind: 'account', name: m.account }) }, m.account), m.options?.host ? ` · ${m.options.host}` : '', m.path ? ` · ${m.path}` : '']),
      statusCard(m, st));
    } else {
      page.append(header({ tone: 'blue', glyph: 'drive' }, 'Add storage', accounts.length ? 'Pick the account, then say where: the bucket, or the server and its share or folder. It shows up in Finder like a drive.' : 'First tell Lagra how to log in, then where: the bucket, or the server and its share or folder. It shows up in Finder like a drive.'));
    }
    page.append(issueHost);

    // --- storage basics: kind, name, account, bucket/folder
    let type = m.type;
    const kindSel = isNew ? null : select(type, [...FEATURED.map(t => [t, typeName(t)]), ...otherTypes()]);
    const name = text(m.name, { placeholder: 'my-storage' });
    const acctSel = select(m.account, [...accounts.map(a => [a.name, a.options?.user ? `${a.name} (${a.options.user})` : a.name]), [NEW_ACCOUNT, 'New account…']]);
    const path = text(m.path);
    const pathLabel = h('span', {}, 'Bucket');
    const pathHelp = h('div', { class: 'help' });
    const editAccount = button('Edit…', () => show({ kind: 'account', name: acctSel.value }), { kind: 'fixed', title: 'Edit this account' });
    const pathRow = h('div', { class: 'row', 'data-key': 'path' }, h('label', { class: 'row-label' }, pathLabel), h('div', { class: 'row-control' }, path, pathHelp));
    const basics = h('div', { class: 'card' },
      row('Name', name, isNew ? 'Short, lowercase, no spaces: this becomes the folder name in Finder.' : null, { key: 'name' }),
      kindSel ? row('Kind', kindSel, null, { key: 'type' }) : null,
      accounts.length ? row('Account', h('div', { class: 'inline' }, acctSel, editAccount), 'The login this storage uses. Several storages can share one, on other servers and over other kinds alike.', { key: 'account' }) : null,
      pathRow);

    // --- a new account, inline
    let newForm = null;
    const newHost = h('div', { 'data-scope': 'account' });
    const newFormHost = h('div');
    const currentType = () => type;
    const updatePath = () => {
      const t = TYPES[currentType()];
      pathLabel.textContent = t?.pathLabel || 'Folder';
      pathHelp.textContent = t?.pathHelp || 'Folder on the storage, or empty for all of it.';
      path.placeholder = currentType() === 's3' ? 'my-bucket' : '';
      editAccount.hidden = acctSel.value === NEW_ACCOUNT;
    };

    // --- where: the storage's own options, the server first
    let opts = null, optsSeq = 0;
    const optsHost = h('div');
    const buildOpts = async () => {
      const seq = ++optsSeq, type = currentType();
      const own = opts ? opts.values() : (m.options || {});
      const built = type ? await storageOptions(type, acctSel.value === NEW_ACCOUNT ? null : accountByName(acctSel.value), own) : null;
      if (seq !== optsSeq) return; // the account changed meanwhile
      for (const r of opts?.rows || []) r.remove();
      opts = built;
      pathRow.before(...(opts?.rows || []));
      optsHost.replaceChildren(opts ? opts.el : '');
      if (adv.open) opts?.build();
      const split = TYPES[type]?.splitAddress, host = opts?.controls.host;
      if (split && host) host.el.addEventListener('change', () => splitInto(split(host.get())));
    };
    // A pasted share address: the server here, the share below and, for a
    // new account, the login.
    const splitInto = p => {
      if (!p) return;
      opts.controls.host.set(p.host);
      const notes = [`server ${p.host}`];
      if (p.path && [p.path, ''].includes(path.value.trim())) { path.value = p.path; notes.push(`share ${p.path}`); }
      for (const k of newForm?.fill({ user: p.user, domain: p.domain }) || []) notes.push(`${k} ${p[k]}`);
      toast(`Address split: ${notes.join(', ')}.`, 'info', 4000);
    };

    const buildNewForm = async () => { newForm = await accountForm(null, [type]); newFormHost.replaceChildren(newForm.el); };
    const buildNew = async () => {
      newHost.replaceChildren(); newForm = null;
      updatePath();
      if (acctSel.value === NEW_ACCOUNT) {
        if (accounts.length) newHost.append(h('div', { class: 'subhead' }, icon('person'), 'New account'));
        newHost.append(newFormHost);
        await buildNewForm();
      }
      await buildOpts();
    };
    acctSel.addEventListener('change', buildNew);
    // The kind decides the fields of the storage and of a new account.
    const setType = t => { type = t; buildNew(); };
    kindSel?.addEventListener('change', () => setType(kindSel.value));

    // The kind first; without any account the account comes next, then the
    // bucket or share.
    if (isNew) page.append(typeChooser(type, setType));
    if (accounts.length) page.append(section('Storage', basics), newHost);
    else page.append(newHost, section('Storage', basics));

    // --- on this Mac
    const defaultPoint = n => `${state.view.defaults?.mount_root || '~/Volumes'}/${n || '<name>'}`;
    const mountPoint = text(m.mount_point, { placeholder: defaultPoint(m.name) });
    name.addEventListener('input', () => { mountPoint.placeholder = defaultPoint(name.value.trim()); });
    const enabled = toggle(m.enabled !== false);
    const readOnly = toggle(m.read_only === true);
    page.append(section('On this Mac', h('div', { class: 'card' },
      row('Show in Finder at', h('div', { class: 'inline' }, mountPoint, pickButton(mountPoint)), 'Leave empty for the default folder.', { key: 'mount_point' }),
      toggleRow('Connect automatically', 'Mount this storage whenever Lagra runs.', enabled, 'enabled'),
      toggleRow('Read only', 'Protects the storage: nothing can be changed or deleted from Finder.', readOnly, 'read_only'))));

    // --- advanced: cache and uploads
    const vfs = m.vfs || {};
    const dv = state.view.defaults?.vfs || {};
    const cacheSize = text(vfs.vfs_cache_max_size, { placeholder: dv.vfs_cache_max_size || '10G' });
    const writeBack = text(vfs.vfs_write_back, { placeholder: dv.vfs_write_back || '5s' });
    const listRefresh = text(vfs.dir_cache_time, { placeholder: dv.dir_cache_time || '1m' });
    const cacheMode = select(vfs.vfs_cache_mode || '', [['', 'Default (full)'], ['full', 'Full'], ['writes', 'Writes only'], ['minimal', 'Minimal'], ['off', 'Off (read-only over NFS)']]);
    const hideApple = select(m.hide_apple_files == null ? '' : String(m.hide_apple_files), [['', 'Default (' + (state.view.defaults?.hide_apple_files === false ? 'off' : 'on') + ')'], ['true', 'On'], ['false', 'Off']]);
    const otherVFS = Object.entries(vfs).filter(([k]) => !['vfs_cache_max_size', 'vfs_write_back', 'dir_cache_time', 'vfs_cache_mode'].includes(k));
    const otherVFSArea = h('textarea', { rows: Math.max(2, otherVFS.length + 1), class: 'mono', spellcheck: 'false', placeholder: 'one per line: vfs_read_ahead = 128M' }, otherVFS.map(([k, v]) => `${k} = ${v}`).join('\n'));
    const adv = disclosure('Advanced', 'Uploads, cache and storage options',
      section('Uploads and cache', h('div', { class: 'card' },
        row('Upload delay', writeBack, 'Wait this long after saving before uploading.', { key: 'vfs_write_back' }),
        row('Refresh listing every', listRefresh, 'How often changes made elsewhere appear.', { key: 'dir_cache_time' }),
        row('Cache size', cacheSize, null, { key: 'vfs_cache_max_size' }),
        row('Cache mode', cacheMode, 'Writing needs a cache. Leave on Full unless you know why.', { key: 'vfs_cache_mode' }),
        row('Hide Finder helper files', hideApple, null, { key: 'hide_apple_files' }),
        row('Other cache options', otherVFSArea, 'rclone VFS options, one per line.'))),
      optsHost);
    adv.addEventListener('toggle', () => { if (adv.open) opts?.build(); });
    page.append(adv);

    const values = () => {
      const vfsOut = { vfs_cache_max_size: cacheSize.value.trim(), vfs_write_back: writeBack.value.trim(), dir_cache_time: listRefresh.value.trim(), vfs_cache_mode: cacheMode.value };
      for (const line of otherVFSArea.value.split('\n')) {
        const mm = line.match(/^\s*([a-z0-9_]+)\s*=\s*(.+?)\s*$/i); if (mm) vfsOut[mm[1]] = mm[2].replace(/^"|"$/g, '');
      }
      const body = {
        name: name.value.trim(), account: acctSel.value === NEW_ACCOUNT ? '' : acctSel.value, type, path: path.value.trim(),
        options: opts ? opts.values() : (m.options || {}), public: opts ? opts.public() : [], mount_point: mountPoint.value.trim(),
        enabled: enabled.input.checked ? null : false, read_only: readOnly.input.checked ? true : null,
        hide_apple_files: hideApple.value === '' ? null : hideApple.value === 'true', vfs: vfsOut,
      };
      if (newForm) body.new_account = newForm.values();
      return body;
    };

    // --- action bar
    const primary = h('button', { class: 'btn primary', onclick: async ev => {
      const body = values();
      await save(ev.currentTarget, page, issueHost, () => isNew ? api('POST', '/api/mounts', body) : api('PUT', `/api/mounts/${enc(m.name)}`, body),
        () => show({ kind: 'mount', name: body.name }, { force: true }));
    } }, isNew ? 'Add storage' : 'Save');
    if (!isNew) {
      bar.append(button('Remove…', async () => {
        if (!confirm(`Remove “${m.name}” from Lagra?\n\nNothing in the storage itself is deleted, and the account “${m.account}” stays.`)) return;
        try {
          await api('DELETE', `/api/mounts/${enc(m.name)}`); toast('Removed'); setDirty(false); await refresh();
          show(state.view.mounts.length ? { kind: 'mount', name: state.view.mounts[0].name } : { kind: 'new' });
        } catch (e) { showIssues(page, issueHost, e); }
      }, { kind: 'danger', glyph: 'trash' }), spacer(), unsaved(), primary);
    } else {
      if (!accounts.length) bar.append(h('span', { class: 'hint' }, icon('lock'), 'Credentials go into your macOS keychain, never into the settings file.'));
      bar.append(spacer(), primary);
    }
    await buildNew();
  }

  // The status card at the top of a storage page. Re-rendered on its own by
  // the status ticker so unsaved edits below are never lost.
  let techOpen = null;
  function statusCard(m, st) {
    const w = statusWords(st);
    const later = () => setTimeout(() => refresh().then(updateStatusCard), 900);
    const act = (label, fn, opts) => button(label, ev => busy(ev.currentTarget, fn), opts);
    const actions = [];
    if (st && (st.state === 'connected' || st.state === 'offline')) {
      actions.push(act('Open in Finder', () => api('POST', `/api/open?what=mount&name=${enc(m.name)}`), { glyph: 'folder' }));
      actions.push(act('Sync now', async () => {
        try {
          const r = await api('POST', `/api/mounts/${enc(m.name)}/sync`);
          toast(r.expedited ? `${r.expedited} upload${r.expedited > 1 ? 's' : ''} sent` : 'Up to date');
        } finally { later(); }
      }, { glyph: 'refresh' }));
      actions.push(spacer());
      actions.push(act('Disconnect', async () => { await api('POST', `/api/mounts/${enc(m.name)}/disconnect`); toast('Disconnecting', 'info'); later(); }, { glyph: 'eject' }));
    } else if (st && st.state === 'connecting') {
      actions.push(act('Cancel', async () => { await api('POST', `/api/mounts/${enc(m.name)}/disconnect`); later(); }));
    } else if (st) {
      actions.push(act('Connect', async () => {
        try { await api('POST', `/api/mounts/${enc(m.name)}/connect`); toast('Connecting', 'info'); } finally { later(); }
      }, { kind: 'primary', glyph: 'power' }));
      if (/in the keychain|keychain item not found/i.test(st.last_error || '')) actions.push(button('Enter credentials', () => show({ kind: 'account', name: m.account }), { glyph: 'lock' }));
      else if (st.state === 'failed' || st.state === 'broken') actions.push(button('Edit account', () => show({ kind: 'account', name: m.account }), { glyph: 'pencil' }));
    }
    const tech = w.tech ? h('details', { class: 'tech', open: techOpen ?? !!w.techOpen, ontoggle: ev => { techOpen = ev.currentTarget.open; } },
      h('summary', {}, icon('chevron', 'chev'), 'Details'), h('pre', {}, w.tech)) : null;
    return h('section', { class: 'card status ' + w.kind, 'data-mount': m.name, 'data-state': st?.state || '' },
      h('div', { class: 'status-main' },
        h('span', { class: 'status-icon' }, icon(w.glyph)),
        h('div', { class: 'status-text' },
          h('div', { class: 'status-title' }, w.title),
          w.line ? h('div', { class: 'status-line' }, w.line) : null,
          w.hint ? h('div', { class: 'status-hint' }, w.hint) : null,
          st?.pending ? h('div', { class: 'status-hint' }, pendingWords(st.pending)) : null,
          w.location ? h('div', { class: 'status-location' }, icon('folder'), pathChip(w.location)) : null,
          tech)),
      actions.length ? h('div', { class: 'status-actions' }, actions) : null);
  }
  function updateStatusCard() {
    const card = $('#main .card.status');
    if (!card) return;
    const m = state.view.mounts.find(x => x.name === card.dataset.mount);
    if (m) card.replaceWith(statusCard(m, mountStatus(m.name)));
  }

  // Builds the form for an account: name, essentials, credentials and,
  // under Advanced, credential sources and every rclone option. An account
  // belongs to no kind of storage, so it asks for what the given kinds
  // need (those of its storages) and keeps every other value as it is.
  async function accountForm(a, types) {
    const sets = await Promise.all(types.map(t => options(t)));
    const byName = {};
    for (const set of sets) for (const o of set) byName[o.Name] ??= o;
    const infos = Object.values(byName);
    const values = { ...(a?.options || {}) };
    const secrets = { ...(a?.secrets || {}) };
    const wasPublic = new Set(a?.public || []);
    const s3 = types.includes('s3');

    // essentials: curated or generic (standard options) per kind, without
    // what each storage says for itself (the server)
    const where = new Set(types.flatMap(t => (TYPES[t]?.where || []).map(f => f.opt)));
    const curated = (t, i, key, generic) => TYPES[t] ? TYPES[t][key] : sets[i].filter(generic).map(o => ({ opt: o.Name, label: prettify(o.Name), generic: true }));
    const unique = list => list.filter((f, i) => byName[f.opt] && !where.has(f.opt) && list.findIndex(g => g.opt === f.opt) === i);
    const essentials = unique(types.flatMap((t, i) => curated(t, i, 'fields', o => !o.Advanced && !o.Sensitive)));
    const creds = unique(types.flatMap((t, i) => curated(t, i, 'creds', o => !o.Advanced && o.Sensitive)));
    const essentialNames = new Set([...essentials.map(f => f.opt), ...creds.map(f => f.opt)]);

    const controls = {}; // opt -> {get(), row, field}
    const el = h('div', { 'data-scope': 'account' });

    // --- name, suggested from the provider, the domain or the user
    const providerOf = () => (controls.provider ? controls.provider.get() : (values.provider || ''));
    const suggestion = () => suggestAccountName(providerOf() || controls.domain?.get() || controls.user?.get() || types[0], a?.name);
    const name = text(a?.name || '');
    const essentialsCard = h('div', { class: 'card' },
      row('Account name', name, a ? 'Renaming keeps the credentials and updates every storage that uses it.' : 'Short, lowercase, no spaces. Leave empty for the suggestion.', { key: 'name' }));
    const updateSuggestion = () => { name.placeholder = suggestion(); };

    const visibility = () => {
      for (const f of essentials) {
        const c = controls[f.opt]; if (!c) continue;
        const info = byName[f.opt];
        c.row.hidden = !((!f.showIf || f.showIf({ provider: providerOf() })) && matchProvider(info.Provider, providerOf()));
      }
    };
    for (const f of essentials) {
      const info = byName[f.opt];
      const ctl = optionControl(info, values[f.opt] ?? '', f, providerOf);
      const r = row(f.label, ctl.el, f.help || (f.generic ? info.Help : null), { code: f.generic ? f.opt : null, key: f.opt });
      controls[f.opt] = { get: ctl.get, row: r, field: f, info, ctl };
      essentialsCard.append(r);
      if (f.opt === 'provider') ctl.el.addEventListener('change', () => { visibility(); refreshChoices(); updateSuggestion(); });
      if (f.opt === 'domain' || f.opt === 'user') ctl.el.addEventListener('input', updateSuggestion);
    }
    const refreshChoices = () => { for (const f of essentials) controls[f.opt]?.ctl.refresh?.(providerOf()); };
    visibility();
    updateSuggestion();
    el.append(section('Account', essentialsCard));

    // --- credentials
    const credCard = h('div', { class: 'card' });
    const credControls = {};
    for (const f of creds) {
      const sec = secrets[f.opt];
      const saved = sec?.source === 'keychain' && sec.stored;
      const input = h('input', { type: 'password', autocomplete: 'new-password', placeholder: saved ? '••••••••••••' : 'Not saved yet' });
      let mode = sec?.source === 'command' ? 'command' : 'keychain';
      const cmd = text(joinCommand(sec?.command), { placeholder: 'op read "op://Private/Hetzner/secret"', class: 'mono' });
      const cmdTimeout = text(sec?.timeout || '', { placeholder: '10s', class: 'narrow' });
      const body = h('div');
      const render = () => {
        if (mode === 'command') {
          body.replaceChildren(h('div', { class: 'inline' }, cmd, cmdTimeout),
            h('div', { class: 'help' }, 'The command’s output is the secret. ', h('button', { class: 'link', onclick: () => { mode = 'keychain'; setDirty(true); render(); } }, 'Use the keychain instead')));
        } else {
          body.replaceChildren(input);
          if (sec?.source === 'keychain') body.append(sec.stored
            ? h('div', { class: 'stored' }, icon('check'), 'Saved in your keychain — type to replace')
            : h('div', { class: 'notstored' }, h('span', { class: 'dot warn' }), 'Not saved yet'));
        }
      };
      render();
      credControls[f.opt] = { mode: () => mode, value: () => input.value, command: () => splitCommand(cmd.value), timeout: () => cmdTimeout.value.trim(), setMode: v => { mode = v; render(); } };
      credCard.append(row(f.label, body, null, { key: f.opt }));
    }
    if (creds.length) el.append(section('Login', credCard));

    // --- advanced: where credentials come from, every rclone option
    const advOpts = infos.filter(o => !essentialNames.has(o.Name));
    const advControls = {};
    const advList = h('div', { class: 'card' });
    const filter = text('', { placeholder: 'Filter options', type: 'search' });
    const buildAdv = () => {
      advList.replaceChildren();
      const q = filter.value.trim().toLowerCase();
      for (const o of advOpts) {
        if (q && !(o.Name.includes(q) || (o.Help || '').toLowerCase().includes(q))) continue;
        if (!matchProvider(o.Provider, providerOf())) continue;
        if (!advControls[o.Name]) advControls[o.Name] = advancedControl(o, values[o.Name] ?? '', secrets[o.Name], wasPublic.has(o.Name), providerOf);
        advList.append(row(prettify(o.Name), advControls[o.Name].el, o.Help, { code: o.Name, key: o.Name }));
      }
      if (!advList.children.length) advList.append(h('div', { class: 'list-empty' }, 'No options match.'));
    };
    filter.addEventListener('input', buildAdv);
    // Options set in the file stay set, even while the list stays closed.
    for (const o of advOpts) {
      if ((values[o.Name] ?? '') !== '' || secrets[o.Name]) advControls[o.Name] = advancedControl(o, values[o.Name] ?? '', secrets[o.Name], wasPublic.has(o.Name), providerOf);
    }
    const credModeRows = creds.map(f => {
      const sel = select(credControls[f.opt].mode(), [['keychain', 'Saved in the keychain'], ['command', 'From a command (1Password CLI, pass, …)']]);
      sel.addEventListener('change', () => credControls[f.opt].setMode(sel.value));
      return row(f.label, sel);
    });
    const adv = disclosure('Advanced account options', 'Credential sources and every option rclone offers',
      credModeRows.length ? section('Where credentials come from', h('div', { class: 'card' }, credModeRows)) : null,
      section(`All options for ${types.map(typeName).join(' and ')}`, h('div', { class: 'search', 'data-transient': '' }, icon('search'), filter), advList));
    adv.addEventListener('toggle', () => { if (adv.open && !advList.children.length) buildAdv(); });
    el.append(adv);

    return {
      el,
      values: () => {
        const opts = {}, secs = {}, pub = [];
        const prov = s3 ? providerOf() : '';
        for (const f of essentials) {
          const c = controls[f.opt]; if (!c) continue;
          if (c.row.hidden) continue;
          const v = c.get(); if (v === '') continue;
          opts[f.opt] = v;
          if (c.info.Sensitive) pub.push(f.opt);
        }
        for (const f of creds) {
          const c = credControls[f.opt];
          if (c.mode() === 'command') { const cmdv = c.command(); if (cmdv.length) secs[f.opt] = { source: 'command', command: cmdv, timeout: c.timeout() }; }
          else {
            const had = secrets[f.opt]?.source === 'keychain';
            if (c.value() || had) secs[f.opt] = { source: 'keychain', value: c.value() };
          }
        }
        for (const [k, c] of Object.entries(advControls)) {
          const o = byName[k]; if (!matchProvider(o.Provider, prov)) continue;
          const r = c.values();
          if (r.option !== undefined) opts[k] = r.option;
          if (r.secret) secs[k] = r.secret;
          if (r.public) pub.push(k);
        }
        // What other kinds of storage use, and the identifiers its storages
        // keep in the file, stay as they are.
        for (const [k, v] of Object.entries(values)) if (!byName[k]) opts[k] = v;
        for (const [k, sec] of Object.entries(secrets)) {
          if (!byName[k]) secs[k] = sec.source === 'command' ? { source: 'command', command: sec.command, timeout: sec.timeout } : { source: 'keychain', value: '' };
        }
        for (const k of wasPublic) if (!secs[k]) pub.push(k);
        return { name: name.value.trim() || suggestion(), options: opts, secrets: secs, public: [...new Set(pub)] };
      },
      // Fills empty fields, e.g. the login from a pasted share address.
      fill: vals => {
        const done = [];
        for (const [k, v] of Object.entries(vals)) if (v && controls[k] && !controls[k].row.hidden && !controls[k].get()) { controls[k].ctl.set(v); done.push(k); }
        updateSuggestion();
        return done;
      },
    };
  }

  // A storage's own backend options: the curated `where` fields of its type
  // (the server) and, under Advanced, any other option. They override what
  // the account sets; passwords and the account's secrets never go here.
  async function storageOptions(type, acct, own) {
    const infos = await options(type);
    const byName = Object.fromEntries(infos.map(o => [o.Name, o]));
    const shared = acct?.options || {};
    const provider = () => shared.provider || '';
    const hint = (opt, f) => shared[opt] ? `${shared[opt]} (from the account)` : (f?.placeholder || byName[opt]?.Default || '');
    const controls = {};
    const rows = (TYPES[type]?.where || []).filter(f => byName[f.opt]).map(f => {
      controls[f.opt] = optionControl(byName[f.opt], own[f.opt] ?? '', { ...f, placeholder: hint(f.opt, f) }, provider);
      return row(f.label, controls[f.opt].el, f.help || null, { key: f.opt });
    });
    // Sensitive options only where the account keeps them in the file.
    const pub = new Set(acct?.public || []);
    const rest = infos.filter(o => !o.Password && !acct?.secrets?.[o.Name] && !controls[o.Name] && (!o.Sensitive || pub.has(o.Name)));
    const advControls = {};
    const control = o => advControls[o.Name] ??= optionControl(o, own[o.Name] ?? '', { placeholder: hint(o.Name) }, provider);
    for (const o of rest) if ((own[o.Name] ?? '') !== '') control(o); // kept while the list stays closed
    const list = h('div', { class: 'card' });
    const filter = text('', { placeholder: 'Filter options', type: 'search' });
    const build = () => {
      list.replaceChildren();
      const q = filter.value.trim().toLowerCase();
      for (const o of rest) {
        if (q && !(o.Name.includes(q) || (o.Help || '').toLowerCase().includes(q))) continue;
        if (!matchProvider(o.Provider, provider())) continue;
        list.append(row(prettify(o.Name), control(o).el, o.Help, { code: o.Name, key: o.Name }));
      }
      if (!list.children.length) list.append(h('div', { class: 'list-empty' }, 'No options match.'));
    };
    filter.addEventListener('input', build);
    return {
      rows, controls, build,
      // The server and other `where` fields rclone calls sensitive: typed
      // in here, they are kept in the file (the account's public list).
      public: () => Object.keys(controls).filter(k => byName[k].Sensitive && controls[k].get() !== ''),
      el: section(`${typeName(type)} options for this storage`, h('p', { class: 'section-help' }, 'They override what the account sets.'),
        h('div', { class: 'search', 'data-transient': '' }, icon('search'), filter), list),
      values: () => {
        const out = {};
        for (const [k, c] of Object.entries({ ...advControls, ...controls })) { const v = c.get(); if (v !== '') out[k] = v; }
        return out;
      },
    };
  }

  // A control for one non-sensitive option. Uses rclone's documented choices
  // (filtered by provider) as a dropdown or as suggestions.
  function optionControl(info, value, field, providerOf) {
    const choices = (info.Choices || []);
    const bool = info.Type === 'bool';
    if (bool) {
      const s = select(value, [['', `Default (${info.Default || 'false'})`], ['true', 'Yes'], ['false', 'No']]);
      return { el: s, get: () => s.value, set: v => { s.value = v; } };
    }
    if ((info.Exclusive || field?.select) && choices.length) {
      const s = select(value, [['', field?.placeholder ? field.placeholder : (info.Default ? `Default (${info.Default})` : 'Choose…')], ...choices.map(c => [c.value, c.help ? `${c.help}` : c.value])]);
      return { el: s, get: () => s.value, set: v => { s.value = v; } };
    }
    if (choices.length) {
      // suggestions filtered by provider (regions, endpoints)
      const id = 'dl-' + info.Name + '-' + Math.random().toString(36).slice(2, 7);
      const input = text(value, { list: id, placeholder: field?.placeholder || info.Default || '', class: field?.narrow ? 'narrow' : '' });
      const dl = h('datalist', { id });
      const wrap = h('div', {}, input, dl);
      const refresh = prov => { dl.replaceChildren(); for (const c of choices) if (matchProvider(c.provider, prov)) dl.append(h('option', { value: c.value }, c.help || '')); };
      refresh(providerOf());
      return { el: wrap, get: () => input.value.trim(), set: v => { input.value = v; }, refresh };
    }
    const input = text(value, { placeholder: field?.placeholder || info.Default || '', class: field?.narrow ? 'narrow' : '' });
    return { el: input, get: () => input.value.trim(), set: v => { input.value = v; } };
  }

  // Advanced control: any option, including sensitive ones with a source chooser.
  function advancedControl(info, value, secret, wasPublic, providerOf) {
    if (!info.Sensitive) {
      const c = optionControl(info, value, null, providerOf);
      return { el: c.el, values: () => { const v = c.get(); return v === '' ? {} : { option: v }; } };
    }
    const modes = info.Password ? [['', 'Not set'], ['keychain', 'Keychain'], ['command', 'Command']] : [['', 'Not set'], ['file', 'In the settings file'], ['keychain', 'Keychain'], ['command', 'Command']];
    const initial = secret ? secret.source : (value !== '' ? 'file' : '');
    const mode = select(initial, modes, { class: 'source' });
    const fileInput = text(value); const keyInput = h('input', { type: 'password', autocomplete: 'new-password', placeholder: secret?.stored ? '•••••••• (saved)' : 'Enter value' });
    const cmd = text(joinCommand(secret?.command), { class: 'mono' }); const to = text(secret?.timeout || '', { placeholder: '10s', class: 'narrow' });
    const body = h('div', { class: 'grow' });
    const render = () => { body.replaceChildren(); if (mode.value === 'file') body.append(fileInput); else if (mode.value === 'keychain') body.append(keyInput); else if (mode.value === 'command') body.append(h('div', { class: 'inline' }, cmd, to)); };
    mode.addEventListener('change', render); render();
    const el = h('div', { class: 'inline' }, mode, body);
    return { el, values: () => {
      switch (mode.value) {
        case 'file': { const v = fileInput.value.trim(); return v ? { option: v, public: true } : {}; }
        case 'keychain': return { secret: { source: 'keychain', value: keyInput.value } };
        case 'command': { const c = splitCommand(cmd.value); return c.length ? { secret: { source: 'command', command: c, timeout: to.value.trim() } } : {}; }
      }
      return {};
    } };
  }

  const prettify = n => n.replace(/_/g, ' ').replace(/^\w/, c => c.toUpperCase());

  // ---------------------------------------------------------------- help & logs
  // slog text lines: time=… level=… msg=… key=value…
  function logLine(l) {
    const m = l.match(/^time=(\S+) level=(\S+) msg=("(?:[^"\\]|\\.)*"|\S+)\s?(.*)$/);
    if (!m) return h('div', { class: 'ln' }, h('span', { class: 'ln-msg' }, l));
    let msg = m[3];
    if (msg.startsWith('"')) { try { msg = JSON.parse(msg); } catch (_) { msg = msg.slice(1, -1); } }
    const lvl = m[2].toLowerCase();
    return h('div', { class: 'ln ' + lvl, 'data-level': lvl },
      h('span', { class: 'ln-time', title: m[1] }, m[1].slice(11, 19)),
      h('span', { class: 'ln-level' }, lvl === 'error' ? 'ERR' : lvl.slice(0, 4).toUpperCase()),
      h('span', { class: 'ln-msg' }, msg, m[4] ? h('span', { class: 'ln-attrs' }, ' ' + m[4]) : null));
  }
  async function renderLogs(page) {
    const viewer = h('div', { class: 'log' });
    let lines = [], problemsOnly = false;
    const fill = () => {
      viewer.replaceChildren(...lines.filter(l => !problemsOnly || /level=(WARN|ERROR)/.test(l)).map(logLine));
      if (!viewer.children.length) viewer.append(h('div', { class: 'ln empty' }, problemsOnly ? 'No problems logged recently.' : 'The log is empty.'));
      viewer.scrollTop = viewer.scrollHeight;
    };
    const load = async () => { const r = await api('GET', '/api/logs?tail=300'); lines = r.lines || []; fill(); return r; };
    const seg = h('div', { class: 'segmented', role: 'tablist', 'data-transient': '' });
    for (const [label, v] of [['All', false], ['Problems', true]]) {
      seg.append(h('button', { class: v === problemsOnly ? 'on' : '', onclick: ev => {
        problemsOnly = v; for (const b of seg.children) b.classList.toggle('on', b === ev.currentTarget); fill();
      } }, label));
    }
    const r = await load();
    const cfgPath = state.view?.real_path || state.view?.path || '';
    page.classList.add('wide-page');
    page.append(
      header({ tone: 'teal', glyph: 'help' }, 'Help & logs', 'When something is off, the log usually says why.'),
      section(null, h('div', { class: 'card' },
        h('div', { class: 'row file-row' }, h('div', { class: 'file-line' }, h('span', { class: 'file-label' }, 'Settings file'), pathChip(cfgPath)),
          button('Open in editor', () => api('POST', '/api/open?what=config'))),
        h('div', { class: 'row file-row' }, h('div', { class: 'file-line' }, h('span', { class: 'file-label' }, 'Log folder'), pathChip(r.path)),
          button('Show in Finder', () => api('POST', '/api/open?what=logs'))),
        h('div', { class: 'row file-row' }, h('div', { class: 'file-line wrap' }, h('span', { class: 'file-label' }, 'Command line'),
          h('kbd', {}, 'lagra status'), h('kbd', {}, 'lagra sync'), h('kbd', {}, 'lagra validate')),
          h('span', { class: 'muted' }, 'Version ', state.status?.version || '')))),
      section(null, h('div', { class: 'log-head' }, h('h2', {}, 'Recent log'), spacer(), seg,
        button('', async ev => busy(ev.currentTarget, load), { glyph: 'refresh', title: 'Reload the log', kind: 'icon-only' }),
        button('', async ev => busy(ev.currentTarget, async () => { await copyText(lines.join('\n')); toast('Log copied'); }), { glyph: 'copy', title: 'Copy the log', kind: 'icon-only' })),
      viewer));
    viewer.scrollTop = viewer.scrollHeight;
  }

  async function copyText(t) {
    try { await navigator.clipboard.writeText(t); return; } catch (_) { /* older WebKit: fall back */ }
    const ta = h('textarea', { class: 'offscreen' }, t);
    document.body.append(ta); ta.select(); document.execCommand('copy'); ta.remove();
  }

  // ---------------------------------------------------------------- boot
  (async () => {
    state.meta = await api('GET', '/api/meta');
    await refresh();
    const wanted = fromHash();
    const known = t => t.kind === 'mount' ? state.view.mounts.some(m => m.name === t.name) : t.kind === 'account' ? !!accountByName(t.name) : true;
    const first = wanted && known(wanted) ? wanted
      : state.view.exists ? (state.view.mounts.length ? { kind: 'mount', name: state.view.mounts[0].name } : { kind: 'new' }) : { kind: 'general' };
    await show(first, { force: true });
    window.addEventListener('hashchange', () => { const t = fromHash(); if (t) show(t); });
    setInterval(async () => {
      try {
        const sig = () => JSON.stringify((state.status?.mounts || []).map(m => [m.name, m.state, m.uploads, m.uploads_stuck, m.pending?.reason, m.last_error]));
        const before = sig();
        state.status = await api('GET', '/api/status');
        if (before !== sig()) {
          renderSidebar();
          if (state.current.kind === 'mount') updateStatusCard();
        }
      } catch (_) { /* daemon gone */ }
    }, 3000);
  })().catch(e => { $('#main').replaceChildren(h('div', { class: 'scroller' }, h('div', { class: 'page' }, callout('bad', 'Could not load the settings', e.message)))); });
})();
