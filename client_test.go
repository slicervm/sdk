package slicer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestColdForkClientWorkflow(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.RequestURI())
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/vm/demo-1/commit":
			var body SlicerCommitVMOptions
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.CacheKey != "cache-v1" || strings.Join(body.Tags, ",") != "base,test" {
				t.Errorf("commit body = %#v", body)
			}
			_, _ = io.WriteString(w, `{"hostname":"demo-1","commit_id":"cmt-demo","status":"committed","parent_status":"stopped","mode":"disk"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/vm/commits":
			if r.URL.Query().Get("cache_key") != "cache-v1" || strings.Join(r.URL.Query()["tag"], ",") != "base,test" {
				t.Errorf("commit list query = %s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `[{"commit_id":"cmt-demo","source_hostname":"demo-1","source_host_group":"demo","created_at":"2026-07-29T12:00:00Z","mode":"disk"}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/vm/commits/cmt-demo/fork":
			if r.URL.Query().Get("wait") != "agent" || r.URL.Query().Get("timeout") != "45s" {
				t.Errorf("fork query = %s", r.URL.RawQuery)
			}
			body, _ := io.ReadAll(r.Body)
			if strings.Contains(string(body), `"hostname"`) || !strings.Contains(string(body), `"allow":[]`) || !strings.Contains(string(body), `"tags":["job=review"]`) || !strings.Contains(string(body), `"tag_mode":"replace"`) || !strings.Contains(string(body), `"secrets":[]`) || !strings.Contains(string(body), `"persistent":false`) || !strings.Contains(string(body), `"fixups":[]`) || !strings.Contains(string(body), `"vcpu":1`) || !strings.Contains(string(body), `"ram_bytes":536870912`) {
				t.Errorf("fork body = %s", body)
			}
			_, _ = io.WriteString(w, `{"hostname":"demo-2","source_hostname":"demo-1","commit_id":"cmt-demo","status":"forked","child_status":"running","mode":"disk"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/vm/commits/cmt-demo":
			_, _ = io.WriteString(w, `{"commit_id":"cmt-demo","status":"deleted"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "test-token", "sdk-test", nil)
	committed, err := client.CommitVMWithOptions(context.Background(), "demo-1", SlicerCommitVMOptions{
		Tags: []string{"base", "test"}, CacheKey: " cache-v1 ",
	})
	if err != nil || committed.CommitID != "cmt-demo" {
		t.Fatalf("commit = %#v, %v", committed, err)
	}
	commits, err := client.ListCommits(context.Background(), SlicerCommitListOptions{
		Tags: []string{"base", "test"}, CacheKey: "cache-v1",
	})
	if err != nil || len(commits) != 1 || commits[0].CommitID != "cmt-demo" {
		t.Fatalf("commits = %#v, %v", commits, err)
	}
	emptyAllow := []string{}
	child, err := committed.Fork(context.Background(),
		WithTimeout(45*time.Second),
		WithNetwork(&SlicerForkVMNetworkPolicy{Allow: &emptyAllow}),
		WithReplaceTags("job=review"),
		WithSecrets(),
		WithEphemeral(),
		WithFixups(),
		WithVCPU(1),
		WithRAMBytes(512<<20),
	)
	if err != nil || child.Hostname != "demo-2" || child.SourceHostname != "demo-1" {
		t.Fatalf("fork = %#v, %v", child, err)
	}
	deleted, err := client.DeleteCommit(context.Background(), "cmt-demo")
	if err != nil || deleted.Status != "deleted" {
		t.Fatalf("delete = %#v, %v", deleted, err)
	}
	if len(requests) != 4 {
		t.Fatalf("requests = %v", requests)
	}
}

func TestColdForkClientNoWaitUsesExplicitWaitQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("wait"); got != "none" {
			t.Errorf("wait = %q, want none", got)
		}
		_, _ = io.WriteString(w, `{"hostname":"demo-2","source_hostname":"demo-1","commit_id":"cmt-demo","status":"forked","child_status":"starting","mode":"disk"}`)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "sdk-test", nil)
	if _, err := client.ForkCommittedVM(context.Background(), "cmt-demo", WithWait(SlicerForkWaitNone)); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ForkCommittedVM(context.Background(), "cmt-demo", WithWait("invalid")); err == nil {
		t.Fatal("invalid wait mode was accepted")
	}
}

