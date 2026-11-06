// DeployDeck integration for Homewarden (beta).
//
// DeployDeck already knows every Windows program, the version on the share, the
// vendor's newest version and what each PC has installed. This integration reads that
// (GET /api/hosts, /api/scripts) and starts DeployDeck's own runs.

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  return (setting(ctx, 'https', false) ? 'https' : 'http') + '://' + ctx.device.host + ':' + setting(ctx, 'port', 8080);
}

var cookie = null;

function login(ctx) {
  var cred = ctx.secret('login');
  if (!cred) throw new Error('Pick a DeployDeck login in the Integration card.');
  var r = ctx.http.request({ method: 'POST', url: base(ctx) + '/api/login', body: { username: cred.username, password: cred.secret }, insecure: true, timeout: 30 });
  if (r.status === 401 || r.status === 403) throw new Error('DeployDeck refused the login.');
  if (r.status >= 300) throw new Error('DeployDeck login: HTTP ' + r.status);
  var m = /dd_session=[^;]+/.exec(r.headers['set-cookie'] || '');
  if (!m) throw new Error('DeployDeck login gave no session cookie.');
  cookie = m[0];
}

function dd(ctx, method, path, body) {
  if (!cookie) login(ctx);
  var r = ctx.http.request({ method: method, url: base(ctx) + path, body: body, insecure: true, timeout: 60,
    headers: { Cookie: cookie, 'X-Requested-With': 'homewarden' } });
  if (r.status === 401) { cookie = null; login(ctx); return dd(ctx, method, path, body); }
  if (r.status >= 400) throw new Error('DeployDeck ' + method + ' ' + path + ': HTTP ' + r.status + ' ' + (r.json && r.json.error || ''));
  return r.json;
}

function newer(ctx, a, b) { return a && b && ctx.util.compareVersions(a, b) > 0; }

function detect(ctx) {
  var overview = dd(ctx, 'GET', '/api/hosts') || { hosts: [], programs: [] };
  var scripts = dd(ctx, 'GET', '/api/scripts?kind=script&status=accepted') || [];
  var byPath = {};
  scripts.forEach(function (s) { byPath[s.relPath] = s; });
  var comps = [];
  var behind = 0;
  (overview.programs || []).forEach(function (p) {
    var sc = byPath[p.script];
    var share = p.localVersion || (sc && sc.localVersion) || '';
    var online = p.onlineVersion || (sc && sc.onlineVersion) || '';
    var upd = newer(ctx, online, share);
    comps.push({ id: 'prog:' + (sc ? sc.id : p.script), name: p.program || (sc && sc.programName) || p.script, group: 'app',
      installed: share, available: upd ? online : '', updateAvailable: upd, updatable: !!sc,
      meta: { summary: 'share', script: p.script } });
    if (setting(ctx, 'perPC', true)) {
      Object.keys(p.installed || {}).forEach(function (host) {
        var v = p.installed[host];
        if (!v || !newer(ctx, share, v)) return;
        behind++;
        comps.push({ id: 'pc:' + host + ':' + (p.installScriptId || 0), name: p.program + ' on ' + host, group: 'package',
          installed: v, available: share, updateAvailable: true, updatable: !!p.installScriptId,
          meta: { host: host, note: p.installScriptId ? '' : 'no accepted install script in DeployDeck' } });
      });
    }
  });
  ctx.log((overview.programs || []).length + ' programs, ' + (overview.hosts || []).length + ' PCs, ' + behind + ' installs behind.');
  return { facts: { programs: (overview.programs || []).length, pcs: (overview.hosts || []).length, pcInstallsBehind: behind }, components: comps };
}

function discover(ctx) {
  var overview = dd(ctx, 'GET', '/api/hosts') || { hosts: [] };
  return (overview.hosts || []).map(function (h) {
    return { externalId: 'pc:' + h, name: h, host: '', kind: 'generic', state: 'stopped', group: 'Windows' };
  });
}

function update(ctx, ids) {
  var all = ids.indexOf('*') >= 0;
  var comps = detect(ctx).components.filter(function (c) { return c.updateAvailable && c.updatable && (all || ids.indexOf(c.id) >= 0); });
  var started = 0;
  // Share first: a PC install should get the version the update script just fetched.
  comps.sort(function (a, b) { return (a.id.indexOf('prog:') === 0 ? 0 : 1) - (b.id.indexOf('prog:') === 0 ? 0 : 1); });
  comps.forEach(function (c) {
    var parts = c.id.split(':');
    if (parts[0] === 'prog' && /^\d+$/.test(parts[1])) {
      dd(ctx, 'POST', '/api/scripts/' + parts[1] + '/run', { readOnly: false });
      ctx.log('DeployDeck: started update script for ' + c.name);
      started++;
    } else if (parts[0] === 'pc' && /^\d+$/.test(parts[parts.length - 1]) && parts[parts.length - 1] !== '0') {
      var host = parts.slice(1, parts.length - 1).join(':');
      dd(ctx, 'POST', '/api/scripts/' + parts[parts.length - 1] + '/install', { computer: host });
      ctx.log('DeployDeck: started install of ' + c.name);
      started++;
    }
  });
  ctx.log(started + ' DeployDeck runs started. Their progress and output are in DeployDeck; the next scan shows the new versions.');
  return { started: started };
}
