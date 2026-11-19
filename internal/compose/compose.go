// Package compose materializes a resolved model into a Docker Compose
// project: docker-compose.yml, the certificate and
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
	"github.com/brian-nunez/breadboard/internal/catalog/sidecar"
	"github.com/brian-nunez/breadboard/internal/model"
	"github.com/brian-nunez/breadboard/internal/secrets"
)

// File names inside a bundle.
const (
	ComposeFile = "docker-compose.yml"
	EnvFile     = ".env"
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
	// LabelCapture is set on a pod holder and every container that joins
	// its namespace: transparent while netinit's traffic rules live in the
	// namespace, off otherwise. The rules die only with the namespace, so a
	// change of mode must recreate the holder and every joiner.
	LabelCapture = "breadboard.capture"
)

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
	// Subnets holds the allocated subnet of every private segment, and of
	// the default network under DefaultNetwork when the lab has static
	// addressing.
	Subnets map[string]netip.Prefix
	// Addresses are the static addresses on the default network, by the
	// service that owns the network (an infra service, an app instance,
	// or a pod holder). Set together with Subnets[DefaultNetwork].
	Addresses map[string]netip.Addr
	// AppDir returns an app's source directory relative to the bundle
	// directory, with forward slashes.
	AppDir func(app string) string
	// ReadFile reads a file named relative to experiment.yaml, such as an
	// Envoy sidecar's user-owned config. Its content is copied into the
	// bundle, so an edit changes the sidecar's config hash and exports
	// stay self-contained.
	ReadFile func(path string) ([]byte, error)
	// Header is the comment written at the top of each compose file.
	Header string
	// HostCert is the host router's certificate from the host CA, and
	// HostPort the 127.0.0.1 port its HTTPS entrypoint is published on.
	// Both are set exactly when the model has a host router.
	HostCert *Cert
	HostPort int
}

// Paths of the host router's host certificate inside a bundle. Only the
// router mounts them.
const (
	HostCertPath = "tls/bb-host/host.crt"
	HostKeyPath  = "tls/bb-host/host.key"
)

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

// InstanceSegmentAddress is an app instance's static address on its
// private segment: SegmentAddress for a single instance or replica 1, then
// one up per replica.
func InstanceSegmentAddress(p netip.Prefix, instance int) netip.Addr {
	a := SegmentAddress(p)
	for i := 1; i < instance; i++ {
		a = a.Next()
	}

	return a
}

// DefaultNetwork is the experiment's shared network. It has a fixed subnet
// and every container a fixed address only in a lab with a transparent pod,
// whose egress policy allows destinations by address.
const DefaultNetwork = "default"

// DynamicRange is the part of the default network's subnet Docker may
// hand out to containers without a fixed address (such as a debugging
// container attached by hand): the upper half, clear of fixed addresses.
func DynamicRange(p netip.Prefix) netip.Prefix {
	a := p.Masked().Addr().As4()
	a[3] = 128

	return netip.PrefixFrom(netip.AddrFrom4(a), 25)
}

