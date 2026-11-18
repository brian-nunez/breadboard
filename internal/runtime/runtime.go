// Package runtime runs experiments through the docker and docker compose
// CLIs. The core reaches it through an interface, so core tests use a fake.
package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/netip"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/errs"
)

// Project identifies a compose project on disk.
type Project struct {
	Name  string   // compose project name, bb-<experiment>
	Dir   string   // directory holding the compose files
	Files []string // compose files, absolute, in -f order
}

// Container is one container's state.
type Container struct {
	ID        string
	Service   string
	Kind      string
	Image     string
	State     string // created, running, restarting, exited, ...
	Health    string // starting, healthy, unhealthy, or empty without a health check
	ExitCode  int
	OOMKilled bool
	StartedAt time.Time
	// RestartCount is how many times the restart policy has restarted it.
	RestartCount int
}

// Usage is a container's live resource use.
type Usage struct {
	CPUPercent  float64
	MemoryBytes uint64
	MemoryLimit uint64
}

// LogLine is one line of container output.
type LogLine struct {
	Service string
	Text    string
}

// Compose implements the runtime with the docker CLI.
type Compose struct {
	// Bin is the docker executable; empty means "docker" on PATH.
	Bin string
}

// New returns a runtime using docker from PATH.
func New() *Compose { return &Compose{} }

func (c *Compose) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return "docker"
}

type cmdError struct {
	args   []string
	err    error
	stderr string
}

func (e *cmdError) Error() string {
	msg := strings.TrimSpace(e.stderr)
	if lines := strings.Split(msg, "\n"); len(lines) > 30 {
		msg = strings.Join(lines[len(lines)-30:], "\n")
	}
	if msg == "" {
		return fmt.Sprintf("docker %s: %v", strings.Join(e.args, " "), e.err)
	}
	return fmt.Sprintf("docker %s: %v\n%s", strings.Join(e.args, " "), e.err, msg)
}

func (e *cmdError) Unwrap() error { return e.err }

