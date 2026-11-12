// AdGuard Home integration for Homewarden (beta).
//
// Reads the status, the query statistics and the filter lists over AdGuard Home's
// HTTP API (basic auth with the web login). Several instances are compared on the
// overview: different filter lists or custom rules mean they are out of sync.

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  var https = !!setting(ctx, 'https', false);
  return (https ? 'https' : 'http') + '://' + ctx.device.host + ':' + setting(ctx, 'port', https ? 443 : 80);
}

function auth(ctx) {
  var cred = ctx.secret('login');
  if (!cred) throw new Error('Pick the AdGuard Home login in the integration setup.');
  return 'Basic ' + b64(utf8(cred.username + ':' + cred.secret));
}

function utf8(s) {
  return unescape(encodeURIComponent(s));
}

function b64(s) {
  var c = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/', out = '';
  for (var i = 0; i < s.length; i += 3) {
    var n = (s.charCodeAt(i) << 16) | ((s.charCodeAt(i + 1) || 0) << 8) | (s.charCodeAt(i + 2) || 0);
    out += c.charAt(n >> 18 & 63) + c.charAt(n >> 12 & 63) + (i + 1 < s.length ? c.charAt(n >> 6 & 63) : '=') + (i + 2 < s.length ? c.charAt(n & 63) : '=');
  }
  return out;
}

function agh(ctx, method, path, body) {
  var r = ctx.http.request({ method: method, url: base(ctx) + '/control' + path, body: body, insecure: true, timeout: 30,
    headers: { Authorization: auth(ctx) } });
  if (r.status === 401 || r.status === 403) throw new Error('AdGuard Home refused the login.');
  if (r.status >= 400) throw new Error('AdGuard Home ' + method + ' ' + path + ': HTTP ' + r.status + ' ' + (r.body || '').slice(0, 120));
  return r.json;
}

// Small stable fingerprint of the blocking setup (filter URLs + custom rules).
function fingerprint(list) {
  var h = 0, s = list.join('\n');
  for (var i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) | 0;
  return (h >>> 0).toString(16);
}

function detect(ctx) {
  var st = agh(ctx, 'GET', '/status') || {};
  var stats = agh(ctx, 'GET', '/stats') || {};
  var filt = agh(ctx, 'GET', '/filtering/status') || {};
  var version = String(st.version || '').replace(/^v/, '');
  var filters = (filt.filters || []).filter(function (f) { return f.enabled; });
  var rules = filters.reduce(function (a, f) { return a + (f.rules_count || 0); }, 0);
  var userRules = (filt.user_rules || []).filter(function (r) { return r && r.trim() && r.trim().charAt(0) !== '!'; });
  var queries = stats.num_dns_queries || 0, blocked = stats.num_blocked_filtering || 0;
  var facts = {
    version: version, protection: !!st.protection_enabled, running: st.running !== false,
    queries: queries, blocked: blocked, blockedPct: queries ? Math.round(1000 * blocked / queries) / 10 : 0,
    avgMs: stats.avg_processing_time ? Math.round(stats.avg_processing_time * 10000) / 10 : null,
    statsHours: stats.time_units === 'days' ? (stats.dns_queries || []).length * 24 : (stats.dns_queries || []).length,
    queriesSeries: (stats.dns_queries || []).slice(-24), blockedSeries: (stats.blocked_filtering || []).slice(-24),
    filterLists: filters.length, rules: rules, customRules: userRules.length,
    syncKey: fingerprint(filters.map(function (f) { return f.url; }).sort().concat(['--']).concat(userRules.slice().sort()))
  };
  if (!facts.protection) facts.warnings = ['adguard_protection_off'];

  var comp = { id: 'app', name: 'AdGuard Home', group: 'app', installed: version, available: '', updateAvailable: false, updatable: false };
  try {
    var v = agh(ctx, 'POST', '/version.json', { recheck_now: false }) || {};
    if (v.new_version && !v.disabled) {
      comp.available = String(v.new_version).replace(/^v/, '');
      comp.updateAvailable = ctx.util.compareVersions(comp.available, version) > 0;
      comp.updatable = comp.updateAvailable && !!v.can_autoupdate;
      comp.meta = { changelog: 'https://github.com/AdguardTeam/AdGuardHome/releases', warning: 'restarts-services' };
      if (comp.updateAvailable && !v.can_autoupdate) comp.meta.note = 'Runs in Docker or cannot update itself: update the image instead.';
    }
  } catch (e) { ctx.log('Version check: ' + (e.message || e)); }
  ctx.log('AdGuard Home ' + version + ': ' + queries + ' queries, ' + blocked + ' blocked, protection ' + (facts.protection ? 'on' : 'OFF') + '.');
  return { facts: facts, components: [comp] };
}

function update(ctx, ids) {
  ctx.log('Asking AdGuard Home to update itself');
  agh(ctx, 'POST', '/update');
  return { started: true };
}
