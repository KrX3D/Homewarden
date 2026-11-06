// Windows integration for Homewarden (beta). Runs PowerShell through the agent
// (ctx.ssh.exec is routed to the agent, which runs commands with PowerShell as SYSTEM).

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function ps(ctx, script, timeout) {
  var r = ctx.ssh.exec(script, { timeout: timeout || 300 });
  if (r.code !== 0 && !r.stdout) throw new Error((r.stderr || 'PowerShell failed').trim().split('\n').slice(-3).join(' '));
  return r.stdout;
}

function json(text) {
  var t = text.trim();
  if (!t) return [];
  var v = JSON.parse(t);
  return Array.isArray(v) ? v : [v];
}

// winget as SYSTEM is not on PATH; find the App Installer copy.
var WINGET = "$w = (Get-Command winget.exe -ErrorAction SilentlyContinue).Source; " +
  "if (-not $w) { $w = (Get-ChildItem \"$env:ProgramFiles\\WindowsApps\\Microsoft.DesktopAppInstaller_*_x64__8wekyb3d8bbwe\\winget.exe\" -ErrorAction SilentlyContinue | Sort-Object FullName | Select-Object -Last 1).FullName }; ";

function wuSearch(ctx) {
  var crit = "IsInstalled=0 and IsHidden=0" + (setting(ctx, 'includeDrivers', false) ? '' : " and Type='Software'");
  var out = ps(ctx, "$s = (New-Object -ComObject Microsoft.Update.Session).CreateUpdateSearcher(); " +
    "$r = $s.Search(\"" + crit + "\"); " +
    "$r.Updates | ForEach-Object { [pscustomobject]@{ id = $_.Identity.UpdateID; title = $_.Title; kb = ($_.KBArticleIDs -join ','); " +
    "security = [bool]($_.Categories | Where-Object { $_.Name -match 'Security|Critical' }); reboot = [bool]($_.InstallationBehavior.RebootBehavior -ne 0) } } | ConvertTo-Json -Compress", 600);
  return json(out);
}

function wingetList(ctx) {
  var out = ps(ctx, WINGET + "if (-not $w) { exit 0 }; " +
    "& $w upgrade --accept-source-agreements --disable-interactivity 2>$null | Out-String -Width 400", 300);
  // Parse the table by the header's column positions.
  var lines = out.split(/\r?\n/);
  var h = -1;
  for (var i = 0; i < lines.length; i++) { if (/\bId\b/.test(lines[i]) && /\bVersion\b/.test(lines[i])) { h = i; break; } }
  if (h < 0) return [];
  var head = lines[h];
  var cId = head.indexOf('Id'), cVer = head.indexOf('Version'), cAv = head.search(/Available|Verfügbar/), cSrc = head.search(/Source|Quelle/);
  var res = [];
  for (var j = h + 2; j < lines.length; j++) {
    var l = lines[j];
    if (!l.trim() || /^\d+ upgrades? available|Aktualisierungen verfügbar/.test(l.trim()) || l.length < cAv) continue;
    var id = l.slice(cId, cVer).trim(), ver = l.slice(cVer, cAv).trim(), av = l.slice(cAv, cSrc > 0 ? cSrc : undefined).trim();
    if (!id || !av || /\s/.test(id)) continue;
    res.push({ name: l.slice(0, cId).trim(), id: id, installed: ver, available: av });
  }
  return res;
}

function detect(ctx) {
  var info = json(ps(ctx, "$o = Get-CimInstance Win32_OperatingSystem; " +
    "$rb = (Test-Path 'HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\WindowsUpdate\\Auto Update\\RebootRequired') -or (Test-Path 'HKLM:\\SOFTWARE\\Microsoft\\Windows\\CurrentVersion\\Component Based Servicing\\RebootPending'); " +
    "[pscustomobject]@{ os = $o.Caption; version = $o.Version; build = $o.BuildNumber; host = $env:COMPUTERNAME; reboot = $rb } | ConvertTo-Json -Compress", 60))[0] || {};
  var comps = [];
  if (setting(ctx, 'windowsUpdate', true)) {
    wuSearch(ctx).forEach(function (u) {
      comps.push({ id: 'wu:' + u.id, name: u.title, group: 'os', installed: '', available: u.kb ? 'KB' + u.kb : 'update',
        updateAvailable: true, security: !!u.security, meta: { warning: u.reboot ? 'reboots' : '' } });
    });
  }
  if (setting(ctx, 'scriptsPath', '')) {
    try { comps = comps.concat(scriptComponents(ctx)); } catch (e) { ctx.log('Update scripts: ' + (e.message || e)); }
  }
  if (setting(ctx, 'winget', true)) {
    try {
      wingetList(ctx).forEach(function (a) {
        comps.push({ id: 'wg:' + a.id, name: a.name || a.id, group: 'app', installed: a.installed, available: a.available, updateAvailable: true });
      });
    } catch (e) { ctx.log('winget: ' + (e.message || e)); }
  }
  ctx.log((info.os || 'Windows') + ' ' + (info.build || '') + ': ' + comps.length + ' updates.');
  return { facts: { os: info.os, version: info.version, build: info.build, computer: info.host, rebootRequired: !!info.reboot }, components: comps };
}

