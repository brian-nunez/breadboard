package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/brian-nunez/breadboard/internal/core"
)

func (c *cli) initCmd(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := atMost(pos, 1); err != nil {
		return err
	}
	req := core.InitRequest{Dir: c.dir}
	if len(pos) == 1 {
		req.Name = pos[0]
	}
	res, err := c.eng.Init(c.ctx, req)
	if err != nil {
		return err
	}
	if res.SpecCreated {
		fmt.Fprintf(c.out, "Created experiment %s in %s\n", res.Name, res.Dir)
	} else {
		fmt.Fprintf(c.out, "Experiment %s already exists in %s\n", res.Name, res.Dir)
	}
	if res.StateCreated {
		fmt.Fprintln(c.out, "Created .bb/ for generated secrets and certificates (never committed)")
	}
	if res.CACreated {
		fmt.Fprintf(c.out, "Created the Breadboard CA: %s\n", res.CAPath)
	}
	fmt.Fprintln(c.out, "\nNext: bb catalog, then bb add <type> or bb starter <id>")
	return nil
}

func (c *cli) catalog(args []string) error {
	if _, err := parse(flag.NewFlagSet("catalog", flag.ContinueOnError), args); err != nil {
		return err
	}
	res, err := c.eng.Catalog(c.ctx)
	if err != nil {
		return err
	}
	tw := c.table()
	fmt.Fprintln(tw, "TYPE\tIMAGE\tPORTS\tLIMITS\tSUMMARY")
	for _, e := range res.Entries {
		ports := make([]string, len(e.Ports))
		for i, p := range e.Ports {
			ports[i] = fmt.Sprint(p)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s, %s cpu\t%s\n", e.Type, e.Image, strings.Join(ports, ","), e.Memory, e.CPUs, e.Summary)
	}
	tw.Flush()
	fmt.Fprintln(c.out, "\nAdd one with bb add <type>; read about it with bb learn <type>.")
	return nil
}

func (c *cli) add(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	name := fs.String("name", "", "service name (default: the type)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 1, "a service type (see bb catalog)"); err != nil {
		return err
	}
	res, err := c.eng.AddService(c.ctx, core.AddServiceRequest{Dir: c.dir, Type: pos[0], Name: *name})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Added %s (%s) to experiment.yaml. Run bb up to start it.\n", res.Name, res.Type)
	return nil
}

func (c *cli) remove(args []string) error {
	pos, err := parse(flag.NewFlagSet("remove", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 1, "a service or app name"); err != nil {
		return err
	}
	res, err := c.eng.RemoveService(c.ctx, core.RemoveServiceRequest{Dir: c.dir, Name: pos[0]})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Removed %s %s from experiment.yaml. Run bb up to apply it.\n", res.Kind, res.Name)
	return nil
}

func (c *cli) starters(args []string) error {
	if _, err := parse(flag.NewFlagSet("starters", flag.ContinueOnError), args); err != nil {
		return err
	}
	res, err := c.eng.Starters(c.ctx)
	if err != nil {
		return err
	}
	tw := c.table()
	fmt.Fprintln(tw, "STARTER\tLANGUAGE\tNEEDS\tAPPS\tTEACHES")
	for _, s := range res.Starters {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.ID, s.Language, join(s.Requires), join(s.Apps), s.Summary)
	}
	tw.Flush()
	fmt.Fprintln(c.out, "\nApply one with bb starter <id>.")
	return nil
}

func (c *cli) starter(args []string) error {
	fs := flag.NewFlagSet("starter", flag.ContinueOnError)
	dir := fs.String("dir", "", "directory for the code, relative to the project")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 1, "a starter id (see bb starters)"); err != nil {
		return err
	}
	res, err := c.eng.ApplyStarter(c.ctx, core.ApplyStarterRequest{Dir: c.dir, ID: pos[0], Target: *dir, Events: c.events()})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Starter %s in %s\n", pos[0], res.Dir)
	if len(res.Written) > 0 {
		fmt.Fprintf(c.out, "  wrote %d %s\n", len(res.Written), plural(len(res.Written), "file", "files"))
	}
	if len(res.Unchanged) > 0 {
		fmt.Fprintf(c.out, "  %d %s already up to date\n", len(res.Unchanged), plural(len(res.Unchanged), "file", "files"))
	}
	for _, f := range res.Conflicts {
		fmt.Fprintf(c.out, "  kept your version of %s (differs from the starter)\n", f)
	}
	if len(res.AddedServices) > 0 {
		fmt.Fprintf(c.out, "  added %s: %s\n", plural(len(res.AddedServices), "service", "services"), join(res.AddedServices))
	}
	if len(res.AddedApps) > 0 {
		fmt.Fprintf(c.out, "  added %s: %s\n", plural(len(res.AddedApps), "app", "apps"), join(res.AddedApps))
	}
	if len(res.ExistingApps) > 0 {
		fmt.Fprintf(c.out, "  left existing %s untouched: %s\n", plural(len(res.ExistingApps), "app", "apps"), join(res.ExistingApps))
	}
	fmt.Fprintf(c.out, "\nNext: bb up, then read %s/README.md\n", res.Dir)
	return nil
}

