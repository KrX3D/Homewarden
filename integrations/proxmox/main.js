// Proxmox VE integration for Homewarden (beta).
//
// Reads through the Proxmox API (token auth), installs over SSH: the API has no call
// that upgrades packages. LXCs are checked and updated from the host with pct exec.

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function pve(ctx, method, path, body) {
  var cred = ctx.secret('apiToken');
  if (!cred) throw new Error('No API token set: pick one in the Integration card.');
  var r = ctx.http.request({
    method: method, url: 'https://' + ctx.device.host + ':' + setting(ctx, 'port', 8006) + '/api2/json' + path,
    headers: { Authorization: 'PVEAPIToken=' + cred.username + '=' + cred.secret }, body: body,
    insecure: !!setting(ctx, 'insecure', true), timeout: 60
  });
  if (r.status === 401) throw new Error('Proxmox refused the API token. Username must be the full token id, e.g. root@pam!homewarden.');
  if (r.status === 403) throw new Error(missingPrivilege(path));
  if (r.status >= 400) throw new Error('Proxmox ' + method + ' ' + path + ': HTTP ' + r.status + ' ' + (r.body || '').slice(0, 200));
  return r.json ? r.json.data : null;
}

// Privileges the token needs (with Privilege Separation on): Sys.Audit (node status,
// tasks, disks), Sys.Modify (list and refresh package updates), VM.Audit (LXC/VM list),
// Datastore.Audit (storage), VM.Snapshot (snapshots before LXC updates).
var PRIVILEGES = 'Sys.Audit, Sys.Modify, VM.Audit, Datastore.Audit, VM.Snapshot';

function missingPrivilege(path) {
  var p = /\/apt\//.test(path) ? 'Sys.Modify' : /\/snapshot/.test(path) ? 'VM.Snapshot'
    : /\/(lxc|qemu)/.test(path) ? 'VM.Audit' : /\/storage/.test(path) ? 'Datastore.Audit' : 'Sys.Audit';
  return 'The API token lacks the privilege ' + p + ' (for ' + path + '). In Proxmox either edit the token ' +
    '(Datacenter, Permissions, API Tokens) and untick Privilege Separation, or give the token a role with ' +
    PRIVILEGES + ' on path / (Datacenter, Permissions, Add, API Token Permission). The built-in PVEAuditor role is not enough: it lacks Sys.Modify.';
}

function node(ctx) {
  var n = setting(ctx, 'node', '');
  if (n) return n;
  var nodes = pve(ctx, 'GET', '/nodes') || [];
  if (!nodes.length) throw new Error('The API lists no nodes.');
  return nodes[0].node;
}

// Waits for a task (UPID) to finish; throws when it failed.
function waitTask(ctx, n, upid, minutes) {
  var until = ctx.now() + (minutes || 10) * 60;
  while (ctx.now() < until) {
    var st = pve(ctx, 'GET', '/nodes/' + n + '/tasks/' + encodeURIComponent(upid) + '/status');
    if (st && st.status === 'stopped') {
      if (st.exitstatus && st.exitstatus !== 'OK') throw new Error('task failed: ' + st.exitstatus);
      return;
    }
    ctx.sleep(2000);
  }
  throw new Error('task did not finish in time');
}

function lxcUpdates(ctx, vmid) {
  // A container without apt (Alpine etc.) just reports nothing.
  var r = ctx.ssh.exec('pct exec ' + vmid + ' -- sh -c "command -v apt-get >/dev/null && LANG=C apt-get -s -o Debug::NoLocking=1 dist-upgrade"', { sudo: true, timeout: 120 });
  if (r.code !== 0) return null;
  var n = 0, sec = 0;
  r.stdout.split('\n').forEach(function (l) {
    if (/^Inst /.test(l)) { n++; if (/security/i.test(l)) sec++; }
  });
  return { n: n, sec: sec };
}

function guests(ctx, n) {
  var lxc = (pve(ctx, 'GET', '/nodes/' + n + '/lxc') || []).map(function (g) { g.type = 'lxc'; return g; });
  var vms = (pve(ctx, 'GET', '/nodes/' + n + '/qemu') || []).map(function (g) { g.type = 'qemu'; return g; });
  return lxc.concat(vms).sort(function (a, b) { return a.vmid - b.vmid; });
}

// Packages whose upgrade restarts a service Homewarden or the user would notice.
var SERVICE_PKGS = /^(pve-manager|pve-cluster|pve-ha-manager|qemu-server|pve-container|pve-firewall|pve-qemu-kvm|lxc-pve|corosync|proxmox-widget-toolkit|libpve-.*|openssh-server|systemd|ceph.*|frr.*|proxmox-backup-client)$/;

