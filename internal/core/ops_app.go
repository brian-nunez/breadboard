package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/starters"
	"github.com/brian-nunez/breadboard/internal/state"
)

// AddAppRequest scaffolds the learner's own app.
type AddAppRequest struct {
	Dir  string
	Name string // the app's name: a DNS label, unused in experiment.yaml
	// Uses lists the infra services the app connects to. Nil means every
	// infra service in experiment.yaml; an empty, non-nil list means none.
	Uses []string
	// Path is the directory for the code, relative to the project. Empty
	// means ./<Name>. It must not exist, or be empty.
	Path string
}

// AddAppResult reports what AddApp wrote.
type AddAppResult struct {
	Name    string
	Dir     string // where the code lives, relative to the project
	Command []string
	Files   []string // written, relative to the project
	// Uses is each used service with the variables the app receives from
	// it, exactly as bb up injects them. Secret values are never returned.
	Uses []AppUse
}

// AppUse is one service an app uses and the variables it gets from it.
type AppUse struct {
	Service   string
	Type      string
	Title     string
	Plaintext bool
	Env       []EnvVar
}

// AddApp writes a minimal Go program into a new directory and adds it to
// experiment.yaml as an app with path, command and uses. The program names
// every variable its services give it, checks them at start, logs what it
// can reach, and shows how to connect to each service with its official
// client library. It never writes into a directory that has files.
func (e *Engine) AddApp(ctx context.Context, req AddAppRequest) (*AddAppResult, error) {
	settings, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	p, unlock, err := e.openLocked(req.Dir, "add app")
	if err != nil {
		return nil, err
	}
	defer unlock()

	name := req.Name
	if !spec.IsServiceOK(name) {
		return nil, invalidRequest("app name %q must be a lowercase DNS label (a-z, 0-9, -) of at most 63 characters", name)
	}

	if name == spec.HostRouterName {
		return nil, invalidRequest("app name %s is reserved for the host router; choose another name", name)
	}

	if p.doc.HasName(name) {
		return nil, fmt.Errorf("a service or app named %q %w in experiment.yaml; choose another name", name, ErrExists)
	}

	uses := req.Uses
	if uses == nil {
		for _, s := range p.spec.Services {
			uses = append(uses, s.Name)
		}
	}

	var vs []string

	for i, u := range uses {
		at := fmt.Sprintf("uses[%d]", i)
		switch _, isApp := p.spec.App(u); {
		case slices.Index(uses, u) < i:
			vs = append(vs, fmt.Sprintf("%s: %s is listed twice", at, u))
		case isApp:
			vs = append(vs, fmt.Sprintf("%s: %s is an app; uses names infra services (apps reach each other by name)", at, u))
		case !slices.ContainsFunc(p.spec.Services, func(s spec.Service) bool { return s.Name == u }):
			vs = append(vs, fmt.Sprintf("%s: %s is not a service in experiment.yaml; add it with bb add <type> first", at, u))
		}
	}

	if len(vs) > 0 {
		return nil, invalidRequest("%s", strings.Join(vs, "; "))
	}

	target := name
	if req.Path != "" {
		target = filepath.ToSlash(filepath.Clean(req.Path))
	}

	target = strings.TrimPrefix(target, "./")
	if filepath.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") || target == "." ||
		target == state.DirName || strings.HasPrefix(target, state.DirName+"/") || strings.ContainsAny(target, ":,\n") {
		return nil, invalidRequest("app path %q must be a new directory inside the project, outside .bb", req.Path)
	}

	root := filepath.Join(p.Dir, filepath.FromSlash(target))
	if err := resolvesInside(p.Dir, root); err != nil {
		return nil, err
	}

	existed, err := checkNewDir(root)
	if err != nil {
		return nil, err
	}

	res := &AddAppResult{Name: name, Dir: "./" + target, Command: []string{name}}

	// The spec change, validated before any file is written.
	app := spec.App{Name: name, Path: res.Dir, Command: res.Command}
	for _, u := range uses {
		app.Uses = append(app.Uses, spec.Use{Service: u})
	}

	doc := p.doc
	if err := doc.AddApp(app); err != nil {
		return nil, err
	}

	s, err := decodeAndValidate(doc)
	if err != nil {
		return nil, err
	}

	// The variables come from the resolved model, as bb up injects them,
	// prefixing included.
	m, err := model.Resolve(s, settings.Domain)
	if err != nil {
		return nil, err
	}

	svc, ok := m.Service(name)
	if !ok {
		return nil, fmt.Errorf("app %s is missing from the resolved model", name)
	}

	masked := func(string) func(string) string { return func(string) string { return "" } }

	groups, err := compose.AppEnv(m, svc, masked, nil)
	if err != nil {
		return nil, err
	}

	scaffold := starters.AppScaffold{Name: name, Experiment: s.Name}
	for _, svc := range s.Services {
		scaffold.Taken = append(scaffold.Taken, svc.Name)
	}

	for _, a := range s.Apps {
		scaffold.Taken = append(scaffold.Taken, a.Name)
	}

	for _, g := range groups {
		if g.Service == "" || g.Sidecar {
			continue // Breadboard's own variables; the scaffold explains them in prose
		}

		infra, _ := m.Service(g.Service)
		use := starters.AppUse{Service: g.Service, Type: infra.Entry.ID, Title: infra.Entry.Title, Summary: infra.Entry.Summary, Plaintext: g.Plaintext}

		au := AppUse{Service: g.Service, Type: infra.Entry.ID, Title: infra.Entry.Title, Plaintext: g.Plaintext}
		for _, v := range g.Vars {
			value := v.Value
			if v.Secret {
				value = ""
			}

			use.Vars = append(use.Vars, starters.AppVar{Name: v.Name, Base: starters.EnvBase(g.Service, v.Name), Value: value, Secret: v.Secret})
			au.Env = append(au.Env, EnvVar{Name: v.Name, Value: value, Secret: v.Secret})
		}

		scaffold.Uses = append(scaffold.Uses, use)
		res.Uses = append(res.Uses, au)
	}

	files, err := starters.RenderApp(scaffold)
	if err != nil {
		return nil, err
	}

	// Undo on failure: the directory was empty or missing, so everything
	// in it is ours.
	undo := func() {
		if existed {
			entries, _ := os.ReadDir(root)
			for _, en := range entries {
				_ = os.RemoveAll(filepath.Join(root, en.Name()))
			}

			return
		}

		_ = os.RemoveAll(root)
	}
	if err := writeAppFiles(root, files); err != nil {
		undo()

		return nil, err
	}

	for _, f := range files {
		res.Files = append(res.Files, filepath.ToSlash(filepath.Join(target, f.Path)))
	}

	if err := p.save(doc); err != nil {
		undo()

		return nil, err
	}

	return res, nil
}

