// Home Assistant integration for Homewarden.
//
// Uses Home Assistant's REST API with a long-lived access token. Home Assistant already
// tracks every update itself as an update.* entity (Core, Operating System, Supervisor,
// each add-on, HACS, device firmware); this integration lists those and installs them
// through the update.install service, the same thing the HA settings page does.

var CORE = 'update.home_assistant_core_update';
var OS = 'update.home_assistant_operating_system_update';
var SUPERVISOR = 'update.home_assistant_supervisor_update';

// update entity supported_features bits
var F_INSTALL = 1, F_BACKUP = 8;

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  var u = setting(ctx, 'url', '');
  if (!u) u = (setting(ctx, 'https', false) ? 'https://' : 'http://') + ctx.device.host + ':' + setting(ctx, 'port', 8123);
  return u.replace(/\/+$/, '');
}

function api(ctx, method, path, body, opts) {
  opts = opts || {};
  var cred = ctx.secret('token');
  if (!cred) throw new Error('No access token set: create a long-lived access token in Home Assistant and pick it in the integration setup.');
  var r;
  try {
    r = ctx.http.request({ method: method, url: base(ctx) + path, body: body, timeout: opts.timeout || 60,
      insecure: !!setting(ctx, 'insecure', false), headers: { Authorization: 'Bearer ' + cred.secret } });
  } catch (e) {
    if (opts.quiet) return null;
    var msg = String(e && e.message || e);
    if (/refused|no such host|timeout|unreachable|no route/i.test(msg)) {
      throw new Error('Home Assistant does not answer at ' + base(ctx) + ' (' + msg.replace(/^.*: /, '') + '). Check address, port (8123) and HTTPS.');
    }
    throw e;
  }
  if (r.status === 401 || r.status === 403) throw new Error('Home Assistant refused the access token (HTTP ' + r.status + '). Create a new long-lived access token.');
  if (r.status >= 400 && !opts.quiet) throw new Error('Home Assistant ' + method + ' ' + path + ': HTTP ' + r.status + (r.body ? ' ' + String(r.body).slice(0, 200) : ''));
  return r;
}

function updateEntities(ctx) {
  var states = api(ctx, 'GET', '/api/states').json || [];
  return states.filter(function (s) { return s.entity_id.indexOf('update.') === 0; });
}

function kindOf(e) {
  if (e.entity_id === CORE) return 'core';
  if (e.entity_id === OS) return 'os';
  if (e.entity_id === SUPERVISOR) return 'supervisor';
  var pic = (e.attributes && e.attributes.entity_picture) || '';
  if (pic.indexOf('/api/hassio/addons/') === 0 || pic.indexOf('/addons/') >= 0) return 'addon';
  return 'other';
}

var ORDER = { core: 0, os: 1, supervisor: 2, addon: 3, other: 4 };

function component(e) {
  var a = e.attributes || {};
  var kind = kindOf(e);
  var name = a.title || (a.friendly_name || e.entity_id).replace(/ (update|Update)$/, '');
  var available = e.state === 'on';
  var c = {
    id: 'ha:' + e.entity_id,
    name: name,
    group: kind === 'os' ? 'os' : kind === 'core' || kind === 'supervisor' ? 'app' : 'plugin',
    installed: a.installed_version || '',
    available: available ? (a.latest_version || '') : '',
    updateAvailable: available,
    updatable: available && ((a.supported_features || 0) & F_INSTALL) !== 0 && !a.in_progress,
    meta: { kind: kind, entity: e.entity_id }
  };
  if (kind === 'os') c.meta.warning = 'reboots';
  else if (kind === 'core' || kind === 'supervisor' || kind === 'addon') c.meta.warning = 'restarts-services';
  if (a.release_url) c.meta.changelog = a.release_url;
  if (a.skipped_version) c.meta.note = 'Skipped in Home Assistant: ' + a.skipped_version;
  if (a.in_progress) c.meta.note = 'Installing in Home Assistant';
  return c;
}

function detect(ctx) {
  var cfg = api(ctx, 'GET', '/api/config').json || {};
  var ents = updateEntities(ctx);
  var comps = ents.map(component).sort(function (x, y) {
    return (ORDER[x.meta.kind] - ORDER[y.meta.kind]) || x.name.localeCompare(y.name);
  });
  var pending = comps.filter(function (c) { return c.updateAvailable; }).length;
  ctx.log('Home Assistant ' + (cfg.version || '?') + ', ' + ents.length + ' update entities, ' + pending + ' updates.');
  var facts = { version: cfg.version || '', name: cfg.location_name || '', updates: pending, safeMode: !!cfg.safe_mode };
  if (cfg.state && cfg.state !== 'RUNNING') facts.state = cfg.state;
  return { facts: facts, components: comps };
}

