// OctoPrint / OctoPi integration for Homewarden.
//
// Talks to OctoPrint's REST API with an application key (X-Api-Key). OctoPrint core
// and plugins are updated through OctoPrint's own Software Update plugin, the same
// thing its UI does. The optional OctoPi OS part uses apt over SSH.

// Job states during which nothing may be updated. "Starting" covers the heat-up of a
// print that was started; plain manual preheating leaves OctoPrint "Operational".
var BUSY_STATES = ['Starting', 'Starting print from SD', 'Printing', 'Printing from SD', 'Sending file to SD',
  'Pausing', 'Paused', 'Resuming', 'Cancelling', 'Finishing'];

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  var u = setting(ctx, 'url', '') || ('http://' + ctx.device.host + '/');
  return u.replace(/\/+$/, '');
}

function api(ctx, method, path, body, quiet) {
  var cred = ctx.secret('apiKey');
  if (!cred) throw new Error('No API key set: pick an API token credential in the Integration card.');
  var r = ctx.http.request({ method: method, url: base(ctx) + path, body: body, timeout: 60,
    insecure: !!setting(ctx, 'insecure', false), headers: { 'X-Api-Key': cred.secret } });
  if (r.status === 401 || r.status === 403) throw new Error('OctoPrint refused the API key (HTTP ' + r.status + '). Create an application key for an admin user.');
  if (r.status >= 400 && !quiet) throw new Error('OctoPrint ' + method + ' ' + path + ': HTTP ' + r.status);
  return r;
}

function jobState(ctx) {
  var j = api(ctx, 'GET', '/api/job', null, true);
  if (j.status !== 200 || !j.json) return { state: 'Unknown' };
  return { state: j.json.state || 'Unknown', file: j.json.job && j.json.job.file ? j.json.job.file.name : '', progress: j.json.progress ? j.json.progress.completion : null };
}

function busy(ctx) {
  var s = jobState(ctx);
  var hit = BUSY_STATES.some(function (b) { return s.state.indexOf(b) === 0; });
  if (hit) {
    var pct = s.progress != null ? ' (' + Math.round(s.progress) + '%)' : '';
    return { busy: true, reason: 'print job ' + s.state.toLowerCase() + (s.file ? ': ' + s.file : '') + pct };
  }
  var su = api(ctx, 'GET', '/plugin/softwareupdate/check', null, true);
  if (su.json && su.json.busy) return { busy: true, reason: 'OctoPrint is already installing an update' };
  return { busy: false, reason: '' };
}

// ---- OctoPi OS packages (apt over SSH), same rules as the Linux (apt) integration

