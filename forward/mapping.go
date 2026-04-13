package forward

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// AddressMapping describes one -L-style forward: a local listener on the host
// plus the upstream target to dial inside the VM.
type AddressMapping struct {
	// RawSpec is the original string supplied by the caller.
	RawSpec string

	// ListenAddr is the TCP bind address (e.g. "127.0.0.1" or "0.0.0.0").
	// Empty when listening on a Unix socket.
	ListenAddr string
	// ListenPort is the TCP listen port. Zero for random; ignored for Unix.
	ListenPort int
	// ListenUnixPath is set when listening on a Unix socket instead of TCP.
	ListenUnixPath string

	// RemoteHost is the guest-side TCP host to dial. Empty when targeting
	// a guest Unix socket.
	RemoteHost string
	// RemotePort is the guest-side TCP port.
	RemotePort int
	// RemoteUnixPath is set when dialing a Unix socket inside the guest.
	RemoteUnixPath string
}

// ListenUnix reports whether the local listener is a Unix socket.
func (m *AddressMapping) ListenUnix() bool { return m.ListenUnixPath != "" }

// RemoteUnix reports whether the upstream target is a Unix socket.
func (m *AddressMapping) RemoteUnix() bool { return m.RemoteUnixPath != "" }

// RemoteTarget renders the X-Inlets-Upstream header value.
//   TCP:  "host:port"
//   Unix: "unix:/path/to/socket"
func (m *AddressMapping) RemoteTarget() string {
	if m.RemoteUnix() {
		return "unix:" + m.RemoteUnixPath
	}
	return fmt.Sprintf("%s:%d", m.RemoteHost, m.RemotePort)
}

// ListenAddress renders a human-readable form of the local side.
func (m *AddressMapping) ListenAddress() string {
	if m.ListenUnix() {
		return m.ListenUnixPath
	}
	return fmt.Sprintf("%s:%d", m.ListenAddr, m.ListenPort)
}

// ParseAddressMapping parses a -L-style spec. Supported formats:
//
//   127.0.0.1:9000                               TCP, listen and forward same host:port
//   9001:127.0.0.1:9000                          TCP, listen 0.0.0.0:9001 → 127.0.0.1:9000
//   0:127.0.0.1:9000                             TCP, random listen port → 127.0.0.1:9000
//   0.0.0.0:9000:127.0.0.1:9000                  TCP, fully explicit
//   127.0.0.1:9000:/var/run/docker.sock          TCP listen, Unix socket forward
//   9000:/var/run/docker.sock                    TCP listen 0.0.0.0:9000, Unix socket forward
//   /tmp/docker.sock:/var/run/docker.sock        Unix-to-Unix
//   ./docker.sock:/var/run/docker.sock           Unix-to-Unix with relative local path
func ParseAddressMapping(spec string) (*AddressMapping, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty spec")
	}

	// Unix-to-Unix: both sides are path-like.
	if i := strings.LastIndex(spec, ":"); i > 0 {
		localPart := spec[:i]
		remotePart := spec[i+1:]
		if looksLikeUnixSocketPath(localPart) && looksLikeUnixSocketPath(remotePart) {
			local := localPart
			if !filepath.IsAbs(local) {
				abs, err := filepath.Abs(local)
				if err != nil {
					return nil, fmt.Errorf("resolve local socket path %q: %w", local, err)
				}
				local = abs
			}
			return &AddressMapping{RawSpec: spec, ListenUnixPath: local, RemoteUnixPath: remotePart}, nil
		}
	}

	// TCP-to-Unix: contains `:/` where the remote side is a path.
	if i := strings.Index(spec, ":/"); i != -1 {
		listenPart := spec[:i]
		socketPath := spec[i+1:] // keep leading slash
		innerColon := strings.LastIndex(listenPart, ":")
		if innerColon == -1 {
			port, err := parsePort(listenPart, spec)
			if err != nil {
				return nil, err
			}
			return &AddressMapping{
				RawSpec:        spec,
				ListenAddr:     "0.0.0.0",
				ListenPort:     port,
				RemoteUnixPath: socketPath,
			}, nil
		}
		port, err := parsePort(listenPart[innerColon+1:], spec)
		if err != nil {
			return nil, err
		}
		return &AddressMapping{
			RawSpec:        spec,
			ListenAddr:     listenPart[:innerColon],
			ListenPort:     port,
			RemoteUnixPath: socketPath,
		}, nil
	}

	parts := strings.Split(spec, ":")
	switch len(parts) {
	case 2:
		port, err := parsePort(parts[1], spec)
		if err != nil {
			return nil, err
		}
		return &AddressMapping{
			RawSpec:    spec,
			ListenAddr: parts[0],
			ListenPort: port,
			RemoteHost: parts[0],
			RemotePort: port,
		}, nil
	case 3:
		listenPort, err := parsePort(parts[0], spec)
		if err != nil {
			return nil, err
		}
		remotePort, err := parsePort(parts[2], spec)
		if err != nil {
			return nil, err
		}
		return &AddressMapping{
			RawSpec:    spec,
			ListenAddr: parts[1],
			ListenPort: listenPort,
			RemoteHost: parts[1],
			RemotePort: remotePort,
		}, nil
	case 4:
		listenPort, err := parsePort(parts[1], spec)
		if err != nil {
			return nil, err
		}
		remotePort, err := parsePort(parts[3], spec)
		if err != nil {
			return nil, err
		}
		return &AddressMapping{
			RawSpec:    spec,
			ListenAddr: parts[0],
			ListenPort: listenPort,
			RemoteHost: parts[2],
			RemotePort: remotePort,
		}, nil
	}

	return nil, fmt.Errorf("invalid forward spec %q: expected 2-4 colon-separated parts", spec)
}

func looksLikeUnixSocketPath(s string) bool {
	if s == "" {
		return false
	}
	return strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "./") ||
		strings.HasPrefix(s, "../") ||
		strings.Contains(s, "/")
}

func parsePort(s, spec string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("invalid port %q in forward spec %q", s, spec)
	}
	return n, nil
}