// requestError is a request that breaks a rule, as opposed to an invalid
// experiment.yaml: it reads as one plain sentence (no "experiment.yaml is
// invalid" heading in the CLI) and still matches ErrValidation.
type requestError struct{ msg string }

func (e *requestError) Error() string        { return e.msg }
func (e *requestError) Is(target error) bool { return target == ErrValidation }

func invalidRequest(format string, args ...any) error {
	return &requestError{msg: fmt.Sprintf(format, args...)}
}

// resolvesInside refuses a directory that, once symlinks are resolved,
// is not strictly inside the project or is inside its .bb directory: the
// nearest existing ancestor is resolved and the missing rest appended, so
// neither a symlink out of the project nor one to .bb (alias -> .bb) can
// put the code in the wrong place.
func resolvesInside(project, dir string) error {
	base, err := filepath.EvalSymlinks(project)
	if err != nil {
		return err
	}

	stateDir := filepath.Join(base, state.DirName)
	if resolved, err := filepath.EvalSymlinks(stateDir); err == nil {
		stateDir = resolved
	}

	dest, err := canonical(dir)
	if err != nil {
		return err
	}

	if !within(base, dest) || dest == base {
		return invalidRequest("app path %s leads outside the project through a symlink; choose a directory inside it", dir)
	}

	if within(stateDir, dest) {
		return invalidRequest("app path %s leads into %s through a symlink; choose a directory outside it", dir, state.DirName)
	}

	return nil
}

// canonical resolves dir's nearest existing ancestor through its symlinks
// and appends the part that does not exist yet.
func canonical(dir string) (string, error) {
	var rest []string

	for d := dir; ; d = filepath.Dir(d) {
		resolved, err := filepath.EvalSymlinks(d)
		if err == nil {
			slices.Reverse(rest)

			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}

		if !isNotExist(err) || d == filepath.Dir(d) {
			return "", err
		}

		rest = append(rest, filepath.Base(d))
	}
}

// within reports whether path is root or below it.
func within(root, path string) bool {
	rel, err := filepath.Rel(root, path)

	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// checkNewDir makes sure dir can take a new app: it does not exist, or it
// is an empty directory (existed reports which). Anything else is
// ErrExists, so the learner's work is never overwritten.
func checkNewDir(dir string) (existed bool, err error) {
	fi, err := os.Lstat(dir)
	switch {
	case isNotExist(err):
		return false, nil
	case err != nil:
		return false, err
	case !fi.IsDir():
		return false, fmt.Errorf("%s %w and is not a directory; choose another directory", dir, ErrExists)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	if len(entries) > 0 {
		return true, fmt.Errorf("%s %w and has files, which bb add app never overwrites; choose another directory", dir, ErrExists)
	}

	return true, nil
}

// writeAppFiles writes the rendered files under root, which is empty or
// missing.
func writeAppFiles(root string, files []starters.File) error {
	var errs []error

	for _, f := range files {
		path := filepath.Join(root, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}

		if err := state.WriteFile(path, f.Data, 0o644); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}