function aptDetect(ctx) {
  var r = ctx.ssh.exec('LANG=C apt-get -s -o Debug::NoLocking=1 dist-upgrade', { timeout: 120 });
  if (r.code !== 0) throw new Error('apt-get -s failed: ' + r.stderr.trim());
  var out = [];
  r.stdout.split('\n').forEach(function (l) {
    var m = /^Inst (\S+) \[([^\]]+)\] \((\S+) ([^\[]*)/.exec(l);
    if (m) out.push({ id: 'pkg:' + m[1], name: m[1], group: 'package', installed: m[2], available: m[3], updateAvailable: true, security: /security/i.test(m[4]) });
  });
  return out;
}

function detect(ctx) {
  var ver = api(ctx, 'GET', '/api/version').json || {};
  var comps = [];
  var su = api(ctx, 'GET', '/plugin/softwareupdate/check?force=true', null, true);
  if (su.status === 200 && su.json && su.json.information) {
    var info = su.json.information;
    Object.keys(info).forEach(function (key) {
      var c = info[key];
      var local = c.information && c.information.local ? (c.information.local.name || c.information.local.value) : c.displayVersion;
      var remote = c.information && c.information.remote ? (c.information.remote.name || c.information.remote.value) : '';
      comps.push({
        id: 'ou:' + key,
        name: c.displayName || key,
        group: key === 'octoprint' ? 'app' : 'plugin',
        installed: String(local || c.displayVersion || ''),
        available: c.updateAvailable ? String(remote || '') : '',
        updateAvailable: !!c.updateAvailable,
        updatable: !!(c.updateAvailable ? c.updatePossible !== false : true),
        meta: { changelog: c.releaseNotes || '', warning: 'restarts-services', key: key }
      });
    });
  } else {
    ctx.log('Software Update plugin did not answer (HTTP ' + su.status + '); only the OctoPrint version is shown.');
    comps.push({ id: 'ou:octoprint', name: 'OctoPrint', group: 'app', installed: ver.server || '', updateAvailable: false });
  }

  var facts = { octoprint: ver.server || '', api: ver.api || '' };
  var pi = api(ctx, 'GET', '/api/plugin/pi_support', null, true);
  if (pi.status === 200 && pi.json) {
    facts.model = pi.json.model || '';
    facts.octopi = pi.json.octopi_version || '';
    var th = pi.json.throttle_state || {};
    facts.undervoltage = !!(th.current_undervoltage || th.past_undervoltage);
    facts.throttled = !!(th.current_issue || th.past_issue);
    if (facts.undervoltage) {
      ctx.log('Warning: the Pi reported undervoltage. Check the power supply.');
      facts.warnings = ['undervoltage'];
    }
  }
  var job = jobState(ctx);
  facts.jobState = job.state;
  if (job.file) facts.jobFile = job.file;

  if (setting(ctx, 'includeOS', false)) {
    try {
      var pk = aptDetect(ctx);
      ctx.log(pk.length + ' OctoPi system packages can be upgraded.');
      if (pk.length) comps.push({ id: 'os', name: 'OctoPi system packages', group: 'os', installed: facts.octopi || '', available: pk.length + ' packages', updateAvailable: true, security: pk.some(function (p) { return p.security; }), meta: { summary: pk.length + ' packages' } });
      comps = comps.concat(pk);
    } catch (e) {
      ctx.log('OctoPi packages skipped: ' + (e.message || e));
    }
  }
  var n = comps.filter(function (c) { return c.updateAvailable; }).length;
  ctx.log('OctoPrint ' + facts.octoprint + ', state ' + job.state + ', ' + n + ' updates.');
  return { facts: facts, components: comps };
}

function waitForOctoPrint(ctx, minutes) {
  // OctoPrint restarts after updating; wait until the API answers again.
  var until = ctx.now() + minutes * 60;
  ctx.sleep(10000);
  while (ctx.now() < until) {
    try {
      var r = api(ctx, 'GET', '/api/version', null, true);
      if (r.status === 200) return true;
    } catch (e) { /* still restarting */ }
    ctx.sleep(5000);
  }
  return false;
}

function backupNow(ctx) {
  var r = api(ctx, 'POST', '/plugin/backup/backup', { exclude: ['timelapse', 'uploads'] }, true);
  if (r.status >= 400) throw new Error('backup plugin answered HTTP ' + r.status);
  // The backup runs in the background; wait for a new file to appear.
  var before = (r.json && r.json.name) || '';
  for (var i = 0; i < 60; i++) {
    ctx.sleep(3000);
    var l = api(ctx, 'GET', '/plugin/backup/backup', null, true).json;
    var list = (l && (l.backups || l)) || [];
    if (!list.length) continue;
    list.sort(function (a, b) { return (b.date || 0) - (a.date || 0); });
    var newest = list[0];
    if (!before || newest.name === before) {
      if (l.backup_in_progress) continue;
      return newest;
    }
  }
  throw new Error('backup did not finish within 3 minutes');
}

// backup(ctx) creates a backup and tells the core how to download it.
function backup(ctx) {
  var b = backupNow(ctx);
  var cred = ctx.secret('apiKey');
  ctx.log('Backup ' + b.name + ' created (' + Math.round((b.size || 0) / 1048576) + ' MB).');
  return { name: b.name, size: b.size || 0, download: { url: base(ctx) + '/plugin/backup/download/' + encodeURIComponent(b.name), headers: { 'X-Api-Key': cred.secret } } };
}

function update(ctx, ids) {
  var all = ids.indexOf('*') >= 0;
  var info = detect(ctx).components;
  var targets = info.filter(function (c) {
    return c.id.indexOf('ou:') === 0 && c.updateAvailable && c.updatable && (all || ids.indexOf(c.id) >= 0);
  }).map(function (c) { return c.id.slice(3); });
  var os = all ? info.some(function (c) { return c.group === 'os'; }) : ids.indexOf('os') >= 0;
  var pkgs = ids.filter(function (i) { return i.indexOf('pkg:') === 0; }).map(function (i) { return i.slice(4); });
  pkgs.forEach(function (n) { if (!/^[a-z0-9][a-z0-9+.\-:]*$/.test(n)) throw new Error('Unexpected package name: ' + n); });

  if ((targets.length || os || pkgs.length) && setting(ctx, 'backupBeforeUpdate', true)) {
    ctx.log('Creating an OctoPrint backup first.');
    var b = backupNow(ctx);
    ctx.log('Backup ' + b.name + ' created.');
  }

  if (targets.length) {
    ctx.log('Updating via OctoPrint: ' + targets.join(', '));
    var r = api(ctx, 'POST', '/plugin/softwareupdate/update', { targets: targets });
    if (r.status >= 400) throw new Error('OctoPrint did not start the update: HTTP ' + r.status);
    ctx.log('OctoPrint is installing; waiting for it to come back.');
    if (!waitForOctoPrint(ctx, 15)) throw new Error('OctoPrint did not come back within 15 minutes. Check the Pi.');
    ctx.log('OctoPrint is back.');
  }

  if (os || pkgs.length) {
    var env = 'DEBIAN_FRONTEND=noninteractive LANG=C';
    var opts = '-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold';
    var cmd = os ? env + ' apt-get ' + opts + ' dist-upgrade' : env + ' apt-get ' + opts + ' install --only-upgrade ' + pkgs.join(' ');
    ctx.log('$ ' + cmd);
    var a = ctx.ssh.exec(cmd, { sudo: true, timeout: 3600 });
    ctx.log((a.stdout + '\n' + a.stderr).trim().split('\n').slice(-40).join('\n'));
    if (a.code !== 0) throw new Error('apt-get exited with code ' + a.code);
  }
  return { octoprint: targets, os: os, packages: pkgs.length };
}
