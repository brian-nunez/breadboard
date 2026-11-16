package mcp

import (
	"context"
	_ "embed"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// Tools that describe bb itself: the guide, the catalog and its learning
// notes, the starters and their code, the CA, and install settings.

//go:embed guide.md
var guide string

type noInput struct{}

type versionOut struct {
	Version   string `json:"version"`
	GoVersion string `json:"go_version"`
}

type guideOut struct {
	Guide string `json:"guide" jsonschema:"markdown"`
}

type catalogEntry struct {
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Summary   string   `json:"summary"`
	Image     string   `json:"image" jsonschema:"the exact image the service runs"`
	Ports     []int    `json:"ports"`
	Memory    string   `json:"memory" jsonschema:"default memory limit"`
	CPUs      string   `json:"cpus" jsonschema:"default CPU limit"`
	Plaintext bool     `json:"plaintext" jsonschema:"whether an app's edge to it may turn TLS off"`
	Env       []string `json:"env" jsonschema:"variables an app that uses this type receives"`
}

type catalogOut struct {
	Entries []catalogEntry `json:"entries"`
}

type learnIn struct {
	Type string `json:"type" jsonschema:"a catalog type, such as kafka or postgres"`
}

type learnOut struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	Notes string `json:"notes" jsonschema:"markdown learning notes"`
}

type starterInfo struct {
	ID       string   `json:"id"`
	Title    string   `json:"title"`
	Summary  string   `json:"summary"`
	Language string   `json:"language"`
	Requires []string `json:"requires" jsonschema:"catalog types it needs; apply_starter adds any that are missing"`
	Apps     []string `json:"apps" jsonschema:"apps it adds to experiment.yaml"`
}

type startersOut struct {
	Starters []starterInfo `json:"starters"`
}

type starterIn struct {
	ID string `json:"id" jsonschema:"a starter id, such as kafka/producer-consumer"`
}

type starterApp struct {
	Name     string    `json:"name"`
	Command  []string  `json:"command"`
	Uses     []string  `json:"uses" jsonschema:"catalog types it connects to"`
	Register *register `json:"register,omitempty" jsonschema:"how a sidecar registers it in Consul"`
}

type register struct {
	Name  string `json:"name,omitempty"`
	Port  int    `json:"port"`
	Check string `json:"check,omitempty"`
}

type starterFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type starterDetailsOut struct {
	Starter starterInfo   `json:"starter"`
	Dir     string        `json:"dir" jsonschema:"directory the code is written to by default"`
	Apps    []starterApp  `json:"apps"`
	Files   []starterFile `json:"files" jsonschema:"every file the starter writes, with its content"`
}

type caOut struct {
	Name        string `json:"name"`
	Domain      string `json:"domain" jsonschema:"every service name is <service>.<experiment>.<domain>"`
	Subject     string `json:"subject"`
	NotBefore   string `json:"not_before"`
	NotAfter    string `json:"not_after"`
	Fingerprint string `json:"fingerprint" jsonschema:"SHA-256, hex"`
	CertPath    string `json:"cert_path"`
	CertPEM     string `json:"cert_pem" jsonschema:"the public CA certificate"`
	Created     bool   `json:"created"`
}

type configGetIn struct {
	Key string `json:"key,omitempty" jsonschema:"one setting; empty lists every setting"`
}

type setting struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Source string `json:"source" jsonschema:"default, config.yaml, or the environment variable that sets it"`
	Help   string `json:"help"`
}

type configOut struct {
	Home     string    `json:"home"`
	Settings []setting `json:"settings"`
}

func settingView(v core.Setting) setting {
	return setting{Key: v.Key, Value: v.Value, Source: v.Source, Help: v.Help}
}

func starterInfoView(i core.StarterInfo) starterInfo {
	return starterInfo{ID: i.ID, Title: i.Title, Summary: i.Summary, Language: i.Language, Requires: orEmpty(i.Requires), Apps: orEmpty(i.Apps)}
}

