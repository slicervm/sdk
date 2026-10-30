package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
