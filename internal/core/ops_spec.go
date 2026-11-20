package core

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/starters"
	"github.com/brian-nunez/breadboard/internal/state"
)

// InitRequest creates an experiment.
type InitRequest struct {
	Dir  string // project directory; created if missing
	Name string // experiment name; empty derives it from the directory
}

// InitResult reports what Init created.
type InitResult struct {
	Dir          string
	Name         string
	SpecCreated  bool // false when experiment.yaml already existed
	StateCreated bool // .bb/ was created
	HomeCreated  bool
	CACreated    bool
	CAPath       string
}

// Init creates experiment.yaml and .bb/ in Dir, and BB_HOME with the CA when
// missing. On a directory that already has experiment.yaml (a fresh clone,
// for example) it only restores .bb/.
func (e *Engine) Init(ctx context.Context, req InitRequest) (*InitResult, error) {
	dir, err := filepath.Abs(orDot(req.Dir))
	if err != nil {
		return nil, err
	}

	res := &InitResult{Dir: dir}

	specPath := filepath.Join(dir, spec.FileName)
	if data, err := os.ReadFile(specPath); err == nil {
		doc, err := spec.Parse(data)
		if err != nil {
			return nil, err
		}

		s, err := decodeAndValidate(doc)
		if err != nil {
			return nil, err
		}

		res.Name = s.Name
	} else if isNotExist(err) {
		name := req.Name
		if name == "" {
			name = deriveName(filepath.Base(dir))
		}

		if !spec.IsExperimentOK(name) {
			return nil, spec.Invalid("name", "%q must be a lowercase DNS label (a-z, 0-9, -) of at most 40 characters", name)
		}

		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}

		data, err := spec.New(name).Bytes()
		if err != nil {
			return nil, err
		}

		if err := state.WriteFile(specPath, data, 0o644); err != nil {
			return nil, err
		}

		res.Name, res.SpecCreated = name, true
	} else {
		return nil, err
	}

	p := state.Project{Dir: dir}
	_, statErr := os.Stat(p.StateDir())
	res.StateCreated = isNotExist(statErr)
	lock, err := p.Lock("init")
	if err != nil {
		return nil, err
	}

	defer func() { _ = lock.Release() }()

	if _, err = p.Ensure(e.version); err != nil {
		return nil, err
	}

	if res.HomeCreated, err = e.home.Ensure(); err != nil {
		return nil, err
	}

	st, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	if _, res.CACreated, err = e.ensureCA(st.Domain); err != nil {
		return nil, err
	}

	res.CAPath = e.home.CACertPath(caName)

	return res, nil
}

// deriveName turns a directory name into a valid experiment name.
func deriveName(base string) string {
	var b strings.Builder

	for _, r := range strings.ToLower(base) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.' || r == ' ':
			b.WriteRune('-')
		}
	}

	name := strings.Trim(b.String(), "-")
	if len(name) > 40 {
		name = strings.Trim(name[:40], "-")
	}

	if name == "" {
		name = "experiment"
	}

	return name
}

func orDot(dir string) string {
	if dir == "" {
		return "."
	}

	return dir
}

// CatalogEntry summarises an infra service type.
type CatalogEntry struct {
	Type      string
	Title     string
	Summary   string
	Image     string
	Ports     []int
	Memory    string
	CPUs      string
	Plaintext bool // whether an edge may turn TLS off
	// Env names the variables an app that uses this type receives.
	Env []string
}

// CatalogResult lists the catalog.
type CatalogResult struct {
	Entries []CatalogEntry
}

// Catalog lists every infra service type.
func (e *Engine) Catalog(_ context.Context) (*CatalogResult, error) {
	res := &CatalogResult{}

	for _, en := range catalog.All() {
		ce := CatalogEntry{Type: en.ID, Title: en.Title, Summary: en.Summary, Image: en.Image, Memory: en.Resources.Memory, CPUs: en.Resources.CPUs, Plaintext: en.SupportsPlaintext()}
		for _, l := range en.Listeners {
			ce.Ports = append(ce.Ports, l.Port)
		}

		for _, v := range en.Client(catalog.Client{Service: en.ID, FQDN: en.ID, Config: en.ResolveConfig(nil), Secret: func(string) string { return "" }}) {
			ce.Env = append(ce.Env, v.Name)
		}

		res.Entries = append(res.Entries, ce)
	}

	return res, nil
}

