// Linux (apt) integration for Homewarden.
//
// Runs in Homewarden's sandbox. Available: ctx.ssh.exec(cmd, {sudo, timeout}),
// ctx.log(...), ctx.settings, ctx.device, ctx.util.compareVersions(a, b).

// Debian/Ubuntu releases and when their (standard) support ends. Used to warn about
// systems that no longer get updates.
var EOL = {
  'debian:stretch': '2022-06-30', 'debian:buster': '2024-06-30', 'debian:bullseye': '2026-08-31',
  'debian:bookworm': '2028-06-30', 'debian:trixie': '2030-06-30',
  'raspbian:stretch': '2022-06-30', 'raspbian:buster': '2024-06-30', 'raspbian:bullseye': '2026-08-31',
  'raspbian:bookworm': '2028-06-30',
  'ubuntu:bionic': '2023-05-31', 'ubuntu:focal': '2025-05-31', 'ubuntu:jammy': '2027-04-30',
  'ubuntu:noble': '2029-04-30'
};

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function run(ctx, cmd, opts) {
  var r = ctx.ssh.exec(cmd, opts || {});
  return r;
}

function facts(ctx) {
  var r = run(ctx, '. /etc/os-release 2>/dev/null; echo "id=$ID"; echo "like=$ID_LIKE"; echo "codename=$VERSION_CODENAME"; ' +
    'echo "pretty=$PRETTY_NAME"; echo "version=$VERSION_ID"; echo "kernel=$(uname -r)"; echo "arch=$(uname -m)"; ' +
    'echo "uptime=$(cut -d. -f1 /proc/uptime)"; [ -f /var/run/reboot-required ] && echo "reboot=yes"; ' +
    'command -v apt-get >/dev/null && echo "apt=yes"; [ -f /etc/rpi-issue ] && echo "rpi=yes"; ' +
    'command -v pveversion >/dev/null && echo "pve=$(pveversion | head -1)"; true');
  var f = {};
  r.stdout.split('\n').forEach(function (l) {
    var i = l.indexOf('=');
    if (i > 0) f[l.slice(0, i)] = l.slice(i + 1).trim();
  });
  return f;
}

// Parses `apt-get -s upgrade` lines like:
// Inst libssl3 [3.0.11-1~deb12u1] (3.0.13-1~deb12u1 Debian-Security:12/stable-security [amd64])
function parseSim(out) {
  var list = [];
  out.split('\n').forEach(function (l) {
    var m = /^Inst (\S+) \[([^\]]+)\] \((\S+) ([^\[]*)/.exec(l);
    if (!m) {
      var n = /^Inst (\S+) \((\S+) ([^\[]*)/.exec(l);
      if (n) list.push({ name: n[1], installed: '', available: n[2], repo: n[3].trim() });
      return;
    }
    list.push({ name: m[1], installed: m[2], available: m[3], repo: m[4].trim() });
  });
  return list;
}

function detect(ctx) {
  var f = facts(ctx);
  if (f.apt !== 'yes') throw new Error('apt-get was not found: this integration is for Debian-based systems.');

  if (setting(ctx, 'refreshLists', true)) {
    var upd = run(ctx, 'apt-get update -qq', { sudo: true, timeout: 300 });
    if (upd.code !== 0) ctx.log('Could not refresh package lists (' + (upd.stderr || '').trim().split('\n').pop() + '). Using the lists already on the device.');
    else ctx.log('Package lists refreshed.');
  }

  // -s simulates; it needs no root and changes nothing.
  var sim = run(ctx, 'LANG=C apt-get -s -o Debug::NoLocking=1 dist-upgrade', { timeout: 120 });
  if (sim.code !== 0) throw new Error('apt-get -s failed: ' + sim.stderr.trim());
  var pkgs = parseSim(sim.stdout);

  var comps = pkgs.map(function (p) {
    return {
      id: 'pkg:' + p.name,
      name: p.name,
      group: 'package',
      installed: p.installed,
      available: p.available,
      updateAvailable: true,
      security: /security/i.test(p.repo),
      meta: { repo: p.repo, kernel: /^linux-image|^raspberrypi-kernel|^pve-kernel|^proxmox-kernel/.test(p.name) }
    };
  });

  var key = (f.id || '') + ':' + (f.codename || '');
  var eol = EOL[key] || (f.like && EOL[f.like.split(' ')[0] + ':' + f.codename]) || '';
  var eolPassed = eol && eol < new Date().toISOString().slice(0, 10);
  var security = comps.filter(function (c) { return c.security; }).length;

  comps.unshift({
    id: 'os',
    name: f.pretty || 'Linux',
    group: 'os',
    installed: f.version || f.codename || '',
    available: '',
    updateAvailable: comps.length > 0,
    security: security > 0,
    meta: {
      summary: comps.length + ' packages',
      eol: eol, eolPassed: !!eolPassed, rebootRequired: f.reboot === 'yes',
      kernel: f.kernel, warning: f.pve ? 'restarts-services' : ''
    }
  });

  if (f.reboot === 'yes' && setting(ctx, 'allowReboot', false)) {
    comps.push({ id: 'reboot', name: 'Reboot', group: 'action', installed: '', available: 'required', updateAvailable: true, meta: { warning: 'reboots' } });
  }

  ctx.log(pkgs.length + ' upgradable packages, ' + security + ' from security repositories.');
  if (eolPassed) ctx.log('Warning: ' + (f.pretty || key) + ' reached end of support on ' + eol + '.');

  return {
    facts: {
      os: f.pretty, codename: f.codename, kernel: f.kernel, arch: f.arch, uptimeSeconds: Number(f.uptime || 0),
      rebootRequired: f.reboot === 'yes', eol: eol, eolPassed: !!eolPassed, raspberryPi: f.rpi === 'yes', proxmox: f.pve || ''
    },
    components: comps
  };
}

function update(ctx, ids) {
  var env = 'DEBIAN_FRONTEND=noninteractive LANG=C';
  var opts = '-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold';
  var all = ids.indexOf('*') >= 0 || ids.indexOf('os') >= 0;
  var names = ids.filter(function (i) { return i.indexOf('pkg:') === 0; }).map(function (i) { return i.slice(4); });
  var reboot = ids.indexOf('reboot') >= 0;

  // Package names come from detect(), but never trust them blindly in a shell.
  names.forEach(function (n) {
    if (!/^[a-z0-9][a-z0-9+.\-:]*$/.test(n)) throw new Error('Unexpected package name: ' + n);
  });

  var cmd = null;
  if (all) cmd = env + ' apt-get ' + opts + ' dist-upgrade';
  else if (names.length) cmd = env + ' apt-get ' + opts + ' install --only-upgrade ' + names.join(' ');

  if (cmd) {
    ctx.log('$ ' + cmd);
    var r = run(ctx, cmd, { sudo: true, timeout: 3600 });
    var out = (r.stdout + '\n' + r.stderr).trim().split('\n');
    // Keep the log readable: the last 60 lines say what happened.
    ctx.log(out.slice(-60).join('\n'));
    if (r.code !== 0) throw new Error('apt-get exited with code ' + r.code);
    run(ctx, env + ' apt-get -y autoremove', { sudo: true, timeout: 600 });
  }
  if (reboot) {
    ctx.log('Rebooting.');
    // The connection drops while the device goes down; that is the expected outcome.
    try { run(ctx, 'reboot', { sudo: true, timeout: 15 }); } catch (e) { ctx.log('Connection closed by the reboot.'); }
  }
  return { packages: all ? 'all' : names.length, rebooted: reboot };
}
