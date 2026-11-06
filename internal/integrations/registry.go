package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"

	"github.com/KrX3D/Homewarden/internal/store"
)

// BuiltinSource names the copies shipped inside the image.
const BuiltinSource = "builtin"

// DefaultSource is the public catalog on GitHub.
const DefaultSource = "https://raw.githubusercontent.com/KrX3D/Homewarden/main/integrations/"

// Integration is a loaded, compiled integration.
type Integration struct {
	Manifest
	Source  string            `json:"source"`
	IconSVG string            `json:"iconSvg,omitempty"`
	files   map[string]string // file name -> content
	prog    *goja.Program
}

// Script returns the entry script text (for review and diffs).
func (i *Integration) Script() string { return i.files[i.Entry] }

// Registry holds installed integrations.
type Registry struct {
	dir     string
	st      *store.Store
	builtin fs.FS
	client  *http.Client

	mu     sync.RWMutex
	loaded map[string]*Integration
}

// NewRegistry returns a registry storing integrations under dataDir/integrations.
func NewRegistry(dataDir string, st *store.Store, builtin fs.FS) *Registry {
	return &Registry{
		dir: filepath.Join(dataDir, "integrations"), st: st, builtin: builtin,
		client: &http.Client{Timeout: 30 * time.Second}, loaded: map[string]*Integration{},
	}
}

func compile(m Manifest, files map[string]string, source string) (*Integration, error) {
	prog, err := goja.Compile(m.ID+"/"+m.Entry, files[m.Entry], true)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", m.ID, err)
	}
	return &Integration{Manifest: m, Source: source, files: files, prog: prog, IconSVG: files[m.Icon]}, nil
}

// readPackage reads manifest + files through a fetch function.
func readPackage(fetch func(name string) ([]byte, error)) (Manifest, map[string]string, error) {
	mb, err := fetch("manifest.json")
	if err != nil {
		return Manifest{}, nil, err
	}
	m, err := ParseManifest(mb)
	if err != nil {
		return m, nil, err
	}
	files := map[string]string{"manifest.json": string(mb)}
	for _, f := range m.Files {
		b, err := fetch(f)
		if err != nil {
			return m, nil, fmt.Errorf("%s: %w", f, err)
		}
		files[f] = string(b)
	}
	if m.Icon != "" {
		if _, ok := files[m.Icon]; !ok {
			if b, err := fetch(m.Icon); err == nil {
				files[m.Icon] = string(b)
			}
		}
	}
	return m, files, nil
}

// Load reads every installed integration from disk. A broken one is logged and
// skipped, so one bad integration cannot stop Homewarden from starting.
func (r *Registry) Load(ctx context.Context) error {
	rows, err := r.st.InstalledIntegrations(ctx)
	if err != nil {
		return err
	}
	loaded := map[string]*Integration{}
	for _, row := range rows {
		dir := filepath.Join(r.dir, row.ID, row.Version)
		m, files, err := readPackage(func(name string) ([]byte, error) { return os.ReadFile(filepath.Join(dir, name)) })
		if err != nil {
			log.Printf("integration %s %s: %v", row.ID, row.Version, err)
			continue
		}
		in, err := compile(m, files, row.Source)
		if err != nil {
			log.Printf("integration %s: %v", row.ID, err)
			continue
		}
		loaded[row.ID] = in
	}
	r.mu.Lock()
	r.loaded = loaded
	r.mu.Unlock()
	return nil
}

// Get returns a loaded integration.
func (r *Registry) Get(id string) (*Integration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	in, ok := r.loaded[id]
	return in, ok
}

