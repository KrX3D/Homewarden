// UniFi Network integration for Homewarden (beta).
//
// Uses the controller's classic API (stat/device, cmd/devmgr). On UniFi OS consoles it
// lives under /proxy/network. Auth: an API key (X-API-KEY, Network 9.0+) or a local
// account (login sets a TOKEN cookie; changes also need the X-CSRF-Token it returns).

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function root(ctx) {
  return 'https://' + ctx.device.host + ':' + setting(ctx, 'port', 443);
}

function apiBase(ctx) {
  return root(ctx) + (setting(ctx, 'unifiOS', true) ? '/proxy/network' : '') + '/api/s/' + encodeURIComponent(setting(ctx, 'site', 'default'));
}

var session = null; // {cookie, csrf} for this call

function authHeaders(ctx) {
  var key = ctx.secret('apiKey');
  if (key) return { 'X-API-KEY': key.secret, Accept: 'application/json' };
  if (session) {
    var h = { Cookie: session.cookie, Accept: 'application/json' };
    if (session.csrf) h['X-CSRF-Token'] = session.csrf;
    return h;
  }
  var login = ctx.secret('login');
  if (!login) throw new Error('Set an API key or a login in the Integration card.');
  var path = setting(ctx, 'unifiOS', true) ? '/api/auth/login' : '/api/login';
  var r = ctx.http.request({ method: 'POST', url: root(ctx) + path, body: { username: login.username, password: login.secret, remember: false },
    insecure: !!setting(ctx, 'insecure', true), timeout: 30 });
  if (r.status === 401 || r.status === 403 || r.status === 400) throw new Error('UniFi refused the login. Use a local account without 2FA.');
  if (r.status >= 300) throw new Error('UniFi login: HTTP ' + r.status);
  var setCookie = r.headers['set-cookie'] || '';
  var m = /(TOKEN|unifises)=[^;]+/.exec(setCookie);
  if (!m) throw new Error('UniFi login gave no session cookie.');
  session = { cookie: m[0], csrf: r.headers['x-csrf-token'] || r.headers['x-updated-csrf-token'] || '' };
  return authHeaders(ctx);
}

function call(ctx, method, path, body) {
  var r;
  try {
    r = ctx.http.request({ method: method, url: apiBase(ctx) + path, body: body, headers: authHeaders(ctx),
      insecure: !!setting(ctx, 'insecure', true), timeout: 60 });
  } catch (e) {
    var msg = String(e && e.message || e);
    if (/refused|no such host|timeout|unreachable|no route/i.test(msg)) {
      throw new Error('No UniFi console answers at ' + apiBase(ctx) + ' (' + msg.replace(/^.*: /, '') + '). ' +
        'Pick UniFi Network only on the console itself (UDM, UCG, Cloud Key, or the host of a self-hosted controller). ' +
        'Other devices get their network details (switch port, access point, signal) from it automatically.');
    }
    throw e;
  }
  if (r.status === 401) throw new Error('UniFi refused the credentials (HTTP 401).');
  if (r.status === 404) throw new Error('UniFi API not found at ' + apiBase(ctx) + '. Check UniFi OS console, port and site.');
  if (r.status >= 400) throw new Error('UniFi ' + method + ' ' + path + ': HTTP ' + r.status);
  if (!r.json || (r.json.meta && r.json.meta.rc && r.json.meta.rc !== 'ok')) throw new Error('UniFi: ' + ((r.json && r.json.meta && r.json.meta.msg) || 'unexpected answer'));
  return r.json.data || [];
}

var TYPES = { uap: 'access point', usw: 'switch', ugw: 'gateway', udm: 'gateway', uxg: 'gateway', ucg: 'gateway', uck: 'cloud key', ubb: 'bridge', uph: 'phone' };

function detect(ctx) {
  var sys = (call(ctx, 'GET', '/stat/sysinfo') || [])[0] || {};
  var devs = call(ctx, 'GET', '/stat/device');
  var comps = [];
  comps.push({ id: 'network-app', name: 'UniFi Network application', group: 'app', installed: sys.version || '',
    available: sys.update_available ? 'newer version' : '', updateAvailable: !!sys.update_available, updatable: false,
    meta: { note: 'Update it in UniFi OS, Settings, Control Plane, Updates' } });
  var offline = 0;
  devs.forEach(function (d) {
    var up = !!d.upgradable;
    if (d.state !== 1) offline++;
    comps.push({
      id: 'dev:' + d.mac, name: (d.name || d.model || d.mac) + ' (' + (TYPES[d.type] || d.type || 'device') + ')', group: 'firmware',
      installed: d.version || '', available: up ? (d.upgrade_to_firmware || 'newer firmware') : '', updateAvailable: up,
      updatable: up && d.state === 1,
      meta: { warning: d.type === 'udm' || d.type === 'uxg' || d.type === 'ugw' || d.type === 'ucg' ? 'restarts-host' : 'reboots', model: d.model, ip: d.ip, state: d.state }
    });
  });
  var facts = { networkApp: sys.version || '', site: setting(ctx, 'site', 'default'), devices: devs.length, offlineDevices: offline };
  if (offline) facts.warnings = ['unifi_offline'];
  ctx.log('UniFi Network ' + (sys.version || '?') + ', ' + devs.length + ' devices, ' + comps.filter(function (c) { return c.updateAvailable; }).length + ' updates.');
  return { facts: facts, components: comps };
}

