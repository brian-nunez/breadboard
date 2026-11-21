package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolContract struct {
	readOnly    bool
	destructive bool
	idempotent  bool
	inReadOnly  bool
}

// TestMCPContractToolCatalog locks the exact MCP tool names, annotations,
// input schemas, and --read-only filtering contract.
func TestMCPContractToolCatalog(t *testing.T) {
	want := map[string]toolContract{
		// Info & read tools
		"guide":           {readOnly: true, idempotent: true, inReadOnly: true},
		"version":         {readOnly: true, idempotent: true, inReadOnly: true},
		"catalog":         {readOnly: true, idempotent: true, inReadOnly: true},
		"learn":           {readOnly: true, idempotent: true, inReadOnly: true},
		"starters":        {readOnly: true, idempotent: true, inReadOnly: true},
		"starter_details": {readOnly: true, idempotent: true, inReadOnly: true},
		"ca":              {readOnly: false, destructive: false, idempotent: true, inReadOnly: true},
		"config_get":      {readOnly: true, idempotent: true, inReadOnly: true},
		// Experiment inspection tools
		"status":          {readOnly: true, idempotent: true, inReadOnly: true},
		"logs":            {readOnly: true, idempotent: true, inReadOnly: true},
		"creds":           {readOnly: false, destructive: false, idempotent: true, inReadOnly: true},
		"service_details": {readOnly: true, idempotent: true, inReadOnly: true},
		"postgres_tables": {readOnly: true, idempotent: true, inReadOnly: true},
		"redis_keys":      {readOnly: true, idempotent: true, inReadOnly: true},
		"consul_catalog":  {readOnly: true, idempotent: true, inReadOnly: true},
		"kafka_topics":    {readOnly: true, idempotent: true, inReadOnly: true},
		"envoy_clusters":  {readOnly: true, idempotent: true, inReadOnly: true},
		"pgbouncer_pools": {readOnly: true, idempotent: true, inReadOnly: true},
		"s3_buckets":      {readOnly: true, idempotent: true, inReadOnly: true},
		"otel_telemetry":  {readOnly: true, idempotent: true, inReadOnly: true},
		"haproxy_stats":   {readOnly: true, idempotent: true, inReadOnly: true},
		"traefik_routes":  {readOnly: true, idempotent: true, inReadOnly: true},
		"nginx_status":    {readOnly: true, idempotent: true, inReadOnly: true},
		// Spec & run mutating tools
		"init":           {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"add_service":    {readOnly: false, destructive: false, idempotent: false, inReadOnly: false},
		"add_app":        {readOnly: false, destructive: false, idempotent: false, inReadOnly: false},
		"remove_service": {readOnly: false, destructive: true, idempotent: false, inReadOnly: false},
		"apply_starter":  {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"migrate":        {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"config_set":     {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"export_compose": {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"up":             {readOnly: false, destructive: false, idempotent: true, inReadOnly: false},
		"down":           {readOnly: false, destructive: true, idempotent: true, inReadOnly: false},
	}

	checkServer := func(t *testing.T, readOnlyMode bool) {
		t.Helper()
		h := newHarness(t, readOnlyMode)

		res, err := h.cs.ListTools(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}

		gotNames := make([]string, 0, len(res.Tools))
		for _, tool := range res.Tools {
			gotNames = append(gotNames, tool.Name)

			c, ok := want[tool.Name]
			if !ok {
				t.Errorf("unexpected MCP tool %q registered (update contract test intentionally if adding a tool)", tool.Name)

				continue
			}

			if readOnlyMode && !c.inReadOnly {
				t.Errorf("--read-only registered mutating tool %q", tool.Name)
			}

			if strings.TrimSpace(tool.Description) == "" {
				t.Errorf("tool %q has empty description", tool.Name)
			}

			if tool.InputSchema == nil {
				t.Errorf("tool %q has nil InputSchema", tool.Name)
			}

			ann := tool.Annotations
			if ann == nil {
				t.Errorf("tool %q has nil Annotations", tool.Name)

				continue
			}

			if ann.OpenWorldHint == nil || *ann.OpenWorldHint {
				t.Errorf("tool %q OpenWorldHint = %v, want false", tool.Name, ann.OpenWorldHint)
			}

			if ann.ReadOnlyHint != c.readOnly {
				t.Errorf("tool %q ReadOnlyHint = %v, want %v", tool.Name, ann.ReadOnlyHint, c.readOnly)
			}

			if ann.IdempotentHint != c.idempotent {
				t.Errorf("tool %q IdempotentHint = %v, want %v", tool.Name, ann.IdempotentHint, c.idempotent)
			}

			if !c.readOnly {
				if ann.DestructiveHint == nil || *ann.DestructiveHint != c.destructive {
					t.Errorf("tool %q DestructiveHint = %v, want %v", tool.Name, ann.DestructiveHint, c.destructive)
				}
			}
		}

		var wantNames []string

		for name, c := range want {
			if !readOnlyMode || c.inReadOnly {
				wantNames = append(wantNames, name)
			}
		}

		slices.Sort(gotNames)
		slices.Sort(wantNames)

		if !slices.Equal(gotNames, wantNames) {
			t.Fatalf("readOnly=%v tool set drifted:\n got (%d): %v\nwant (%d): %v", readOnlyMode, len(gotNames), gotNames, len(wantNames), wantNames)
		}
	}

	t.Run("full", func(t *testing.T) { checkServer(t, false) })
	t.Run("read-only", func(t *testing.T) { checkServer(t, true) })
}

// TestMCPContractPayloadsAndBoundary locks structured JSON response contracts
// and dir-escape refusal across key tools.
func TestMCPContractPayloadsAndBoundary(t *testing.T) {
	h := newHarness(t, false)
	ctx := context.Background()

	callJSON := func(name string, args map[string]any) map[string]any {
		t.Helper()

		res, err := h.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): %v", name, err)
		}

		if res.IsError {
			t.Fatalf("CallTool(%s) returned tool error: %+v", name, res.Content)
		}

		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			t.Fatalf("marshal %s structured content: %v", name, err)
		}

		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal %s structured content: %v", name, err)
		}

		return m
	}

	ver := callJSON("version", nil)
	if ver["version"] != "v0.0.0-test" || ver["go_version"] == "" {
		t.Fatalf("version contract drifted: %+v", ver)
	}

	cat := callJSON("catalog", nil)
	if entries, ok := cat["entries"].([]any); !ok || len(entries) < 10 {
		t.Fatalf("catalog contract drifted: %+v", cat)
	}

	initOut := callJSON("init", map[string]any{"name": "mcp-contract"})
	if initOut["name"] != "mcp-contract" {
		t.Fatalf("init contract drifted: %+v", initOut)
	}

	addOut := callJSON("add_service", map[string]any{"type": "postgres"})
	if addOut["name"] != "postgres" || addOut["type"] != "postgres" {
		t.Fatalf("add_service contract drifted: %+v", addOut)
	}

	appOut := callJSON("add_app", map[string]any{"name": "api"})
	if appOut["name"] != "api" {
		t.Fatalf("add_app contract drifted: %+v", appOut)
	}

	// Path traversal outside root must always be refused.
	escapeRes, err := h.cs.CallTool(ctx, &sdk.CallToolParams{
		Name:      "status",
		Arguments: map[string]any{"dir": "../outside"},
	})
	if err == nil && !escapeRes.IsError {
		t.Fatal("status with dir=../outside succeeded; expected refusal")
	}
}
