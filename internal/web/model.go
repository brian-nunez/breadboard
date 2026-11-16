package web

import (
	"cmp"
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
	case r.State == core.StateNotCreated || r.Health == "":
		return status{"Not started", "grey"}
	default:
		return status{strings.ToUpper(r.Health[:1]) + r.Health[1:], "grey"}
	}
}

// Subtitle is the second line under a service's name: the image for infra
// and sidecars, the source directory and command for apps.
func (r row) Subtitle() string {
	if r.Kind == "app" && r.Path != "" {
		s := r.Path
		if len(r.Command) > 0 {
			s += " · " + strings.Join(r.Command, " ")
		}
		if r.Replicas > 1 {
			s = fmt.Sprintf("replica %d of %d · %s", r.Instance, r.Replicas, s)
		}
		return s
	}
	if r.Kind == "sidecar" && r.Registry != "" {
		return "registers " + r.Parent + " · " + r.Image
	}
	if r.Kind == "sidecar" {
		return cmp.Or(r.Type, "sidecar") + " sidecar of " + r.Parent + " · " + r.Image
	}
	return r.Image
}

// Label is how a service is named where an app counts once: the app's name
// for an instance of an app with replicas.
func (r row) Label() string {
	if r.Replicas > 1 && r.App != "" {
		return r.App
	}
	return r.Name
}

// instancesOf lists an app's instances.
func instancesOf(rows []row, app string) []row {
	var out []row
	for _, r := range rows {
		if r.Kind == "app" && r.App == app {
			out = append(out, r)
		}
	}
	return out
}

// sidecarsOf lists the sidecars in an app instance's pod.
func sidecarsOf(rows []row, instance string) []row {
	var out []row
	for _, r := range rows {
		if r.Kind == "sidecar" && r.Parent == instance {
			out = append(out, r)
		}
	}
	return out
}

// sidecarMode is a sidecar's type, marked when it is a transparent Envoy.
func sidecarMode(r row) string {
	if r.Sidecar != nil && r.Sidecar.Transparent {
		return r.Type + " · transparent, egress " + r.Sidecar.Egress
	}
	return r.Type
}

// portAddr is where a sidecar port is bound inside the pod.
func portAddr(p core.SidecarPort) string {
	if p.Loopback {
		return fmt.Sprintf("127.0.0.1:%d", p.Port)
	}
	return fmt.Sprintf(":%d", p.Port)
}

// firstInstanceUses is a row's edges, once per app: replicas after the
// first share them.
func firstInstanceUses(r row) []core.Edge {
	if r.Instance > 1 {
		return nil
	}
	return r.Uses
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
		for _, e := range firstInstanceUses(s) {
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

func clusterTitle(c core.EnvoyCluster) string {
	if c.Target != "" {
		return c.Name + " → " + c.Target
	}
	return c.Name
}

// clusterRole explains what an Envoy cluster carries.
func clusterRole(c core.EnvoyCluster) string {
	switch c.Role {
	case "outbound":
		return "The app's calls to " + c.Target + ", sent over verified TLS and balanced across its addresses."
	case "inbound":
		return "Requests that arrived over TLS, forwarded to the app on loopback."
	case "egress":
		return "Transparent mode: connections to undeclared destinations, passed to their original address."
	case "inbound passthrough":
		return "Transparent mode: connections into the pod, forwarded to the app on loopback."
	case "upstream":
		return "Upstream " + c.Target + " (" + modeName(c.Mode) + "): " + modeHelp(c.Mode) + "."
	}
	return "Defined by your own Envoy configuration."
}

// hostName is the replica or service at a host's address.
func hostName(h core.EnvoyHost) string {
	if h.Instance != "" {
		return h.Instance
	}
	if h.Hostname != "" {
		return h.Hostname
	}
	return h.Address
}

func hostStatus(h core.EnvoyHost) status {
	switch {
	case h.Ejected:
		return status{"Ejected", "red"}
	case !h.Healthy:
		return status{strings.Join(h.Flags, ", "), "red"}
	case h.ConnectFailures > 0 || h.Errors > 0:
		return status{"Healthy, with errors", "amber"}
	}
	return status{"Healthy", "green"}
}

// clusterRequests is every request the cluster's hosts served.
func clusterRequests(c core.EnvoyCluster) int64 {
	var n int64
	for _, h := range c.Hosts {
		n += h.Requests
	}
	return n
}

func emptyClusterText(c core.EnvoyCluster) string {
	if c.Role == "egress" || c.Role == "inbound passthrough" {
		return "No connections right now: Envoy forgets original destinations a few seconds after they go idle."
	}
	return "No hosts: Envoy has not resolved this cluster's name (yet)."
}

// micros formats a duration in microseconds.
func micros(us int64) string {
	switch {
	case us >= 1_000_000:
		return fmt.Sprintf("%.1f s", float64(us)/1e6)
	case us >= 1000:
		return fmt.Sprintf("%.1f ms", float64(us)/1e3)
	}
	return fmt.Sprintf("%d µs", us)
}

// pgbouncerConsole is the command that opens PgBouncer's admin console
// inside its container, with the password it already holds.
func pgbouncerConsole(experiment string, x *core.PgBouncerPools) string {
	return fmt.Sprintf(`docker exec -it %s sh -c 'PGPASSWORD="$BB_PG_PASSWORD" psql -h 127.0.0.1 -p %d -U "$BB_PG_USER" pgbouncer'`, container(experiment, pgbouncerName(x)), x.Port)
}
