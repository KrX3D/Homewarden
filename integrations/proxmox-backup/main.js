// Proxmox Backup Server integration for Homewarden (beta).

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function pbs(ctx, method, path) {
  var cred = ctx.secret('apiToken');
  if (!cred) throw new Error('No API token set: pick one in the Integration card.');
  // PBS separates token id and secret with a colon (Proxmox VE uses "=").
  var r = ctx.http.request({
    method: method, url: 'https://' + ctx.device.host + ':' + setting(ctx, 'port', 8007) + '/api2/json' + path,
    headers: { Authorization: 'PBSAPIToken=' + cred.username + ':' + cred.secret },
    insecure: !!setting(ctx, 'insecure', true), timeout: 60
  });
  if (r.status === 401) throw new Error('PBS refused the API token. Username must be the token id, e.g. root@pam!homewarden.');
  if (r.status === 403) throw new Error('The API token lacks a permission for ' + path + '. In PBS give the token the role Audit on path / (Configuration, Access Control, Permissions, Add, API Token Permission).');
  if (r.status >= 400) throw new Error('PBS ' + method + ' ' + path + ': HTTP ' + r.status);
  return r.json ? r.json.data : null;
}

function detect(ctx) {
  var ver = pbs(ctx, 'GET', '/version') || {};
  var comps = [];
  var warnings = [];
  var facts = { version: ver.version + (ver.release ? '.' + ver.release : '') };

  if (setting(ctx, 'packages', true)) {
    var pkgs = [];
    try { pkgs = pbs(ctx, 'GET', '/nodes/localhost/apt/update') || []; }
    catch (e) { ctx.log('Package list not available: ' + (e.message || e)); }
    pkgs.forEach(function (p) {
      comps.push({ id: 'pkg:' + p.Package, name: p.Package, group: 'package', installed: p.OldVersion || '', available: p.Version,
        updateAvailable: true, security: /security/i.test((p.Origin || '') + ' ' + (p.Title || '')),
        meta: { warning: /kernel|linux-image/.test(p.Package) ? 'needs-reboot' : 'restarts-services' } });
    });
    comps.unshift({ id: 'os', name: 'Proxmox Backup Server ' + facts.version, group: 'os', installed: facts.version, available: '',
      updateAvailable: pkgs.length > 0, security: comps.some(function (c) { return c.security; }), meta: { summary: pkgs.length + ' packages' } });
  } else {
    comps.push({ id: 'app', name: 'Proxmox Backup Server', group: 'app', installed: facts.version, updateAvailable: false });
  }

  var full = Number(setting(ctx, 'fullPercent', 85));
  var maxAge = Number(setting(ctx, 'maxAgeHours', 48)) * 3600;
  var stores = pbs(ctx, 'GET', '/status/datastore-usage') || [];
  // One entry per backup group (guest): newest snapshot, its verify state, count.
  // Shown on the Backups page and in the overview.
  var groups = {};
  facts.maxAgeHours = maxAge / 3600;
  stores.forEach(function (s) {
    var pct = s.total ? Math.round(100 * s.used / s.total) : 0;
    facts['datastore ' + s.store] = pct + '% used';
    if (pct > full) warnings.push('datastore_full');
    try {
      var snaps = pbs(ctx, 'GET', '/admin/datastore/' + encodeURIComponent(s.store) + '/snapshots') || [];
      var newest = snaps.reduce(function (a, x) { return Math.max(a, x['backup-time'] || 0); }, 0);
      snaps.forEach(function (x) {
        var key = s.store + '/' + x['backup-type'] + '/' + x['backup-id'];
        var g = groups[key];
        if (!g) g = groups[key] = { store: s.store, type: x['backup-type'], id: x['backup-id'], comment: '', last: 0, count: 0, verify: 'none', size: 0 };
        g.count++;
        if ((x['backup-time'] || 0) > g.last) {
          g.last = x['backup-time'] || 0;
          g.comment = x.comment || g.comment;
          g.verify = (x.verification && x.verification.state) || 'none';
          g.size = x.size || 0;
        }
      });
      if (newest) {
        facts['newest backup ' + s.store] = new Date(newest * 1000).toISOString().slice(0, 16).replace('T', ' ');
        if (ctx.now() - newest > maxAge) warnings.push('backup_old');
      }
    } catch (e) { ctx.log(s.store + ': ' + (e.message || e)); }
  });

  facts.backupGroups = Object.keys(groups).sort().map(function (k) { return groups[k]; });
  if (facts.backupGroups.some(function (g) { return g.verify === 'failed'; })) warnings.push('backup_verify_failed');

  try {
    var since = ctx.now() - 7 * 86400;
    var failed = (pbs(ctx, 'GET', '/nodes/localhost/tasks?errors=1&since=' + since + '&limit=50') || [])
      .filter(function (t) { return t.status && t.status !== 'OK' && t.status.indexOf('WARNINGS') !== 0; });
    facts.failedTasks7d = failed.length;
    if (failed.length) {
      warnings.push('backup_errors');
      ctx.log('Failed tasks in the last 7 days: ' + failed.slice(0, 5).map(function (t) { return t.worker_type + ' ' + (t.worker_id || ''); }).join(', '));
    }
  } catch (e) { ctx.log('Task list: ' + (e.message || e)); }

  if (warnings.length) facts.warnings = warnings.filter(function (w, i) { return warnings.indexOf(w) === i; });
  ctx.log('PBS ' + facts.version + ', ' + stores.length + ' datastores.');
  return { facts: facts, components: comps };
}

function busy(ctx) {
  var tasks = pbs(ctx, 'GET', '/nodes/localhost/tasks?running=1') || [];
  var t = tasks.filter(function (x) { return /backup|verif|garbage|prune|sync|reader/.test(x.worker_type || ''); })[0];
  if (t) return { busy: true, reason: t.worker_type + ' running' + (t.worker_id ? ' (' + t.worker_id + ')' : '') };
  return { busy: false, reason: '' };
}

function update(ctx, ids) {
  if (!setting(ctx, 'packages', true)) throw new Error('Package updates are turned off for this device.');
  var env = 'DEBIAN_FRONTEND=noninteractive LANG=C';
  var opts = '-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold';
  var all = ids.indexOf('*') >= 0 || ids.indexOf('os') >= 0;
  var pkgs = ids.filter(function (i) { return i.indexOf('pkg:') === 0; }).map(function (i) { return i.slice(4); });
  pkgs.forEach(function (p) { if (!/^[a-z0-9][a-z0-9+.\-:]*$/.test(p)) throw new Error('Unexpected package name: ' + p); });
  var cmd = all ? env + ' apt-get ' + opts + ' dist-upgrade' : env + ' apt-get ' + opts + ' install --only-upgrade ' + pkgs.join(' ');
  ctx.log('$ ' + cmd);
  var r = ctx.ssh.exec(cmd, { sudo: true, timeout: 3600 });
  ctx.log((r.stdout + '\n' + r.stderr).trim().split('\n').slice(-40).join('\n'));
  if (r.code !== 0) throw new Error('apt-get exited with code ' + r.code);
  return { packages: all ? 'all' : pkgs.length };
}