// The Proxmox API lists updates but cannot install them; that needs SSH. Returns ''
// when SSH works, else the reason.
function sshProblem(ctx) {
  var last = '';
  for (var i = 0; i < 4; i++) {
    try {
      var r = ctx.ssh.exec('true', { timeout: 20 });
      return r.code === 0 ? '' : 'SSH test command failed';
    } catch (e) {
      last = String(e && e.message || e);
      // Root's authorized_keys is a link into /etc/pve (pmxcfs). Right after an update
      // restarted pve-cluster it is briefly unreadable, so key auth fails: wait, retry.
      // "not set up" will not change by waiting.
      if (/not set up/.test(last)) return last;
      ctx.sleep(5000);
    }
  }
  return last;
}

var SSH_HINT = 'Installing needs SSH: the Proxmox API can only list updates. Set up the Connection card with SSH as root, or a shared connection in the integration setup.';

function detect(ctx) {
  var n = node(ctx);
  var noSSH = sshProblem(ctx);
  if (noSSH) ctx.log('SSH not available (' + noSSH + '). Updates are listed but cannot be installed, LXC checks are skipped.');
  var ver = pve(ctx, 'GET', '/version') || {};
  if (setting(ctx, 'refreshLists', false)) {
    try { waitTask(ctx, n, pve(ctx, 'POST', '/nodes/' + n + '/apt/update'), 10); ctx.log('Package lists refreshed.'); }
    catch (e) { ctx.log('Could not refresh package lists: ' + (e.message || e)); }
  }
  var pkgs = pve(ctx, 'GET', '/nodes/' + n + '/apt/update') || [];
  var comps = pkgs.map(function (p) {
    var kernel = /^(pve|proxmox)-kernel|^linux-image/.test(p.Package);
    return {
      id: 'pkg:' + p.Package, name: p.Package, group: 'package', installed: p.OldVersion || '', available: p.Version,
      updateAvailable: true, security: /security/i.test((p.Origin || '') + ' ' + (p.Title || '')),
      updatable: !noSSH,
      meta: { warning: kernel ? 'needs-reboot' : SERVICE_PKGS.test(p.Package) ? 'restarts-services' : undefined, repo: p.Origin, note: noSSH ? SSH_HINT : undefined }
    };
  });
  var status = pve(ctx, 'GET', '/nodes/' + n + '/status') || {};
  comps.unshift({
    id: 'os', name: 'Proxmox VE ' + (ver.version || ''), group: 'os', installed: ver.version || '', available: '',
    updateAvailable: pkgs.length > 0, security: comps.some(function (c) { return c.security; }), updatable: !noSSH,
    meta: { summary: pkgs.length + ' packages', warning: comps.some(function (c) { return c.meta.warning === 'needs-reboot'; }) ? 'needs-reboot' : 'restarts-services' }
  });

  var list = guests(ctx, n);
  if (setting(ctx, 'includeLXC', true) && !noSSH) {
    list.filter(function (g) { return g.type === 'lxc' && g.status === 'running'; }).forEach(function (g) {
      try {
        var u = lxcUpdates(ctx, g.vmid);
        if (u === null) return;
        comps.push({ id: 'lxc:' + g.vmid, name: 'LXC ' + g.vmid + ' ' + (g.name || ''), group: 'container',
          installed: '', available: u.n ? u.n + ' packages' : '', updateAvailable: u.n > 0, security: u.sec > 0,
          meta: { summary: u.n + ' packages', warning: 'restarts-services' } });
      } catch (e) {
        ctx.log('LXC ' + g.vmid + ': ' + (e.message || e));
      }
    });
  }
  var mem = status.memory ? Math.round(100 * status.memory.used / status.memory.total) : null;
  ctx.log('Proxmox VE ' + ver.version + ' on ' + n + ': ' + pkgs.length + ' host packages, ' + list.length + ' guests.');
  var warnings = noSSH && pkgs.length ? ['ssh_needed'] : [];
  // Homewarden never reboots a Proxmox host. A newer installed kernel than the running
  // one means the user should, when it suits them.
  var rebootRequired = false;
  if (!noSSH) {
    try {
      var k = ctx.ssh.exec("uname -r; ls /boot | sed -n 's/^vmlinuz-//p' | sort -V | tail -n 1", { timeout: 20 }).stdout.trim().split('\n');
      rebootRequired = k.length === 2 && !!k[1] && k[0] !== k[1];
      if (rebootRequired) ctx.log('Running kernel ' + k[0] + ', newest installed ' + k[1] + ': a reboot is needed to use it.');
    } catch (e) { /* informational only */ }
  }
  return {
    facts: { warnings: warnings, rebootRequired: rebootRequired, version: ver.version, node: n, kernel: status.kversion || '', cpuPercent: status.cpu != null ? Math.round(status.cpu * 100) : null,
      memoryPercent: mem, uptimeSeconds: status.uptime || 0, guests: list.length,
      running: list.filter(function (g) { return g.status === 'running'; }).length },
    components: comps
  };
}