// All returns the loaded integrations sorted by id.
func (r *Registry) All() []*Integration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Integration, 0, len(r.loaded))
	for _, in := range r.loaded {
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ---- catalog

type indexFile struct {
	Integrations []struct {
		ID      string `json:"id"`
		Version string `json:"version"`
		Path    string `json:"path"`
	} `json:"integrations"`
}

// CatalogEntry is one installable integration from one source.
type CatalogEntry struct {
	Source      string      `json:"source"`
	ID          string      `json:"id"`
	Version     string      `json:"version"`
	Name        Text        `json:"name"`
	Description Text        `json:"description"`
	Permissions Permissions `json:"permissions"`
	IconSVG     string      `json:"iconSvg,omitempty"`
	path        string
}

// SourceError reports a catalog source that could not be read.
type SourceError struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

func (r *Registry) fetcher(ctx context.Context, source, sub string) func(string) ([]byte, error) {
	if source == BuiltinSource {
		return func(name string) ([]byte, error) { return fs.ReadFile(r.builtin, path.Join(sub, name)) }
	}
	base := strings.TrimSuffix(source, "/") + "/"
	if sub != "" {
		base += strings.Trim(sub, "/") + "/"
	}
	return func(name string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		resp, err := r.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", base+name, resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	}
}

func (r *Registry) sourceEntries(ctx context.Context, source string) ([]CatalogEntry, error) {
	idxb, err := r.fetcher(ctx, source, "")("index.json")
	if err != nil {
		return nil, err
	}
	var idx indexFile
	if err := json.Unmarshal(idxb, &idx); err != nil {
		return nil, fmt.Errorf("index.json: %w", err)
	}
	out := []CatalogEntry{}
	for _, e := range idx.Integrations {
		if !idRe.MatchString(e.ID) || strings.Contains(e.Path, "..") {
			continue
		}
		mb, err := r.fetcher(ctx, source, e.Path)("manifest.json")
		if err != nil {
			continue
		}
		m, err := ParseManifest(mb)
		if err != nil || m.ID != e.ID {
			continue
		}
		ce := CatalogEntry{Source: source, ID: m.ID, Version: m.Version, Name: m.Name, Description: m.Description, Permissions: m.Permissions, path: e.Path}
		if m.Icon != "" {
			if b, err := r.fetcher(ctx, source, e.Path)(m.Icon); err == nil {
				ce.IconSVG = string(b)
			}
		}
		out = append(out, ce)
	}
	return out, nil
}

// Catalog lists what each source offers. Unreachable sources are reported, not fatal.
func (r *Registry) Catalog(ctx context.Context, sources []string) ([]CatalogEntry, []SourceError) {
	all := []CatalogEntry{}
	errs := []SourceError{}
	for _, src := range append([]string{BuiltinSource}, sources...) {
		entries, err := r.sourceEntries(ctx, src)
		if err != nil {
			errs = append(errs, SourceError{Source: src, Error: err.Error()})
			continue
		}
		all = append(all, entries...)
	}
	return all, errs
}

// Candidate fetches (without installing) the package a source offers for id.
func (r *Registry) Candidate(ctx context.Context, source, id string) (*Integration, string, error) {
	entries, err := r.sourceEntries(ctx, source)
	if err != nil {
		return nil, "", err
	}
	for _, e := range entries {
		if e.ID != id {
			continue
		}
		m, files, err := readPackage(r.fetcher(ctx, source, e.path))
		if err != nil {
			return nil, "", err
		}
		in, err := compile(m, files, source)
		return in, e.path, err
	}
	return nil, "", fmt.Errorf("%s does not offer %s", source, id)
}

// Install fetches an integration from a source, writes it to the data folder under
// its exact version, records it and makes it active.
func (r *Registry) Install(ctx context.Context, source, id string) (*Integration, error) {
	in, _, err := r.Candidate(ctx, source, id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(r.dir, in.ID, in.Version)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	for name, content := range in.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o640); err != nil {
			return nil, err
		}
	}
	if err := r.st.SaveIntegration(ctx, store.InstalledIntegration{ID: in.ID, Version: in.Version, Source: source, Enabled: true}); err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.loaded[in.ID] = in
	r.mu.Unlock()
	return in, nil
}

// ErrInUse means devices still use the integration.
var ErrInUse = errors.New("integration in use")

// Uninstall removes an integration that no device uses. Its files stay on disk so a
// reinstall of the same version works offline.
func (r *Registry) Uninstall(ctx context.Context, id string) error {
	devs, err := r.st.Devices(ctx, "")
	if err != nil {
		return err
	}
	for _, d := range devs {
		if d.Kind == id {
			return ErrInUse
		}
	}
	if err := r.st.RemoveIntegration(ctx, id); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.loaded, id)
	r.mu.Unlock()
	return nil
}