// LearnRequest asks for an entry's learning notes.
type LearnRequest struct {
	Type string
}

// LearnResult carries the notes as markdown.
type LearnResult struct {
	Type  string
	Title string
	Notes string
}

// Learn returns a catalog entry's learning notes.
func (e *Engine) Learn(_ context.Context, req LearnRequest) (*LearnResult, error) {
	en, ok := catalog.Lookup(req.Type)
	if !ok {
		return nil, unknownType(req.Type)
	}

	return &LearnResult{Type: en.ID, Title: en.Title, Notes: en.Notes}, nil
}

func unknownType(typ string) error {
	var ids []string
	for _, en := range catalog.All() {
		ids = append(ids, en.ID)
	}

	return fmt.Errorf("type %q (known: %s): %w", typ, strings.Join(ids, ", "), ErrNotFound)
}

// AddServiceRequest adds an infra service.
type AddServiceRequest struct {
	Dir  string
	Type string
	Name string // empty means the type id
}

// AddServiceResult names the service added.
type AddServiceResult struct {
	Name string
	Type string
}

// AddService adds an infra service to experiment.yaml, keeping comments.
func (e *Engine) AddService(_ context.Context, req AddServiceRequest) (*AddServiceResult, error) {
	entry, ok := catalog.Lookup(req.Type)
	if !ok {
		return nil, unknownType(req.Type)
	}

	name := req.Name
	if name == "" {
		name = req.Type
	}

	p, unlock, err := e.openLocked(req.Dir, "add")
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := p.doc.AddService(name, req.Type, defaultUpstreams(p.spec, entry)...); err != nil {
		return nil, fmt.Errorf("%w; choose another name with --name", err)
	}

	if err := p.save(p.doc); err != nil {
		return nil, err
	}

	return &AddServiceResult{Name: name, Type: req.Type}, nil
}

// RemoveServiceRequest removes a service or app.
type RemoveServiceRequest struct {
	Dir  string
	Name string
}

// RemoveServiceResult reports what was removed.
type RemoveServiceResult struct {
	Name string
	Kind string // "service" or "app"
}

// RemoveService removes an infra service or app from experiment.yaml. A
// service still listed in an app's uses is refused.
func (e *Engine) RemoveService(_ context.Context, req RemoveServiceRequest) (*RemoveServiceResult, error) {
	p, unlock, err := e.openLocked(req.Dir, "remove")
	if err != nil {
		return nil, err
	}
	defer unlock()

	var vs []spec.Violation

	for _, app := range p.spec.Apps {
		for i, u := range app.Uses {
			if u.Service == req.Name {
				vs = append(vs, spec.Violation{
					Path:    fmt.Sprintf("apps.%s.uses[%d]", app.Name, i),
					Message: fmt.Sprintf("%s uses %s; remove it from %s's uses first", app.Name, req.Name, app.Name),
				})
			}
		}
	}

	for _, svc := range p.spec.Services {
		for i, u := range svc.Upstreams {
			if u.Service == req.Name {
				vs = append(vs, spec.Violation{
					Path:    fmt.Sprintf("services.%s.upstreams[%d]", svc.Name, i),
					Message: fmt.Sprintf("%s proxies to %s; remove it from %s's upstreams first", svc.Name, req.Name, svc.Name),
				})
			}
		}
	}

	if len(vs) > 0 {
		return nil, &spec.ValidationError{Violations: vs}
	}

	section, err := p.doc.Remove(req.Name)
	if err != nil {
		return nil, err
	}

	if err := p.save(p.doc); err != nil {
		return nil, err
	}

	kind := "service"
	if section == "apps" {
		kind = "app"
	}

	return &RemoveServiceResult{Name: req.Name, Kind: kind}, nil
}

// openLocked opens the experiment and takes its operation lock.
func (e *Engine) openLocked(dir, operation string) (*project, func(), error) {
	sp, err := findProject(dir)
	if err != nil {
		return nil, nil, err
	}

	lock, err := sp.Lock(operation)
	if err != nil {
		return nil, nil, err
	}

	p, err := e.open(sp.Dir)
	if err != nil {
		_ = lock.Release()

		return nil, nil, err
	}

	return p, func() { _ = lock.Release() }, nil
}

