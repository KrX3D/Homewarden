// Unraid integration for Homewarden (beta).
//
// Everything runs over SSH as root. Plugins go through /usr/local/sbin/plugin, the
// same tool Unraid's Plugins page uses: "plugin check" downloads the newest .plg to
// /tmp/plugins and prints its version, "plugin update" installs it.

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function sh(ctx, cmd, timeout) {
  return ctx.ssh.exec(cmd, { sudo: true, timeout: timeout || 120 });
}

// Reads an Unraid .ini file (key="value" lines, [section] headers).
function parseIni(text) {
  var out = {}, sec = null;
  text.split('\n').forEach(function (l) {
    var s = /^\["?([^"\]]+)"?\]/.exec(l);
    if (s) { sec = s[1]; out[sec] = {}; return; }
    var m = /^([A-Za-z0-9_.]+)="?([^"]*)"?$/.exec(l.trim());
    if (!m) return;
    if (sec) out[sec][m[1]] = m[2]; else out[m[1]] = m[2];
  });
  return out;
}

function plgVersion(text) {
  var m = /<!ENTITY\s+version\s+"([^"]+)"/.exec(text) || /version="([^"]+)"/.exec(text);
  return m ? m[1] : '';
}

function persistKey(ctx) {
  // Unraid 6.9+ restores /boot/config/ssh/root/authorized_keys at boot.
  sh(ctx, 'mkdir -p /boot/config/ssh/root && [ -f /root/.ssh/authorized_keys ] && ' +
    'cat /root/.ssh/authorized_keys /boot/config/ssh/root/authorized_keys 2>/dev/null | sort -u > /tmp/hw_ak && ' +
    'cp /tmp/hw_ak /boot/config/ssh/root/authorized_keys && rm -f /tmp/hw_ak; true');
}

function detect(ctx) {
  var ver = sh(ctx, 'cat /etc/unraid-version 2>/dev/null').stdout;
  var m = /version="?([^"\n]+)"?/.exec(ver);
  if (!m) throw new Error('This does not look like Unraid (/etc/unraid-version missing).');
  var version = m[1];
  if (setting(ctx, 'persistKey', true)) persistKey(ctx);

  var comps = [];
  // OS: Unraid ships its own OS as the unRAIDServer plugin; checking it shows the
  // newest release for the configured branch. Notify only.
  var os = { id: 'os', name: 'Unraid OS', group: 'os', installed: version, available: '', updateAvailable: false, updatable: false,
    meta: { warning: 'self', note: 'Update in Unraid: Tools, Update OS' } };
  var osc = sh(ctx, 'plugin check unRAIDServer.plg 2>/dev/null | tail -1', 120);
  var remoteOS = (osc.stdout || '').trim();
  if (osc.code === 0 && /^\d+\.\d+/.test(remoteOS) && ctx.util.compareVersions(remoteOS, version) > 0) {
    os.available = remoteOS;
    os.updateAvailable = true;
  }
  comps.push(os);

  var ls = sh(ctx, 'ls /boot/config/plugins/*.plg 2>/dev/null');
  var plgs = ls.stdout.split('\n').map(function (x) { return x.trim(); }).filter(function (x) { return x && !/unRAIDServer/.test(x); });
  plgs.forEach(function (path) {
    var file = path.split('/').pop();
    var name = file.replace(/\.plg$/, '');
    var local = plgVersion(sh(ctx, 'cat ' + JSON.stringify(path)).stdout);
    var c = { id: 'plg:' + file, name: name, group: 'plugin', installed: local, available: '', updateAvailable: false };
    if (setting(ctx, 'checkPlugins', true)) {
      var r = sh(ctx, 'plugin check ' + JSON.stringify(file) + ' 2>/dev/null | tail -1', 90);
      var remote = (r.stdout || '').trim();
      if (r.code === 0 && remote && remote !== local && /^[0-9a-zA-Z.\-_]+$/.test(remote)) {
        if (!local || ctx.util.compareVersions(remote, local) > 0) {
          c.available = remote;
          c.updateAvailable = true;
        }
      }
    }
    comps.push(c);
  });

  var facts = { version: version };
  var warnings = [];
  var vars = parseIni(sh(ctx, 'cat /var/local/emhttp/var.ini 2>/dev/null').stdout);
  facts.array = vars.mdState || '';
  if (vars.mdResyncPos && vars.mdResyncPos !== '0') facts.parity = 'running (' + (vars.mdResyncAction || 'check') + ')';
  var disks = parseIni(sh(ctx, 'cat /var/local/emhttp/disks.ini 2>/dev/null').stdout);
  var problems = [];
  Object.keys(disks).forEach(function (k) {
    var d = disks[k];
    if (!d.device && !d.id) return; // empty slot
    var bad = d.status && d.status !== 'DISK_OK' && d.status !== 'DISK_NP' && d.status !== 'DISK_NP_DSBL';
    var errs = Number(d.numErrors || 0);
    if (bad || errs > 0) problems.push(k + (bad ? ' ' + d.status : '') + (errs ? ' ' + errs + ' errors' : ''));
    if (d.temp && d.temp !== '*' && Number(d.temp) >= 55) problems.push(k + ' ' + d.temp + ' C');
  });
  if (problems.length) {
    warnings.push('disk_problems');
    facts.disks = problems.join(', ');
    ctx.log('Disk problems: ' + facts.disks);
  }
  var df = sh(ctx, "df -P /mnt/user 2>/dev/null | tail -1 | awk '{print $5}'").stdout.trim();
  if (df) facts.arrayUsed = df;
  if (warnings.length) facts.warnings = warnings;
  ctx.log('Unraid ' + version + ', ' + plgs.length + ' plugins, array ' + facts.array + '.');
  return { facts: facts, components: comps };
}

function busy(ctx) {
  var vars = parseIni(sh(ctx, 'cat /var/local/emhttp/var.ini 2>/dev/null').stdout);
  if (vars.mdResyncPos && vars.mdResyncPos !== '0') return { busy: true, reason: 'parity ' + (vars.mdResyncAction || 'check') + ' running' };
  // The [m] keeps pgrep from matching the shell that runs this very check.
  var mover = sh(ctx, 'pgrep -f "/usr/local/s?bin/[m]over" >/dev/null && echo yes || echo no').stdout.trim();
  if (mover === 'yes') return { busy: true, reason: 'mover is running' };
  return { busy: false, reason: '' };
}

function update(ctx, ids) {
  var all = ids.indexOf('*') >= 0;
  var targets = all ? detect(ctx).components.filter(function (c) { return c.group === 'plugin' && c.updateAvailable; }).map(function (c) { return c.id.slice(4); })
    : ids.filter(function (i) { return i.indexOf('plg:') === 0; }).map(function (i) { return i.slice(4); });
  var failed = [];
  targets.forEach(function (file) {
    if (!/^[A-Za-z0-9._\-]+\.plg$/.test(file)) { failed.push(file); return; }
    ctx.log('Updating plugin ' + file);
    var r = sh(ctx, 'plugin update ' + JSON.stringify(file), 900);
    ctx.log((r.stdout + '\n' + r.stderr).trim().split('\n').slice(-10).join('\n'));
    if (r.code !== 0) failed.push(file);
  });
  if (failed.length) throw new Error('Not updated: ' + failed.join(', '));
  return { plugins: targets.length };
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
