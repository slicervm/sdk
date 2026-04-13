package forward

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAddressMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		spec        string
		wantListen  string // ListenAddress()
		wantRemote  string // RemoteTarget()
		wantErr     bool
		relListenOK bool // when local is resolved from a relative path
	}{
		{
			name:       "tcp same host:port",
			spec:       "127.0.0.1:9000",
			wantListen: "127.0.0.1:9000",
			wantRemote: "127.0.0.1:9000",
		},
		{
			name:       "port only to remote tcp",
			spec:       "9001:127.0.0.1:9000",
			wantListen: "127.0.0.1:9001",
			wantRemote: "127.0.0.1:9000",
		},
		{
			name:       "random port to remote tcp",
			spec:       "0:127.0.0.1:9000",
			wantListen: "127.0.0.1:0",
			wantRemote: "127.0.0.1:9000",
		},
		{
			name:       "fully explicit tcp",
			spec:       "0.0.0.0:9000:127.0.0.1:9000",
			wantListen: "0.0.0.0:9000",
			wantRemote: "127.0.0.1:9000",
		},
		{
			name:       "tcp to unix socket",
			spec:       "127.0.0.1:9000:/var/run/docker.sock",
			wantListen: "127.0.0.1:9000",
			wantRemote: "unix:/var/run/docker.sock",
		},
		{
			name:       "port only tcp to unix",
			spec:       "9000:/var/run/docker.sock",
			wantListen: "0.0.0.0:9000",
			wantRemote: "unix:/var/run/docker.sock",
		},
		{
			name:       "unix to unix absolute",
			spec:       "/tmp/docker.sock:/var/run/docker.sock",
			wantListen: "/tmp/docker.sock",
			wantRemote: "unix:/var/run/docker.sock",
		},
		{
			name:        "unix to unix relative",
			spec:        "./local.sock:/var/run/docker.sock",
			wantRemote:  "unix:/var/run/docker.sock",
			relListenOK: true,
		},
		{name: "empty", spec: "", wantErr: true},
		{name: "one part", spec: "127.0.0.1", wantErr: true},
		{name: "too many parts", spec: "a:b:c:d:e", wantErr: true},
		{name: "invalid port", spec: "x:127.0.0.1:9000", wantErr: true},
		{name: "out of range port", spec: "70000:127.0.0.1:9000", wantErr: true},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, err := ParseAddressMapping(tc.spec)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for spec %q, got mapping %+v", tc.spec, m)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for spec %q: %v", tc.spec, err)
			}
			if m.RemoteTarget() != tc.wantRemote {
				t.Errorf("remote: got %q, want %q", m.RemoteTarget(), tc.wantRemote)
			}
			if tc.relListenOK {
				if !filepath.IsAbs(m.ListenAddress()) {
					t.Errorf("expected relative listen path to be resolved to absolute, got %q", m.ListenAddress())
				}
				if !strings.HasSuffix(m.ListenAddress(), "local.sock") {
					t.Errorf("expected resolved path to end with local.sock, got %q", m.ListenAddress())
				}
			} else if m.ListenAddress() != tc.wantListen {
				t.Errorf("listen: got %q, want %q", m.ListenAddress(), tc.wantListen)
			}
		})
	}
}
