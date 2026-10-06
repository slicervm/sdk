package slicer

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestShutdownDaemon(t *testing.T) {
	for _, transport := range []string{"tcp", "unix"} {
		t.Run(transport, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != "/daemon/shutdown" {
					t.Errorf("request=%s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer test-token" || r.UserAgent() != "test-agent" {
					t.Error("missing request credentials or user agent")
				}
				w.WriteHeader(202)
			})
			var apiURL string
			if transport == "tcp" {
				s := httptest.NewServer(h)
				defer s.Close()
				apiURL = s.URL
			} else {
				apiURL = filepath.Join(t.TempDir(), "slicer.sock")
				l, err := net.Listen("unix", apiURL)
				if err != nil {
					t.Fatal(err)
				}
				s := &http.Server{Handler: h}
				go s.Serve(l)
				defer s.Close()
			}
			if err := NewSlicerClient(apiURL, "test-token", "test-agent", nil).ShutdownDaemon(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShutdownDaemonRejectsErrorAndHonoursCancellation(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "permission denied", 401) }))
	defer s.Close()
	c := NewSlicerClient(s.URL, "", "", nil)
	if err := c.ShutdownDaemon(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error=%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.ShutdownDaemon(ctx); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}