// Kinds of the containers a pod adds. They carry the breadboard.kind label
// but are not services of their own: status folds the holder into its app.
const (
	KindPod  = "pod"
	KindInit = "init"
)

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
	volumes := mapping()
	// Rendered sidecars, filled in when their app instance is rendered
	// (each instance comes before its sidecars in the model).
	outputs := map[string]sidecar.Output{}

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

		if svc.HostRouter {
			if in.HostCert == nil || in.HostPort == 0 {
				return nil, fmt.Errorf("compose: %s needs the host certificate and a host port", svc.Name)
			}
			// The host certificate is mounted in the router only, and is
			// part of its config hash, so a reissue recreates it.
			hash.Write(in.HostCert.Cert)
			hash.Write(in.HostCert.Key)
			add(HostCertPath, in.HostCert.Cert, 0o644)
			add(HostKeyPath, in.HostCert.Key, 0o644)
			mounts = append(mounts,
				"./"+HostCertPath+":"+catalog.HostCertFile+":ro",
				"./"+HostKeyPath+":"+catalog.HostKeyFile+":ro",
			)
		}

		addHashed := func(p string, data []byte, mode os.FileMode) {
			hash.Write([]byte(p))
			hash.Write(data)
			add(p, data, mode)
		}
		// The namespace the service's network lives in: its own, or its pod's.
		var (
			namespace string
			def       *yaml.Node
			err       error
		)

		switch svc.Kind {
		case model.Infra:
			def, mounts, err = infraService(in, svc, mounts, addHashed)
			if err != nil {
				return nil, err
			}

			if svc.Entry.DataPath != "" { // proxies keep no data
				vol := svc.Name + "-data"
				volumes.Content = append(volumes.Content, str(vol), mapping(str("labels"), in.labels("", "")))
			}
		case model.Sidecar:
			parent, ok := m.Service(svc.Parent)
			if !ok {
				return nil, fmt.Errorf("compose: sidecar %s has no app %s", svc.Name, svc.Parent)
			}

			out, ok := outputs[svc.Name]
			if !ok {
				return nil, fmt.Errorf("compose: sidecar %s was not rendered with its app", svc.Name)
			}

			namespace = parent.Name
			if parent.Pod != "" {
				namespace = parent.Pod
				mounts = append(mounts, sharedVolume(parent)+":"+sidecar.SharedDir)
			}

			def, mounts, err = sidecarService(in, svc, parent, out, mounts, addHashed)
			if err != nil {
				return nil, err
			}
		case model.App:
			groups, outs, err := appEnv(m, svc, in.Secrets.Ref, in.Subnets, in.Addresses, in.ReadFile)
			if err != nil {
				return nil, err
			}

			for k, v := range outs {
				if v.Init != nil && svc.Netinit == "" {
					return nil, fmt.Errorf("compose: sidecar %s rendered an init container, but %s has no transparent sidecar", k, svc.Name)
				}

				outputs[k] = v
			}

			if svc.Pod != "" {
				namespace = svc.Pod
				services.Content = append(services.Content, str(svc.Pod), podHolder(in, svc))
				vol := sharedVolume(svc)
				volumes.Content = append(volumes.Content, str(vol), mapping(str("labels"), in.labels("", "")))
				mounts = append(mounts, vol+":"+sidecar.SharedDir)

				if svc.Netinit != "" {
					init, err := netinit(in, svc, outs, add)
					if err != nil {
						return nil, err
					}

					services.Content = append(services.Content, str(svc.Netinit), init)
				}
			}

			def = appService(in, svc, groups)
		}

		def.Content = append(def.Content, str("volumes"), seq(strs(mounts...)...))
		if namespace != "" {
			// Same network namespace: same address, same aliases, same localhost.
			def.Content = append(def.Content, str("network_mode"), str("service:"+namespace))
		} else {
			def.Content = append(def.Content, str("networks"), networks(in, svc))
		}

		lbl := in.labels(svc.Name, string(svc.Kind))
		if pod := podOf(m, svc); pod != nil {
			lbl.Content = append(lbl.Content, str(LabelCapture), str(capture(pod)))
		}

		lbl.Content = append(lbl.Content, str(LabelConfigHash), str(hex.EncodeToString(hash.Sum(nil))[:16]))
		def.Content = append(def.Content, str("labels"), lbl)
		def.Content = append(def.Content, str("mem_limit"), str(svc.Resources.Memory))
		def.Content = append(def.Content, str("cpus"), float(svc.Resources.CPUs))
		def.Content = append(def.Content, str("restart"), str("unless-stopped"))
		services.Content = append(services.Content, str(svc.Name), def)
	}

	defNet := mapping()
	if subnet, ok := in.Subnets[DefaultNetwork]; ok {
		defNet.Content = append(defNet.Content, str("ipam"), mapping(str("config"), seq(mapping(
			str("subnet"), str(subnet.String()),
			str("ip_range"), str(DynamicRange(subnet).String()),
		))))
	}

	defNet.Content = append(defNet.Content, str("labels"), in.labels("", ""))
	nets := mapping(str(DefaultNetwork), defNet)

	for _, seg := range m.Segments {
		subnet, ok := in.Subnets[seg.Name]
		if !ok {
			return nil, fmt.Errorf("compose: no subnet allocated for segment %s", seg.Name)
		}

		key := str(seg.Name)
		key.HeadComment = fmt.Sprintf("Private segment for plaintext to %s; only %s can reach it.\n%s",
			seg.Target, strings.Join(seg.Clients, ", "), reasonLines(seg.Reasons))

		ipam := mapping(str("subnet"), str(subnet.String()))
		if seg.Port != 0 {
			// An app's segment: replicas hold fixed addresses from .10 up,
			// so Docker hands out only the upper half (proxies, D-122).
			ipam.Content = append(ipam.Content, str("ip_range"), str(DynamicRange(subnet).String()))
		}

		nets.Content = append(nets.Content, key, mapping(
			str("internal"), boolean(true),
			str("ipam"), mapping(str("config"), seq(ipam)),
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

	if env := in.Secrets.Dotenv(); env != nil {
		add(EnvFile, env, 0o600)
	}

	return b, nil
}

// sharedVolume is the named volume a pod's containers share at SharedDir.
func sharedVolume(inst *model.Service) string { return inst.Name + "-shared" }

// podHolder renders an instance's namespace holder: it owns the networks,
// aliases and published ports, and sleeps. The app and its sidecars join
// its network namespace.
func podHolder(in Input, inst *model.Service) *yaml.Node {
	def := mapping(
		str("image"), str(sidecar.PodImage),
		str("command"), command([]string{"sleep", "infinity"}),
		// A tiny init reaps and forwards SIGTERM, so the holder stops at once.
		str("init"), boolean(true),
		str("healthcheck"), healthcheck(catalog.Health{
			Test:          []string{"CMD", "true"},
			Interval:      10 * time.Second,
			Timeout:       2 * time.Second,
			Retries:       3,
			StartPeriod:   5 * time.Second,
			StartInterval: 1 * time.Second,
		}),
	)
	if ports := publish(inst.App.Ports); ports != nil {
		def.Content = append(def.Content, str("ports"), ports)
	}

	def.Content = append(def.Content,
		str("networks"), networks(in, inst),
		str("labels"), podLabels(in, inst),
		str("mem_limit"), str(sidecar.PodResources.Memory),
		str("cpus"), float(sidecar.PodResources.CPUs),
		str("restart"), str("unless-stopped"),
	)

	return def
}

// podOf is the app instance whose pod a service runs in, or nil.
func podOf(m *model.Model, svc *model.Service) *model.Service {
	inst := svc
	if svc.Kind == model.Sidecar {
		inst, _ = m.Service(svc.Parent)
	}

	if inst == nil || inst.Pod == "" {
		return nil
	}

	return inst
}

// capture is a pod's LabelCapture value: transparent, guard (a terminate
// target's plaintext guard) or off. Each names the rules netinit leaves in
// the namespace, so a change recreates the holder and its joiners.
func capture(inst *model.Service) string {
	switch {
	case inst.Transparent:
		return "transparent"
	case inst.PlainGuard:
		return "guard"
	}

	return "off"
}

func podLabels(in Input, inst *model.Service) *yaml.Node {
	l := in.labels(inst.Pod, KindPod)
	l.Content = append(l.Content, str(LabelCapture), str(capture(inst)))

	return l
}

// netinit renders the pod's one-shot init container from the sidecar that
// asked for it. It is the only container that may hold extra capabilities.
func netinit(in Input, inst *model.Service, outs map[string]sidecar.Output, add func(string, []byte, os.FileMode)) (*yaml.Node, error) {
	var init *sidecar.Init

	for _, name := range inst.Sidecars {
		if o, ok := outs[name]; ok && o.Init != nil {
			if init != nil {
				return nil, fmt.Errorf("compose: %s: more than one sidecar asks for an init container", inst.Name)
			}

			init = o.Init
		}
	}

	if inst.PlainGuard {
		guard, err := plainGuard(in, inst)
		if err != nil {
			return nil, err
		}

		if init == nil {
			init = &sidecar.Init{Dockerfile: sidecar.NetinitDockerfile(), CapAdd: []string{"NET_ADMIN"}, Resources: sidecar.NetinitResources}
		} else {
			c := *init
			init = &c
		}
		// One script installs every rule of the namespace: a sidecar's,
		// then the guard's.
		if n := len(init.Command); n >= 3 && init.Command[0] == "/bin/sh" && init.Command[1] == "-c" {
			init.Command = []string{"/bin/sh", "-c", init.Command[2] + "\n" + guard}
		} else if n == 0 {
			init.Command = []string{"/bin/sh", "-c", guard}
		} else {
			return nil, fmt.Errorf("compose: %s: cannot add the plaintext guard to the init container's command", inst.Name)
		}

		if !slices.Contains(init.CapAdd, "NET_ADMIN") {
			init.CapAdd = append(slices.Clone(init.CapAdd), "NET_ADMIN")
		}
	}

	if init == nil {
		return nil, fmt.Errorf("compose: %s: transparent mode needs an init container, and no sidecar rendered one", inst.Name)
	}

	dir := "build/" + inst.Netinit
	add(dir+"/Dockerfile", init.Dockerfile, 0o644)
	hash := sha256.Sum256(append(append([]byte{}, init.Dockerfile...), strings.Join(init.Command, "\x00")...))

	def := mapping(str("build"), mapping(str("context"), str("./"+dir)))
	if len(init.Command) > 0 {
		def.Content = append(def.Content, str("command"), command(init.Command))
	}

	if len(init.CapAdd) > 0 {
		def.Content = append(def.Content, str("cap_add"), flow(strs(init.CapAdd...)...))
	}

	lbl := in.labels(inst.Netinit, KindInit)
	lbl.Content = append(lbl.Content, str(LabelConfigHash), str(hex.EncodeToString(hash[:])[:16]))

	res := init.Resources
	if res.Memory == "" {
		res = sidecar.PodResources
	}

	def.Content = append(def.Content,
		str("depends_on"), mapping(str(inst.Pod), mapping(str("condition"), str("service_healthy"))),
		str("network_mode"), str("service:"+inst.Pod),
		str("labels"), lbl,
		str("mem_limit"), str(res.Memory),
		str("cpus"), float(res.CPUs),
		// One shot: it exits after installing the rules, and must not restart.
		// Quoted, so no YAML 1.1 reader takes it for false.
		str("restart"), &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "no", Style: yaml.DoubleQuotedStyle},
	)

	return def, nil
}

// plainGuard is the netinit script of a terminate target's instance: its
// plaintext port answers only on its segment address.
func plainGuard(in Input, inst *model.Service) (string, error) {
	seg, ok := in.Model.Segment(inst.Group())
	if !ok || seg.Port == 0 {
		return "", fmt.Errorf("compose: %s: plaintext guard without a segment", inst.Name)
	}

	subnet, ok := in.Subnets[seg.Name]
	if !ok {
		return "", fmt.Errorf("compose: no subnet allocated for segment %s", seg.Name)
	}

	return sidecar.PlaintextGuardScript(seg.Port, InstanceSegmentAddress(subnet, inst.Instance).String()), nil
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

	srv.Upstreams = catalog.WithSecrets(svc.Upstreams, in.Secrets.Ref)

	srv.HostTargets = svc.HostTargets
	if svc.File != "" {
		if in.ReadFile == nil {
			return nil, nil, fmt.Errorf("compose: %s: file %s cannot be read here", svc.Name, svc.File)
		}

		data, err := in.ReadFile(svc.File)
		if err != nil {
			return nil, nil, fmt.Errorf("compose: %s: file: %w", svc.Name, err)
		}

		if data == nil {
			data = []byte{}
		}

		srv.UserConfig = data
	}

	if svc.StaticFile != "" {
		if in.ReadFile == nil {
			return nil, nil, fmt.Errorf("compose: %s: static_file %s cannot be read here", svc.Name, svc.StaticFile)
		}

		data, err := in.ReadFile(svc.StaticFile)
		if err != nil {
			return nil, nil, fmt.Errorf("compose: %s: static_file: %w", svc.Name, err)
		}

		srv.UserStatic = &catalog.UserFile{Path: svc.StaticFile, Data: data}
	}

	var c catalog.Container
	if svc.HostRouter {
		c = catalog.HostRouter(srv)
	} else {
		var err error
		if c, err = svc.Entry.Render(srv); err != nil {
			return nil, nil, fmt.Errorf("compose: %w", err)
		}
	}

	def := mapping(str("image"), str(svc.Entry.Image))
	if len(c.Entrypoint) > 0 {
		def.Content = append(def.Content, str("entrypoint"), command(c.Entrypoint))
	}

	if len(c.Command) > 0 {
		def.Content = append(def.Content, str("command"), command(c.Command))
	}

	env := append([]catalog.EnvVar{{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile}}, c.Environment...)
	def.Content = append(def.Content, str("environment"), environment(env))

	mounts = configFiles(svc.Name, c.Files, mounts, add)
	if svc.Entry.DataPath != "" {
		mounts = append([]string{svc.Name + "-data:" + svc.Entry.DataPath}, mounts...)
	}

	def.Content = append(def.Content, str("healthcheck"), healthcheck(c.Health))
	if svc.HostRouter {
		// Loopback only, like every published port (D-190).
		def.Content = append(def.Content, str("ports"), seq(str(fmt.Sprintf("127.0.0.1:%d:%d", in.HostPort, catalog.HostRouterPort))))
	}

	return def, mounts, nil
}

// configFiles writes a container's generated files under config/<service>/
// and mounts each read-only.
func configFiles(service string, files []catalog.File, mounts []string, add func(string, []byte, os.FileMode)) []string {
	for _, f := range files {
		p := "config/" + service + "/" + f.Name

		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0o644
		}

		add(p, f.Data, mode)
		mounts = append(mounts, "./"+p+":"+f.MountPath()+":ro")
	}

	return mounts
}

