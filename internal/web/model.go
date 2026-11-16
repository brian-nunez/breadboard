package web

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
)

// nearLimit is the share of a memory limit at which a service shows amber.
const nearLimit = 0.85

// dashboardData is what every dashboard page renders: the experiment card
// and service list in the sidebar, and the overview's content.
type dashboardData struct {
	Dir          string
	Version      string
	NoExperiment bool
	Problem      string // why status could not be read, e.g. Docker is not running
	Experiment   string
	Domain       string
	Services     []row
	Warnings     []string
}

// row is one service as the dashboard shows it.
type row struct {
	core.ServiceStatus
	CPUUsed  float64 // cores
	CPULimit float64 // cores
	MemUsed  float64 // MiB
	MemLimit float64 // MiB
	TLS      bool    // false when any of its edges is plaintext
}

func newRow(s core.ServiceStatus) row {
	r := row{ServiceStatus: s, TLS: true}
	r.CPUUsed = s.CPUPercent / 100
	r.CPULimit, _ = strconv.ParseFloat(s.CPULimit, 64)
	r.MemUsed = float64(s.MemoryBytes) / (1 << 20)
	r.MemLimit = float64(dockerBytes(s.MemoryLimit)) / (1 << 20)
	for _, e := range s.Uses {
		if !e.TLS {
			r.TLS = false
		}
	}
	return r
}

// dockerBytes parses a Docker size such as 512m or 1g.
func dockerBytes(s string) uint64 {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := uint64(1)
	switch {
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "b"):
		s = strings.TrimSuffix(s, "b")
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return uint64(v * float64(mult))
}

// Status is the word shown next to a service, and its colour.
type status struct {
	Word  string
	Color string // green, amber, red, blue, grey
}

func (r row) Status() status {
	switch {
	case r.Health == core.HealthUnhealthy || r.OOMKilled:
		return status{"Unhealthy", "red"}
	case r.Health == core.HealthStarting:
		return status{"Starting", "blue"}
	case r.Health == core.HealthHealthy && r.MemLimit > 0 && r.MemUsed/r.MemLimit >= nearLimit:
		return status{"Near limit", "amber"}
	case r.Health == core.HealthHealthy:
		return status{"Healthy", "green"}
	case r.State == core.StateNotCreated:
		return status{"Not started", "grey"}
	default:
		return status{strings.ToUpper(r.Health[:1]) + r.Health[1:], "grey"}
	}
}

// Subtitle is the second line under a service's name: the image for infra
// and sidecars, the source directory and command for apps.
func (r row) Subtitle() string {
	if r.Kind == "app" && r.Path != "" {
		if len(r.Command) > 0 {
			return r.Path + " · " + strings.Join(r.Command, " ")
		}
		return r.Path
	}
	if r.Kind == "sidecar" {
		return "registers " + r.Parent + " · " + r.Image
	}
	return r.Image
}

// homeRelative shortens a path under the home directory to ~/...
func homeRelative(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~/" + rest
	}
	return p
}

// Initial is the letter in the service's badge.
func (r row) Initial() string { return strings.ToUpper(r.Name[:1]) }

func (d dashboardData) Healthy() int {
	n := 0
	for _, s := range d.Services {
		if s.Health == core.HealthHealthy {
			n++
		}
	}
	return n
}

// Edges counts edges and how many use TLS.
func (d dashboardData) Edges() (tls, total int) {
	for _, s := range d.Services {
		for _, e := range s.Uses {
			total++
			if e.TLS {
				tls++
			}
		}
	}
	return tls, total
}

func (d dashboardData) Memory() (used, limit float64) {
	for _, s := range d.Services {
		used += s.MemUsed
		limit += s.MemLimit
	}
	return used, limit
}

// Uptime is how long the longest-running service has been up.
func (d dashboardData) Uptime() string {
	var first time.Time
	for _, s := range d.Services {
		if s.State == "running" && !s.StartedAt.IsZero() && (first.IsZero() || s.StartedAt.Before(first)) {
			first = s.StartedAt
		}
	}
	if first.IsZero() {
		return ""
	}
	return "up " + humanDuration(time.Since(first))
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func gib(mib float64) string { return fmt.Sprintf("%.2f GiB", mib/1024) }

// pct is a bar's fill, 0-100.
func pct(used, limit float64) string {
	if limit <= 0 {
		return "0"
	}
	p := used / limit * 100
	if p > 100 {
		p = 100
	}
	return fmt.Sprintf("%.0f", p)
}

// commas formats an integer with thousands separators.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// size formats bytes in binary units.
func size(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(b)/(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}

// ttl describes a Redis TTL in seconds.
func ttl(sec int64) string {
	switch {
	case sec == -1:
		return "never"
	case sec < 0:
		return "—"
	}
	return "in " + humanDuration(time.Duration(sec)*time.Second)
}

func tableName(t core.PostgresTable) string {
	if t.Schema == "public" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// container is a service's container name in compose's naming scheme, for
// commands the page shows.
func container(experiment, service string) string { return "bb-" + experiment + "-" + service + "-1" }
