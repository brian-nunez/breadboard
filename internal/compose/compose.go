// Package compose materializes a resolved model into a Docker Compose
// project: docker-compose.yml, docker-compose.dev.yml, the certificate and
// configuration files they mount, and a .env file in dotenv mode.
//
// The same bundle drives `bb up` (written to .bb/runtime/) and `bb export
// compose` (written anywhere, runnable without bb).
package compose

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/brian-nunez/breadboard/internal/catalog"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
)

// File names inside a bundle.
const (
	ComposeFile    = "docker-compose.yml"
	DevComposeFile = "docker-compose.dev.yml"
	EnvFile        = ".env"
)

// Labels set on every container, network, and volume.
const (
	LabelExperiment = "breadboard.experiment"
	LabelService    = "breadboard.service"
	LabelKind       = "breadboard.kind"
	// LabelExperimentID tells experiments with the same name apart.
	LabelExperimentID = "breadboard.experiment-id"
	// LabelConfigHash changes whenever a service's mounted files change, so
	// compose recreates the container and it reads the new files.
	LabelConfigHash = "breadboard.config-hash"
)

// AppWorkdir is where an app's dev stage keeps its source. The dev compose
// file bind-mounts the source directory here.
const AppWorkdir = "/src"

// Cert is a service's certificate and key, PEM.
type Cert struct {
	Cert []byte
	Key  []byte
}

// Input is everything the materializer needs besides the model.
type Input struct {
	Model *model.Model
	// ExperimentID is the project's id from .bb/state.json.
	ExperimentID string
	Secrets      *secrets.Materializer
	CA           []byte // Breadboard CA certificate, PEM
	TrustBundle  []byte // CA plus public roots, PEM
	Certs        map[string]Cert
	// Subnets holds the allocated subnet of every private segment.
	Subnets map[string]netip.Prefix
	// AppDir returns an app's source directory relative to the bundle
	// directory, with forward slashes.
	AppDir func(app string) string
	// Header is the comment written at the top of each compose file.
	Header string
}

// File is one generated file.
type File struct {
	Path string // slash-separated, relative to the bundle directory
	Data []byte
	Mode os.FileMode
}

// Bundle is a complete compose project.
type Bundle struct {
	Files []File
}

// File returns a file by path.
func (b *Bundle) File(p string) (File, bool) {
	i := slices.IndexFunc(b.Files, func(f File) bool { return f.Path == p })
	if i < 0 {
		return File{}, false
	}
	return b.Files[i], true
}

// SegmentAddress is a target's static address on its segment: the tenth
// host, clear of the gateway.
func SegmentAddress(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr()
	for range 10 {
		a = a.Next()
	}
	return a
}