// sidecarService renders a sidecar from its type's output. Outside a pod
// (an app with only register) the agent joins the app's own namespace once
// the app has started; in a pod, every sidecar waits for the holder and
// the init container, and the app waits for the sidecars.
func sidecarService(in Input, svc, parent *model.Service, out sidecar.Output, mounts []string, add func(string, []byte, os.FileMode)) (*yaml.Node, []string, error) {
	c := out.Container
	if len(c.Health.Test) == 0 {
		return nil, nil, fmt.Errorf("compose: sidecar %s has no health check", svc.Name)
	}

	if svc.Image == "" {
		return nil, nil, fmt.Errorf("compose: sidecar %s has no image", svc.Name)
	}

	def := mapping(str("image"), str(svc.Image))
	if len(c.Entrypoint) > 0 {
		def.Content = append(def.Content, str("entrypoint"), command(c.Entrypoint))
	}

	if len(c.Command) > 0 {
		def.Content = append(def.Content, str("command"), command(c.Command))
	}

	env := append([]catalog.EnvVar{{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile}}, c.Environment...)
	def.Content = append(def.Content, str("environment"), environment(env))
	mounts = configFiles(svc.Name, c.Files, mounts, add)
	def.Content = append(def.Content, str("healthcheck"), healthcheck(c.Health))

	depends := mapping()
	if parent.Pod == "" {
		depends.Content = append(depends.Content, str(parent.Name), condition("service_started"))
	} else {
		depends.Content = append(depends.Content, str(parent.Pod), condition("service_healthy"))
		if parent.Netinit != "" {
			depends.Content = append(depends.Content, str(parent.Netinit), condition("service_completed_successfully"))
		}
	}

	for _, need := range out.Needs {
		if _, ok := in.Model.Service(need); !ok {
			return nil, nil, fmt.Errorf("compose: sidecar %s needs %s, which is not a service", svc.Name, need)
		}

		depends.Content = append(depends.Content, str(need), condition("service_healthy"))
	}

	def.Content = append(def.Content, str("depends_on"), depends)

	return def, mounts, nil
}