// StarterInfo summarises a starter.
type StarterInfo struct {
	ID       string
	Title    string
	Summary  string
	Language string
	Requires []string
	Apps     []string
	Services []string // proxy services the starter adds
}

// StartersResult lists starters.
type StartersResult struct {
	Starters []StarterInfo
}

// Starters lists every starter.
func (e *Engine) Starters(_ context.Context) (*StartersResult, error) {
	res := &StartersResult{}

	for _, s := range starters.All() {
		info := StarterInfo{ID: s.ID, Title: s.Title, Summary: s.Summary, Language: s.Language, Requires: s.Requires}
		for _, a := range s.Apps {
			info.Apps = append(info.Apps, a.Name)
		}

		for _, svc := range s.Services {
			info.Services = append(info.Services, svc.Name)
		}

		res.Starters = append(res.Starters, info)
	}

	return res, nil
}

// ApplyStarterRequest applies a starter to the experiment.
type ApplyStarterRequest struct {
	Dir    string
	ID     string
	Target string // directory for the code, relative to the project; empty uses the starter's default
	Events EventSink
}

// ApplyStarterResult reports what changed. Files that already exist with
// different content are never overwritten; they are listed as conflicts.
type ApplyStarterResult struct {
	Dir           string // where the code lives, relative to the project
	Written       []string
	Unchanged     []string
	Conflicts     []string
	AddedServices []string
	AddedApps     []string
	ExistingApps  []string // app names already present, left untouched
	// ExistingServices are the starter's proxy services already present
	// from an earlier apply to the same directory, left untouched.
	ExistingServices []string
}

// ApplyStarter writes a starter's code into the project and adds its apps,
// its proxy services and any infra services it needs to experiment.yaml.
// Re-applying never overwrites user changes.
func (e *Engine) ApplyStarter(ctx context.Context, req ApplyStarterRequest) (*ApplyStarterResult, error) {
	st, ok := starters.Lookup(req.ID)
	if !ok {
		var ids []string
		for _, s := range starters.All() {
			ids = append(ids, s.ID)
		}

		return nil, fmt.Errorf("starter %q (known: %s): %w", req.ID, strings.Join(ids, ", "), ErrNotFound)
	}

	return e.applyStarter(ctx, st, starters.Render, req)
}

// starterInfra is the service of type typ among services, with its config
// resolved against the catalog. The caller has made sure one exists.
func starterInfra(services []spec.Service, typ string) starters.Infra {
	i := slices.IndexFunc(services, func(s spec.Service) bool { return s.Type == typ })
	if i < 0 {
		return starters.DefaultInfra(typ)
	}

	in := starters.DefaultInfra(typ)

	in.Name = services[i].Name
	if e, ok := catalog.Lookup(typ); ok {
		in.Config = e.ResolveConfig(services[i].Config)
	}

	return in
}

// renderFunc renders a starter's templates for a lab.
type renderFunc func(s starters.Starter, lab starters.Lab) ([]starters.File, error)