// Render builds the bundle.
func Render(in Input) (*Bundle, error) {
	m := in.Model
	b := &Bundle{}
	add := func(p string, data []byte, mode os.FileMode) {
		b.Files = append(b.Files, File{Path: p, Data: data, Mode: mode})
	}
	add("ca.pem", in.CA, 0o644)
	add("trust-bundle.pem", in.TrustBundle, 0o644)

	services := mapping()
	dev := mapping()
	volumes := mapping()
	devVolumes := mapping()
	hasApps := false

	for _, svc := range m.Services {
		// A sidecar shares its app's network identity, so it mounts the app's
		// certificate files instead of having its own.
		owner := svc.Name
		if svc.Kind == model.Sidecar {
			owner = svc.Parent
		}
		cert, ok := in.Certs[owner]
		if !ok {
			return nil, fmt.Errorf("compose: no certificate for %s", owner)
		}
		tlsDir := "tls/" + owner
		if owner == svc.Name {
			add(tlsDir+"/tls.crt", cert.Cert, 0o644)
			// Keys are readable inside the bundle so non-root container users can
			// load them; the bundle's directories are private to the host user.
			add(tlsDir+"/tls.key", cert.Key, 0o644)
			add(tlsDir+"/keystore.pem", append(append([]byte{}, cert.Key...), cert.Cert...), 0o644)
		}

		mounts := []string{
			"./" + tlsDir + "/tls.crt:" + catalog.CertFile + ":ro",
			"./" + tlsDir + "/tls.key:" + catalog.KeyFile + ":ro",
			"./" + tlsDir + "/keystore.pem:" + catalog.KeystoreFile + ":ro",
			"./ca.pem:" + catalog.CAFile + ":ro",
			"./trust-bundle.pem:" + catalog.TrustBundleFile + ":ro",
		}
		hash := sha256.New()
		for _, part := range [][]byte{cert.Cert, cert.Key, in.CA, in.TrustBundle} {
			hash.Write(part)
		}
		var def *yaml.Node
		var err error
		switch svc.Kind {
		case model.Infra:
			def, mounts, err = infraService(in, svc, mounts, func(p string, data []byte, mode os.FileMode) {
				hash.Write([]byte(p))
				hash.Write(data)
				add(p, data, mode)
			})
			if err != nil {
				return nil, err
			}
			vol := svc.Name + "-data"
			volumes.Content = append(volumes.Content, str(vol), mapping(str("labels"), in.labels("", "")))
		case model.Sidecar:
			def, mounts = sidecarService(in, svc, mounts, func(p string, data []byte, mode os.FileMode) {
				hash.Write([]byte(p))
				hash.Write(data)
				add(p, data, mode)
			})
		case model.App:
			hasApps = true
			def = appService(in, svc)
			src := in.AppDir(svc.Name)
			dev.Content = append(dev.Content, str(svc.Name), mapping(
				str("volumes"), seq(
					str(escape(src)+":"+AppWorkdir),
					cacheMount("go-mod-cache", "/go/pkg/mod"),
					cacheMount("go-build-cache", "/root/.cache/go-build"),
				),
			))
		}
		def.Content = append(def.Content, str("volumes"), seq(strs(mounts...)...))
		if svc.Kind == model.Sidecar {
			// Same network namespace as the app: same address, same aliases.
			def.Content = append(def.Content, str("network_mode"), str("service:"+svc.Parent))
		} else {
			def.Content = append(def.Content, str("networks"), networks(in, svc))
		}
		lbl := in.labels(svc.Name, string(svc.Kind))
		lbl.Content = append(lbl.Content, str(LabelConfigHash), str(hex.EncodeToString(hash.Sum(nil))[:16]))
		def.Content = append(def.Content, str("labels"), lbl)
		def.Content = append(def.Content, str("mem_limit"), str(svc.Resources.Memory))
		def.Content = append(def.Content, str("cpus"), float(svc.Resources.CPUs))
		def.Content = append(def.Content, str("restart"), str("unless-stopped"))
		services.Content = append(services.Content, str(svc.Name), def)
	}

	nets := mapping(str("default"), mapping(str("labels"), in.labels("", "")))
	for _, seg := range m.Segments {
		subnet, ok := in.Subnets[seg.Name]
		if !ok {
			return nil, fmt.Errorf("compose: no subnet allocated for segment %s", seg.Name)
		}
		key := str(seg.Name)
		key.HeadComment = fmt.Sprintf("Private segment for plaintext to %s; only %s can reach it.\n%s",
			seg.Target, strings.Join(seg.Clients, ", "), reasonLines(seg.Reasons))
		nets.Content = append(nets.Content, key, mapping(
			str("internal"), boolean(true),
			str("ipam"), mapping(str("config"), seq(mapping(str("subnet"), str(subnet.String())))),
			str("labels"), in.labels("", ""),
		))
	}

	top := mapping(str("name"), str(m.Project), str("services"), services, str("networks"), nets)
	if len(volumes.Content) > 0 {
		top.Content = append(top.Content, str("volumes"), volumes)
	}
	top.HeadComment = in.Header
	data, err := encode(top)
	if err != nil {
		return nil, err
	}
	add(ComposeFile, data, 0o644)

	devTop := mapping(str("services"), dev)
	if hasApps {
		for _, v := range []string{"go-mod-cache", "go-build-cache"} {
			devVolumes.Content = append(devVolumes.Content, str(v), mapping(str("labels"), in.labels("", "")))
		}
		devTop.Content = append(devTop.Content, str("volumes"), devVolumes)
	} else {
		dev.Style = yaml.FlowStyle
	}
	devTop.HeadComment = in.Header + "\nDevelopment overrides: live source and Go caches for app services."
	data, err = encode(devTop)
	if err != nil {
		return nil, err
	}
	add(DevComposeFile, data, 0o644)

	if env := in.Secrets.Dotenv(); env != nil {
		add(EnvFile, env, 0o600)
	}
	return b, nil
}