function busy(ctx) {
  var ents = updateEntities(ctx);
  var running = ents.filter(function (e) { return e.attributes && e.attributes.in_progress; });
  if (running.length) return { busy: true, reason: 'Home Assistant is installing ' + (running[0].attributes.title || running[0].entity_id) };
  var bk = api(ctx, 'GET', '/api/states/sensor.backup_backup_manager_state', null, { quiet: true });
  if (bk && bk.status === 200 && bk.json && bk.json.state && bk.json.state !== 'idle') {
    return { busy: true, reason: 'Home Assistant backup is running (' + bk.json.state + ')' };
  }
  return { busy: false, reason: '' };
}

// Waits until the entity reports the target version (or no update any more).
// Core and OS updates restart Home Assistant, so failed requests just mean "wait".
function waitFor(ctx, entity, target, minutes) {
  var until = ctx.now() + minutes * 60;
  var seenDown = false;
  for (var first = true; ctx.now() < until; first = false) {
    if (!first) ctx.sleep(10000);
    var r = api(ctx, 'GET', '/api/states/' + entity, null, { quiet: true, timeout: 15 });
    if (!r || r.status !== 200 || !r.json) {
      if (!seenDown) ctx.log('  Home Assistant is restarting...');
      seenDown = true;
      continue;
    }
    var a = r.json.attributes || {};
    if (a.in_progress) continue;
    if (r.json.state === 'off' || (target && a.installed_version === target)) return a.installed_version || target;
  }
  throw new Error(entity + ': no new version after ' + minutes + ' minutes. Check Home Assistant, Settings, System, Logs.');
}

function update(ctx, ids) {
  var all = ids.length === 1 && ids[0] === '*';
  var ents = updateEntities(ctx).filter(function (e) {
    return e.state === 'on' && ((e.attributes.supported_features || 0) & F_INSTALL) !== 0 && (all || ids.indexOf('ha:' + e.entity_id) >= 0);
  });
  if (!ents.length) { ctx.log('Nothing to update.'); return; }
  // Add-ons and other components first, then Supervisor, Core, OS last: the restarts
  // of Core and OS would otherwise interrupt the remaining installs.
  var rank = { addon: 0, other: 0, supervisor: 1, core: 2, os: 3 };
  ents.sort(function (x, y) { return rank[kindOf(x)] - rank[kindOf(y)]; });
  var withBackup = !!setting(ctx, 'backupBeforeUpdate', true);
  ents.forEach(function (e) {
    var a = e.attributes;
    var body = { entity_id: e.entity_id };
    if (withBackup && ((a.supported_features || 0) & F_BACKUP) !== 0) body.backup = true;
    ctx.log('Updating ' + (a.title || e.entity_id) + ' ' + a.installed_version + ' -> ' + a.latest_version + (body.backup ? ' (with backup)' : ''));
    var kind = kindOf(e);
    // The service call returns when the install finished; for Core/OS the connection
    // drops during the restart, which is expected.
    var r = api(ctx, 'POST', '/api/services/update/install', body, { quiet: true, timeout: 1800 });
    if (r && r.status >= 400) throw new Error((a.title || e.entity_id) + ': Home Assistant answered HTTP ' + r.status + ' ' + String(r.body || '').slice(0, 300));
    var v = waitFor(ctx, e.entity_id, a.latest_version, kind === 'os' ? 30 : 20);
    ctx.log('  now ' + v);
  });
}

function changelog(ctx, id) {
  var entity = (id || '').replace(/^ha:/, '');
  var r = api(ctx, 'GET', '/api/states/' + entity, null, { quiet: true });
  var a = (r && r.json && r.json.attributes) || {};
  var url = a.release_url || '';
  var text = a.release_summary || '';
  var m = /github\.com\/([^\/]+)\/([^\/]+)\/releases\/tag\/([^\/?#]+)/.exec(url);
  if (m) {
    var g = ctx.http.request({ url: 'https://api.github.com/repos/' + m[1] + '/' + m[2] + '/releases/tags/' + m[3], headers: { Accept: 'application/vnd.github+json' } });
    if (g.json && g.json.body) text = g.json.body.slice(0, 20000);
  }
  return { text: text, url: url };
}
