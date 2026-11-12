// Gitea / Forgejo integration for Homewarden (beta).
//
// Lists open pull requests (Renovate, Dependabot and your own) across the repositories
// the token can see, plus open PRs of chosen GitHub repositories. Shown under
// "Waiting for you" on the overview. The Gitea version is compared with the newest
// release (notify only; the container is updated through the Docker integration).

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function base(ctx) {
  var https = !!setting(ctx, 'https', false);
  return (https ? 'https' : 'http') + '://' + ctx.device.host + ':' + setting(ctx, 'port', https ? 443 : 3000);
}

var BOTS = /renovate|dependabot|bot\b|\[bot\]/i;

function gitea(ctx, path) {
  var cred = ctx.secret('token');
  if (!cred) throw new Error('Pick the Gitea access token in the integration setup.');
  var r = ctx.http.request({ method: 'GET', url: base(ctx) + '/api/v1' + path, insecure: true, timeout: 30,
    headers: { Authorization: 'token ' + cred.secret, Accept: 'application/json' } });
  if (r.status === 401 || r.status === 403) throw new Error('Gitea refused the token (needs read:repository and read:issue).');
  if (r.status >= 400) throw new Error('Gitea GET ' + path + ': HTTP ' + r.status);
  return r.json;
}

function github(ctx, path) {
  var headers = { Accept: 'application/vnd.github+json', 'User-Agent': 'Homewarden' };
  var cred = ctx.secret('githubToken');
  if (cred) headers.Authorization = 'Bearer ' + cred.secret;
  var r = ctx.http.request({ method: 'GET', url: 'https://api.github.com' + path, timeout: 30, headers: headers });
  if (r.status >= 400) throw new Error('GitHub GET ' + path + ': HTTP ' + r.status);
  return r.json;
}

function detect(ctx) {
  var ver = (gitea(ctx, '/version') || {}).version || '';
  var prs = [];
  // Issues search across every repository the token can read.
  for (var page = 1; page <= 5; page++) {
    var list = gitea(ctx, '/repos/issues/search?type=pulls&state=open&limit=50&page=' + page) || [];
    list.forEach(function (i) {
      var user = (i.user && i.user.login) || '';
      prs.push({ forge: 'gitea', repo: (i.repository && i.repository.full_name) || '', number: i.number, title: i.title, author: user,
        bot: BOTS.test(user), url: i.html_url, created: Math.floor(Date.parse(i.created_at) / 1000) || 0 });
    });
    if (list.length < 50) break;
  }
  String(setting(ctx, 'githubRepos', '')).split(',').map(function (s) { return s.trim(); }).filter(Boolean).forEach(function (repo) {
    if (!/^[\w.-]+\/[\w.-]+$/.test(repo)) { ctx.log('Skipping "' + repo + '": use owner/repo'); return; }
    try {
      (github(ctx, '/repos/' + repo + '/pulls?state=open&per_page=50') || []).forEach(function (p) {
        var user = (p.user && p.user.login) || '';
        prs.push({ forge: 'github', repo: repo, number: p.number, title: p.title, author: user, bot: BOTS.test(user) || (p.user && p.user.type === 'Bot'),
          url: p.html_url, created: Math.floor(Date.parse(p.created_at) / 1000) || 0 });
      });
    } catch (e) { ctx.log(repo + ': ' + (e.message || e)); }
  });
  prs.sort(function (a, b) { return b.created - a.created; });
  var facts = { version: ver, openPRs: prs.length, botPRs: prs.filter(function (p) { return p.bot; }).length, pullRequests: prs.slice(0, 100) };

  var comp = { id: 'app', name: /forgejo/i.test(ver) ? 'Forgejo' : 'Gitea', group: 'app', installed: ver.replace(/\+.*$/, ''), available: '',
    updateAvailable: false, updatable: false, meta: { note: 'Update the container through the Docker integration.' } };
  if (!/forgejo/i.test(ver) && setting(ctx, 'releaseCheck', true)) {
    try {
      var rel = github(ctx, '/repos/go-gitea/gitea/releases/latest');
      if (rel && rel.tag_name) {
        comp.available = String(rel.tag_name).replace(/^v/, '');
        comp.updateAvailable = !!comp.installed && ctx.util.compareVersions(comp.available, comp.installed) > 0;
        comp.meta.changelog = rel.html_url;
      }
    } catch (e) { ctx.log('Release check: ' + (e.message || e)); }
  }
  ctx.log('Gitea ' + (ver || '?') + ': ' + prs.length + ' open pull requests (' + facts.botPRs + ' from bots).');
  return { facts: facts, components: [comp] };
}