function pveTry(ctx, path) {
  try { return pve(ctx, 'GET', path); } catch (e) { return null; }
}

var MAC_RE = /([0-9a-f]{2}(?::[0-9a-f]{2}){5})/i;
var IPV4_RE = /^(\d{1,3}\.){3}\d{1,3}$/;

// MAC (and a static IP, if any) from the first network device of a guest config.
// LXC: "name=eth0,bridge=vmbr0,hwaddr=BC:24:11:..,ip=192.168.90.20/24"; VM: "virtio=BC:24:11:..,bridge=vmbr0".
function netOf(cfg) {
  for (var i = 0; i < 10; i++) {
    var v = cfg && cfg['net' + i];
    if (typeof v !== 'string') continue;
    var m = MAC_RE.exec(v);
    if (!m) continue;
    var ip = /(?:^|,)ip=(\d{1,3}(?:\.\d{1,3}){3})/.exec(v);
    return { mac: m[1].toLowerCase(), ip: ip ? ip[1] : '' };
  }
  return { mac: '', ip: '' };
}

// The address a running guest has right now: LXCs report their interfaces, VMs only
// through the QEMU guest agent.
function liveIP(ctx, n, g, mac) {
  if (g.status !== 'running') return '';
  if (g.type === 'lxc') {
    var ifs = pveTry(ctx, '/nodes/' + n + '/lxc/' + g.vmid + '/interfaces') || [];
    for (var i = 0; i < ifs.length; i++) {
      var f = ifs[i];
      if (f.name === 'lo' || !f.inet) continue;
      if (mac && f.hwaddr && f.hwaddr.toLowerCase() !== mac) continue;
      return String(f.inet).split('/')[0];
    }
    return '';
  }
  var ag = pveTry(ctx, '/nodes/' + n + '/qemu/' + g.vmid + '/agent/network-get-interfaces');
  var list = (ag && ag.result) || [];
  for (var j = 0; j < list.length; j++) {
    var it = list[j];
    if (mac && (it['hardware-address'] || '').toLowerCase() !== mac) continue;
    var addrs = it['ip-addresses'] || [];
    for (var k = 0; k < addrs.length; k++) {
      var a = addrs[k]['ip-address'];
      if (addrs[k]['ip-address-type'] === 'ipv4' && IPV4_RE.test(a) && a.indexOf('127.') !== 0) return a;
    }
  }
  return '';
}

function discover(ctx) {
  var n = node(ctx);
  return guests(ctx, n).map(function (g) {
    var cfg = pveTry(ctx, '/nodes/' + n + '/' + g.type + '/' + g.vmid + '/config') || {};
    var net = netOf(cfg);
    var ip = liveIP(ctx, n, g, net.mac) || net.ip;
    return { externalId: g.type + '/' + g.vmid, name: (g.name || cfg.hostname || cfg.name || (g.type + ' ' + g.vmid)), host: ip, mac: net.mac,
      kind: 'generic', state: g.status === 'running' ? 'running' : 'stopped' };
  });
}

function busy(ctx) {
  var n = node(ctx);
  var tasks = pve(ctx, 'GET', '/nodes/' + n + '/tasks?source=active') || [];
  var running = tasks.filter(function (t) { return !t.endtime && /^(vzdump|vzmigrate|qmigrate|vzrestore|qmrestore)$/.test(t.type); });
  if (running.length) return { busy: true, reason: running[0].type + ' task running' + (running[0].id ? ' (' + running[0].id + ')' : '') };
  return { busy: false, reason: '' };
}

function snapshot(ctx, n, vmid) {
  var name = 'homewarden-' + new Date().toISOString().slice(0, 16).replace(/[-:T]/g, '');
  ctx.log('LXC ' + vmid + ': snapshot ' + name);
  waitTask(ctx, n, pve(ctx, 'POST', '/nodes/' + n + '/lxc/' + vmid + '/snapshot', { snapname: name, description: 'Before update by Homewarden' }), 10);
  var snaps = (pve(ctx, 'GET', '/nodes/' + n + '/lxc/' + vmid + '/snapshot') || [])
    .filter(function (s) { return /^homewarden-/.test(s.name); })
    .sort(function (a, b) { return (b.snaptime || 0) - (a.snaptime || 0); });
  snaps.slice(3).forEach(function (s) {
    try { waitTask(ctx, n, pve(ctx, 'DELETE', '/nodes/' + n + '/lxc/' + vmid + '/snapshot/' + s.name), 10); ctx.log('LXC ' + vmid + ': removed old snapshot ' + s.name); }
    catch (e) { ctx.log('LXC ' + vmid + ': could not remove ' + s.name + ': ' + (e.message || e)); }
  });
}