func (s *Server) addInfoTools() {
	add(s, readTool, true, &sdk.Tool{
		Name:        "guide",
		Title:       "How to use Breadboard",
		Description: "How bb works for an agent: what an experiment is, experiment.yaml, TLS and certificates, the usual workflow, and how to debug. Read this first.",
	}, func(context.Context, *sdk.CallToolRequest, noInput) (guideOut, error) {
		return guideOut{Guide: guide}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "version",
		Description: "The bb version.",
	}, func(context.Context, *sdk.CallToolRequest, noInput) (versionOut, error) {
		v := s.eng.Version()
		return versionOut{Version: v.Version, GoVersion: v.GoVersion}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "catalog",
		Description: "Infra service types bb can run (Kafka, Postgres, Redis, Consul, S3 compatible), with the exact image, ports, default resources, and the environment variables an app that uses each one receives.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noInput) (catalogOut, error) {
		res, err := s.eng.Catalog(ctx)
		if err != nil {
			return catalogOut{}, err
		}
		return catalogOut{Entries: mapSlice(res.Entries, func(e core.CatalogEntry) catalogEntry {
			return catalogEntry{Type: e.Type, Title: e.Title, Summary: e.Summary, Image: e.Image, Ports: orEmpty(e.Ports), Memory: e.Memory, CPUs: e.CPUs, Plaintext: e.Plaintext, Env: orEmpty(e.Env)}
		})}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "learn",
		Description: "A catalog type's learning notes (markdown): what it is, how bb runs it, how apps connect, and exercises. Same as bb learn.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in learnIn) (learnOut, error) {
		res, err := s.eng.Learn(ctx, core.LearnRequest{Type: in.Type})
		if err != nil {
			return learnOut{}, err
		}
		return learnOut{Type: res.Type, Title: res.Title, Notes: res.Notes}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "starters",
		Description: "Starters: working example apps (official client libraries) that apply_starter writes into the experiment.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noInput) (startersOut, error) {
		res, err := s.eng.Starters(ctx)
		if err != nil {
			return startersOut{}, err
		}
		return startersOut{Starters: mapSlice(res.Starters, starterInfoView)}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "starter_details",
		Description: "Everything a starter would write and add, without writing anything: its apps and the full content of every file (code, Dockerfile, README with exercises).",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in starterIn) (starterDetailsOut, error) {
		res, err := s.eng.StarterDetails(ctx, core.StarterDetailsRequest{ID: in.ID})
		if err != nil {
			return starterDetailsOut{}, err
		}
		return starterDetailsOut{
			Starter: starterInfoView(res.StarterInfo),
			Dir:     res.Dir,
			Apps: mapSlice(res.Apps, func(a core.StarterApp) starterApp {
				out := starterApp{Name: a.Name, Command: orEmpty(a.Command), Uses: orEmpty(a.Uses)}
				if a.Register != nil {
					out.Register = &register{Name: a.Register.Name, Port: a.Register.Port, Check: a.Register.Check}
				}
				return out
			}),
			Files: mapSlice(res.Files, func(f core.StarterFile) starterFile {
				return starterFile{Path: f.Path, Content: string(f.Data)}
			}),
		}, nil
	})

	add(s, stateTool, true, &sdk.Tool{
		Name:        "ca",
		Description: "The Breadboard CA that signs every service certificate: subject, validity, fingerprint, the path of its certificate and the certificate itself. Creates the CA on first use; never touches the host trust store.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, _ noInput) (caOut, error) {
		res, err := s.eng.CA(ctx)
		if err != nil {
			return caOut{}, err
		}
		return caOut{Name: res.Name, Domain: res.Domain, Subject: res.Subject, NotBefore: timeView(res.NotBefore), NotAfter: timeView(res.NotAfter), Fingerprint: res.Fingerprint, CertPath: res.CertPath, CertPEM: string(res.CertPEM), Created: res.Created}, nil
	})

	add(s, readTool, true, &sdk.Tool{
		Name:        "config_get",
		Description: "Install settings (BB_HOME/config.yaml), with where each value comes from.",
	}, func(ctx context.Context, _ *sdk.CallToolRequest, in configGetIn) (configOut, error) {
		res, err := s.eng.ConfigGet(ctx, core.ConfigGetRequest{Key: in.Key})
		if err != nil {
			return configOut{}, err
		}
		return configOut{Home: res.Home, Settings: mapSlice(res.Settings, settingView)}, nil
	})
}
