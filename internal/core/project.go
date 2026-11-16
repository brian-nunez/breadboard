package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/config"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/pki"
	"github.com/brian-nunez/breadboard/internal/runtime"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/spec"
	"github.com/brian-nunez/breadboard/internal/state"
)

// caName is the only CA in v1. CAs are addressed by name so later phases can
// add more.
const caName = "root"

// project is an opened experiment.
type project struct {
	state.Project
	doc  *spec.Document
	spec *spec.Spec
}

func (p *project) specPath() string { return filepath.Join(p.Dir, spec.FileName) }

// findProject walks up from dir to the nearest directory with experiment.yaml.
func findProject(dir string) (state.Project, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return state.Project{}, err
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, spec.FileName)); err == nil {
			return state.Project{Dir: d}, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return state.Project{}, fmt.Errorf("%s: %w", abs, ErrNotInitialized)
		}
		d = parent
	}
}

// open loads and validates the experiment containing dir.
func (e *Engine) open(dir string) (*project, error) {
	sp, err := findProject(dir)
	if err != nil {
		return nil, err
	}
	p := &project{Project: sp}
	data, err := os.ReadFile(p.specPath())
	if err != nil {
		return nil, err
	}
	if p.doc, err = spec.Parse(data); err != nil {
		return nil, err
	}
	if p.spec, err = decodeAndValidate(p.doc); err != nil {
		return nil, err
	}
	return p, nil
}

func decodeAndValidate(doc *spec.Document) (*spec.Spec, error) {
	s, err := doc.Decode()
	if err != nil {
		return nil, err
	}
	if err := spec.Validate(s, catalog.Types); err != nil {
		return nil, err
	}
	return s, nil
}

// save validates doc and writes it to experiment.yaml.
func (p *project) save(doc *spec.Document) (*spec.Spec, error) {
	s, err := decodeAndValidate(doc)
	if err != nil {
		return nil, err
	}
	data, err := doc.Bytes()
	if err != nil {
		return nil, err
	}
	if err := state.WriteFile(p.specPath(), data, 0o644); err != nil {
		return nil, err
	}
	p.doc, p.spec = doc, s
	return s, nil
}

var devStage = regexp.MustCompile(`(?im)^\s*FROM\s+\S+(\s+--\S+)*\s+AS\s+dev\s*$`)

// checkApps verifies every app directory has a Dockerfile with a dev stage.
func (p *project) checkApps() error {
	var vs []spec.Violation
	for _, app := range p.spec.Apps {
		path := "apps." + app.Name + ".path"
		df := filepath.Join(p.Dir, app.Path, "Dockerfile")
		data, err := os.ReadFile(df)
		switch {
		case isNotExist(err):
			vs = append(vs, spec.Violation{Path: path, Message: fmt.Sprintf("%s has no Dockerfile", app.Path)})
		case err != nil:
			return err
		case !devStage.Match(data):
			vs = append(vs, spec.Violation{Path: path, Message: fmt.Sprintf("%s/Dockerfile has no stage named dev (FROM <image> AS dev)", app.Path)})
		}
	}
	if len(vs) > 0 {
		return &spec.ValidationError{Violations: vs}
	}
	return nil
}

// settings loads the effective install settings.
func (e *Engine) settings(ctx context.Context) (config.Settings, error) {
	return config.Load(ctx, e.home)
}

// ensureCA loads the install CA, creating it when missing, constrained to a
// different domain, or too close to expiry to sign a full leaf.
func (e *Engine) ensureCA(domain string) (ca *pki.CA, created bool, err error) {
	lock, err := e.home.LockCA("ca")
	if err != nil {
		return nil, false, err
	}
	defer lock.Release()
	certPEM, keyPEM, ok, err := e.home.LoadCA(caName)
	if err != nil {
		return nil, false, err
	}
	if ok {
		ca, err = pki.LoadCA(certPEM, keyPEM)
		switch {
		case errors.Is(err, pki.ErrKeyMismatch):
			// A torn pair from an interrupted write: replace it. Leaves
			// signed by the old CA are reissued by reconcileCerts.
		case err != nil:
			return nil, false, fmt.Errorf("load CA from %s: %w", e.home.CADir(), err)
		case ca.Usable(domain, e.now()):
			return ca, false, nil
		}
	}
	ca, err = pki.NewCA(domain, e.now())
	if err != nil {
		return nil, false, err
	}
	keyPEM, err = ca.KeyPEM()
	if err != nil {
		return nil, false, err
	}
	if err := e.home.SaveCA(caName, ca.CertPEM, keyPEM); err != nil {
		return nil, false, err
	}
	return ca, true, nil
}

// prepared is a reconciled experiment ready to materialize.
type prepared struct {
	model   *model.Model
	ca      *pki.CA
	secrets *state.Secrets
	state   *state.ProjectState
	certs   map[string]compose.Cert
	subnets map[string]netip.Prefix
}