func condition(c string) *yaml.Node { return mapping(str("condition"), str(c)) }

// EnvGroup is a block of an app's environment: Breadboard's own variables
// (Service empty), one used service's env contract, or the variables a
// sidecar adds (Sidecar set).
type EnvGroup struct {
	Service   string
	Title     string // the service's catalog title, or the sidecar type's
	Plaintext bool
	Reason    string
	Sidecar   bool
	Vars      []catalog.EnvVar
	// Rewrites names the variables of earlier groups a sidecar set in
	// place (a pooler pointing DATABASE_URL at itself).
	Rewrites []string
}

// AppEnv is the environment an app instance receives, in order:
// Breadboard's own variables first, then one group per edge, then one per
// sidecar that adds or rewrites variables. When two used services share a
// variable name, the later one is prefixed with its service name. A sidecar
// may rewrite a variable in place (a pooler pointing DATABASE_URL at
// itself); its group then lists the name in Rewrites.
// ref resolves a service's secrets (values, or ${VAR} references in dotenv
// mode).
func AppEnv(m *model.Model, svc *model.Service, ref func(service string) func(key string) string, subnets map[string]netip.Prefix) ([]EnvGroup, error) {
	groups, _, err := appEnv(m, svc, ref, subnets, nil, nil)

	return groups, err
}

