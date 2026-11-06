// Docker host integration for Homewarden.
//
// ctx.docker.request(method, path, body) talks to the Docker Engine API of this host.
// ctx.http.request(...) reaches the registries listed in the manifest (plus the
// user's extra registries) to compare image digests.

var ACCEPT = [
  'application/vnd.oci.image.index.v1+json',
  'application/vnd.docker.distribution.manifest.list.v2+json',
  'application/vnd.docker.distribution.manifest.v2+json',
  'application/vnd.oci.image.manifest.v1+json'
].join(', ');

function setting(ctx, key, def) {
  var v = ctx.settings[key];
  return v === undefined || v === null || v === '' ? def : v;
}

function dock(ctx, method, path, body) {
  var r = ctx.docker.request(method, path, body);
  if (r.status >= 400) {
    var msg = (r.json && r.json.message) || r.body;
    throw new Error('Docker ' + method + ' ' + path.split('?')[0] + ': ' + r.status + ' ' + msg);
  }
  return r;
}

// "lscr.io/linuxserver/sonarr:latest" -> {domain, registry, repo, tag, name}
function parseRef(image) {
  var ref = image.split('@')[0];
  var tag = 'latest';
  var slash = ref.lastIndexOf('/');
  var colon = ref.lastIndexOf(':');
  if (colon > slash) { tag = ref.slice(colon + 1); ref = ref.slice(0, colon); }
  var parts = ref.split('/');
  var domain = 'docker.io';
  if (parts.length > 1 && (parts[0].indexOf('.') >= 0 || parts[0].indexOf(':') >= 0 || parts[0] === 'localhost')) {
    domain = parts.shift();
  }
  var repo = parts.join('/');
  if (domain === 'docker.io' && repo.indexOf('/') < 0) repo = 'library/' + repo;
  var registry = domain === 'docker.io' ? 'registry-1.docker.io' : domain;
  var name = (domain === 'docker.io' ? '' : domain + '/') + (repo.indexOf('library/') === 0 && domain === 'docker.io' ? repo.slice(8) : repo);
  return { domain: domain, registry: registry, repo: repo, tag: tag, name: name, full: name + ':' + tag };
}

var tokens = {};

// Registry request with the anonymous Bearer token dance most registries use.
function reg(ctx, ref, method, path, accept) {
  var key = ref.registry + '/' + ref.repo;
  var headers = { Accept: accept || ACCEPT };
  if (tokens[key]) headers.Authorization = 'Bearer ' + tokens[key];
  var url = 'https://' + ref.registry + '/v2/' + ref.repo + path;
  var r = ctx.http.request({ method: method, url: url, headers: headers, timeout: 30 });
  if (r.status === 401 && !tokens[key]) {
    var h = r.headers['www-authenticate'] || '';
    var realm = /realm="([^"]+)"/.exec(h), service = /service="([^"]+)"/.exec(h), scope = /scope="([^"]+)"/.exec(h);
    if (!realm) throw new Error(ref.registry + ' wants a login, which is not supported yet');
    var turl = realm[1] + '?scope=' + encodeURIComponent(scope ? scope[1] : 'repository:' + ref.repo + ':pull') + (service ? '&service=' + encodeURIComponent(service[1]) : '');
    var t = ctx.http.request({ url: turl, timeout: 30 });
    if (t.status !== 200 || !t.json) throw new Error('token from ' + realm[1] + ': HTTP ' + t.status);
    tokens[key] = t.json.token || t.json.access_token;
    return reg(ctx, ref, method, path, accept);
  }
  return r;
}

function remoteDigest(ctx, ref) {
  var r = reg(ctx, ref, 'HEAD', '/manifests/' + ref.tag);
  if (r.status === 404) throw new Error('tag ' + ref.tag + ' no longer exists in ' + ref.name);
  if (r.status === 401 || r.status === 403) throw new Error(ref.name + ' is not on ' + ref.domain + ' or needs a login (locally built images cannot be checked)');
  if (r.status !== 200) throw new Error(ref.registry + ': HTTP ' + r.status);
  var d = r.headers['docker-content-digest'];
  if (!d) {
    var g = reg(ctx, ref, 'GET', '/manifests/' + ref.tag);
    d = g.headers['docker-content-digest'];
  }
  return d || '';
}