func TestCommittedVMForkAcceptsDefaultsAndOptions(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("wait"); got != "agent" {
			t.Errorf("wait = %q, want agent", got)
		}
		_, _ = io.WriteString(w, `{"hostname":"demo-2","source_hostname":"demo-1","commit_id":"cmt-demo","status":"forked","child_status":"running","mode":"disk"}`)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "sdk-test", nil)
	committed := &SlicerCommittedVM{
		SlicerCommitVMResponse: SlicerCommitVMResponse{CommitID: "cmt-demo"},
		client:                 client,
	}
	if _, err := committed.Fork(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := committed.Fork(context.Background(), WithVCPU(1), WithRAMBytes(512<<20)); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestColdForkClientRejectsInvalidCommitIDs(t *testing.T) {
	client := NewSlicerClient("http://127.0.0.1:8080", "", "sdk-test", nil)
	for _, commitID := range []string{"", "../commit", "commit/child", `commit\\child`} {
		t.Run(commitID, func(t *testing.T) {
			if _, err := client.DeleteCommit(context.Background(), commitID); err == nil {
				t.Fatal("DeleteCommit accepted an invalid commit ID")
			}
			if _, err := client.ForkCommittedVM(context.Background(), commitID); err == nil {
				t.Fatal("ForkCommittedVM accepted an invalid commit ID")
			}
		})
	}
}

func TestNormalizeUnixSocketPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	tests := []struct {
		name  string
		input string
		want  string
		ok    bool
	}{
		{name: "absolute path", input: "/tmp/slicer.sock", want: "/tmp/slicer.sock", ok: true},
		{name: "relative path", input: "./slicer.sock", want: "./slicer.sock", ok: true},
		{name: "parent relative path", input: "../slicer.sock", want: "../slicer.sock", ok: true},
		{name: "bare socket path", input: "slicer.sock", want: "slicer.sock", ok: true},
		{name: "tilde socket path", input: "~/slicer.sock", want: filepath.Join(home, "slicer.sock"), ok: true},
		{name: "unix scheme path", input: "unix:///tmp/slicer.sock", want: "/tmp/slicer.sock", ok: true},
		{name: "http url", input: "http://127.0.0.1:8080", want: "", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := normalizeUnixSocketPath(tt.input)
			if ok != tt.ok {
				t.Fatalf("normalizeUnixSocketPath(%q) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("normalizeUnixSocketPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewSlicerClient_NormalizesUnixSocketPath(t *testing.T) {
	home := t.TempDir()
	originalHome := os.Getenv("HOME")
	t.Setenv("HOME", home)
	t.Cleanup(func() {
		_ = os.Setenv("HOME", originalHome)
	})

	client := NewSlicerClient("unix://~/slicer.sock", "", "agent", nil)
	want := filepath.Join(home, "slicer.sock")
	if client.unixSocket != want {
		t.Fatalf("client.unixSocket = %q, want %q", client.unixSocket, want)
	}
	if client.baseURL != "http://unix" {
		t.Fatalf("client.baseURL = %q, want %q", client.baseURL, "http://unix")
	}
}

func TestMakeRequest_AuthHeaderWithToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		want := "Bearer test-token"
		if auth != want {
			t.Errorf("Want '%s', got '%s'", want, auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "test-token", "test-agent", nil)
	resp, err := client.makeJSONRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_NoAuthHeaderWhenTokenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("Want no Authorization header, got '%s'", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test-agent", nil)
	resp, err := client.makeJSONRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_WithBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Content-Type header
		ct := r.Header.Get("Content-Type")
		want := "application/json"
		if ct != want {
			t.Errorf("Want '%s', got '%s'", want, ct)
		}

		// Verify body content
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("Failed to read request body: %v", err)
		}
		wantBody := `{"name":"test","value":"data"}`
		if string(body) != wantBody {
			t.Errorf("Want body '%s', got '%s'", wantBody, string(body))
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", "agent", nil)
	requestBody := map[string]string{"name": "test", "value": "data"}
	resp, err := client.makeJSONRequest(http.MethodPost, "/test", requestBody)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_WithoutBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify no Content-Type header for requests without body
		ct := r.Header.Get("Content-Type")
		if ct != "" {
			t.Errorf("Want no Content-Type header, got '%s'", ct)
		}

		// Verify method and path
		if r.Method != http.MethodGet {
			t.Errorf("Want %s method, got %s", http.MethodGet, r.Method)
		}
		want := "/test"
		if r.URL.Path != want {
			t.Errorf("Want %s path, got %s", want, r.URL.Path)
		}

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", "agent", nil)
	resp, err := client.makeJSONRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_InvalidJSON(t *testing.T) {
	client := NewSlicerClient("http://localhost", "token", "agent", nil)

	// Use a channel which can't be marshaled to JSON
	invalidBody := make(chan int)
	_, err := client.makeJSONRequest(http.MethodPost, "/test", invalidBody)

	if err == nil {
		t.Error("Want error, got nil")
	}
}

func TestMakeRequest_CustomUserAgent(t *testing.T) {
	customAgent := "custom-user-agent/1.0"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua := r.Header.Get("User-Agent")
		if ua != customAgent {
			t.Errorf("Want '%s', got '%s'", customAgent, ua)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", customAgent, nil)
	resp, err := client.makeJSONRequest(http.MethodGet, "/test", nil)

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}

func TestMakeRequest_InvalidBaseURL(t *testing.T) {
	client := NewSlicerClient("://invalid-url", "token", "agent", nil)
	_, err := client.makeJSONRequest(http.MethodGet, "/test", nil)

	if err == nil {
		t.Error("Want error, got nil")
	}
}

func TestCreateVMWithOptions_WaitQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("Want %s method, got %s", http.MethodPost, r.Method)
		}
		if r.URL.Path != "/hostgroup/vm/nodes" {
			t.Fatalf("Want path /hostgroup/vm/nodes, got %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("wait"); got != "agent" {
			t.Fatalf("Want wait=agent, got %q", got)
		}
		if got := r.URL.Query().Get("timeout"); got != "2m0s" {
			t.Fatalf("Want timeout=2m0s, got %q", got)
		}

		var body SlicerCreateNodeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("failed to decode request body: %v", err)
		}
		if len(body.Tags) != 1 || body.Tags[0] != "e2e" {
			t.Fatalf("Want tag e2e, got %#v", body.Tags)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"hostname":"vm-1","ip":"192.168.1.10/24","created_at":"2026-04-13T10:09:25Z","arch":"arm64"}`)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "token", "test-agent", nil)
	resp, err := client.CreateVMWithOptions(context.Background(), "vm", SlicerCreateNodeRequest{
		Tags: []string{"e2e"},
	}, SlicerCreateNodeOptions{
		Wait:    SlicerCreateNodeWaitAgent,
		Timeout: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("CreateVMWithOptions() failed: %v", err)
	}
	if resp.Hostname != "vm-1" || resp.Arch != "arm64" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestCreateVMWithOptions_InvalidWait(t *testing.T) {
	client := NewSlicerClient("http://unused", "token", "test-agent", nil)
	_, err := client.CreateVMWithOptions(context.Background(), "vm", SlicerCreateNodeRequest{}, SlicerCreateNodeOptions{
		Wait: SlicerCreateNodeWaitFor("ready"),
	})
	if err == nil {
		t.Fatal("Want invalid wait error, got nil")
	}
}

func TestCreateVMWithOptions_PreservesExplicitEmptyNetworkList(t *testing.T) {
	empty := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"allow":[]`) {
			t.Fatalf("request body did not preserve explicit empty allow: %s", body)
		}
		_, _ = io.WriteString(w, `{"hostname":"vm-1"}`)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test-agent", nil)
	_, err := client.CreateVM(context.Background(), "vm", SlicerCreateNodeRequest{
		Network: &SlicerCreateNodeNetworkPolicy{Allow: empty},
	})
	if err != nil {
		t.Fatalf("CreateVM() failed: %v", err)
	}
}

func TestCreateNodeNetworkPolicyOmitsInheritedLists(t *testing.T) {
	body, err := json.Marshal(SlicerCreateNodeNetworkPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{}` {
		t.Fatalf("policy JSON = %s, want {}", got)
	}
}

func TestDescribeVM(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/vm/vm-1" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"hostname":"vm-1","network":{"mode":"isolated","policy_source":"vm","host_group":{"allow":["10.0.0.0/8"],"drop":[]},"override":{"allow":[]},"effective":{"allow":[],"drop":[]}}}`)
	}))
	defer server.Close()

	client := NewSlicerClient(server.URL, "", "test-agent", nil)
	description, err := client.DescribeVM(context.Background(), "vm-1")
	if err != nil {
		t.Fatalf("DescribeVM() failed: %v", err)
	}
	if description.Hostname != "vm-1" || description.Network.Override == nil || description.Network.Override.Allow == nil || len(description.Network.Override.Allow) != 0 {
		t.Fatalf("unexpected description: %#v", description)
	}
}