// prepare reconciles project state with the spec: CA, secrets, leaf
// certificates, and segment subnets. It writes state; the caller holds the lock.
func (e *Engine) prepare(ctx context.Context, p *project, events EventSink) (*prepared, error) {
	st, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}
	m, err := model.Resolve(p.spec, st.Domain)
	if err != nil {
		return nil, err
	}
	if _, err := p.Ensure(e.version); err != nil {
		return nil, err
	}
	ca, created, err := e.ensureCA(st.Domain)
	if err != nil {
		return nil, err
	}
	if created {
		events.emit(Event{Kind: EventInfo, Message: fmt.Sprintf("Created the Breadboard CA for %s in %s", st.Domain, e.home.CADir())})
	}
	ps, err := p.LoadState()
	if err != nil {
		return nil, err
	}
	sec, err := p.LoadSecrets()
	if err != nil {
		return nil, err
	}
	added, err := secrets.Ensure(m, sec)
	if err != nil {
		return nil, err
	}
	// Secrets of removed services are kept: their data volumes outlive the
	// service, and a re-added service must find the password its data expects.
	if added {
		if err := p.SaveSecrets(sec); err != nil {
			return nil, err
		}
	}

	events.step("Issuing certificates")
	certs, err := e.reconcileCerts(p, m, ca, ps)
	if err != nil {
		return nil, err
	}
	subnets, err := e.allocateSubnets(ctx, m, ps)
	if err != nil {
		return nil, err
	}
	if err := p.SaveState(ps, e.version); err != nil {
		return nil, err
	}
	for _, edge := range m.PlaintextEdges() {
		events.warn(fmt.Sprintf("Plaintext edge %s -> %s (tls.mode: off): %s", edge.From, edge.To, edge.Reason))
	}
	return &prepared{model: m, ca: ca, secrets: sec, state: ps, certs: certs, subnets: subnets}, nil
}

// reconcileCerts issues a leaf for every service that lacks a valid one and
// removes leaves of services that no longer exist.
func (e *Engine) reconcileCerts(p *project, m *model.Model, ca *pki.CA, ps *state.ProjectState) (map[string]compose.Cert, error) {
	now := e.now()
	certs := map[string]compose.Cert{}
	for _, svc := range m.Services {
		if svc.Kind == model.Sidecar {
			continue // a sidecar presents its app's certificate
		}
		req := pki.LeafRequest{DNSNames: []string{svc.FQDN}, Identity: svc.Identity}
		certPEM, keyPEM, ok, err := p.ReadCert(svc.Name)
		if err != nil {
			return nil, err
		}
		if ok {
			cert, err := pki.ParseCert(certPEM)
			if err == nil && pki.KeyMatches(cert, keyPEM) && !ca.NeedsReissue(cert, req, now) {
				certs[svc.Name] = compose.Cert{Cert: certPEM, Key: keyPEM}
				continue
			}
		}
		leaf, err := ca.Issue(req, now)
		if err != nil {
			return nil, fmt.Errorf("issue certificate for %s: %w", svc.Name, err)
		}
		if err := p.WriteCert(svc.Name, leaf.CertPEM, leaf.KeyPEM); err != nil {
			return nil, err
		}
		ps.Certificates[svc.Name] = state.CertRecord{Issuer: caName, Serial: leaf.Cert.SerialNumber.Text(16), NotAfter: leaf.Cert.NotAfter}
		certs[svc.Name] = compose.Cert{Cert: leaf.CertPEM, Key: leaf.KeyPEM}
	}
	stored, err := p.CertServices()
	if err != nil {
		return nil, err
	}
	for _, name := range stored {
		if _, ok := m.Service(name); !ok {
			if err := p.RemoveCert(name); err != nil {
				return nil, err
			}
			delete(ps.Certificates, name)
		}
	}
	return certs, nil
}

// segmentPool is where private segment subnets come from: /24 blocks of
// 10.213.0.0/16, skipping any block another docker network already uses.
var segmentPool = netip.MustParsePrefix("10.213.0.0/16")

