package mcp

import (
	"context"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/brian-nunez/breadboard/internal/core"
)

// up and down start and stop the experiment's containers. Both report their
// steps as progress notifications while they run and return them in the
// result; cancelling the call cancels the operation.

type upIn struct {
	Dir            string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"how long to wait for every service to be healthy; empty uses the default"`
}

type upOut struct {
	Experiment string          `json:"experiment"`
	Services   []serviceStatus `json:"services"`
	Warnings   []string        `json:"warnings"`
	Events     []event         `json:"events"`
	HostURLs   []hostURL       `json:"host_urls,omitempty" jsonschema:"URLs that reach host targets from the host; absent without host access"`
	HostPort   int             `json:"host_port,omitempty" jsonschema:"the 127.0.0.1 port the host router is published on"`
}

type downIn struct {
	Dir     string `json:"dir,omitempty" jsonschema:"directory of the experiment, inside the one bb mcp was started in; empty means that directory"`
	Volumes bool   `json:"volumes,omitempty" jsonschema:"also delete the experiment's data volumes; the data is lost for good"`
}

type downOut struct {
	Experiment     string  `json:"experiment"`
	VolumesRemoved bool    `json:"volumes_removed"`
	Events         []event `json:"events"`
}

func (s *Server) addRunTools() {
	add(s, writeTool, true, &sdk.Tool{
		Name:        "up",
		Description: "Start the experiment: issue certificates, generate secrets, build apps, run docker compose, and wait until every service is healthy and stays healthy. Fails if a service is unhealthy or restarts while starting; call logs for it then. Takes from seconds to a few minutes. Same as bb up.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in upIn) (upOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return upOut{}, err
		}

		p := newProgress(ctx, req)

		res, err := s.eng.Up(ctx, core.UpRequest{Dir: dir, Events: p.sink, Timeout: time.Duration(in.TimeoutSeconds) * time.Second})
		if err != nil {
			return upOut{}, withEvents(err, p.list())
		}

		return upOut{
			Experiment: res.Experiment, Services: mapSlice(res.Services, statusView), Warnings: orEmpty(res.Warnings), Events: p.list(),
			HostURLs: mapSlice(res.HostURLs, hostURLView), HostPort: res.HostPort,
		}, nil
	})

	add(s, destructiveTool, true, &sdk.Tool{
		Name:        "down",
		Description: "Stop the experiment and remove its containers and networks. With volumes, also delete its data (databases, topics, buckets) for good. Same as bb down.",
	}, func(ctx context.Context, req *sdk.CallToolRequest, in downIn) (downOut, error) {
		dir, err := s.dir(in.Dir)
		if err != nil {
			return downOut{}, err
		}

		p := newProgress(ctx, req)

		res, err := s.eng.Down(ctx, core.DownRequest{Dir: dir, Volumes: in.Volumes, Events: p.sink})
		if err != nil {
			return downOut{}, withEvents(err, p.list())
		}

		return downOut{Experiment: res.Experiment, VolumesRemoved: res.VolumesRemoved, Events: p.list()}, nil
	})
}