// applyStarter is ApplyStarter for a starter value and its templates, so
// tests can apply starters that do not ship.
func (e *Engine) applyStarter(ctx context.Context, st starters.Starter, render renderFunc, req ApplyStarterRequest) (*ApplyStarterResult, error) {
	settings, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	p, unlock, err := e.openLocked(req.Dir, "starter")
	if err != nil {
		return nil, err
	}
	defer unlock()

	target := st.Dir
	if req.Target != "" {
		target = filepath.ToSlash(filepath.Clean(req.Target))
	}

	if filepath.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") || target == "." ||
		target == state.DirName || strings.HasPrefix(target, state.DirName+"/") || strings.ContainsAny(target, ":,\n") {
		return nil, spec.Invalid("target", "must be a new directory inside the project, outside .bb")
	}

	res := &ApplyStarterResult{Dir: "./" + strings.TrimPrefix(target, "./")}

	// Spec changes first, validated before any file is written.
	doc := p.doc
	have := slices.Clone(p.spec.Services)

	typeService := func(typ string) string {
		for _, s := range have {
			if s.Type == typ {
				return s.Name
			}
		}

		return ""
	}
	for _, typ := range st.Requires {
		if typeService(typ) != "" {
			continue
		}

		name := typ
		if doc.HasName(name) {
			return nil, fmt.Errorf("starter needs a %s service but the name %q is taken by an app: %w", typ, name, ErrExists)
		}

		if err := doc.AddService(name, typ); err != nil {
			return nil, err
		}

		have = append(have, spec.Service{Name: name, Type: typ})
		res.AddedServices = append(res.AddedServices, name)
	}

	for _, a := range st.Apps {
		if doc.HasName(a.Name) {
			// Re-applying the same starter to the same directory keeps the
			// app as the user left it. Any other owner of the name is a clash:
			// failing here, before any file is written, beats silently
			// skipping an app the starter's code expects to exist.
			if existing, ok := p.spec.App(a.Name); ok && path.Clean(existing.Path) == path.Clean(res.Dir) {
				res.ExistingApps = append(res.ExistingApps, a.Name)

				continue
			}

			return nil, fmt.Errorf("starter %s needs an app named %q, but experiment.yaml already uses that name for something else; rename it there, then apply the starter again: %w", st.ID, a.Name, ErrExists)
		}

		app := spec.App{Name: a.Name, Path: res.Dir, Command: a.Command, Register: a.Register, Replicas: a.Replicas}
		for _, ref := range a.Uses {
			// A proxy of this starter by its name; otherwise a catalog type.
			if !slices.ContainsFunc(st.Services, func(s starters.Service) bool { return s.Name == ref }) {
				ref = typeService(ref)
			}

			app.Uses = append(app.Uses, spec.Use{Service: ref})
		}

		app.Sidecars = starterSidecars(a.Sidecars, func(ref string) string {
			if slices.Contains(st.Requires, ref) {
				return typeService(ref)
			}

			return ref // an app of the starter
		})
		if err := doc.AddApp(app); err != nil {
			return nil, err
		}

		res.AddedApps = append(res.AddedApps, a.Name)
	}

	for _, sv := range st.Services {
		file, static := "", ""
		if sv.File != "" {
			file = "./" + path.Join(target, sv.File)
		}

		if sv.StaticFile != "" {
			static = "./" + path.Join(target, sv.StaticFile)
		}

		if doc.HasName(sv.Name) {
			// Same rule as apps (D-047): only this starter's service from an
			// earlier apply to this directory is kept as the user left it.
			if existing, ok := serviceNamed(p.spec, sv.Name); ok && starterOwnsService(p.spec, st, existing, sv.Type, file, static, res.Dir) {
				res.ExistingServices = append(res.ExistingServices, sv.Name)

				continue
			}

			return nil, fmt.Errorf("starter %s needs a service named %q, but experiment.yaml already uses that name for something else; rename it there, then apply the starter again: %w", st.ID, sv.Name, ErrExists)
		}

		svc := spec.Service{Name: sv.Name, Type: sv.Type, File: file, StaticFile: static, Config: maps.Clone(sv.Config)}
		for _, u := range sv.Upstreams {
			if !slices.ContainsFunc(st.Apps, func(a starters.App) bool { return a.Name == u.Service }) && slices.Contains(st.Requires, u.Service) {
				u.Service = typeService(u.Service)
			}

			svc.Upstreams = append(svc.Upstreams, u)
		}

		if err := doc.AddProxy(svc); err != nil {
			return nil, err
		}

		res.AddedServices = append(res.AddedServices, sv.Name)
	}

	if _, err := decodeAndValidate(doc); err != nil {
		return nil, err
	}

	// Templates name the experiment's services of the required types, as
	// resolved above (an existing one, or the one just added), with their
	// config, defaults applied (D-161).
	lab := starters.Lab{Experiment: p.spec.Name, Domain: settings.Domain, Infra: map[string]starters.Infra{}}
	for _, typ := range st.Requires {
		lab.Infra[typ] = starterInfra(have, typ)
	}

	files, err := render(st, lab)
	if err != nil {
		return nil, err
	}

	root := filepath.Join(p.Dir, filepath.FromSlash(target))
	for _, f := range files {
		rel := filepath.ToSlash(filepath.Join(target, f.Path))
		path := filepath.Join(root, filepath.FromSlash(f.Path))

		existing, err := os.ReadFile(path)
		switch {
		case err == nil && bytes.Equal(existing, f.Data):
			res.Unchanged = append(res.Unchanged, rel)

			continue
		case err == nil:
			res.Conflicts = append(res.Conflicts, rel)
			req.Events.warn(rel + " differs from the starter; left as is")

			continue
		case !isNotExist(err):
			return nil, err
		}

		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return nil, err
		}

		if err := state.WriteFile(path, f.Data, 0o644); err != nil {
			return nil, err
		}

		res.Written = append(res.Written, rel)
	}

	if len(res.AddedApps) > 0 || len(res.AddedServices) > 0 {
		if err := p.save(doc); err != nil {
			return nil, err
		}
	}

	return res, nil
}