func infraService(in Input, svc *model.Service, mounts []string, add func(string, []byte, os.FileMode)) (*yaml.Node, []string, error) {
	m := in.Model
	srv := catalog.Server{
		Name:   svc.Name,
		FQDN:   svc.FQDN,
		Domain: m.Domain,
		Config: svc.Config,
		Secret: in.Secrets.Ref(svc.Name),
	}
	if seg, ok := m.Segment(svc.Name); ok {
		subnet := in.Subnets[seg.Name]
		srv.Plain = &catalog.Plain{
			FQDN:    model.SegmentFQDN(svc.Name, m.Domain),
			Address: SegmentAddress(subnet).String(),
			Subnet:  subnet.String(),
			Clients: seg.Clients,
			Reasons: seg.Reasons,
		}
	}
	c := svc.Entry.Server(srv)
	def := mapping(str("image"), str(svc.Entry.Image))
	if len(c.Entrypoint) > 0 {
		def.Content = append(def.Content, str("entrypoint"), command(c.Entrypoint))
	}
	if len(c.Command) > 0 {
		def.Content = append(def.Content, str("command"), command(c.Command))
	}
	env := append([]catalog.EnvVar{{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile}}, c.Environment...)
	def.Content = append(def.Content, str("environment"), environment(env))
	for _, f := range c.Files {
		p := "config/" + svc.Name + "/" + f.Name
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		add(p, f.Data, mode)
		mounts = append(mounts, "./"+p+":"+f.MountPath()+":ro")
	}
	mounts = append([]string{svc.Name + "-data:" + svc.Entry.DataPath}, mounts...)
	def.Content = append(def.Content, str("healthcheck"), healthcheck(c.Health))
	return def, mounts, nil
}

// sidecarService renders the agent that registers an app with its registry.
func sidecarService(in Input, svc *model.Service, mounts []string, add func(string, []byte, os.FileMode)) (*yaml.Node, []string) {
	m := in.Model
	registry, _ := m.Service(svc.Registry)
	c := svc.Entry.Sidecar(catalog.Sidecar{
		App:      svc.Parent,
		AppFQDN:  svc.FQDN,
		Registry: registry.FQDN,
		Secret:   in.Secrets.Ref(registry.Name),
		Service:  svc.Register.Name,
		Port:     svc.Register.Port,
		Check:    svc.Register.Check,
	})
	def := mapping(str("image"), str(svc.Entry.Image))
	if len(c.Entrypoint) > 0 {
		def.Content = append(def.Content, str("entrypoint"), command(c.Entrypoint))
	}
	if len(c.Command) > 0 {
		def.Content = append(def.Content, str("command"), command(c.Command))
	}
	env := append([]catalog.EnvVar{{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile}}, c.Environment...)
	def.Content = append(def.Content, str("environment"), environment(env))
	for _, f := range c.Files {
		p := "config/" + svc.Name + "/" + f.Name
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}
		add(p, f.Data, mode)
		mounts = append(mounts, "./"+p+":"+f.MountPath()+":ro")
	}
	def.Content = append(def.Content, str("healthcheck"), healthcheck(c.Health))
	def.Content = append(def.Content, str("depends_on"), mapping(
		str(svc.Parent), mapping(str("condition"), str("service_started")),
		str(registry.Name), mapping(str("condition"), str("service_healthy")),
	))
	return def, mounts
}