function versionFromLabels(labels) {
  if (!labels) return '';
  var v = labels['org.opencontainers.image.version'] || labels['version'] || '';
  var b = /version:-\s*(\S+)/.exec(labels['build_version'] || '');
  if (b) v = b[1]; // linuxserver.io images carry the app version here
  return v;
}

// Version of the newest image for our architecture, read from its config labels.
function remoteVersion(ctx, ref, arch) {
  try {
    var m = reg(ctx, ref, 'GET', '/manifests/' + ref.tag).json;
    if (m && m.manifests) {
      var pick = m.manifests.filter(function (x) { return x.platform && x.platform.os === 'linux' && x.platform.architecture === arch; })[0];
      if (!pick) return '';
      m = reg(ctx, ref, 'GET', '/manifests/' + pick.digest).json;
    }
    if (!m || !m.config) return '';
    var cfg = reg(ctx, ref, 'GET', '/blobs/' + m.config.digest, 'application/json').json;
    return cfg && cfg.config ? versionFromLabels(cfg.config.Labels) : '';
  } catch (e) {
    return '';
  }
}

var SEMVER = /^(v?)(\d+)(\.\d+){1,3}$/;

function newerTag(ctx, ref) {
  var m = SEMVER.exec(ref.tag);
  if (!m) return '';
  var r = reg(ctx, ref, 'GET', '/tags/list?n=1000', 'application/json');
  if (r.status !== 200 || !r.json || !r.json.tags) return '';
  var best = ref.tag;
  r.json.tags.forEach(function (t) {
    var n = SEMVER.exec(t);
    if (!n || n[1] !== m[1] || n[2] !== m[2]) return; // same "v" style, same major
    if (t.split('.').length !== ref.tag.split('.').length) return;
    if (ctx.util.compareVersions(t, best) > 0) best = t;
  });
  return best !== ref.tag ? best : '';
}

function containers(ctx) {
  return dock(ctx, 'GET', '/containers/json?all=1').json || [];
}

function isSelf(ctx, c) {
  var h = (ctx.self && ctx.self.hostname) || '';
  return h.length >= 12 && c.Id.indexOf(h) === 0;
}

function shortDigest(d) { return d ? d.replace('sha256:', '').slice(0, 12) : ''; }

