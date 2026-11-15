package core

import (
	"bytes"
	"context"
	"fmt"
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
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
	defer lock.Release()
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
}

// CatalogResult lists the catalog.
type CatalogResult struct {
	Entries []CatalogEntry
}

// Catalog lists every infra service type.
func (e *Engine) Catalog(ctx context.Context) (*CatalogResult, error) {
	res := &CatalogResult{}
	for _, en := range catalog.All() {
		ce := CatalogEntry{Type: en.ID, Title: en.Title, Summary: en.Summary, Image: en.Image, Memory: en.Resources.Memory, CPUs: en.Resources.CPUs, Plaintext: en.SupportsPlaintext()}
		for _, l := range en.Listeners {
			ce.Ports = append(ce.Ports, l.Port)
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
func (e *Engine) Learn(ctx context.Context, req LearnRequest) (*LearnResult, error) {
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
func (e *Engine) AddService(ctx context.Context, req AddServiceRequest) (*AddServiceResult, error) {
	if _, ok := catalog.Lookup(req.Type); !ok {
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
	if err := p.doc.AddService(name, req.Type); err != nil {
		return nil, fmt.Errorf("%w; choose another name with --name", err)
	}
	if _, err := p.save(p.doc); err != nil {
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
func (e *Engine) RemoveService(ctx context.Context, req RemoveServiceRequest) (*RemoveServiceResult, error) {
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
	if len(vs) > 0 {
		return nil, &spec.ValidationError{Violations: vs}
	}
	section, err := p.doc.Remove(req.Name)
	if err != nil {
		return nil, err
	}
	if _, err := p.save(p.doc); err != nil {
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
}

// StartersResult lists starters.
type StartersResult struct {
	Starters []StarterInfo
}

// Starters lists every starter.
func (e *Engine) Starters(ctx context.Context) (*StartersResult, error) {
	res := &StartersResult{}
	for _, s := range starters.All() {
		info := StarterInfo{ID: s.ID, Title: s.Title, Summary: s.Summary, Language: s.Language, Requires: s.Requires}
		for _, a := range s.Apps {
			info.Apps = append(info.Apps, a.Name)
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
}

// ApplyStarter writes a starter's code into the project and adds its apps
// (and any infra services it needs) to experiment.yaml. Re-applying never
// overwrites user changes.
func (e *Engine) ApplyStarter(ctx context.Context, req ApplyStarterRequest) (*ApplyStarterResult, error) {
	st, ok := starters.Lookup(req.ID)
	if !ok {
		var ids []string
		for _, s := range starters.All() {
			ids = append(ids, s.ID)
		}
		return nil, fmt.Errorf("starter %q (known: %s): %w", req.ID, strings.Join(ids, ", "), ErrNotFound)
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
		app := spec.App{Name: a.Name, Path: res.Dir, Command: a.Command}
		for _, typ := range a.Uses {
			app.Uses = append(app.Uses, spec.Use{Service: typeService(typ)})
		}
		if err := doc.AddApp(app); err != nil {
			return nil, err
		}
		res.AddedApps = append(res.AddedApps, a.Name)
	}
	if _, err := decodeAndValidate(doc); err != nil {
		return nil, err
	}

	files, err := starters.Render(st, p.spec.Name)
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
			req.Events.warn(fmt.Sprintf("%s differs from the starter; left as is", rel))
			continue
		case !isNotExist(err):
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := state.WriteFile(path, f.Data, 0o644); err != nil {
			return nil, err
		}
		res.Written = append(res.Written, rel)
	}
	if len(res.AddedApps) > 0 || len(res.AddedServices) > 0 {
		if _, err := p.save(doc); err != nil {
			return nil, err
		}
	}
	return res, nil
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
func (e *Engine) Migrate(ctx context.Context, req MigrateRequest) (*MigrateResult, error) {
	sp, err := findProject(req.Dir)
	if err != nil {
		return nil, err
	}
	lock, err := sp.Lock("migrate")
	if err != nil {
		return nil, err
	}
	defer lock.Release()
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
		if _, err := p.save(doc); err != nil {
			return nil, err
		}
	}
	return &MigrateResult{From: from, To: spec.CurrentVersion, Changed: changed}, nil
}
