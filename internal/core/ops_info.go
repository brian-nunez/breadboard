package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/compose"
	"github.com/brian-nunez/breadboard/internal/config"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
	"github.com/brian-nunez/breadboard/internal/state"
)

// CredsRequest asks for generated credentials.
type CredsRequest struct {
	Dir     string
	Service string // empty means every infra service
}

// Secret is one generated value.
type Secret struct {
	Key   string
	Value string
	Help  string
}

// EnvVar is one variable of a service's env contract.
type EnvVar struct {
	Name   string
	Value  string
	Secret bool
}

// ServiceCreds is one infra service's credentials and the env an app gets
// when it uses the service.
type ServiceCreds struct {
	Name    string
	Type    string
	Address string
	Secrets []Secret
	Env     []EnvVar
}

// CredsResult lists credentials.
type CredsResult struct {
	Services []ServiceCreds
}

// Creds returns generated credentials and connection strings, generating any
// that do not exist yet.
func (e *Engine) Creds(ctx context.Context, req CredsRequest) (*CredsResult, error) {
	p, unlock, err := e.openLocked(req.Dir, "creds")
	if err != nil {
		return nil, err
	}
	defer unlock()

	st, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	m, err := model.Resolve(p.spec, st.Domain)
	if err != nil {
		return nil, err
	}

	if req.Service != "" {
		svc, ok := m.Service(req.Service)
		if !ok || svc.Kind != model.Infra {
			return nil, fmt.Errorf("%q is not an infra service in experiment.yaml: %w", req.Service, ErrNotFound)
		}
	}

	if _, err := p.Ensure(e.version); err != nil {
		return nil, err
	}

	store, err := p.LoadSecrets()
	if err != nil {
		return nil, err
	}

	if added, err := secrets.Ensure(m, store); err != nil {
		return nil, err
	} else if added {
		if err := p.SaveSecrets(store); err != nil {
			return nil, err
		}
	}

	mat := secrets.NewMaterializer(secrets.Inline, store)
	res := &CredsResult{}

	for _, svc := range m.Services {
		if svc.Kind != model.Infra || (req.Service != "" && svc.Name != req.Service) {
			continue
		}

		sc := ServiceCreds{Name: svc.Name, Type: svc.Entry.ID, Address: listenerAddress(svc)}
		for _, s := range svc.Entry.Secrets {
			v, _ := store.Get(svc.Name, s.Key)
			sc.Secrets = append(sc.Secrets, Secret{Key: s.Key, Value: v, Help: s.Help})
		}

		env := svc.Entry.Client(catalog.Client{
			Service: svc.Name, FQDN: svc.FQDN, Config: svc.Config, Secret: mat.Ref(svc.Name),
			Upstreams: catalog.WithSecrets(svc.Upstreams, mat.Ref),
		})
		for _, v := range env {
			sc.Env = append(sc.Env, EnvVar{Name: v.Name, Value: v.Value, Secret: v.Secret})
		}

		res.Services = append(res.Services, sc)
	}

	return res, nil
}

// CAResult describes the install CA. Its top-level fields are the lab CA,
// root, which signs every lab certificate; Host is the host CA.
type CAResult struct {
	Name        string
	Domain      string
	Subject     string
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256, hex
	CertPath    string
	CertPEM     []byte
	Created     bool
	// Host is the host CA, which signs only the host router's certificate
	// for https://<target>.<experiment>.localhost names.
	Host *CAInfo
}

// CAInfo describes one CA.
type CAInfo struct {
	Name        string
	Domain      string // the DNS domain the CA is constrained to
	Subject     string
	NotBefore   time.Time
	NotAfter    time.Time
	Fingerprint string // SHA-256, hex
	CertPath    string
	CertPEM     []byte
	Created     bool
}