// serviceNamed finds an infra service by name.
func serviceNamed(s *spec.Spec, name string) (spec.Service, bool) {
	i := slices.IndexFunc(s.Services, func(svc spec.Service) bool { return svc.Name == name })
	if i < 0 {
		return spec.Service{}, false
	}

	return s.Services[i], true
}

// starterOwnsService reports whether existing is the starter's proxy from
// an earlier apply to dir: the same type, and either the file or
// static_file this apply would set, an upstream to one of the starter's apps in dir, or a client
// among the starter's apps in dir that the starter declares as using this
// proxy. The last two keep a re-apply idempotent after the user points file
// elsewhere: an app-backed proxy is recognised by its upstream, and a proxy
// in front of infra (a shared PgBouncer) by its client.
func starterOwnsService(s *spec.Spec, st starters.Starter, existing spec.Service, typ, file, static, dir string) bool {
	if existing.Type != typ {
		return false
	}

	if file != "" && existing.File != "" && path.Clean(existing.File) == path.Clean(file) {
		return true
	}

	if static != "" && existing.StaticFile != "" && path.Clean(existing.StaticFile) == path.Clean(static) {
		return true
	}

	starterAppInDir := func(name string) (spec.App, bool) {
		if !slices.ContainsFunc(st.Apps, func(a starters.App) bool { return a.Name == name }) {
			return spec.App{}, false
		}

		app, ok := s.App(name)

		return app, ok && path.Clean(app.Path) == path.Clean(dir)
	}
	for _, u := range existing.Upstreams {
		if _, ok := starterAppInDir(u.Service); ok {
			return true
		}
	}

	for _, a := range st.Apps {
		if !slices.Contains(a.Uses, existing.Name) {
			continue
		}

		if app, ok := starterAppInDir(a.Name); ok && slices.ContainsFunc(app.Uses, func(u spec.Use) bool { return u.Service == existing.Name }) {
			return true
		}
	}

	return false
}

// starterSidecars copies a starter app's sidecars with their infra
// references (catalog types) resolved to the experiment's service names.
func starterSidecars(in []spec.Sidecar, resolve func(string) string) []spec.Sidecar {
	var out []spec.Sidecar

	for _, sc := range in {
		if e := sc.Envoy; e != nil {
			c := *e

			c.Outbound = slices.Clone(e.Outbound)
			for i := range c.Outbound {
				c.Outbound[i].Service = resolve(c.Outbound[i].Service)
			}

			sc.Envoy = &c
		}

		if p := sc.PgBouncer; p != nil {
			c := *p
			c.Service = resolve(c.Service)
			sc.PgBouncer = &c
		}

		if f := sc.FluentBit; f != nil {
			c := *f
			c.To = resolve(c.To)
			sc.FluentBit = &c
		}

		out = append(out, sc)
	}

	return out
}

// MigrateRequest upgrades experiment.yaml.
type MigrateRequest struct {
	Dir string
}

// MigrateResult reports the upgrade.
type MigrateResult struct {
	From    int
	To      int
	Changed bool
}

