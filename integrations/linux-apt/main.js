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

// ---- metrics (shared Linux snippet; keep in sync between linux-apt and unraid)

var METRICS_SH = 'read -r _ u n s i w q sq st _ < /proc/stat; sleep 1; read -r _ u2 n2 s2 i2 w2 q2 sq2 st2 _ < /proc/stat; ' +
  't1=$((u+n+s+i+w+q+sq+st)); t2=$((u2+n2+s2+i2+w2+q2+sq2+st2)); d=$((t2-t1)); [ $d -gt 0 ] || d=1; ' +
  'echo "cpu=$(( 100*(d-((i2+w2)-(i+w)))/d ))"; ' +
  "awk '/^MemTotal:/{t=$2}/^MemAvailable:/{a=$2}END{print \"mem=\" int((t-a)/1024) \" \" int(t/1024)}' /proc/meminfo; " +
  'echo "load=$(cut -d" " -f1 /proc/loadavg)"; ' +
  "df -P -T -x tmpfs -x devtmpfs -x overlay -x squashfs -x efivarfs -x nsfs 2>/dev/null | awk 'NR>1{gsub(\"%\",\"\",$6); print \"disk=\" $7 \" \" $6 \" \" int($3/1048576)}'; " +
  'for z in /sys/class/thermal/thermal_zone*; do [ -r "$z/temp" ] && echo "temp=$(cat $z/type 2>/dev/null) $(cat $z/temp)"; done; true';

function parseMetrics(out) {
  var m = { cpu: null, memUsedMB: null, memTotalMB: null, memPct: null, load1: null, disks: [], temps: [], smart: [] };
  out.split('\n').forEach(function (l) {
    var i = l.indexOf('=');
    if (i < 0) return;
    var k = l.slice(0, i), v = l.slice(i + 1).trim().split(/\s+/);
    if (k === 'cpu') m.cpu = Math.max(0, Math.min(100, Number(v[0])));
    if (k === 'mem') { m.memUsedMB = Number(v[0]); m.memTotalMB = Number(v[1]); m.memPct = m.memTotalMB ? Math.round(100 * m.memUsedMB / m.memTotalMB) : null; }
    if (k === 'load') m.load1 = Number(v[0]);
    if (k === 'disk' && v[0] && !/^\/(boot\/efi|snap)/.test(v[0])) m.disks.push({ mount: v[0], usedPct: Number(v[1]), sizeGB: Number(v[2]) });
    if (k === 'temp' && v.length >= 2) m.temps.push({ name: v[0], c: Math.round(Number(v[v.length - 1]) / 100) / 10 });
  });
  return m;
}

// SMART through smartctl (needs root or sudo for smartctl). -n standby leaves sleeping
// disks asleep; they simply report nothing this round.
function readSmart(ctx) {
  var out = [];
  var scan;
  try { scan = ctx.ssh.exec('smartctl --scan', { sudo: true, timeout: 30 }); } catch (e) { return out; }
  if (scan.code !== 0) return out;
  scan.stdout.split('\n').forEach(function (l) {
    var dev = (l.split(/\s+/)[0] || '').trim();
    if (!/^\/dev\/[a-z0-9]+$/.test(dev)) return;
    var r;
    try { r = ctx.ssh.exec('smartctl -j -H -A -i -n standby ' + dev, { sudo: true, timeout: 60 }); } catch (e) { return; }
    var j;
    try { j = JSON.parse(r.stdout); } catch (e) { return; }
    if (!j || (j.smartctl && j.smartctl.exit_status & 2)) return; // asleep or no access
    var attr = function (id) {
      var t = (j.ata_smart_attributes && j.ata_smart_attributes.table) || [];
      for (var i = 0; i < t.length; i++) if (t[i].id === id) return t[i].raw ? t[i].raw.value : null;
      return null;
    };
    var nv = j.nvme_smart_health_information_log || {};
    out.push({
      dev: dev, model: j.model_name || '', healthy: j.smart_status ? !!j.smart_status.passed : null,
      tempC: j.temperature ? j.temperature.current : null, hours: j.power_on_time ? j.power_on_time.hours : null,
      reallocated: attr(5), pending: attr(197), wearPct: nv.percentage_used != null ? nv.percentage_used : null,
      mediaErrors: nv.media_errors != null ? nv.media_errors : null
    });
  });
  return out;
}

function metrics(ctx, opts) {
  var r = ctx.ssh.exec(METRICS_SH, { timeout: 60 });
  var m = parseMetrics(r.stdout);
  if (opts && opts.smart) m.smart = readSmart(ctx);
  return m;
}

// changelog(ctx, componentId): the Debian changelog of the new version (top entries).
function changelog(ctx, id) {
  if (!id || id.indexOf('pkg:') !== 0) return { text: '', url: '' };
  var pkg = id.slice(4);
  if (!/^[a-z0-9][a-z0-9+.\-]*$/.test(pkg)) throw new Error('invalid package');
  // Errors (no internet, missing certificates) are shown instead of an empty window.
  var r = ctx.ssh.exec('LANG=C apt-get changelog -qq ' + pkg + " 2>&1 | grep -v '^W:' | head -80", { timeout: 60 });
  return { text: r.stdout.trim(), url: '' };
}
