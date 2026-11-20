package mcp

import (
	"context"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Tools that change experiment.yaml, project files, or install settings.
// experiment.yaml edits go through the core's YAML node tree, so the user's
// comments and ordering survive.

type initIn struct {
	Dir  string `json:"dir,omitempty" jsonschema:"directory for the experiment, created if missing, inside the one bb mcp was started in; empty means that directory"`
	Name string `json:"name,omitempty" jsonschema:"experiment name, a lowercase DNS label; empty derives it from the directory"`
}

type initOut struct {
	Dir          string `json:"dir"`
	Name         string `json:"name"`
	SpecCreated  bool   `json:"spec_created" jsonschema:"false when experiment.yaml already existed"`
	StateCreated bool   `json:"state_created" jsonschema:".bb/ was created"`
	HomeCreated  bool   `json:"home_created"`
	CACreated    bool   `json:"ca_created"`
	CAPath       string `json:"ca_path"`
}

type addIn struct {
	Dir  string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Type string `json:"type" jsonschema:"catalog type, such as kafka, postgres, redis, consul or s3-compatible"`
	Name string `json:"name,omitempty" jsonschema:"service name; empty means the type"`
}

type addOut struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type addAppIn struct {
	Dir  string   `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Name string   `json:"name" jsonschema:"app name, a lowercase DNS label not yet used in experiment.yaml; also the binary and command; the Go module is example.com/<name>"`
	Uses []string `json:"uses,omitempty" jsonschema:"infra services the app connects to; leave it out for every infra service in experiment.yaml, or pass [] for none"`
	Path string   `json:"path,omitempty" jsonschema:"directory for the code, relative to experiment.yaml; empty means ./<name>. It must not exist or must be empty"`
}

type appUse struct {
	Service   string   `json:"service"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Plaintext bool     `json:"plaintext,omitempty"`
	Env       []envVar `json:"env" jsonschema:"the variables the app receives from this service, as bb up injects them; secret values are never returned"`
}

type addAppOut struct {
	Name    string   `json:"name"`
	Dir     string   `json:"dir" jsonschema:"where the code lives, relative to the project"`
	Command []string `json:"command"`
	Files   []string `json:"files" jsonschema:"files written, relative to the project"`
	Uses    []appUse `json:"uses"`
}

type removeIn struct {
	Dir  string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Name string `json:"name" jsonschema:"service or app to remove from experiment.yaml"`
}

type removeOut struct {
	Name string `json:"name"`
	Kind string `json:"kind" jsonschema:"service or app"`
}

type applyStarterIn struct {
	Dir    string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	ID     string `json:"id" jsonschema:"starter id, such as kafka/producer-consumer"`
	Target string `json:"target,omitempty" jsonschema:"directory for the code, relative to the project; empty uses the starter's default"`
}

type applyStarterOut struct {
	Dir           string   `json:"dir" jsonschema:"where the code lives, relative to the project"`
	Written       []string `json:"written"`
	Unchanged     []string `json:"unchanged"`
	Conflicts     []string `json:"conflicts" jsonschema:"files that exist with other content; never overwritten"`
	AddedServices []string `json:"added_services"`
	AddedApps     []string `json:"added_apps"`
	ExistingApps  []string `json:"existing_apps" jsonschema:"app names already present, left untouched"`
	// Proxy services from an earlier apply to the same directory.
	ExistingServices []string `json:"existing_services" jsonschema:"the starter's proxy services already present from an earlier apply, left untouched"`
	Events           []event  `json:"events"`
}

type migrateOut struct {
	From    int  `json:"from"`
	To      int  `json:"to"`
	Changed bool `json:"changed"`
}

type configSetIn struct {
	Key   string `json:"key" jsonschema:"setting name; config_get lists them"`
	Value string `json:"value"`
}

type configSetOut struct {
	Setting      setting `json:"setting"`
	OverriddenBy string  `json:"overridden_by,omitempty" jsonschema:"an environment variable that still overrides the stored value"`
}

type exportIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Out     string `json:"out,omitempty" jsonschema:"output directory, inside the one bb mcp was started in; empty means <project>/export/compose. A non-empty directory is only replaced if bb wrote it"`
	Secrets string `json:"secrets,omitempty" jsonschema:"inline (default) or dotenv"`
}

type exportOut struct {
	Dir    string   `json:"dir"`
	Files  []string `json:"files"`
	Events []event  `json:"events"`
}