// Migrate rewrites experiment.yaml to the current spec version. It is the
// only operation that changes the file's version.
func (e *Engine) Migrate(_ context.Context, req MigrateRequest) (*MigrateResult, error) {
	sp, err := findProject(req.Dir)
	if err != nil {
		return nil, err
	}

	lock, err := sp.Lock("migrate")
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	p := &project{Project: sp}

	data, err := os.ReadFile(p.specPath())
	if err != nil {
		return nil, err
	}

	doc, err := spec.Parse(data)
	if err != nil {
		return nil, err
	}

	from, changed, err := doc.Migrate()
	if err != nil {
		return nil, err
	}

	if changed {
		if err := p.save(doc); err != nil {
			return nil, err
		}
	}

	return &MigrateResult{From: from, To: spec.CurrentVersion, Changed: changed}, nil
}

// StarterDetailsRequest names a starter.
type StarterDetailsRequest struct {
	ID string
}

// StarterDetails is everything a starter would write and add, without
// writing anything: for the web UI's starter pages.
type StarterDetails struct {
	StarterInfo
	Dir      string // default directory the code is written to
	Apps     []StarterApp
	Services []StarterService
	Files    []StarterFile
}

// StarterService is a proxy service the starter adds to experiment.yaml.
type StarterService struct {
	Name string
	Type string
	// Upstreams name the starter's apps or catalog types (resolved to the
	// experiment's service of that type when applied).
	Upstreams []spec.Upstream
	// File is the native config, relative to the starter's directory
	// (one of Files); empty when the generated config is kept.
	File string
	// StaticFile is the static configuration merged into the proxy's
	// generated one, relative to the starter's directory (one of Files);
	// empty when there is none.
	StaticFile string
	// Config is the service's config as the starter writes it.
	Config map[string]string
}

// StarterApp is an app the starter adds to experiment.yaml.
type StarterApp struct {
	Name     string
	Command  []string
	Uses     []string // catalog types
	Register *spec.Register
	Replicas int
	Sidecars []spec.Sidecar // infra references are catalog types
}

// StarterFile is one rendered file, relative to the starter's directory.
type StarterFile struct {
	Path string
	Data []byte
}

// StarterDetails renders a starter's files for an example experiment.
func (e *Engine) StarterDetails(ctx context.Context, req StarterDetailsRequest) (*StarterDetails, error) {
	st, ok := starters.Lookup(req.ID)
	if !ok {
		return nil, fmt.Errorf("starter %q: %w", req.ID, ErrNotFound)
	}

	settings, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	files, err := starters.Render(st, st.DefaultLab("lab", settings.Domain))
	if err != nil {
		return nil, err
	}

	d := &StarterDetails{StarterInfo: StarterInfo{ID: st.ID, Title: st.Title, Summary: st.Summary, Language: st.Language, Requires: st.Requires}, Dir: st.Dir}
	for _, a := range st.Apps {
		d.StarterInfo.Apps = append(d.StarterInfo.Apps, a.Name)
		d.Apps = append(d.Apps, StarterApp{Name: a.Name, Command: a.Command, Uses: a.Uses, Register: a.Register, Replicas: a.Replicas, Sidecars: a.Sidecars})
	}

	for _, svc := range st.Services {
		d.StarterInfo.Services = append(d.StarterInfo.Services, svc.Name)
		d.Services = append(d.Services, StarterService{Name: svc.Name, Type: svc.Type, Upstreams: slices.Clone(svc.Upstreams), File: svc.File, StaticFile: svc.StaticFile, Config: maps.Clone(svc.Config)})
	}

	for _, f := range files {
		d.Files = append(d.Files, StarterFile{Path: f.Path, Data: f.Data})
	}

	return d, nil
}

// defaultUpstreams wires a proxy that needs an upstream of a fixed type (a
// PgBouncer pool in front of Postgres) to the experiment's only service of
// that type, so `bb add pgbouncer` works without editing experiment.yaml.
// With none or several candidates it adds nothing and validation explains.
func defaultUpstreams(s *spec.Spec, entry *catalog.Entry) []string {
	if entry.Proxy == nil || entry.Proxy.MinUpstreams == 0 || len(entry.Proxy.Targets) == 0 {
		return nil
	}

	var candidates []string
	for _, t := range entry.Proxy.Targets {
		candidates = append(candidates, s.ServicesOfType(t)...)
	}

	if len(candidates) != 1 {
		return nil
	}

	return candidates
}
