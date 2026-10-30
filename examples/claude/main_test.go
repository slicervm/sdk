package main

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	slicer "github.com/slicervm/sdk"
)

func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "HTTPS", value: "https://slicer.example.com"},
		{name: "localhost", value: "http://localhost:8080"},
		{name: "IPv4 loopback", value: "http://127.0.0.1:8080"},
		{name: "IPv6 loopback", value: "http://[::1]:8080"},
		{name: "absolute socket", value: "/run/slicer.sock"},
		{name: "relative socket", value: "./slicer.sock"},
		{name: "Mac socket", value: "~/slicer-mac/slicer.sock"},
		{name: "trimmed HTTPS", value: "  https://slicer.example.com  "},
		{name: "Unix absolute socket", value: "unix:///run/slicer.sock"},
		{name: "malformed Unix socket", value: "unix://relative/path", wantErr: true},
		{name: "remote plaintext", value: "http://192.0.2.1:8080", wantErr: true},
		{name: "remote plaintext socket suffix", value: "http://192.0.2.1/api.sock", wantErr: true},
		{name: "HTTPS without host", value: "https:///api", wantErr: true},
		{name: "missing scheme", value: "slicer.example.com:8080", wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateEndpoint(test.value)
			if test.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !test.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidatePositionalArgs(t *testing.T) {
	if err := validatePositionalArgs(nil); err != nil {
		t.Fatal(err)
	}
	err := validatePositionalArgs([]string{"accidental", "--keep"})
	if err == nil || !strings.Contains(err.Error(), "--keep") {
		t.Fatalf("error = %v, want the unparsed flag to be reported", err)
	}
}

func TestValidateResources(t *testing.T) {
	const maxRAMGiB = int64(math.MaxInt64 / (1 << 30))
	if err := validateResources(1, maxRAMGiB); err != nil {
		t.Fatalf("maximum RAM should be valid: %v", err)
	}
	if err := validateResources(1, maxRAMGiB+1); err == nil {
		t.Fatal("overflowing RAM should be rejected")
	}
	if err := validateResources(0, 1); err == nil {
		t.Fatal("zero CPUs should be rejected")
	}
}

func TestSelectHostGroup(t *testing.T) {
	tests := []struct {
		name      string
		response  string
		requested string
		want      string
		wantErr   string
	}{
		{name: "only group", response: `[{"name":"demo"}]`, want: "demo"},
		{name: "prefer Mac sandbox", response: `[{"name":"slicer"},{"name":"sbox"}]`, want: "sbox"},
		{name: "explicit group", response: `[{"name":"one"},{"name":"two"}]`, requested: "two", want: "two"},
		{name: "ambiguous", response: `[{"name":"one"},{"name":"two"}]`, wantErr: "set --hostgroup"},
		{name: "missing explicit group", response: `[{"name":"one"}]`, requested: "two", wantErr: `host group "two" not found`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/hostgroup" {
					t.Fatalf("path: got %q, want /hostgroup", r.URL.Path)
				}
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()

			client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
			got, err := selectHostGroup(t.Context(), client, test.requested)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error: got %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("group: got %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveToken(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte(" file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got, err := resolveToken(" direct-token ", tokenFile, false); err != nil || got != "direct-token" {
		t.Fatalf("direct token: got %q, err=%v", got, err)
	}
	if got, err := resolveToken("", tokenFile, false); err != nil || got != "file-token" {
		t.Fatalf("file token: got %q, err=%v", got, err)
	}
	if got, err := resolveToken("environment-token", tokenFile, true); err != nil || got != "file-token" {
		t.Fatalf("explicit token file: got %q, err=%v", got, err)
	}
}

func TestResolveClaudeCredentialsFromKeychain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	path, cleanup, err := resolveClaudeCredentials(ctx, "", func(ctx context.Context) ([]byte, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("Keychain lookup context has no deadline")
		}
		return []byte(`{"claudeAiOauth":{"accessToken":"test"}}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: got %o, want 600", info.Mode().Perm())
	}
}

func TestCreateVMCleansUpAfterWaitTimeout(t *testing.T) {
	var created atomic.Bool
	var deleted atomic.Bool
	const runTag = "claude-timeout-test"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/hostgroup/demo/nodes":
			wait, err := time.ParseDuration(r.URL.Query().Get("timeout"))
			if err != nil {
				t.Errorf("parse timeout: %v", err)
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			created.Store(true)
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-timer.C:
				w.WriteHeader(http.StatusRequestTimeout)
				_, _ = w.Write([]byte(`{"error":"wait timed out"}`))
			case <-r.Context().Done():
				return
			}
		case r.Method == http.MethodGet && r.URL.Path == "/hostgroup/demo/nodes":
			_, _ = w.Write([]byte(`[{"hostname":"unrelated","hostgroup":"demo","tags":["purpose=other"]},{"hostname":"demo-1","hostgroup":"demo","tags":["run=claude-timeout-test"]}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/hostgroup/demo/nodes/demo-1":
			deleted.Store(true)
			_, _ = w.Write([]byte(`{"message":"deleted"}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	wait, err := serverWaitTimeout(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = createVM(ctx, client, "demo", runTag, false, slicer.SlicerCreateNodeRequest{
		Tags: []string{"example=claude", "run=" + runTag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: wait,
	})
	if !slicer.IsAPIStatus(err, http.StatusRequestTimeout) {
		t.Fatalf("error: got %v, want create timeout", err)
	}
	if !created.Load() {
		t.Fatal("fake VM was not created")
	}
	if !deleted.Load() {
		t.Fatal("timed-out VM was not deleted")
	}
}

func TestCreateVMReconcilesAfterClientDeadline(t *testing.T) {
	var created atomic.Bool
	var deleted atomic.Bool
	const runTag = "claude-client-timeout-test"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/hostgroup/demo/nodes":
			time.Sleep(150 * time.Millisecond)
			created.Store(true)
			_, _ = w.Write([]byte(`{"hostname":"demo-2","hostgroup":"demo"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/hostgroup/demo/nodes":
			if !created.Load() {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hostname":"unrelated","hostgroup":"demo","tags":["purpose=other"]},{"hostname":"demo-2","hostgroup":"demo","tags":["run=claude-client-timeout-test"]}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/hostgroup/demo/nodes/demo-2":
			deleted.Store(true)
			_, _ = w.Write([]byte(`{"message":"deleted"}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), 75*time.Millisecond)
	defer cancel()
	_, err := createVM(ctx, client, "demo", runTag, false, slicer.SlicerCreateNodeRequest{
		Tags: []string{"example=claude", "run=" + runTag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: 50 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("error: got %v, want context deadline", err)
	}
	if !created.Load() {
		t.Fatal("fake VM did not finish creating after the client deadline")
	}
	if !deleted.Load() {
		t.Fatal("late-created VM was not deleted")
	}
}

func TestCreateVMReconcilesAfterGatewayFailure(t *testing.T) {
	var created atomic.Bool
	var deleted atomic.Bool
	var listCalls atomic.Int32
	const runTag = "claude-gateway-failure-test"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/hostgroup/demo/nodes":
			go func() {
				time.Sleep(150 * time.Millisecond)
				created.Store(true)
			}()
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream response lost"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/hostgroup/demo/nodes":
			if listCalls.Add(1) == 1 {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte(`{"error":"temporary list failure"}`))
				return
			}
			if deleted.Load() {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			if !created.Load() {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hostname":"unrelated","hostgroup":"demo","tags":["purpose=other"]},{"hostname":"demo-3","hostgroup":"demo","tags":["run=claude-gateway-failure-test"]}]`))
		case r.Method == http.MethodDelete && r.URL.Path == "/hostgroup/demo/nodes/demo-3":
			deleted.Store(true)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"delete response lost"}`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	_, err := createVM(t.Context(), client, "demo", runTag, false, slicer.SlicerCreateNodeRequest{
		Tags: []string{"example=claude", "run=" + runTag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: time.Second,
	})
	if !slicer.IsAPIStatus(err, http.StatusBadGateway) {
		t.Fatalf("error: got %v, want gateway failure", err)
	}
	if !created.Load() {
		t.Fatal("fake VM did not finish creating after the gateway failure")
	}
	if !deleted.Load() {
		t.Fatal("VM created after the gateway failure was not deleted")
	}
}

func TestCreateVMKeepRetainsAmbiguousCreate(t *testing.T) {
	var followUpCalls atomic.Int32
	const runTag = "claude-keep-test"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/hostgroup/demo/nodes" {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"upstream response lost"}`))
			return
		}
		followUpCalls.Add(1)
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	_, err := createVM(t.Context(), client, "demo", runTag, true, slicer.SlicerCreateNodeRequest{
		Tags: []string{"example=claude", "run=" + runTag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: time.Second,
	})
	if !slicer.IsAPIStatus(err, http.StatusBadGateway) {
		t.Fatalf("error: got %v, want gateway failure", err)
	}
	if !strings.Contains(err.Error(), "a VM may remain with tag run="+runTag) {
		t.Fatalf("error %q does not explain retained VM lookup", err)
	}
	if got := followUpCalls.Load(); got != 0 {
		t.Fatalf("follow-up API calls = %d, want 0", got)
	}
}

func TestDeleteVMWithRetryConfirmsLostResponse(t *testing.T) {
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/hostgroup/demo/nodes/demo-4":
			deleteCalls.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"response lost"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/hostgroup/demo/nodes":
			_, _ = w.Write([]byte(`[{"hostname":"unrelated","hostgroup":"demo","tags":["purpose=other"]}]`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := deleteVMWithRetry(ctx, client, "demo", "demo-4", "claude-normal-cleanup-test"); err != nil {
		t.Fatal(err)
	}
	if got := deleteCalls.Load(); got != 2 {
		t.Fatalf("delete calls = %d, want 2 stable absence observations", got)
	}
}

func TestDeleteVMWithRetryRejectsSingleStaleAbsence(t *testing.T) {
	var deleteCalls atomic.Int32
	var listCalls atomic.Int32
	const runTag = "claude-stale-list-test"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/hostgroup/demo/nodes/demo-5":
			if deleteCalls.Add(1) >= 3 {
				_, _ = w.Write([]byte(`{"message":"deleted"}`))
				return
			}
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":"response lost"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/hostgroup/demo/nodes":
			if listCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`[]`))
				return
			}
			_, _ = w.Write([]byte(`[{"hostname":"demo-5","hostgroup":"demo","tags":["run=claude-stale-list-test"]}]`))
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := slicer.NewSlicerClient(server.URL, "", "test", server.Client())
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := deleteVMWithRetry(ctx, client, "demo", "demo-5", runTag); err != nil {
		t.Fatal(err)
	}
	if got := deleteCalls.Load(); got != 3 {
		t.Fatalf("delete calls = %d, want retry after stale absence", got)
	}
	if got := listCalls.Load(); got != 2 {
		t.Fatalf("list calls = %d, want stale absence followed by presence", got)
	}
}