function detect(ctx) {
  var ver = dock(ctx, 'GET', '/version').json;
  var list = containers(ctx);
  var arch = ver.Arch === 'aarch64' ? 'arm64' : ver.Arch;
  var images = {};
  var checked = {};
  var comps = [];
  var updates = 0;

  list.forEach(function (c) {
    var name = (c.Names[0] || c.Id).replace(/^\//, '');
    var imageRef = c.Image;
    if (imageRef.indexOf('sha256:') === 0) {
      imageRef = dock(ctx, 'GET', '/containers/' + c.Id + '/json').json.Config.Image;
    }
    if (!images[c.ImageID]) images[c.ImageID] = dock(ctx, 'GET', '/images/' + c.ImageID + '/json').json;
    var img = images[c.ImageID];
    var ref = parseRef(imageRef);
    var self = isSelf(ctx, c);
    var comp = {
      id: 'ctr:' + name, name: name, group: 'container',
      installed: versionFromLabels(img.Config && img.Config.Labels) || ref.tag,
      available: '', updateAvailable: false, updatable: !self,
      meta: { image: ref.full, state: c.State, digest: shortDigest((img.RepoDigests || [])[0] ? img.RepoDigests[0].split('@')[1] : ''),
        warning: self ? 'self' : 'restarts-container', unraid: !!(c.Labels && c.Labels['net.unraid.docker.managed']) }
    };
    if (!img.RepoDigests || img.RepoDigests.length === 0) {
      comp.meta.note = 'local image';
      comps.push(comp);
      return;
    }
    try {
      if (!checked[ref.full]) {
        var d = remoteDigest(ctx, ref);
        checked[ref.full] = { digest: d };
      }
      var remote = checked[ref.full].digest;
      var current = img.RepoDigests.some(function (rd) { return rd.split('@')[1] === remote; });
      if (!current && remote) {
        if (checked[ref.full].version === undefined) checked[ref.full].version = remoteVersion(ctx, ref, arch);
        comp.available = checked[ref.full].version || ('new image ' + shortDigest(remote));
        comp.updateAvailable = true;
        updates++;
      }
      if (setting(ctx, 'checkTags', true)) {
        if (checked[ref.full].tag === undefined) checked[ref.full].tag = newerTag(ctx, ref);
        if (checked[ref.full].tag) {
          comps.push({ id: 'tag:' + name, name: name + ': ' + ref.tag + ' → ' + checked[ref.full].tag, group: 'image',
            installed: ref.tag, available: checked[ref.full].tag, updateAvailable: true, updatable: false,
            meta: { image: ref.full, note: 'pinned tag' } });
        }
      }
    } catch (e) {
      comp.meta.error = String(e.message || e);
      ctx.log(name + ': ' + comp.meta.error);
    }
    comps.push(comp);
  });

  var dangling = dock(ctx, 'GET', '/images/json?filters=' + encodeURIComponent('{"dangling":["true"]}')).json || [];
  if (dangling.length) {
    var mb = Math.round(dangling.reduce(function (a, i) { return a + (i.Size || 0); }, 0) / 1048576);
    comps.push({ id: 'cleanup', name: 'Unused images', group: 'action', installed: dangling.length + ' images', available: mb + ' MB', updateAvailable: true, meta: { summary: mb + ' MB' } });
  }

  ctx.log(list.length + ' containers, ' + updates + ' with a newer image.');
  return {
    facts: { docker: ver.Version, api: ver.ApiVersion, os: ver.Os + '/' + ver.Arch, containers: list.length,
      running: list.filter(function (c) { return c.State === 'running'; }).length, unusedImages: dangling.length },
    components: comps
  };
}

function discover(ctx) {
  return containers(ctx).map(function (c) {
    var name = (c.Names[0] || c.Id).replace(/^\//, '');
    return { externalId: name, name: name, host: '', kind: 'generic', state: c.State === 'running' ? 'running' : 'stopped' };
  });
}

function recreate(ctx, name) {
  var old = dock(ctx, 'GET', '/containers/' + encodeURIComponent(name) + '/json').json;
  if (isSelf(ctx, { Id: old.Id })) throw new Error(name + ' is Homewarden itself; update it from the host');
  var ref = parseRef(old.Config.Image);
  ctx.log(name + ': pulling ' + ref.full);
  var pull = dock(ctx, 'POST', '/images/create?fromImage=' + encodeURIComponent((ref.domain === 'docker.io' ? '' : ref.domain + '/') + ref.repo) + '&tag=' + encodeURIComponent(ref.tag));
  var errLine = /"error":\s*"([^"]+)"/.exec(pull.body);
  if (errLine) throw new Error('pull failed: ' + errLine[1]);
  var img = dock(ctx, 'GET', '/images/' + encodeURIComponent(old.Config.Image) + '/json').json;
  if (img.Id === old.Image) {
    ctx.log(name + ': already on the newest image');
    return;
  }
  var wasRunning = old.State.Running;
  var safety = setting(ctx, 'safetyNet', false);
  var tmp = name + '-hw-old';
  if (wasRunning) { ctx.log(name + ': stopping'); dock(ctx, 'POST', '/containers/' + old.Id + '/stop?t=30'); }
  if (safety) {
    dock(ctx, 'POST', '/containers/' + old.Id + '/rename?name=' + encodeURIComponent(tmp));
  } else {
    // Same as Unraid's own update: the old container goes, the new one replaces it.
    dock(ctx, 'DELETE', '/containers/' + old.Id);
  }

  var cfg = JSON.parse(JSON.stringify(old.Config));
  if (cfg.Hostname && old.Id.indexOf(cfg.Hostname) === 0) delete cfg.Hostname; // auto-assigned, not a user setting
  var nets = old.NetworkSettings.Networks || {};
  var names = Object.keys(nets);
  var mode = old.HostConfig.NetworkMode || '';
  var endpoint = function (n) {
    var e = nets[n];
    return { IPAMConfig: e.IPAMConfig, Links: e.Links, Aliases: (e.Aliases || []).filter(function (a) { return old.Id.indexOf(a) !== 0; }) };
  };
  cfg.HostConfig = old.HostConfig;
  var first = null;
  if (names.length && mode.indexOf('container:') !== 0 && mode !== 'host') {
    first = names.indexOf(mode) >= 0 ? mode : names[0];
    cfg.NetworkingConfig = { EndpointsConfig: {} };
    cfg.NetworkingConfig.EndpointsConfig[first] = endpoint(first);
  }

  // Creates and starts a container from cfg with the given image; returns its id.
  function launch(image) {
    var c = JSON.parse(JSON.stringify(cfg));
    c.Image = image;
    var created = dock(ctx, 'POST', '/containers/create?name=' + encodeURIComponent(name), c).json;
    try {
      names.forEach(function (n) {
        if (n === first || mode.indexOf('container:') === 0 || mode === 'host') return;
        dock(ctx, 'POST', '/networks/' + encodeURIComponent(n) + '/connect', { Container: created.Id, EndpointConfig: endpoint(n) });
      });
      if (wasRunning) {
        dock(ctx, 'POST', '/containers/' + created.Id + '/start');
        ctx.sleep(5000);
        var st = dock(ctx, 'GET', '/containers/' + created.Id + '/json').json.State;
        if (!st.Running) throw new Error('the new container stopped right away (exit code ' + st.ExitCode + ')');
      }
    } catch (e) {
      ctx.docker.request('DELETE', '/containers/' + created.Id + '?force=1');
      throw e;
    }
    return created.Id;
  }

  try {
    ctx.log(name + ': creating the new container');
    launch(old.Config.Image);
  } catch (e) {
    var why = String(e.message || e);
    if (safety) {
      ctx.log(name + ': ' + why + '. Restoring the old container.');
      dock(ctx, 'POST', '/containers/' + old.Id + '/rename?name=' + encodeURIComponent(name));
      if (wasRunning) dock(ctx, 'POST', '/containers/' + old.Id + '/start');
      throw new Error(name + ': update rolled back: ' + why);
    }
    // No safety net: the old container is gone, but its image is still there.
    ctx.log(name + ': ' + why + '. Recreating it from the previous image.');
    try {
      launch(old.Image);
      throw new Error(name + ': new image failed (' + why + '); running the previous image again');
    } catch (e2) {
      if (String(e2.message).indexOf('running the previous image again') >= 0) throw e2;
      throw new Error(name + ': update failed and the previous image did not start either: ' + why);
    }
  }
  if (!safety) {
    ctx.log(name + ': updated');
  } else if (setting(ctx, 'removeOld', true)) {
    dock(ctx, 'DELETE', '/containers/' + old.Id);
    ctx.log(name + ': updated, old container removed');
  } else {
    ctx.log(name + ': updated, old container kept as ' + tmp);
  }
}

function update(ctx, ids) {
  var targets = [];
  var cleanup = ids.indexOf('cleanup') >= 0;
  if (ids.indexOf('*') >= 0) {
    detect(ctx).components.forEach(function (c) {
      if (c.group === 'container' && c.updateAvailable && c.updatable) targets.push(c.name);
    });
  } else {
    ids.forEach(function (id) { if (id.indexOf('ctr:') === 0) targets.push(id.slice(4)); });
  }
  var failed = [];
  targets.forEach(function (name) {
    try { recreate(ctx, name); } catch (e) { failed.push(name); ctx.log(String(e.message || e)); }
  });
  if (cleanup) {
    var r = dock(ctx, 'POST', '/images/prune?filters=' + encodeURIComponent('{"dangling":["true"]}')).json;
    ctx.log('Removed ' + ((r.ImagesDeleted || []).length) + ' unused images, freed ' + Math.round((r.SpaceReclaimed || 0) / 1048576) + ' MB.');
  }
  if (failed.length) throw new Error('Not updated: ' + failed.join(', '));
  return { updated: targets.length - failed.length };
}