var ENV = 'DEBIAN_FRONTEND=noninteractive LANG=C';
var OPTS = '-y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold';

function update(ctx, ids) {
  var n = node(ctx);
  var why = sshProblem(ctx);
  if (why) throw new Error(SSH_HINT + ' (' + why + ')');
  var all = ids.indexOf('*') >= 0;
  var host = all || ids.indexOf('os') >= 0;
  var pkgs = ids.filter(function (i) { return i.indexOf('pkg:') === 0; }).map(function (i) { return i.slice(4); });
  pkgs.forEach(function (p) { if (!/^[a-z0-9][a-z0-9+.\-:]*$/.test(p)) throw new Error('Unexpected package name: ' + p); });
  var lxcs = all ? detect(ctx).components.filter(function (c) { return c.id.indexOf('lxc:') === 0 && c.updateAvailable; }).map(function (c) { return c.id.slice(4); })
    : ids.filter(function (i) { return i.indexOf('lxc:') === 0; }).map(function (i) { return i.slice(4); });
  var failed = [];

  lxcs.forEach(function (vmid) {
    if (!/^\d+$/.test(vmid)) return;
    try {
      if (setting(ctx, 'snapshotBeforeUpdate', true)) snapshot(ctx, n, vmid);
      ctx.log('LXC ' + vmid + ': updating');
      var r = ctx.ssh.exec('pct exec ' + vmid + ' -- env ' + ENV + ' apt-get ' + OPTS + ' dist-upgrade', { sudo: true, timeout: 3600 });
      ctx.log((r.stdout + '\n' + r.stderr).trim().split('\n').slice(-15).join('\n'));
      if (r.code !== 0) throw new Error('apt-get exited with ' + r.code);
    } catch (e) {
      failed.push('LXC ' + vmid);
      ctx.log('LXC ' + vmid + ': ' + (e.message || e));
    }
  });

  if (host || pkgs.length) {
    var cmd = host ? ENV + ' apt-get ' + OPTS + ' dist-upgrade' : ENV + ' apt-get ' + OPTS + ' install --only-upgrade ' + pkgs.join(' ');
    ctx.log('$ ' + cmd);
    var r = ctx.ssh.exec(cmd, { sudo: true, timeout: 3600 });
    ctx.log((r.stdout + '\n' + r.stderr).trim().split('\n').slice(-40).join('\n'));
    if (r.code !== 0) failed.push('host packages');
  }
  if (failed.length) throw new Error('Failed: ' + failed.join(', '));
  return { lxc: lxcs.length, host: host || pkgs.length > 0 };
}

// metrics(ctx, opts): host values and every guest's CPU/RAM from the API; SMART health
// of the host's disks from /disks/list (Proxmox runs smartctl itself).
function metrics(ctx, opts) {
  var n = node(ctx);
  var st = pve(ctx, 'GET', '/nodes/' + n + '/status') || {};
  var m = { cpu: st.cpu != null ? Math.round(st.cpu * 1000) / 10 : null, load1: st.loadavg ? Number(st.loadavg[0]) : null,
    memUsedMB: st.memory ? Math.round(st.memory.used / 1048576) : null, memTotalMB: st.memory ? Math.round(st.memory.total / 1048576) : null,
    disks: [], temps: [], smart: [], containers: [] };
  m.memPct = m.memTotalMB ? Math.round(100 * m.memUsedMB / m.memTotalMB) : null;
  if (st.rootfs && st.rootfs.total) m.disks.push({ mount: '/', usedPct: Math.round(100 * st.rootfs.used / st.rootfs.total), sizeGB: Math.round(st.rootfs.total / 1073741824) });
  try {
    (pve(ctx, 'GET', '/nodes/' + n + '/storage') || []).forEach(function (s) {
      if (s.active && s.total) m.disks.push({ mount: 'storage ' + s.storage, usedPct: Math.round(100 * s.used / s.total), sizeGB: Math.round(s.total / 1073741824) });
    });
  } catch (e) {}
  if (opts && opts.smart) {
    try {
      (pve(ctx, 'GET', '/nodes/' + n + '/disks/list') || []).forEach(function (d) {
        m.smart.push({ dev: d.devpath, model: d.model || '', healthy: d.health ? /PASSED|OK/i.test(d.health) : null, wearPct: d.wearout != null && d.wearout !== 'N/A' ? 100 - Number(d.wearout) : null });
      });
    } catch (e) {}
  }
  guests(ctx, n).forEach(function (g) {
    if (g.status !== 'running') return;
    m.containers.push({ name: (g.name || g.vmid) + ' (' + g.vmid + ')', cpu: g.cpu != null ? Math.round(g.cpu * 1000) / 10 : null, memMB: g.mem ? Math.round(g.mem / 1048576) : null });
  });
  return m;
}