// CA returns the install CAs' details, creating either CA if needed. It
// never touches the host trust store.
func (e *Engine) CA(ctx context.Context) (*CAResult, error) {
	st, err := e.settings(ctx)
	if err != nil {
		return nil, err
	}

	ca, created, err := e.ensureCA(st.Domain)
	if err != nil {
		return nil, err
	}

	host, hostCreated, err := e.ensureHostCA()
	if err != nil {
		return nil, err
	}

	return &CAResult{
		Name:        caName,
		Domain:      ca.Domain(),
		Subject:     ca.Cert.Subject.CommonName,
		NotBefore:   ca.Cert.NotBefore,
		NotAfter:    ca.Cert.NotAfter,
		Fingerprint: ca.Fingerprint(),
		CertPath:    e.home.CACertPath(caName),
		CertPEM:     ca.CertPEM,
		Created:     created,
		Host: &CAInfo{
			Name:        hostCAName,
			Domain:      host.Domain(),
			Subject:     host.Cert.Subject.CommonName,
			NotBefore:   host.Cert.NotBefore,
			NotAfter:    host.Cert.NotAfter,
			Fingerprint: host.Fingerprint(),
			CertPath:    e.home.CACertPath(hostCAName),
			CertPEM:     host.CertPEM,
			Created:     hostCreated,
		},
	}, nil
}

// Setting is one install setting.
type Setting = config.Value

// ConfigGetRequest reads settings.
type ConfigGetRequest struct {
	Key string // empty lists every setting
}

// ConfigResult lists settings.
type ConfigResult struct {
	Home     string
	Settings []Setting
}

// ConfigGet returns one setting or all of them.
func (e *Engine) ConfigGet(ctx context.Context, req ConfigGetRequest) (*ConfigResult, error) {
	res := &ConfigResult{Home: e.home.Dir}
	if req.Key == "" {
		all, err := config.All(ctx, e.home)
		if err != nil {
			return nil, err
		}

		res.Settings = all

		return res, nil
	}

	v, err := config.Get(ctx, e.home, req.Key)
	if err != nil {
		return nil, err
	}

	res.Settings = []Setting{v}

	return res, nil
}

// ConfigSetRequest changes a setting.
type ConfigSetRequest struct {
	Key   string
	Value string
}

// ConfigSetResult reports the stored value. When an environment variable
// still overrides the file, OverriddenBy names it.
type ConfigSetResult struct {
	Setting      Setting
	OverriddenBy string
}

// ConfigSet writes a setting to config.yaml.
func (e *Engine) ConfigSet(ctx context.Context, req ConfigSetRequest) (*ConfigSetResult, error) {
	v, err := config.Set(ctx, e.home, req.Key, req.Value)
	if err != nil {
		return nil, err
	}

	res := &ConfigSetResult{Setting: v}
	if strings.HasPrefix(v.Source, config.EnvPrefix) {
		res.OverriddenBy = v.Source
	}

	return res, nil
}

// ExportComposeRequest writes a standalone compose stack.
type ExportComposeRequest struct {
	Dir string
	// Out is the output directory; empty means <project>/export/compose.
	// An existing non-empty directory is only replaced if bb wrote it.
	Out string
	// Secrets selects inline (default) or dotenv.
	Secrets string
	Events  EventSink
}

// ExportComposeResult reports what was written.
type ExportComposeResult struct {
	Dir   string
	Files []string
}

// exportMarker identifies a directory bb exported, so a re-export may replace it.
const exportMarker = ".bb-export"

// ExportCompose writes a compose stack that runs with plain docker compose,
// without bb installed.
func (e *Engine) ExportCompose(ctx context.Context, req ExportComposeRequest) (*ExportComposeResult, error) {
	mode, err := secrets.ParseMode(req.Secrets)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrValidation, err)
	}

	p, unlock, err := e.openLocked(req.Dir, "export")
	if err != nil {
		return nil, err
	}
	defer unlock()

	if err := p.checkApps(); err != nil {
		return nil, err
	}

	out := req.Out
	if out == "" {
		out = filepath.Join(p.Dir, "export", "compose")
	}

	if out, err = filepath.Abs(out); err != nil {
		return nil, err
	}

	if err := checkExportDir(out, p.Dir); err != nil {
		return nil, err
	}

	pr, err := e.prepare(ctx, p, req.Events, false, nil)
	if err != nil {
		return nil, err
	}

	req.Events.step("Writing the compose stack")

	header := fmt.Sprintf("Exported by bb %s from experiment.yaml (%s). Runs without bb:\n  docker compose up -d --wait", e.version, p.spec.Name)

	b, err := e.render(p, pr, out, mode, header)
	if err != nil {
		return nil, err
	}

	b.Files = append(b.Files,
		compose.File{Path: exportMarker, Data: []byte("Written by bb export compose. bb replaces this directory on the next export.\n"), Mode: 0o644},
		compose.File{Path: "README.md", Data: exportReadme(p.spec.Name, pr.model, mode, pr.hostPort), Mode: 0o644},
	)

	ignore := "# Private keys and secrets stay on this machine.\ntls/*/tls.key\ntls/*/keystore.pem\n"
	if pr.hostCert != nil {
		ignore += "tls/*/host.key\n"
	}

	if mode == secrets.Dotenv {
		ignore += ".env\n"
	} else {
		ignore += "# docker-compose.yml holds secrets inline; export with --secrets dotenv to commit it.\ndocker-compose.yml\n"
	}

	b.Files = append(b.Files, compose.File{Path: ".gitignore", Data: []byte(ignore), Mode: 0o644})
	if err := writeBundle(out, b); err != nil {
		return nil, err
	}

	res := &ExportComposeResult{Dir: out}
	for _, f := range b.Files {
		res.Files = append(res.Files, f.Path)
	}

	return res, nil
}