func appService(in Input, svc *model.Service) *yaml.Node {
	m := in.Model
	app := svc.App
	def := mapping(str("build"), mapping(
		str("context"), str(escape(in.AppDir(svc.Name))),
		str("target"), str("dev"),
	))
	if len(app.Command) > 0 {
		cmd := make([]string, len(app.Command))
		for i, c := range app.Command {
			cmd[i] = escape(c)
		}
		def.Content = append(def.Content, str("command"), command(cmd))
	}
	env := []catalog.EnvVar{
		{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile},
		// The app's own certificate, for apps that serve TLS to other apps.
		{Name: "TLS_CERT_FILE", Value: catalog.CertFile},
		{Name: "TLS_KEY_FILE", Value: catalog.KeyFile},
		{Name: "BB_SERVICE", Value: svc.Name},
	}
	if svc.Health != nil {
		env = append(env, catalog.EnvVar{Name: "BB_HEALTH_PORT", Value: strconv.Itoa(svc.Health.Port)})
	}
	seen := map[string]bool{}
	for _, e := range env {
		seen[e.Name] = true
	}
	envNode := environment(env)
	depends := mapping()
	for _, edge := range m.EdgesFrom(svc.Name) {
		target, _ := m.Service(edge.To)
		cl := catalog.Client{
			Service: target.Name,
			FQDN:    target.FQDN,
			Config:  target.Config,
			Secret:  in.Secrets.Ref(target.Name),
		}
		if !edge.TLS {
			seg, _ := m.Segment(target.Name)
			cl.Plain = &catalog.Plain{FQDN: model.SegmentFQDN(target.Name, m.Domain), Subnet: in.Subnets[seg.Name].String()}
		}
		first := true
		for _, v := range target.Entry.Client(cl) {
			name := v.Name
			if seen[name] {
				// A second service of the same type: prefix its variables.
				name = secrets.EnvPrefixed(target.Name, v.Name)
			}
			seen[name] = true
			k := str(name)
			if first {
				k.HeadComment = fmt.Sprintf("%s (%s)", target.Name, target.Entry.Title)
				if !edge.TLS {
					k.HeadComment = fmt.Sprintf("%s (%s): PLAINTEXT edge (tls.mode: off). Reason: %s", target.Name, target.Entry.Title, edge.Reason)
				}
				first = false
			}
			envNode.Content = append(envNode.Content, k, str(v.Value))
		}
		depends.Content = append(depends.Content, str(target.Name), mapping(str("condition"), str("service_healthy")))
	}
	def.Content = append(def.Content, str("environment"), envNode)
	if len(app.Ports) > 0 {
		var ports []*yaml.Node
		for _, p := range app.Ports {
			ports = append(ports, str(fmt.Sprintf("127.0.0.1:%d:%d", p, p)))
		}
		def.Content = append(def.Content, str("ports"), seq(ports...))
	}
	// By default the check only asks whether the main process is running, so
	// app code needs no health endpoint. `health` in the spec opts into HTTP.
	check := catalog.Health{
		Test:          []string{"CMD-SHELL", "kill -0 1"},
		Interval:      10 * time.Second,
		Timeout:       5 * time.Second,
		Retries:       3,
		StartPeriod:   10 * time.Second,
		StartInterval: 1 * time.Second,
	}
	if h := svc.Health; h != nil {
		// Loopback inside the container: not an edge, so no TLS.
		check.Test = []string{"CMD", "wget", "-q", "-O", "/dev/null", fmt.Sprintf("http://127.0.0.1:%d%s", h.Port, escape(h.Path))}
		check.StartPeriod = 180 * time.Second
	}
	def.Content = append(def.Content, str("healthcheck"), healthcheck(check))
	if len(depends.Content) > 0 {
		def.Content = append(def.Content, str("depends_on"), depends)
	}
	return def
}

func networks(in Input, svc *model.Service) *yaml.Node {
	m := in.Model
	n := mapping(str("default"), mapping(str("aliases"), flow(str(svc.FQDN))))
	for _, seg := range m.SegmentsOf(svc.Name) {
		def := mapping()
		if seg.Target == svc.Name {
			def.Content = append(def.Content,
				str("ipv4_address"), str(SegmentAddress(in.Subnets[seg.Name]).String()),
				str("aliases"), flow(str(model.SegmentFQDN(svc.Name, m.Domain))),
			)
		} else {
			def = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Style: yaml.FlowStyle}
		}
		n.Content = append(n.Content, str(seg.Name), def)
	}
	return n
}