// appEnv is AppEnv plus the rendered output of each of the instance's
// sidecars. readFile reads files sidecars name (nil: their content is not
// needed, only the environment).
func appEnv(m *model.Model, svc *model.Service, ref func(service string) func(key string) string, subnets map[string]netip.Prefix, addrs map[string]netip.Addr, readFile func(string) ([]byte, error)) ([]EnvGroup, map[string]sidecar.Output, error) {
	base := []catalog.EnvVar{
		{Name: "SSL_CERT_FILE", Value: catalog.TrustBundleFile},
		// The app's own certificate, for apps that serve TLS to other apps.
		{Name: "TLS_CERT_FILE", Value: catalog.CertFile},
		{Name: "TLS_KEY_FILE", Value: catalog.KeyFile},
		{Name: "BB_SERVICE", Value: svc.Name},
		// The app's own address, matching its certificate.
		{Name: "BB_FQDN", Value: svc.FQDN},
	}
	if svc.Health != nil {
		base = append(base, catalog.EnvVar{Name: "BB_HEALTH_PORT", Value: strconv.Itoa(svc.Health.Port)})
	}

	if svc.Pod != "" {
		base = append(base, catalog.EnvVar{Name: "BB_SHARED_DIR", Value: sidecar.SharedDir})
	}

	if seg, ok := m.Segment(svc.Group()); ok && svc.Kind == model.App {
		// A terminating proxy sends plaintext here: the app binds its
		// segment address only, so no other container reaches the port.
		addr := "<segment address>" // before subnets are allocated (status)
		if p, ok := subnets[seg.Name]; ok {
			addr = InstanceSegmentAddress(p, svc.Instance).String()
		}

		base = append(base, catalog.EnvVar{Name: "BB_PLAIN_LISTEN_ADDR", Value: fmt.Sprintf("%s:%d", addr, seg.Port)})
	}

	groups := []EnvGroup{{Vars: base}}

	seen := map[string]bool{}
	for _, e := range base {
		seen[e.Name] = true
	}

	var uses []sidecar.Use

	for _, edge := range m.EdgesFrom(svc.Group()) {
		if edge.Via != "" {
			continue // a sidecar's outbound target: the sidecar sets its variables
		}

		target, _ := m.Service(edge.To)

		cl := catalog.Client{
			Service:   target.Name,
			FQDN:      target.FQDN,
			Config:    target.Config,
			Secret:    ref(target.Name),
			Upstreams: catalog.WithSecrets(target.Upstreams, ref),
		}
		if !edge.TLS {
			seg, _ := m.Segment(target.Name)
			cl.Plain = &catalog.Plain{FQDN: model.SegmentFQDN(target.Name, m.Domain), Subnet: subnets[seg.Name].String()}
		}

		g := EnvGroup{Service: target.Name, Title: target.Entry.Title, Plaintext: !edge.TLS, Reason: edge.Reason}
		for _, v := range target.Entry.Client(cl) {
			if seen[v.Name] {
				// A second service of the same type: prefix its variables.
				v.Name = secrets.EnvPrefixed(target.Name, v.Name)
			}

			seen[v.Name] = true
			g.Vars = append(g.Vars, v)
		}

		groups = append(groups, g)

		use := sidecar.Use{
			Service: target.Name, Type: target.Entry.ID, FQDN: target.FQDN, Port: edge.Port,
			TLS: edge.TLS, Config: target.Config, Env: slices.Clone(g.Vars),
		}
		if a, ok := addrs[target.Name]; ok && edge.TLS {
			use.Addresses = []string{a.String()}
		} else if seg, ok := m.Segment(target.Name); ok && !edge.TLS && addrs != nil {
			use.Addresses = []string{SegmentAddress(subnets[seg.Name]).String()}
		}

		uses = append(uses, use)
	}

	outs := map[string]sidecar.Output{}

	for _, name := range svc.Sidecars {
		sc, ok := m.Service(name)
		if !ok {
			return nil, nil, fmt.Errorf("compose: %s has no sidecar %s", svc.Name, name)
		}

		sin := sidecarInput(m, svc, sc, uses, ref, addrs)
		if sc.Sidecar != nil && sc.Sidecar.Envoy != nil && sc.Sidecar.Envoy.File != "" && readFile != nil {
			data, err := readFile(sc.Sidecar.Envoy.File)
			if err != nil {
				return nil, nil, fmt.Errorf("compose: %s: file: %w", name, err)
			}

			sin.UserFile = data
		}

		out, err := sc.SidecarType.RenderSidecar(sin)
		if err != nil {
			return nil, nil, fmt.Errorf("compose: %s: %w", name, err)
		}

		outs[name] = out

		g := EnvGroup{Service: name, Title: sc.SidecarType.Title + " sidecar", Sidecar: true}
		for _, v := range out.AppEnv {
			owner, found := varOwner(append(groups, g), v.Name)
			switch {
			case !found:
				g.Vars = append(g.Vars, v)
			case owner.Sidecar && slices.ContainsFunc(owner.Vars, func(o catalog.EnvVar) bool { return o == v }):
				// Another sidecar already gives the app exactly this
				// (every Fluent Bit sidecar sets the same BB_LOG_DIR).
			case out.Rewrites != "" && !owner.Sidecar && owner.Service == out.Rewrites:
				replaceVar(groups, v) // a pooler pointing its own service's variables at itself
				g.Rewrites = append(g.Rewrites, v.Name)
			default:
				// Validation reports this with both declarations' paths.
				who := "Breadboard"
				if owner.Service != "" {
					who = owner.Service
				}

				return nil, nil, fmt.Errorf("compose: %s sets %s, which %s already sets", name, v.Name, who)
			}
		}

		if len(g.Vars) > 0 || len(g.Rewrites) > 0 {
			groups = append(groups, g)
		}
	}

	return groups, outs, nil
}

