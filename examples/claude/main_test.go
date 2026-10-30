package main

import (
	"context"
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

	path, cleanup, err := resolveClaudeCredentials("", func(context.Context) ([]byte, error) {
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
			_, _ = w.Write([]byte(`[{"hostname":"demo-1","hostgroup":"demo","tags":["run=claude-timeout-test"]}]`))
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
	_, err = createVM(ctx, client, "demo", runTag, slicer.SlicerCreateNodeRequest{
		Tags: []string{"example=claude", "run=" + runTag},
	}, slicer.SlicerCreateNodeOptions{
		Wait:    slicer.SlicerCreateNodeWaitUserdata,
		Timeout: wait,
	})
	if err == nil || !strings.Contains(err.Error(), "408 Request Timeout") {
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
			_, _ = w.Write([]byte(`[{"hostname":"demo-2","hostgroup":"demo","tags":["run=claude-client-timeout-test"]}]`))
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
	_, err := createVM(ctx, client, "demo", runTag, slicer.SlicerCreateNodeRequest{
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