func (c *cli) up(args []string) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	timeout := fs.Duration("timeout", 0, "how long to wait for health (default 5m)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	res, err := c.eng.Up(c.ctx, core.UpRequest{Dir: c.dir, Events: c.events(), Timeout: *timeout})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "\n%s is up.\n\n", res.Experiment)
	c.printServices(res.Services, res.Warnings)
	return nil
}

func (c *cli) down(args []string) error {
	fs := flag.NewFlagSet("down", flag.ContinueOnError)
	volumes := fs.Bool("volumes", false, "also delete data volumes")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	res, err := c.eng.Down(c.ctx, core.DownRequest{Dir: c.dir, Volumes: *volumes, Events: c.events()})
	if err != nil {
		return err
	}
	if res.VolumesRemoved {
		fmt.Fprintf(c.out, "%s is down; its data volumes are deleted.\n", res.Experiment)
	} else {
		fmt.Fprintf(c.out, "%s is down; data volumes are kept (use --volumes to delete them).\n", res.Experiment)
	}
	return nil
}

func (c *cli) status(args []string) error {
	pos, err := parse(flag.NewFlagSet("status", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	res, err := c.eng.Status(c.ctx, core.StatusRequest{Dir: c.dir})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "%s (domain %s)\n\n", res.Experiment, res.Domain)
	c.printServices(res.Services, res.Warnings)
	return nil
}

func (c *cli) printServices(services []core.ServiceStatus, warnings []string) {
	tw := c.table()
	fmt.Fprintln(tw, "SERVICE\tKIND\tHEALTH\tCPU\tMEMORY\tADDRESS\tUSES")
	for _, s := range services {
		cpu, mem := "-", "-"
		if s.State == "running" {
			cpu = fmt.Sprintf("%.1f%% / %s", s.CPUPercent, s.CPULimit)
			mem = fmt.Sprintf("%s / %s", bytesHuman(s.MemoryBytes), s.MemoryLimit)
		}
		var uses []string
		for _, e := range s.Uses {
			if e.TLS {
				uses = append(uses, e.To)
			} else {
				uses = append(uses, e.To+" (PLAINTEXT)")
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Kind, s.Health, cpu, mem, s.Address, join(uses))
	}
	tw.Flush()
	for _, w := range warnings {
		fmt.Fprintf(c.out, "warning: %s\n", w)
	}
}

func bytesHuman(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.2fGiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fMiB", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%.0fKiB", float64(n)/(1<<10))
	}
}

func (c *cli) logs(args []string) error {
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow new output")
	fs.BoolVar(follow, "follow", false, "follow new output")
	tail := fs.Int("tail", 0, "lines per service from the end (default all)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := atMost(pos, 1); err != nil {
		return err
	}
	req := core.LogsRequest{Dir: c.dir, Follow: *follow, Tail: *tail}
	if len(pos) == 1 {
		req.Service = pos[0]
	}
	lines, err := c.eng.Logs(c.ctx, req)
	if err != nil {
		return err
	}
	for l, err := range lines {
		if err != nil {
			return err
		}
		if req.Service != "" || l.Service == "" {
			fmt.Fprintln(c.out, l.Text)
		} else {
			fmt.Fprintf(c.out, "%-12s | %s\n", l.Service, l.Text)
		}
	}
	return nil
}