function update(ctx, ids) {
  var all = ids.indexOf('*') >= 0;
  var cur = all ? detect(ctx).components : [];
  var wu = all ? cur.filter(function (c) { return c.id.indexOf('wu:') === 0; }).map(function (c) { return c.id.slice(3); })
    : ids.filter(function (i) { return i.indexOf('wu:') === 0; }).map(function (i) { return i.slice(3); });
  var wg = all ? cur.filter(function (c) { return c.id.indexOf('wg:') === 0; }).map(function (c) { return c.id.slice(3); })
    : ids.filter(function (i) { return i.indexOf('wg:') === 0; }).map(function (i) { return i.slice(3); });
  var failed = [];
  var scripts = all ? cur.filter(function (c) { return /^(ps|share):/.test(c.id); }).map(function (c) { return c.id; })
    : ids.filter(function (i) { return /^(ps|share):/.test(i); });
  var ran = {};
  scripts.forEach(function (id) {
    var script = id.replace(/^(ps|share):/, '');
    if (ran[script]) return;
    ran[script] = true;
    try { runScript(ctx, script); } catch (e) { failed.push(script); ctx.log(String(e.message || e)); }
  });
  if (wu.length) {
    wu.forEach(function (id) { if (!/^[0-9a-fA-F-]{36}$/.test(id)) throw new Error('Unexpected update id ' + id); });
    ctx.log('Windows Update: installing ' + wu.length + ' updates');
    var out = ps(ctx, "$ids = @('" + wu.join("','") + "'); $ses = New-Object -ComObject Microsoft.Update.Session; " +
      "$r = $ses.CreateUpdateSearcher().Search('IsInstalled=0'); $c = New-Object -ComObject Microsoft.Update.UpdateColl; " +
      "$r.Updates | Where-Object { $ids -contains $_.Identity.UpdateID } | ForEach-Object { if (-not $_.EulaAccepted) { $_.AcceptEula() }; [void]$c.Add($_) }; " +
      "if ($c.Count -eq 0) { 'nothing to install'; exit 0 }; " +
      "$d = $ses.CreateUpdateDownloader(); $d.Updates = $c; [void]$d.Download(); " +
      "$i = $ses.CreateUpdateInstaller(); $i.Updates = $c; $res = $i.Install(); " +
      "\"result=$($res.ResultCode) reboot=$($res.RebootRequired)\"", 7200);
    ctx.log(out.trim());
    if (!/result=[23]/.test(out) && !/nothing to install/.test(out)) failed.push('Windows Update');
  }
  wg.forEach(function (id) {
    if (!/^[A-Za-z0-9._+-]+$/.test(id)) { failed.push(id); return; }
    ctx.log('winget: upgrading ' + id);
    var r = ctx.ssh.exec(WINGET + "& $w upgrade --id '" + id + "' --exact --silent --accept-package-agreements --accept-source-agreements --disable-interactivity 2>&1 | Out-String -Width 300; exit $LASTEXITCODE", { timeout: 1800 });
    ctx.log(r.stdout.trim().split(/\r?\n/).slice(-5).join('\n'));
    if (r.code !== 0) failed.push(id);
  });
  if (failed.length) throw new Error('Not updated: ' + failed.join(', '));
  return { windowsUpdate: wu.length, winget: wg.length };
}

function metrics(ctx, opts) {
  var j = json(ps(ctx, "$c = (Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average; " +
    "$o = Get-CimInstance Win32_OperatingSystem; " +
    "$d = Get-CimInstance Win32_LogicalDisk -Filter 'DriveType=3' | ForEach-Object { @{ mount = $_.DeviceID; usedPct = [math]::Round(100 - 100 * $_.FreeSpace / $_.Size); sizeGB = [math]::Round($_.Size / 1GB) } }; " +
    "$h = @(); if (" + (opts && opts.smart ? '$true' : '$false') + ") { $h = Get-PhysicalDisk | ForEach-Object { @{ dev = $_.FriendlyName; model = $_.Model; healthy = ($_.HealthStatus -eq 'Healthy') } } }; " +
    "[pscustomobject]@{ cpu = $c; memTotalMB = [math]::Round($o.TotalVisibleMemorySize / 1024); memUsedMB = [math]::Round(($o.TotalVisibleMemorySize - $o.FreePhysicalMemory) / 1024); disks = @($d); smart = @($h) } | ConvertTo-Json -Depth 4 -Compress", 120))[0] || {};
  j.memPct = j.memTotalMB ? Math.round(100 * j.memUsedMB / j.memTotalMB) : null;
  j.temps = [];
  return j;
}

// ---- your own update scripts on the share (the DeployDeck scripts)

