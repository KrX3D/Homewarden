// Nginx Proxy Manager integration for Homewarden (beta).
//
// Logs in with an NPM user (a limited one is enough) and reads the proxy hosts (enabled,
// online in nginx) and the certificates with their expiry. Version updates come from
// the GitHub releases; the container itself is updated through the Docker integration.

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  var https = !!setting(ctx, 'https', false);
  return (https ? 'https' : 'http') + '://' + ctx.device.host + ':' + setting(ctx, 'port', 81);
}

var token = null;

function login(ctx) {
  var cred = ctx.secret('login');
  if (!cred) throw new Error('Pick the NPM login in the integration setup.');
  var r = ctx.http.request({ method: 'POST', url: base(ctx) + '/api/tokens', body: { identity: cred.username, secret: cred.secret }, insecure: true, timeout: 30 });
  if (r.status === 401 || r.status === 403 || r.status === 400) throw new Error('Nginx Proxy Manager refused the login (user name is the e-mail address).');
  if (r.status >= 300 || !r.json || !r.json.token) throw new Error('NPM login: HTTP ' + r.status);
  token = r.json.token;
}

function npm(ctx, path) {
  if (!token) login(ctx);
  var r = ctx.http.request({ method: 'GET', url: base(ctx) + '/api' + path, insecure: true, timeout: 30, headers: { Authorization: 'Bearer ' + token } });
  if (r.status === 401) { token = null; login(ctx); return npm(ctx, path); }
  if (r.status >= 400) throw new Error('NPM GET ' + path + ': HTTP ' + r.status);
  return r.json;
}

function parseDate(s) {
  if (!s) return 0;
  var t = Date.parse(String(s).replace(' ', 'T') + (/[zZ]|[+-]\d\d:?\d\d$/.test(s) ? '' : 'Z'));
  return isNaN(t) ? 0 : Math.floor(t / 1000);
}

function detect(ctx) {
  var info = ctx.http.request({ method: 'GET', url: base(ctx) + '/api/', insecure: true, timeout: 15 }).json || {};
  var v = info.version || {};
  var version = v.major !== undefined ? v.major + '.' + v.minor + '.' + v.revision : '';
  var hosts = npm(ctx, '/nginx/proxy-hosts') || [];
  var certs = npm(ctx, '/nginx/certificates') || [];
  var warnDays = Number(setting(ctx, 'certDays', 14));
  var now = ctx.now();
  var enabled = hosts.filter(function (h) { return h.enabled; });
  var offline = enabled.filter(function (h) { return h.meta && h.meta.nginx_online === false; });
  var facts = {
    version: version, proxyHosts: hosts.length, enabledHosts: enabled.length, offlineHosts: offline.length,
    offlineNames: offline.map(function (h) { return (h.domain_names || [])[0] || ('#' + h.id); }),
    certs: certs.map(function (c) {
      return { name: c.nice_name || (c.domain_names || []).join(', '), domains: c.domain_names || [], expires: parseDate(c.expires_on), provider: c.provider || '' };
    }).sort(function (a, b) { return a.expires - b.expires; })
  };
  var warnings = [];
  if (offline.length) warnings.push('npm_host_offline');
  if (warnDays > 0 && facts.certs.some(function (c) { return c.expires && c.expires - now < warnDays * 86400; })) warnings.push('npm_cert_expiring');
  if (warnings.length) facts.warnings = warnings;

  var comp = { id: 'app', name: 'Nginx Proxy Manager', group: 'app', installed: version, available: '', updateAvailable: false, updatable: false,
    meta: { note: 'Update the container through the Docker integration.', changelog: 'https://github.com/NginxProxyManager/nginx-proxy-manager/releases' } };
  if (setting(ctx, 'releaseCheck', true)) try {
    var rel = ctx.http.request({ method: 'GET', url: 'https://api.github.com/repos/NginxProxyManager/nginx-proxy-manager/releases/latest', timeout: 20,
      headers: { Accept: 'application/vnd.github+json', 'User-Agent': 'Homewarden' } });
    if (rel.status === 200 && rel.json && rel.json.tag_name) {
      comp.available = String(rel.json.tag_name).replace(/^v/, '');
      comp.updateAvailable = !!version && ctx.util.compareVersions(comp.available, version) > 0;
    }
  } catch (e) { ctx.log('Release check: ' + (e.message || e)); }
  ctx.log('NPM ' + (version || '?') + ': ' + enabled.length + ' of ' + hosts.length + ' proxy hosts enabled, ' + offline.length + ' offline, ' + certs.length + ' certificates.');
  return { facts: facts, components: [comp] };
}