func (c *cli) creds(args []string) error {
	pos, err := parse(flag.NewFlagSet("creds", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := atMost(pos, 1); err != nil {
		return err
	}
	req := core.CredsRequest{Dir: c.dir}
	if len(pos) == 1 {
		req.Service = pos[0]
	}
	res, err := c.eng.Creds(c.ctx, req)
	if err != nil {
		return err
	}
	if len(res.Services) == 0 {
		fmt.Fprintln(c.out, "No infra services yet. Add one with bb add <type>.")
		return nil
	}
	for i, s := range res.Services {
		if i > 0 {
			fmt.Fprintln(c.out)
		}
		fmt.Fprintf(c.out, "%s (%s) at %s\n", s.Name, s.Type, s.Address)
		if len(s.Secrets) > 0 {
			fmt.Fprintln(c.out, "  secrets:")
			tw := c.table()
			for _, sec := range s.Secrets {
				fmt.Fprintf(tw, "    %s\t%s\t%s\n", sec.Key, sec.Value, sec.Help)
			}
			tw.Flush()
		}
		fmt.Fprintln(c.out, "  env injected into apps that use it:")
		for _, v := range s.Env {
			fmt.Fprintf(c.out, "    %s=%s\n", v.Name, v.Value)
		}
	}
	return nil
}

func (c *cli) learn(args []string) error {
	pos, err := parse(flag.NewFlagSet("learn", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 1, "a service type (see bb catalog)"); err != nil {
		return err
	}
	res, err := c.eng.Learn(c.ctx, core.LearnRequest{Type: pos[0]})
	if err != nil {
		return err
	}
	fmt.Fprint(c.out, res.Notes)
	return nil
}

func (c *cli) ca(args []string) error {
	fs := flag.NewFlagSet("ca", flag.ContinueOnError)
	pem := fs.Bool("pem", false, "print only the CA certificate (PEM)")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	res, err := c.eng.CA(c.ctx)
	if err != nil {
		return err
	}
	if *pem {
		_, err := c.out.Write(res.CertPEM)
		return err
	}
	tw := c.table()
	fmt.Fprintf(tw, "Subject\t%s\n", res.Subject)
	fmt.Fprintf(tw, "Constrained to\t*.%s (cannot sign for any other domain)\n", res.Domain)
	fmt.Fprintf(tw, "Valid\t%s to %s\n", res.NotBefore.Format(time.DateOnly), res.NotAfter.Format(time.DateOnly))
	fmt.Fprintf(tw, "SHA-256\t%s\n", res.Fingerprint)
	fmt.Fprintf(tw, "Certificate\t%s\n", res.CertPath)
	tw.Flush()
	fmt.Fprintln(c.out, "\nContainers already trust this CA. bb never adds it to your machine's trust store.")
	fmt.Fprintln(c.out, "To trust it on the host anyway (optional):")
	fmt.Fprintf(c.out, "  macOS: sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %q\n", res.CertPath)
	fmt.Fprintf(c.out, "  Linux (Debian/Ubuntu): sudo cp %q /usr/local/share/ca-certificates/breadboard.crt && sudo update-ca-certificates\n", res.CertPath)
	return nil
}

func (c *cli) config(args []string) error {
	pos, err := parse(flag.NewFlagSet("config", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) == 0 || (len(pos) == 1 && pos[0] == "get"):
		res, err := c.eng.ConfigGet(c.ctx, core.ConfigGetRequest{})
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "BB_HOME %s\n\n", res.Home)
		tw := c.table()
		fmt.Fprintln(tw, "KEY\tVALUE\tSOURCE\tDESCRIPTION")
		for _, s := range res.Settings {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Key, s.Value, s.Source, s.Help)
		}
		tw.Flush()
		return nil
	case pos[0] == "get" && len(pos) == 2:
		res, err := c.eng.ConfigGet(c.ctx, core.ConfigGetRequest{Key: pos[1]})
		if err != nil {
			return err
		}
		fmt.Fprintln(c.out, res.Settings[0].Value)
		return nil
	case pos[0] == "set" && len(pos) == 3:
		res, err := c.eng.ConfigSet(c.ctx, core.ConfigSetRequest{Key: pos[1], Value: pos[2]})
		if err != nil {
			return err
		}
		fmt.Fprintf(c.out, "Set %s = %s in config.yaml\n", pos[1], pos[2])
		if res.OverriddenBy != "" {
			fmt.Fprintf(c.out, "warning: %s is set and still overrides it (effective value %s)\n", res.OverriddenBy, res.Setting.Value)
		}
		if pos[1] == "domain" {
			fmt.Fprintln(c.out, "The next bb up mints a CA for the new domain and reissues every certificate.")
		}
		return nil
	}
	return usagef("expected get [key] or set <key> <value>")
}

func (c *cli) migrate(args []string) error {
	pos, err := parse(flag.NewFlagSet("migrate", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	if err := exactly(pos, 0, ""); err != nil {
		return err
	}
	res, err := c.eng.Migrate(c.ctx, core.MigrateRequest{Dir: c.dir})
	if err != nil {
		return err
	}
	if res.Changed {
		fmt.Fprintf(c.out, "Upgraded experiment.yaml from version %d to %d.\n", res.From, res.To)
	} else {
		fmt.Fprintf(c.out, "experiment.yaml is already at version %d.\n", res.To)
	}
	return nil
}

func (c *cli) export(args []string) error {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	out := fs.String("out", "", "output directory (default export/compose)")
	secretsMode := fs.String("secrets", "inline", "how secrets are written: inline or dotenv")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 || pos[0] != "compose" {
		return usagef("the only export target in this version is compose")
	}
	res, err := c.eng.ExportCompose(c.ctx, core.ExportComposeRequest{Dir: c.dir, Out: *out, Secrets: *secretsMode, Events: c.events()})
	if err != nil {
		return err
	}
	fmt.Fprintf(c.out, "Exported %d files to %s\n", len(res.Files), res.Dir)
	fmt.Fprintln(c.out, "Run it without bb:")
	fmt.Fprintf(c.out, "  cd %q && docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d --wait\n", res.Dir)
	return nil
}

func (c *cli) version(args []string) error {
	if _, err := parse(flag.NewFlagSet("version", flag.ContinueOnError), args); err != nil {
		return err
	}
	v := c.eng.Version()
	fmt.Fprintf(c.out, "bb %s (%s)\n", v.Version, v.GoVersion)
	return nil
}