func (c *Compose) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, fmt.Errorf("%w: the docker CLI is not on PATH", errs.ErrDockerUnavailable)
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Some commands (inspect of several objects) print what they found
		// even when one object is missing, so the output goes back too.
		return stdout.Bytes(), &cmdError{args: args, err: err, stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

func (p Project) args() []string {
	a := []string{"compose", "--project-name", p.Name, "--project-directory", p.Dir}
	for _, f := range p.Files {
		a = append(a, "--file", f)
	}
	return a
}

// Available checks that the docker daemon and the compose plugin respond.
func (c *Compose) Available(ctx context.Context) error {
	if _, err := c.run(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		if errors.Is(err, errs.ErrDockerUnavailable) || ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("%w: %v", errs.ErrDockerUnavailable, err)
	}
	if _, err := c.run(ctx, "compose", "version"); err != nil {
		return fmt.Errorf("%w: docker compose is not installed: %v", errs.ErrDockerUnavailable, err)
	}
	return nil
}

// ErrSubnetTaken means Docker refused to create a network because its
// subnet overlaps a network that already exists. Another process (a second
// bb, or any docker client) created that network after the subnet was
// chosen, so choosing again sees it and avoids it.
var ErrSubnetTaken = errors.New("a network's subnet is taken by another network")

// subnetTaken matches the daemon's messages for ErrSubnetTaken: IPAM's,
// when the subnet is already allocated, and the bridge driver's, when it
// overlaps an existing bridge.
var subnetTaken = regexp.MustCompile(`Pool overlaps with other one on this address space|networks have overlapping IPv4`)

// Up builds app images and starts every service in the background.
func (c *Compose) Up(ctx context.Context, p Project) error {
	_, err := c.run(ctx, append(p.args(), "up", "--detach", "--build", "--remove-orphans")...)
	if ce := (*cmdError)(nil); errors.As(err, &ce) && subnetTaken.MatchString(ce.stderr) {
		return fmt.Errorf("%w: %w", ErrSubnetTaken, err)
	}
	return err
}

// Build builds the images of the given services without starting anything.
func (c *Compose) Build(ctx context.Context, p Project, services []string) error {
	_, err := c.run(ctx, append(append(p.args(), "build"), services...)...)
	return err
}

// ErrUserUnresolved means an image's USER is a name the image cannot map to
// a numeric uid (it has no passwd entry, or no id command to ask).
var ErrUserUnresolved = errors.New("cannot resolve the image's user to a uid")

// ImageUser is the user a service's built image starts its process as.
type ImageUser struct {
	Image string // the image name compose gave the service
	User  string // Config.User as written in the image: empty, a uid, or a name, maybe with :group
	UID   int
}

// ImageUser resolves the uid a built service image starts as: Config.User
// from docker image inspect (empty means root), a number as is, and a name
// through `id -u <name>` run in the image with no network.
func (c *Compose) ImageUser(ctx context.Context, p Project, service string) (ImageUser, error) {
	out, err := c.run(ctx, append(p.args(), "config", "--images", service)...)
	if err != nil {
		return ImageUser{}, err
	}
	// The list includes the images of the services it depends on; compose
	// names a built service's image <project>-<service>.
	u := ImageUser{Image: p.Name + "-" + service}
	if !slices.Contains(strings.Fields(string(out)), u.Image) {
		return ImageUser{}, fmt.Errorf("runtime: compose builds no image %s for %s (it lists %s)", u.Image, service, strings.Join(strings.Fields(string(out)), ", "))
	}
	out, err = c.run(ctx, "image", "inspect", "--format", "{{.Config.User}}", u.Image)
	if err != nil {
		return ImageUser{}, err
	}
	u.User = strings.TrimSpace(string(out))
	name, _, _ := strings.Cut(u.User, ":")
	if name == "" {
		return u, nil // root
	}
	if n, err := strconv.Atoi(name); err == nil {
		u.UID = n
		return u, nil
	}
	out, err = c.run(ctx, "run", "--rm", "--network", "none", "--entrypoint", "id", u.Image, "-u", name)
	if err != nil {
		return u, fmt.Errorf("%w: user %q of %s: %v", ErrUserUnresolved, name, u.Image, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return u, fmt.Errorf("%w: user %q of %s: id printed %q", ErrUserUnresolved, name, u.Image, strings.TrimSpace(string(out)))
	}
	u.UID = n
	return u, nil
}

// Down stops and removes the project's containers and networks, and its
// volumes when volumes is true. It works without compose files too.
func (c *Compose) Down(ctx context.Context, p Project, volumes bool) error {
	args := append(p.args(), "down", "--remove-orphans")
	if volumes {
		args = append(args, "--volumes")
	}
	_, err := c.run(ctx, args...)
	return err
}

// RemoveOwned removes networks, and volumes when volumes is true, labeled with
// experimentID. Compose only removes what the current compose files declare,
// so this catches resources of services or segments removed since an earlier
// up. Matching on the experiment id, not the name, leaves a same-named
// experiment from another directory alone. Run it after Down.
func (c *Compose) RemoveOwned(ctx context.Context, experimentID string, volumes bool) error {
	if experimentID == "" {
		return errors.New("runtime: RemoveOwned needs an experiment id")
	}
	filter := "label=" + labelExperimentID + "=" + experimentID
	kinds := []string{"network"}
	if volumes {
		kinds = append(kinds, "volume")
	}
	for _, kind := range kinds {
		out, err := c.run(ctx, kind, "ls", "--quiet", "--filter", filter)
		if err != nil {
			return err
		}
		if names := strings.Fields(string(out)); len(names) > 0 {
			if _, err := c.run(ctx, append([]string{kind, "rm"}, names...)...); err != nil {
				return err
			}
		}
	}
	return nil
}

// Containers lists an experiment's containers by label.
func (c *Compose) Containers(ctx context.Context, experiment string) ([]Container, error) {
	out, err := c.run(ctx, "ps", "--all", "--quiet", "--no-trunc", "--filter", "label=breadboard.experiment="+experiment)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = c.run(ctx, append([]string{"inspect"}, ids...)...)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		ID           string `json:"Id"`
		RestartCount int    `json:"RestartCount"`
		Config       struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status    string    `json:"Status"`
			ExitCode  int       `json:"ExitCode"`
			OOMKilled bool      `json:"OOMKilled"`
			StartedAt time.Time `json:"StartedAt"`
			Health    *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	list := make([]Container, 0, len(raw))
	for _, r := range raw {
		ct := Container{
			ID:           r.ID,
			RestartCount: r.RestartCount,
			Service:      r.Config.Labels["breadboard.service"],
			Kind:         r.Config.Labels["breadboard.kind"],
			Image:        r.Config.Image,
			State:        r.State.Status,
			ExitCode:     r.State.ExitCode,
			OOMKilled:    r.State.OOMKilled,
			StartedAt:    r.State.StartedAt,
		}
		if r.State.Health != nil {
			ct.Health = r.State.Health.Status
		}
		list = append(list, ct)
	}
	return list, nil
}

// PublishedPort is a host port a container publishes (or will publish
// when it starts), with the experiment and service labels of its owner.
type PublishedPort struct {
	HostIP       string
	HostPort     int
	ExperimentID string // empty for a container no experiment owns
	Service      string
	// Running reports whether the container runs and so holds the port
	// now; a stopped container only reserves it.
	Running bool
}

// PublishedPorts lists the fixed host ports of every container, running or
// not: a stopped container takes its ports back when it starts.
func (c *Compose) PublishedPorts(ctx context.Context) ([]PublishedPort, error) {
	out, err := c.run(ctx, "ps", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = c.run(ctx, append([]string{"inspect"}, ids...)...)
	// A container removed between the list and the inspect publishes
	// nothing any more: inspect still prints every other one.
	if ce := (*cmdError)(nil); errors.As(err, &ce) && onlyMissingContainers(ce.stderr) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	return parsePublishedPorts(out)
}

func parsePublishedPorts(data []byte) ([]PublishedPort, error) {
	var raw []struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
		HostConfig struct {
			PortBindings map[string][]struct {
				HostIP   string `json:"HostIp"`
				HostPort string `json:"HostPort"`
			} `json:"PortBindings"`
		} `json:"HostConfig"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("docker inspect: %w", err)
	}
	var list []PublishedPort
	for _, r := range raw {
		for _, bindings := range r.HostConfig.PortBindings {
			for _, b := range bindings {
				lo, hi, _ := strings.Cut(b.HostPort, "-")
				first, err := strconv.Atoi(lo)
				if err != nil {
					continue // empty: Docker picks an ephemeral port
				}
				last := first
				if hi != "" {
					if last, err = strconv.Atoi(hi); err != nil {
						last = first
					}
				}
				for port := first; port <= last; port++ {
					list = append(list, PublishedPort{HostIP: b.HostIP, HostPort: port, ExperimentID: r.Config.Labels[labelExperimentID], Service: r.Config.Labels["breadboard.service"], Running: r.State.Running})
				}
			}
		}
	}
	return list, nil
}

// missingContainer is the daemon's message for a container that does not
// exist.
var missingContainer = regexp.MustCompile(`^Error(: No such (object|container)| response from daemon: No such container): \S+$`)

// onlyMissingContainers reports whether every line of an inspect's stderr
// says a container does not exist.
func onlyMissingContainers(stderr string) bool {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	return lines[0] != "" && !slices.ContainsFunc(lines, func(l string) bool { return !missingContainer.MatchString(strings.TrimSpace(l)) })
}

// Exec runs a command in the running container of an experiment's service
// and returns its standard output. It is how the web UI's explorers read
// service data with each service's own tools (ADR-0012).
func (c *Compose) Exec(ctx context.Context, experiment, service string, cmd []string) ([]byte, error) {
	out, err := c.run(ctx, "ps", "--quiet", "--no-trunc",
		"--filter", "label=breadboard.experiment="+experiment,
		"--filter", "label=breadboard.service="+service,
		"--filter", "status=running")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s is not running: %w", service, errs.ErrNotFound)
	}
	if out, err = c.run(ctx, append([]string{"exec", ids[0]}, cmd...)...); err != nil {
		return nil, err
	}
	return out, nil
}

// Stats samples CPU and memory use once for running containers.
func (c *Compose) Stats(ctx context.Context, ids []string) (map[string]Usage, error) {
	if len(ids) == 0 {
		return map[string]Usage{}, nil
	}
	out, err := c.run(ctx, append([]string{"stats", "--no-stream", "--no-trunc", "--format", "{{json .}}"}, ids...)...)
	if err != nil {
		return nil, err
	}
	usage := map[string]Usage{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if id, u, ok := parseStatsLine(sc.Bytes()); ok {
			usage[id] = u
		}
	}
	return usage, nil
}

// StatsStream runs one streaming docker stats over every running
// container, a container started later included, and yields each row as
// docker refreshes it (about once a second) until ctx is cancelled or the
// command ends. It costs one process however many readers use the result.
func (c *Compose) StatsStream(ctx context.Context) iter.Seq2[Sample, error] {
	return func(yield func(Sample, error) bool) {
		cmd := exec.CommandContext(ctx, c.bin(), "stats", "--no-trunc", "--format", "{{json .}}")
		out, err := cmd.StdoutPipe()
		if err != nil {
			yield(Sample{}, err)
			return
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			yield(Sample{}, err)
			return
		}
		stopCmd := func() {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		sc := bufio.NewScanner(out)
		// A row is a few hundred bytes; a container name can make it longer.
		sc.Buffer(make([]byte, 0, 64<<10), statsRowMax)
		for sc.Scan() {
			if id, u, ok := parseStatsLine(sc.Bytes()); ok {
				if !yield(Sample{ID: id, Usage: u}, nil) {
					stopCmd()
					return
				}
			}
		}
		if err := sc.Err(); err != nil {
			// Reading stopped (a row over the limit, say) while docker
			// stats may still be writing: end it so the caller can retry.
			stopCmd()
			if ctx.Err() == nil {
				yield(Sample{}, fmt.Errorf("docker stats: read: %w", err))
			}
			return
		}
		if err := cmd.Wait(); err != nil && ctx.Err() == nil {
			yield(Sample{}, fmt.Errorf("docker stats: %w: %s", err, strings.TrimSpace(stderr.String())))
		}
	}
}

// statsRowMax is the longest stats row StatsStream reads.
const statsRowMax = 1 << 20

// Sample is one container's usage from a stats stream.
type Sample struct {
	ID    string
	Usage Usage
}

// parseStatsLine reads one row of docker stats --format '{{json .}}'.
// Streaming output puts terminal control sequences before and after the
// JSON (\x1b[H{...}\x1b[K).
func parseStatsLine(line []byte) (string, Usage, bool) {
	i, j := bytes.IndexByte(line, '{'), bytes.LastIndexByte(line, '}')
	if i < 0 || j < i {
		return "", Usage{}, false
	}
	line = line[:j+1]
	var row struct {
		ID       string `json:"ID"`
		CPUPerc  string `json:"CPUPerc"`
		MemUsage string `json:"MemUsage"`
	}
	if json.Unmarshal(line[i:], &row) != nil || row.ID == "" {
		return "", Usage{}, false
	}
	u := Usage{}
	u.CPUPercent, _ = strconv.ParseFloat(strings.TrimSuffix(row.CPUPerc, "%"), 64)
	if used, limit, ok := strings.Cut(row.MemUsage, "/"); ok {
		u.MemoryBytes = parseSize(used)
		u.MemoryLimit = parseSize(limit)
	}
	return row.ID, u, true
}

var sizeRe = regexp.MustCompile(`^([0-9.]+)\s*([A-Za-z]*)$`)

func parseSize(s string) uint64 {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	mult := map[string]float64{
		"": 1, "B": 1,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
		"kB": 1e3, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}[m[2]]
	return uint64(v * mult)
}

var logPrefix = regexp.MustCompile(`^(\S+?)(-\d+)?\s+\| ?(.*)$`)

// Logs streams service output. With follow it runs until ctx is cancelled.
func (c *Compose) Logs(ctx context.Context, p Project, services []string, follow bool, tail int) iter.Seq2[LogLine, error] {
	return func(yield func(LogLine, error) bool) {
		args := append(p.args(), "logs", "--no-color")
		if follow {
			args = append(args, "--follow")
		}
		if tail > 0 {
			args = append(args, "--tail", strconv.Itoa(tail))
		}
		args = append(args, services...)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		cmd := exec.CommandContext(ctx, c.bin(), args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.StdoutPipe()
		if err != nil {
			yield(LogLine{}, err)
			return
		}
		if err := cmd.Start(); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				err = fmt.Errorf("%w: the docker CLI is not on PATH", errs.ErrDockerUnavailable)
			}
			yield(LogLine{}, err)
			return
		}
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := LogLine{Text: sc.Text()}
			if m := logPrefix.FindStringSubmatch(line.Text); m != nil {
				line.Service, line.Text = m[1], m[3]
			}
			if !yield(line, nil) {
				cancel()
				_ = cmd.Wait()
				return
			}
		}
		err = cmd.Wait()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			yield(LogLine{}, &cmdError{args: args, err: err, stderr: stderr.String()})
		}
	}
}

// Network is a docker network's subnet and the Breadboard experiment id
// that owns it, if any.
type Network struct {
	Name         string
	Subnet       netip.Prefix
	ExperimentID string
	// IPRange is the part of Subnet Docker hands out dynamically; zero
	// when the network has none (the whole subnet).
	IPRange netip.Prefix
}

// labelExperimentID matches compose.LabelExperimentID.
const labelExperimentID = "breadboard.experiment-id"

// Owners lists the distinct experiment ids on containers, networks, and
// volumes labeled for experiment. An empty string means an unlabeled resource.
func (c *Compose) Owners(ctx context.Context, experiment string) ([]string, error) {
	filter := "label=breadboard.experiment=" + experiment
	// The id= prefix keeps unlabeled resources visible as an empty id.
	format := `id={{.Label "` + labelExperimentID + `"}}`
	seen := map[string]bool{}
	var out []string
	for _, args := range [][]string{
		{"ps", "--all", "--filter", filter, "--format", format},
		{"network", "ls", "--filter", filter, "--format", format},
		{"volume", "ls", "--filter", filter, "--format", format},
	} {
		data, err := c.run(ctx, args...)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(data), "\n") {
			id, ok := strings.CutPrefix(strings.TrimSpace(line), "id=")
			if !ok {
				continue
			}
			if id == "<no value>" {
				id = ""
			}
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out, nil
}

// Subnets lists every docker network subnet on this host with its owner.
func (c *Compose) Subnets(ctx context.Context) ([]Network, error) {
	out, err := c.run(ctx, "network", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil, nil
	}
	out, err = c.run(ctx, append([]string{"network", "inspect"}, ids...)...)
	// A network removed between the list and the inspect (another lab's
	// down) holds no subnet any more: inspect still prints every other one.
	if ce := (*cmdError)(nil); errors.As(err, &ce) && onlyMissingNetworks(ce.stderr) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name   string            `json:"Name"`
		Labels map[string]string `json:"Labels"`
		IPAM   struct {
			Config []struct {
				Subnet  string `json:"Subnet"`
				IPRange string `json:"IPRange"`
			} `json:"Config"`
		} `json:"IPAM"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("docker network inspect: %w", err)
	}
	var list []Network
	for _, n := range raw {
		for _, cfg := range n.IPAM.Config {
			if p, err := netip.ParsePrefix(cfg.Subnet); err == nil {
				r, _ := netip.ParsePrefix(cfg.IPRange) // zero when unset
				list = append(list, Network{Name: n.Name, Subnet: p, ExperimentID: n.Labels[labelExperimentID], IPRange: r})
			}
		}
	}
	return list, nil
}

// missingNetwork is the daemon's message for a network that does not exist
// (current and older wordings).
var missingNetwork = regexp.MustCompile(`^(Error response from daemon: network \S+ not found|Error: No such network: \S+)$`)

// onlyMissingNetworks reports whether every line of an inspect's stderr
// says a network does not exist.
func onlyMissingNetworks(stderr string) bool {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	return lines[0] != "" && !slices.ContainsFunc(lines, func(l string) bool { return !missingNetwork.MatchString(strings.TrimSpace(l)) })
}
