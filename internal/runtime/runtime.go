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
		return nil, &cmdError{args: args, err: err, stderr: stderr.String()}
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

// Up builds app images and starts every service in the background.
func (c *Compose) Up(ctx context.Context, p Project) error {
	_, err := c.run(ctx, append(p.args(), "up", "--detach", "--build", "--remove-orphans")...)
	return err
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
		ID     string `json:"Id"`
		Config struct {
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
			ID:        r.ID,
			Service:   r.Config.Labels["breadboard.service"],
			Kind:      r.Config.Labels["breadboard.kind"],
			Image:     r.Config.Image,
			State:     r.State.Status,
			ExitCode:  r.State.ExitCode,
			OOMKilled: r.State.OOMKilled,
			StartedAt: r.State.StartedAt,
		}
		if r.State.Health != nil {
			ct.Health = r.State.Health.Status
		}
		list = append(list, ct)
	}
	return list, nil
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
		var row struct {
			ID       string `json:"ID"`
			CPUPerc  string `json:"CPUPerc"`
			MemUsage string `json:"MemUsage"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil {
			continue
		}
		u := Usage{}
		u.CPUPercent, _ = strconv.ParseFloat(strings.TrimSuffix(row.CPUPerc, "%"), 64)
		if used, limit, ok := strings.Cut(row.MemUsage, "/"); ok {
			u.MemoryBytes = parseSize(used)
			u.MemoryLimit = parseSize(limit)
		}
		usage[row.ID] = u
	}
	return usage, nil
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
	Subnet       netip.Prefix
	ExperimentID string
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
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Labels map[string]string `json:"Labels"`
		IPAM   struct {
			Config []struct {
				Subnet string `json:"Subnet"`
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
				list = append(list, Network{Subnet: p, ExperimentID: n.Labels[labelExperimentID]})
			}
		}
	}
	return list, nil
}