func healthcheck(h catalog.Health) *yaml.Node {
	n := mapping(str("test"), command(h.Test),
		str("interval"), str(dur(h.Interval)),
		str("timeout"), str(dur(h.Timeout)),
		str("retries"), integer(h.Retries),
		str("start_period"), str(dur(h.StartPeriod)),
	)
	if h.StartInterval > 0 {
		n.Content = append(n.Content, str("start_interval"), str(dur(h.StartInterval)))
	}
	return n
}

func environment(vars []catalog.EnvVar) *yaml.Node {
	n := mapping()
	for _, v := range vars {
		n.Content = append(n.Content, str(v.Name), str(v.Value))
	}
	return n
}

func (in Input) labels(service, kind string) *yaml.Node {
	n := mapping(str(LabelExperiment), str(in.Model.Name))
	if in.ExperimentID != "" {
		n.Content = append(n.Content, str(LabelExperimentID), str(in.ExperimentID))
	}
	if service != "" {
		n.Content = append(n.Content, str(LabelService), str(service))
	}
	if kind != "" {
		n.Content = append(n.Content, str(LabelKind), str(kind))
	}
	return n
}

func reasonLines(reasons []string) string {
	lines := make([]string, len(reasons))
	for i, r := range reasons {
		lines[i] = "Reason (" + r + ")"
	}
	return strings.Join(lines, "\n")
}

// command renders an argv list; multi-line arguments use literal block style.
func command(argv []string) *yaml.Node {
	multi := slices.ContainsFunc(argv, func(s string) bool { return strings.Contains(s, "\n") })
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	if !multi {
		n.Style = yaml.FlowStyle
	}
	for _, a := range argv {
		s := str(a)
		if strings.Contains(a, "\n") {
			s.Style = yaml.LiteralStyle
		}
		n.Content = append(n.Content, s)
	}
	return n
}

// escape protects literal dollar signs from compose interpolation.
func escape(s string) string { return strings.ReplaceAll(s, "$", "$$") }

func dur(d time.Duration) string {
	if d%time.Minute == 0 && d >= time.Minute {
		return strconv.Itoa(int(d/time.Minute)) + "m"
	}
	return strconv.Itoa(int(d/time.Second)) + "s"
}

// cacheMount mounts a Go cache volume that every app shares. nocopy stops
// Docker from copying the image's files into the empty volume on first use:
// apps start together, and concurrent copies into one new volume fail with
// "mkdir ...: file exists". Go fills the cache itself and locks it safely.
func cacheMount(volume, target string) *yaml.Node {
	return mapping(
		str("type"), str("volume"),
		str("source"), str(volume),
		str("target"), str(target),
		str("volume"), mapping(str("nocopy"), boolean(true)),
	)
}

func mapping(kv ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: kv}
}

func seq(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: items}
}

func flow(items ...*yaml.Node) *yaml.Node {
	n := seq(items...)
	n.Style = yaml.FlowStyle
	return n
}

func strs(v ...string) []*yaml.Node {
	out := make([]*yaml.Node, len(v))
	for i, s := range v {
		out[i] = str(s)
	}
	return out
}

func str(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	var probe any
	if err := yaml.Unmarshal([]byte(v), &probe); err != nil {
		n.Style = yaml.DoubleQuotedStyle
	} else if s, ok := probe.(string); !ok || s != v {
		n.Style = yaml.DoubleQuotedStyle
	}
	return n
}

func integer(v int) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(v)}
}

func float(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: v}
}

func boolean(v bool) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(v)}
}

func encode(n *yaml.Node) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(n); err != nil {
		return nil, fmt.Errorf("compose: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("compose: encode: %w", err)
	}
	return buf.Bytes(), nil
}

// RelPath returns target relative to base, both absolute, with forward
// slashes. Paths inside base start with ./ so compose reads them as bind
// mounts rather than volume names.
func RelPath(base, target string) (string, error) {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if rel != ".." && !strings.HasPrefix(rel, "../") {
		rel = "./" + rel
	}
	return rel, nil
}