func (e *Engine) allocateSubnets(ctx context.Context, m *model.Model, ps *state.ProjectState) (map[string]netip.Prefix, error) {
	out := map[string]netip.Prefix{}
	keep := map[string]bool{}
	for _, seg := range m.Segments {
		keep[seg.Name] = true
	}
	for name := range ps.Segments {
		if !keep[name] {
			delete(ps.Segments, name)
		}
	}
	if len(m.Segments) == 0 {
		return out, nil
	}
	networks, err := e.rt.Subnets(ctx)
	if err != nil {
		return nil, err
	}
	// Networks owned by this experiment do not count: they are the segments
	// being reconciled. Every other network's subnet is off limits.
	var used []netip.Prefix
	for _, n := range networks {
		if n.ExperimentID != ps.ExperimentID {
			used = append(used, n.Subnet)
		}
	}
	overlapsUsed := func(p netip.Prefix) bool {
		return slices.ContainsFunc(used, p.Overlaps)
	}
	// Keep a stored subnet only while no other network has taken it since.
	for name, cidr := range ps.Segments {
		if p, err := netip.ParsePrefix(cidr); err != nil || overlapsUsed(p) {
			delete(ps.Segments, name)
		}
	}
	taken := func(p netip.Prefix) bool {
		if overlapsUsed(p) {
			return true
		}
		for _, cidr := range ps.Segments {
			if q, err := netip.ParsePrefix(cidr); err == nil && q.Overlaps(p) {
				return true
			}
		}
		return false
	}
	for _, seg := range m.Segments {
		if cidr, ok := ps.Segments[seg.Name]; ok {
			out[seg.Name], _ = netip.ParsePrefix(cidr)
			continue
		}
		base := segmentPool.Addr().As4()
		found := false
		for i := 0; i < 256 && !found; i++ {
			cand := netip.PrefixFrom(netip.AddrFrom4([4]byte{base[0], base[1], byte(i), 0}), 24)
			if !taken(cand) {
				ps.Segments[seg.Name] = cand.String()
				out[seg.Name] = cand
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no free subnet in %s for segment %s", segmentPool, seg.Name)
		}
	}
	return out, nil
}

// checkOwnership refuses to act when Docker holds resources of another
// experiment with the same name (another directory): compose would treat
// them as this experiment's and remove or reuse them.
func (e *Engine) checkOwnership(ctx context.Context, p *project) error {
	ps, err := p.LoadState()
	if err != nil {
		return err
	}
	owners, err := e.rt.Owners(ctx, p.spec.Name)
	if err != nil {
		return err
	}
	for _, id := range owners {
		if id != ps.ExperimentID {
			return fmt.Errorf("another experiment named %q (in a different directory) has containers, networks, or volumes in Docker; run bb down --volumes in that directory, or rename this one in experiment.yaml: %w", p.spec.Name, ErrExists)
		}
	}
	return nil
}

// render materializes the prepared experiment into a bundle written for dir.
func (e *Engine) render(p *project, pr *prepared, dir string, mode secrets.Mode, header string) (*compose.Bundle, error) {
	appDirs := map[string]string{}
	for _, app := range p.spec.Apps {
		rel, err := compose.RelPath(dir, filepath.Join(p.Dir, app.Path))
		if err != nil {
			return nil, err
		}
		appDirs[app.Name] = rel
	}
	bundle := pr.ca.CertPEM
	if roots := e.systemRoots(); len(roots) > 0 {
		bundle = append(append(append([]byte{}, pr.ca.CertPEM...), '\n'), bytes.TrimSpace(roots)...)
		bundle = append(bundle, '\n')
	}
	return compose.Render(compose.Input{
		Model:        pr.model,
		ExperimentID: pr.state.ExperimentID,
		Secrets:      secrets.NewMaterializer(mode, pr.secrets),
		CA:           pr.ca.CertPEM,
		TrustBundle:  bundle,
		Certs:        pr.certs,
		Subnets:      pr.subnets,
		AppDir:       func(app string) string { return appDirs[app] },
		Header:       header,
	})
}

// writeBundle replaces dir's contents with the bundle. Directories are 0700.
// writeBundle makes dir hold exactly the bundle's files. Files whose content
// and mode are unchanged are left alone: running containers bind-mount single
// files from here, and replacing one (even atomically, with a new inode)
// leaves a container that compose does not recreate with a missing file on
// Docker Desktop. A changed file always changes its service's config hash, so
// compose recreates that container with fresh mounts.
func writeBundle(dir string, b *compose.Bundle) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, f := range b.Files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		keep[path] = true
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, f.Data) {
			if st, err := os.Stat(path); err == nil && st.Mode().Perm() == f.Mode.Perm() {
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := state.WriteFile(path, f.Data, f.Mode); err != nil {
			return err
		}
	}
	// Remove files no longer in the bundle, then directories left empty,
	// deepest first.
	var dirs []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if path != dir {
				dirs = append(dirs, path)
			}
			return nil
		case !keep[path]:
			return os.Remove(path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if entries, err := os.ReadDir(dirs[i]); err == nil && len(entries) == 0 {
			if err := os.Remove(dirs[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

// runtimeProject describes the compose project in .bb/runtime/.
func runtimeProject(p state.Project, name string) runtime.Project {
	dir := p.RuntimeDir()
	if _, err := os.Stat(dir); err != nil {
		// .bb/ was deleted: compose still finds the project by its name.
		dir = p.Dir
	}
	rp := runtime.Project{Name: "bb-" + name, Dir: dir}
	for _, f := range []string{compose.ComposeFile, compose.DevComposeFile} {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); err == nil {
			rp.Files = append(rp.Files, path)
		}
	}
	return rp
}

// serviceNames returns a model's service names in order.
func serviceNames(m *model.Model) []string {
	out := make([]string, len(m.Services))
	for i, s := range m.Services {
		out[i] = s.Name
	}
	return out
}

func listenerAddress(svc *model.Service) string {
	if svc.Kind == model.Infra && len(svc.Entry.Listeners) > 0 {
		return fmt.Sprintf("%s:%d", svc.FQDN, svc.Entry.Listeners[0].Port)
	}
	return svc.FQDN
}