// varOwner finds the group that sets a variable.
func varOwner(groups []EnvGroup, name string) (EnvGroup, bool) {
	for _, g := range groups {
		if slices.ContainsFunc(g.Vars, func(v catalog.EnvVar) bool { return v.Name == name }) {
			return g, true
		}
	}

	return EnvGroup{}, false
}

// replaceVar sets an existing variable's value in place.
func replaceVar(groups []EnvGroup, v catalog.EnvVar) bool {
	for i := range groups {
		for j := range groups[i].Vars {
			if groups[i].Vars[j].Name == v.Name {
				groups[i].Vars[j] = v

				return true
			}
		}
	}

	return false
}

// sidecarInput is what a sidecar's type renders from.
func sidecarInput(m *model.Model, inst, sc *model.Service, uses []sidecar.Use, ref func(string) func(string) string, addrs map[string]netip.Addr) sidecar.Input {
	in := sidecar.Input{
		Name:   sc.Name,
		Domain: m.Domain,
		App: sidecar.App{
			Service:    inst.Name,
			Name:       inst.Group(),
			FQDN:       inst.FQDN,
			SharedFQDN: inst.SharedFQDN,
			Instance:   inst.Instance,
		},
		Targets:     slices.Clone(sc.Targets),
		Uses:        uses,
		Secret:      ref,
		Transparent: inst.Transparent,
	}
	for i, t := range in.Targets {
		var owners []*model.Service
		if t.App {
			owners = m.Instances(t.Service)
		} else if svc, ok := m.Service(t.Service); ok {
			owners = []*model.Service{svc}
		}

		for _, o := range owners {
			if a, ok := addrs[networkOwner(o)]; ok {
				in.Targets[i].Addresses = append(in.Targets[i].Addresses, a.String())
			}
		}
	}

	if r := inst.App.Register; r != nil {
		// The app serves TLS on its register port, unless that port names
		// its Envoy sidecar's inbound listener.
		tls := r.Port
		for _, o := range inst.App.Sidecars {
			if o.Envoy != nil && o.Envoy.Inbound != nil && o.Envoy.Inbound.Port == r.Port {
				tls = 0
			}
		}

		if tls != 0 {
			in.AppTLSPorts = []int{tls}
		}
	}

	for _, name := range inst.Sidecars {
		if other, ok := m.Service(name); ok && name != sc.Name {
			in.Siblings = append(in.Siblings, other.SidecarType.ID)
		}
	}

	if sc.Sidecar != nil {
		in.Spec = *sc.Sidecar
	}

	if sc.Register != nil {
		registry, _ := m.Service(sc.Registry)
		in.Register = &sidecar.Register{
			Entry:           sc.Entry,
			RegistryService: registry.Name,
			RegistryFQDN:    registry.FQDN,
			Service:         sc.Register.Name,
			Port:            sc.Register.Port,
			Check:           sc.Register.Check,
			Tags:            sc.Register.Tags,
		}
	}

	return in
}

