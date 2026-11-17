//go:build integration

// The integration test drives a real bb binary over stdio, as an MCP client
// does, against real Docker:
//
//	go test -tags integration -timeout 30m ./internal/mcp/
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAgentRunsAStarterOverStdio(t *testing.T) {
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
	bin := filepath.Join(t.TempDir(), "bb")
	if out, err := exec.Command("go", "build", "-o", bin, "../../bb").CombinedOutput(); err != nil {
		t.Fatalf("build bb: %v\n%s", err, out)
	}
	root := t.TempDir()
	cmd := exec.Command(bin, "-C", root, "mcp")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "BB_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "BB_HOME="+filepath.Join(t.TempDir(), "home"))
	cmd.Stderr = os.Stderr

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	var (
		mu    sync.Mutex
		steps []string
	)
	client := sdk.NewClient(&sdk.Implementation{Name: "integration", Version: "1"}, &sdk.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *sdk.ProgressNotificationClientRequest) {
			mu.Lock()
			steps = append(steps, req.Params.Message)
			mu.Unlock()
		},
	})
	cs, err := client.Connect(ctx, &sdk.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		params := &sdk.CallToolParams{Name: name, Arguments: args}
		params.SetProgressToken(name)
		res, err := cs.CallTool(ctx, params)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if res.IsError {
			t.Fatalf("%s: %s", name, res.Content[0].(*sdk.TextContent).Text)
		}
		var out map[string]any
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Unique, so this test can run beside the core integration tests or
	// another run of itself.
	experiment := fmt.Sprintf("mcp-it-%d", time.Now().UnixNano()%1000000000)
	call("init", map[string]any{"name": experiment})
	defer func() {
		// Leave nothing behind, even when the test fails part way: the
		// lab, and the images compose built for it.
		_ = exec.Command(bin, "-C", root, "down", "--volumes").Run()
		if out, err := exec.Command("docker", "image", "ls", "-q", "--filter", "label=com.docker.compose.project=bb-"+experiment).Output(); err == nil {
			ids := strings.Fields(string(out))
			slices.Sort(ids)
			if ids = slices.Compact(ids); len(ids) > 0 {
				_ = exec.Command("docker", append([]string{"image", "rm", "--force"}, ids...)...).Run()
			}
		}
	}()
	applied := call("apply_starter", map[string]any{"id": "redis/cache-invalidation"})
	if len(applied["added_apps"].([]any)) == 0 {
		t.Fatalf("apply_starter added no apps: %v", applied)
	}

	up := call("up", nil)
	mu.Lock()
	gotSteps := len(steps)
	mu.Unlock()
	if gotSteps == 0 {
		t.Error("up sent no progress notifications")
	}
	for _, s := range up["services"].([]any) {
		if s := s.(map[string]any); s["health"] != "healthy" {
			t.Errorf("%s is %s after up", s["name"], s["health"])
		}
	}

	// The app writes keys; the explorer reads them with redis-cli.
	var keys []any
	for deadline := time.Now().Add(time.Minute); len(keys) == 0 && time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		keys = call("redis_keys", map[string]any{"service": "redis"})["keys"].([]any)
	}
	if len(keys) == 0 {
		t.Error("redis_keys found nothing the starter wrote")
	}

	creds := call("creds", map[string]any{"service": "redis"})
	var url string
	for _, v := range creds["services"].([]any)[0].(map[string]any)["env"].([]any) {
		if v := v.(map[string]any); v["name"] == "REDIS_URL" {
			url = v["value"].(string)
		}
	}
	if !strings.HasPrefix(url, "rediss://default:") || strings.HasPrefix(url, "rediss://default:@") {
		t.Errorf("REDIS_URL = %q, want it with the password", url)
	}

	app := applied["added_apps"].([]any)[0].(string)
	if lines := call("logs", map[string]any{"service": app, "tail": 20})["lines"].([]any); len(lines) == 0 {
		t.Errorf("no logs from %s", app)
	}

	if down := call("down", map[string]any{"volumes": true}); down["volumes_removed"] != true {
		t.Errorf("down = %v", down)
	}
	label := "label=breadboard.experiment=" + experiment
	for _, args := range [][]string{{"ps", "-aq", "--filter", label}, {"network", "ls", "-q", "--filter", label}, {"volume", "ls", "-q", "--filter", label}} {
		out, err := exec.Command("docker", args...).Output()
		if err != nil || strings.TrimSpace(string(out)) != "" {
			t.Errorf("docker %s: %q, %v", strings.Join(args, " "), out, err)
		}
	}
}