func (s *Server) addSpecTools() {
	add(s, writeTool, true, &sdk.Tool{
		Name:        "init",
		Description: "Create an experiment: experiment.yaml and .bb/ in dir, plus BB_HOME and the CA if missing. On a directory that already has experiment.yaml it only restores .bb/. Same as bb init.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in initIn) (initOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return initOut{}, err
		}

		res, err := s.eng.Init(ctx, core.InitRequest{Dir: dir, Name: in.Name})
		if err != nil {
			return initOut{}, err
		}

		return initOut{Dir: res.Dir, Name: res.Name, SpecCreated: res.SpecCreated, StateCreated: res.StateCreated, HomeCreated: res.HomeCreated, CACreated: res.CACreated, CAPath: res.CAPath}, nil
	})

	add(s, writeTool, false, &sdk.Tool{
		Name:        "add_service",
		Description: "Add an infra service from the catalog to experiment.yaml. Run up afterwards to start it. Same as bb add.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in addIn) (addOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return addOut{}, err
		}

		res, err := s.eng.AddService(ctx, core.AddServiceRequest{Dir: dir, Type: in.Type, Name: in.Name})
		if err != nil {
			return addOut{}, err
		}

		return addOut{Name: res.Name, Type: res.Type}, nil
	})

	add(s, writeTool, false, &sdk.Tool{
		Name:        "add_app",
		Description: "Write a minimal, runnable Go program for the user's own code into a new directory and add it to experiment.yaml as an app (path, command, uses). main.go maps every variable each used service gives the app, checks them with requireEnv, auto-wires each service's official client library with startup connectivity checks, logs ready, and waits on SIGTERM so up succeeds immediately. Never writes into a directory that has files. Run up afterwards. Same as bb add app.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in addAppIn) (addAppOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return addAppOut{}, err
		}

		res, err := s.eng.AddApp(ctx, core.AddAppRequest{Dir: dir, Name: in.Name, Uses: in.Uses, Path: in.Path})
		if err != nil {
			return addAppOut{}, err
		}

		out := addAppOut{Name: res.Name, Dir: res.Dir, Command: res.Command, Files: orEmpty(res.Files), Uses: []appUse{}}
		for _, u := range res.Uses {
			au := appUse{Service: u.Service, Type: u.Type, Title: u.Title, Plaintext: u.Plaintext, Env: []envVar{}}
			for _, v := range u.Env {
				au.Env = append(au.Env, envVarView(v))
			}

			out.Uses = append(out.Uses, au)
		}

		return out, nil
	})

	add(s, destructiveTool, false, &sdk.Tool{
		Name:        "remove_service",
		Description: "Remove a service or app from experiment.yaml. Its data volume and generated secrets are kept until down with volumes. Same as bb remove.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in removeIn) (removeOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return removeOut{}, err
		}

		res, err := s.eng.RemoveService(ctx, core.RemoveServiceRequest{Dir: dir, Name: in.Name})
		if err != nil {
			return removeOut{}, err
		}

		return removeOut{Name: res.Name, Kind: res.Kind}, nil
	})

	add(s, writeTool, true, &sdk.Tool{
		Name:        "apply_starter",
		Description: "Write a starter's code into the project and add its apps, its proxies (with their native config files), and any infra services it needs, to experiment.yaml. Never overwrites a file the user changed: those are reported as conflicts. Run up afterwards. Same as bb starter.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in applyStarterIn) (applyStarterOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return applyStarterOut{}, err
		}

		p := newProgress(ctx, req)

		res, err := s.eng.ApplyStarter(ctx, core.ApplyStarterRequest{Dir: dir, ID: in.ID, Target: in.Target, Events: p.sink})
		if err != nil {
			return applyStarterOut{}, err
		}

		return applyStarterOut{
			Dir: res.Dir, Written: orEmpty(res.Written), Unchanged: orEmpty(res.Unchanged), Conflicts: orEmpty(res.Conflicts),
			AddedServices: orEmpty(res.AddedServices), AddedApps: orEmpty(res.AddedApps), ExistingApps: orEmpty(res.ExistingApps),
			ExistingServices: orEmpty(res.ExistingServices), Events: p.list(),
		}, nil
	})

	add(s, writeTool, true, &sdk.Tool{
		Name:        "migrate",
		Description: "Upgrade experiment.yaml to the current spec version. Same as bb migrate.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in dirIn) (migrateOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return migrateOut{}, err
		}

		res, err := s.eng.Migrate(ctx, core.MigrateRequest{Dir: dir})
		if err != nil {
			return migrateOut{}, err
		}

		return migrateOut{From: res.From, To: res.To, Changed: res.Changed}, nil
	})

	add(s, writeTool, true, &sdk.Tool{
		Name:        "config_set",
		Description: "Change an install setting in BB_HOME/config.yaml. Same as bb config set.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in configSetIn) (configSetOut, error) {
		res, err := s.eng.ConfigSet(ctx, core.ConfigSetRequest{Key: in.Key, Value: in.Value})
		if err != nil {
			return configSetOut{}, err
		}

		return configSetOut{Setting: settingView(res.Setting), OverriddenBy: res.OverriddenBy}, nil
	})

	add(s, writeTool, true, &sdk.Tool{
		Name:        "export_compose",
		Description: "Write a standalone Docker Compose stack of the experiment that runs without bb. Same as bb export compose.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in exportIn) (exportOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return exportOut{}, err
		}

		out := ""
		if in.Out != "" {
			if out, err = s.dir(in.Out); err != nil {
				return exportOut{}, err
			}
		}

		p := newProgress(ctx, req)

		res, err := s.eng.ExportCompose(ctx, core.ExportComposeRequest{Dir: dir, Out: out, Secrets: in.Secrets, Events: p.sink})
		if err != nil {
			return exportOut{}, err
		}

		return exportOut{Dir: res.Dir, Files: orEmpty(res.Files), Events: p.list()}, nil
	})
}