func appService(in Input, svc *model.Service, groups []EnvGroup) *yaml.Node {
	app := svc.App
	def := mapping(str("build"), mapping(
		str("context"), str(escape(in.AppDir(svc.Group()))),
		str("target"), str("dev"),
	))

	if len(app.Command) > 0 {
		cmd := make([]string, len(app.Command))
		for i, c := range app.Command {
			cmd[i] = escape(c)
		}

		def.Content = append(def.Content, str("command"), command(cmd))
	}

	envNode := environment(groups[0].Vars)

	depends := mapping()
	if svc.Pod != "" {
		depends.Content = append(depends.Content, str(svc.Pod), condition("service_healthy"))
		if svc.Netinit != "" {
			depends.Content = append(depends.Content, str(svc.Netinit), condition("service_completed_successfully"))
		}
	}

	for _, g := range groups[1:] {
		for i, v := range g.Vars {
			k := str(v.Name)
			if i == 0 {
				k.HeadComment = fmt.Sprintf("%s (%s)", g.Service, g.Title)
				if g.Plaintext {
					k.HeadComment = fmt.Sprintf("%s (%s): PLAINTEXT edge (tls.mode: off). Reason: %s", g.Service, g.Title, g.Reason)
				}
			}

			envNode.Content = append(envNode.Content, k, str(v.Value))
		}

		if !g.Sidecar {
			depends.Content = append(depends.Content, str(g.Service), condition("service_healthy"))
		}
	}

	if svc.Pod != "" {
		// The app starts last: its sidecars are ready before its first packet.
		for _, name := range svc.Sidecars {
			depends.Content = append(depends.Content, str(name), condition("service_healthy"))
		}
	}

	def.Content = append(def.Content, str("environment"), envNode)
	if ports := publish(app.Ports); ports != nil && svc.Pod == "" {
		def.Content = append(def.Content, str("ports"), ports)
	}

	if svc.Transparent {
		// Transparent mode exempts the sidecars' reserved users from its
		// rules, so the app must never become one of them.
		def.Content = append(def.Content,
			str("security_opt"), flow(str("no-new-privileges:true")),
			str("cap_drop"), flow(str("SETUID"), str("SETGID")),
		)
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

// publish renders published ports, bound to 127.0.0.1; nil when none.
func publish(ports []int) *yaml.Node {
	if len(ports) == 0 {
		return nil
	}

	var out []*yaml.Node
	for _, p := range ports {
		out = append(out, str(fmt.Sprintf("127.0.0.1:%d:%d", p, p)))
	}

	return seq(out...)
}

// networkOwner is the service whose container holds an instance's place on
// the default network: its pod holder, or itself.
func networkOwner(svc *model.Service) string {
	if svc.Pod != "" {
		return svc.Pod
	}

	return svc.Name
}

func networks(in Input, svc *model.Service) *yaml.Node {
	m := in.Model

	def := mapping()
	if a, ok := in.Addresses[networkOwner(svc)]; ok {
		def.Content = append(def.Content, str("ipv4_address"), str(a.String()))
	}

	def.Content = append(def.Content, str("aliases"), flow(strs(svc.Aliases...)...))

	n := mapping(str(DefaultNetwork), def)
	for _, seg := range m.SegmentsOf(svc.Group()) {
		def := mapping()

		switch {
		case seg.Target == svc.Name && svc.Kind != model.App:
			def.Content = append(def.Content,
				str("ipv4_address"), str(SegmentAddress(in.Subnets[seg.Name]).String()),
				str("aliases"), flow(str(model.SegmentFQDN(svc.Name, m.Domain))),
			)
		case seg.Target == svc.Group() && svc.Kind == model.App:
			// Every replica answers to the app's segment alias, and to its own.
			aliases := []string{model.SegmentFQDN(svc.Name, m.Domain)}
			if svc.Name != svc.Group() {
				aliases = append(aliases, model.SegmentFQDN(svc.Group(), m.Domain))
			}

			def.Content = append(def.Content,
				str("ipv4_address"), str(InstanceSegmentAddress(in.Subnets[seg.Name], svc.Instance).String()),
				str("aliases"), flow(strs(aliases...)...),
			)
		default:
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