// Network details for one client, shown in the Network card of the matching device.
function networkOf(c, names) {
  var n = { source: 'UniFi', via: c.is_wired ? 'wired' : 'wifi', ip: c.ip || '', mac: c.mac,
    network: c.network || '', vlan: c.vlan || null, uptime: c.uptime || null, lastSeen: c.last_seen || null,
    alias: c.name || '', hostname: c.hostname || '' };
  if (c.is_wired) {
    n.uplink = names[c.sw_mac] || c.sw_mac || '';
    n.port = c.sw_port || null;
  } else {
    n.uplink = names[c.ap_mac] || c.ap_mac || '';
    n.ssid = c.essid || '';
    n.signal = c.signal || c.rssi || null;
    n.channel = c.channel || null;
    n.radio = c.radio_proto || '';
    if (c.tx_rate) n.txMbps = Math.round(c.tx_rate / 1000);
    if (c.rx_rate) n.rxMbps = Math.round(c.rx_rate / 1000);
  }
  return n;
}

// Names given in UniFi stick to a MAC. Containers on a macvlan/br0 network get a new
// random MAC when they are recreated, so the connected client has no name while the
// old, now offline record still has it. Known clients (/rest/user) with a name are
// matched by their fixed IP, else by their last IP when that record is not online.
function aliasesByIP(ctx, sta) {
  var online = {};
  sta.forEach(function (c) { online[c.mac] = true; });
  var users = [];
  try { users = call(ctx, 'GET', '/rest/user'); } catch (e) { return {}; }
  var fixed = {}, last = {};
  users.forEach(function (u) {
    if (!u.name) return;
    if (u.use_fixedip && u.fixed_ip) fixed[u.fixed_ip] = u.name;
    else if (u.last_ip && !online[u.mac] && !last[u.last_ip]) last[u.last_ip] = u.name;
  });
  var out = {};
  Object.keys(last).forEach(function (ip) { out[ip] = last[ip]; });
  Object.keys(fixed).forEach(function (ip) { out[ip] = fixed[ip]; });
  return out;
}

function discover(ctx) {
  var devs = call(ctx, 'GET', '/stat/device');
  var names = {};
  devs.forEach(function (d) { names[d.mac] = d.name || d.model || d.mac; });
  var out = devs.map(function (d) {
    return { externalId: 'dev:' + d.mac, name: d.name || d.model || d.mac, host: d.ip || '', mac: d.mac, kind: 'generic',
      state: d.state === 1 ? 'running' : 'stopped', group: 'UniFi' };
  });
  // Clients always feed the Network card of devices Homewarden already has; with
  // importClients they also land in the inbox.
  var importClients = !!setting(ctx, 'importClients', false);
  var sta = call(ctx, 'GET', '/stat/sta');
  var alias = aliasesByIP(ctx, sta);
  sta.forEach(function (c) {
    out.push({ externalId: 'client:' + c.mac, name: c.name || alias[c.ip] || c.hostname || c.oui || c.mac, host: c.ip || '', mac: c.mac, kind: 'generic',
      state: 'running', group: 'Clients', enrichOnly: !importClients, network: networkOf(c, names),
      presence: c.ip ? { reach: { method: 'ping' }, ready: { method: 'none' }, settleSeconds: 0, intervalSeconds: 60 } : null });
  });
  return out;
}

function update(ctx, ids) {
  var all = ids.indexOf('*') >= 0;
  var devs = call(ctx, 'GET', '/stat/device');
  var targets = devs.filter(function (d) { return d.upgradable && d.state === 1 && (all || ids.indexOf('dev:' + d.mac) >= 0); });
  if (!targets.length) { ctx.log('Nothing to upgrade.'); return { upgraded: 0 }; }
  // Gateways last: upgrading the gateway interrupts the connection to everything else.
  targets.sort(function (a, b) { return (/udm|uxg|ugw|ucg/.test(a.type) ? 1 : 0) - (/udm|uxg|ugw|ucg/.test(b.type) ? 1 : 0); });
  targets.forEach(function (d) {
    ctx.log('Upgrading ' + (d.name || d.mac) + ' ' + d.version + ' -> ' + (d.upgrade_to_firmware || 'newest'));
    call(ctx, 'POST', '/cmd/devmgr', { cmd: 'upgrade', mac: d.mac });
  });
  ctx.log('The controller is upgrading ' + targets.length + ' devices; they restart on their own. Waiting for them to come back.');
  var until = ctx.now() + 15 * 60;
  var before = {};
  targets.forEach(function (d) { before[d.mac] = d.version; });
  while (ctx.now() < until) {
    ctx.sleep(30000);
    var now;
    try { now = call(ctx, 'GET', '/stat/device'); } catch (e) { continue; } // gateway may be rebooting
    // Done = back online with a different firmware version than before.
    var pending = now.filter(function (d) { return before[d.mac] !== undefined && (d.state !== 1 || d.version === before[d.mac]); });
    if (!pending.length) { ctx.log('All upgraded devices are back online.'); return { upgraded: targets.length }; }
  }
  ctx.log('Some devices did not report back within 15 minutes; check the controller.');
  return { upgraded: targets.length };
}