function q(s) { return "'" + String(s).replace(/'/g, "''") + "'"; }

// The share login travels on stdin, so it never shows up in a process list.
function shareSession(ctx) {
  var login = ctx.secret('shareLogin');
  var path = setting(ctx, 'scriptsPath', '');
  var root = (/^(\\\\[^\\]+\\[^\\]+)/.exec(path) || [])[1];
  if (!login || !root) return { prefix: '', stdin: '' };
  return {
    prefix: "$hwpw = [Console]::In.ReadLine(); $hwc = New-Object System.Management.Automation.PSCredential(" + q(login.username) +
      ", (ConvertTo-SecureString $hwpw -AsPlainText -Force)); New-PSDrive -Name HWShare -PSProvider FileSystem -Root " + q(root) +
      " -Credential $hwc -ErrorAction Stop | Out-Null; ",
    stdin: login.secret + '\n'
  };
}

function scriptComponents(ctx) {
  var path = setting(ctx, 'scriptsPath', '');
  var state = setting(ctx, 'statePath', '') || (path.replace(/\\+$/, '') + '\\Log\\State');
  var sess = shareSession(ctx);
  var key = setting(ctx, 'hostKey', '');
  var cmd = sess.prefix + "$key = " + q(key) + "; $src = 'setting'; $why = ''; if (-not $key) { $src = 'GetPcByUUID.ps1'; " +
    "try { $key = & (Join-Path " + q(path) + " 'GetPcByUUID.ps1') 2>$null | Select-Object -Last 1 } catch { $why = $_.Exception.Message } ; " +
    "if (-not $key -or $key -eq 'Unknown') { $src = 'hardware UUID'; $key = (Get-CimInstance Win32_ComputerSystemProduct).UUID.ToUpper() } }; " +
    "$recs = Get-ChildItem -LiteralPath " + q(state) + " -Filter *.json -ErrorAction Stop | ForEach-Object { try { Get-Content -LiteralPath $_.FullName -Raw | ConvertFrom-Json } catch {} }; " +
    "[pscustomobject]@{ key = $key; src = $src; why = $why; records = @($recs | Select-Object script, program, localVersion, onlineVersion, lastCheck, lastCheckResult, installedByHost) } | ConvertTo-Json -Depth 6 -Compress";
  var r = ctx.ssh.exec(cmd, { timeout: 120, stdin: sess.stdin });
  if (r.code !== 0 || !r.stdout.trim()) throw new Error((r.stderr || 'cannot read the state folder').trim().split('\n').slice(-2).join(' '));
  var data = JSON.parse(r.stdout.trim());
  var out = [];
  var behind = 0, missing = 0;
  (data.records || []).forEach(function (rec) {
    if (!rec || !rec.script) return;
    var mine = rec.installedByHost ? rec.installedByHost[data.key] : undefined;
    if (typeof mine === 'string') mine = mine.trim();
    var share = String(rec.localVersion || '').trim();
    rec.onlineVersion = String(rec.onlineVersion || '').trim();
    if (mine === undefined || mine === null) return; // never ran here
    if (mine === '') { missing++; return; }        // not installed on this PC
    var upd = !!share && ctx.util.compareVersions(share, mine) > 0;
    if (upd) behind++;
    out.push({ id: 'ps:' + rec.script, name: rec.program || rec.script, group: 'app', installed: mine, available: upd ? share : '',
      updateAvailable: upd, meta: { script: rec.script, summary: 'script' } });
    if (setting(ctx, 'showShare', false) && rec.onlineVersion && share && ctx.util.compareVersions(rec.onlineVersion, share) > 0) {
      out.push({ id: 'share:' + rec.script, name: (rec.program || rec.script) + ' (share)', group: 'package', installed: share,
        available: rec.onlineVersion, updateAvailable: true, meta: { script: rec.script, note: 'installer on the share is behind the vendor' } });
    }
  });
  ctx.log('Update scripts: PC key ' + data.key + ' (from ' + data.src + (data.why ? ', GetPcByUUID.ps1 failed: ' + data.why : '') + '), ' + out.length + ' programs tracked here, ' + behind + ' behind the share, ' + missing + ' not installed.');
  return out;
}

function runScript(ctx, script) {
  if (!/^[A-Za-z0-9+._-]+Update\.ps1$/.test(script)) throw new Error('Unexpected script name ' + script);
  var path = setting(ctx, 'scriptsPath', '');
  var sess = shareSession(ctx);
  ctx.log('Running ' + script + ' on this PC');
  var r = ctx.ssh.exec(sess.prefix + "& (Join-Path " + q(path) + " " + q(script) + ") *>&1 | Out-String -Width 300; exit $LASTEXITCODE",
    { timeout: 3600, stdin: sess.stdin });
  var lines = (r.stdout + '\n' + r.stderr).split(/\r?\n/);
  var result = lines.filter(function (l) { return l.indexOf('##deploydeck:') === 0; }).pop();
  ctx.log(lines.filter(function (l) { return l.trim() && l.indexOf('##deploydeck:') !== 0; }).slice(-25).join('\n'));
  if (result) {
    try {
      var res = JSON.parse(result.slice('##deploydeck:'.length));
      ctx.log(script + ': share ' + (res.local || '?') + ', vendor ' + (res.online || '?') + (res.updated ? ', updated' : ''));
    } catch (e) {}
  }
  if (r.code !== 0) throw new Error(script + ' exited with code ' + r.code);
}