func checkExportDir(out, projectDir string) error {
	if out == projectDir || strings.HasPrefix(projectDir, out+string(filepath.Separator)) {
		return fmt.Errorf("%w: the export directory must not contain the project", ErrValidation)
	}

	if rel, err := filepath.Rel(filepath.Join(projectDir, state.DirName), out); err == nil && !strings.HasPrefix(rel, "..") {
		return fmt.Errorf("%w: the export directory must not be inside .bb", ErrValidation)
	}

	entries, err := os.ReadDir(out)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(entries) == 0) {
		return nil
	}

	if err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(out, exportMarker)); err == nil {
		return nil
	}

	return fmt.Errorf("%s is not empty and was not written by bb export: %w", out, ErrExists)
}

func exportReadme(name string, m *model.Model, mode secrets.Mode, hostPort int) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (compose export)\n\nExported by Breadboard. This stack runs with plain Docker Compose; bb is not needed.\n\n", name)
	b.WriteString("```sh\ndocker compose up -d --wait\n```\n\n")
	b.WriteString("- `docker-compose.yml`: every service with its image, health check, limits, and TLS files.\n")
	b.WriteString("- `tls/`, `ca.pem`, `trust-bundle.pem`: certificates from the Breadboard CA. Leaf certificates expire after 90 days; export again to renew them.\n")

	if mode == secrets.Dotenv {
		b.WriteString("- `.env`: generated secrets, read by compose. It is git-ignored.\n")
	} else {
		b.WriteString("- Secrets are inline in `docker-compose.yml`, so that file is git-ignored. Export with `--secrets dotenv` to keep them in `.env` instead.\n")
	}

	if urls := hostURLs(m, hostPort); len(urls) > 0 {
		fmt.Fprintf(&b, "\n## Host access\n\n`bb-host` publishes these on 127.0.0.1:%d with the certificate in `tls/bb-host/host.crt`, from the Breadboard host CA (`bb ca` shows it and how to trust it):\n\n", hostPort)

		for _, u := range urls {
			to := u.Target
			if u.Instance != "" {
				to = u.Instance
			}

			fmt.Fprintf(&b, "- %s -> %s\n", u.URL, to)
		}
	}

	var own []string

	for _, svc := range m.Services {
		if svc.File != "" {
			own = append(own, fmt.Sprintf("- %s: `%s` (file) replaces the generated config.\n", svc.Name, svc.File))
		}

		if svc.StaticFile != "" {
			own = append(own, fmt.Sprintf("- %s: `%s` (static_file) is merged into `config/%s/%s`.\n", svc.Name, svc.StaticFile, svc.Name, svc.Entry.Proxy.StaticFile))
		}
	}

	if len(own) > 0 {
		b.WriteString("\n## Your proxy configs\n\nCopied into `config/` at export from these files next to experiment.yaml. Export again after editing them.\n\n")
		b.WriteString(strings.Join(own, ""))
	}

	if pe := m.PlaintextEdges(); len(pe) > 0 {
		b.WriteString("\n## Plaintext edges\n\n")

		for _, e := range pe {
			fmt.Fprintf(&b, "- %s -> %s: %s\n", e.From, e.To, e.Reason)
		}
	}

	return []byte(b.String())
}
