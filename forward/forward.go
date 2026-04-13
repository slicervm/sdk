// Package forward implements host-to-VM port forwarding for Slicer.
//
// Each accepted local TCP/Unix connection opens a per-connection WebSocket to
// the daemon at /vm/{hostname}/forward with an X-Inlets-Upstream header that
// tells the daemon what host:port (or unix:/path) to dial inside the guest.
// The WebSocket carries raw bytes both directions as binary frames. No
// subprotocol framing, no multiplexing — one local connection per WebSocket.
//
// Usage:
//
//	fwd, err := forward.Start(ctx, forward.Options{
//	    BaseURL: "http://127.0.0.1:8080",
//	    Token:   os.Getenv("SLICER_TOKEN"),
//	    VMName:  "sbox-1",
//	    Specs:   []string{"127.0.0.1:8080:127.0.0.1:80"},
//	})
//	if err != nil { log.Fatal(err) }
//	defer fwd.Close()
//
//	// listeners are bound and serving; issue your host-side requests now.
//	resp, _ := http.Get("http://127.0.0.1:8080/")
package forward

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Options configures a Forwarder.
type Options struct {
	// BaseURL is the Slicer API URL: `http(s)://host[:port]` or an absolute
	// Unix socket path (`/path/to/slicer.sock` or `unix:///path`).
	BaseURL string

	// Token is the bearer token for the Slicer API. Optional for unix sockets
	// with no auth configured.
	Token string

	// VMName is the hostname of the VM to forward into.
	VMName string

	// Specs are -L-style mappings. See ParseAddressMapping for the accepted
	// formats.
	Specs []string

	// ClientID identifies this client to the daemon via the
	// X-Inlets-Client-ID header. Defaults to os.Hostname().
	ClientID string

	// UserAgent overrides the HTTP User-Agent header on the upgrade request.
	UserAgent string

	// DialTimeout bounds the initial WebSocket handshake. Default 10s.
	DialTimeout time.Duration

	// Logger receives short progress strings. If nil, events are discarded.
	Logger *log.Logger
}

// Listener describes one bound local listener.
type Listener struct {
	// Spec is the original -L string that created this listener.
	Spec string
	// Local is a human-readable form of the local address (e.g.
	// "127.0.0.1:50306" or "/tmp/docker.sock").
	Local string
	// Remote is the upstream target inside the VM as sent in X-Inlets-Upstream.
	Remote string
	// Port is the resolved local TCP port. Useful when the caller asked for 0.
	// Zero for Unix-socket listeners.
	Port int
}

// Forwarder holds bound listeners and the per-connection tunnel machinery.
// A Forwarder is safe to close from any goroutine; Close is idempotent.
type Forwarder struct {
	opts      Options
	mappings  []*AddressMapping
	ctx       context.Context
	cancel    context.CancelFunc
	listeners []Listener
	tcps      []*net.TCPListener
	unixes    []*net.UnixListener

	mu    sync.Mutex
	conns map[io.Closer]struct{}

	wg     sync.WaitGroup
	closed chan struct{}
}

// Start parses specs, binds all local listeners, begins accepting, and
// returns a Forwarder. If binding any listener fails the partial state is
// cleaned up before returning the error.
//
// ctx governs the lifetime of accept loops. Cancelling it is equivalent to
// calling Close.
func Start(ctx context.Context, opts Options) (*Forwarder, error) {
	if len(opts.Specs) == 0 {
		return nil, fmt.Errorf("forward: no specs provided")
	}
	if strings.TrimSpace(opts.VMName) == "" {
		return nil, fmt.Errorf("forward: VMName is required")
	}
	if strings.TrimSpace(opts.BaseURL) == "" {
		return nil, fmt.Errorf("forward: BaseURL is required")
	}
	if opts.ClientID == "" {
		h, _ := os.Hostname()
		opts.ClientID = h
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "slicer-forward/1.0"
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}

	mappings := make([]*AddressMapping, 0, len(opts.Specs))
	for _, spec := range opts.Specs {
		m, err := ParseAddressMapping(spec)
		if err != nil {
			return nil, err
		}
		mappings = append(mappings, m)
	}

	fctx, cancel := context.WithCancel(ctx)
	f := &Forwarder{
		opts:     opts,
		mappings: mappings,
		ctx:      fctx,
		cancel:   cancel,
		conns:    make(map[io.Closer]struct{}),
		closed:   make(chan struct{}),
	}

	for _, m := range mappings {
		if err := f.bind(m); err != nil {
			_ = f.Close()
			return nil, err
		}
	}

	for i, m := range mappings {
		m := m
		switch {
		case m.ListenUnix():
			ln := f.unixes[findUnixIndex(f.unixes, f.listeners[i].Local)]
			f.wg.Add(1)
			go f.acceptLoop(m, ln.Accept)
		default:
			ln := f.tcps[findTCPIndex(f.tcps, f.listeners[i].Local)]
			f.wg.Add(1)
			go f.acceptLoop(m, ln.Accept)
		}
	}

	go func() {
		<-fctx.Done()
		_ = f.Close()
	}()

	return f, nil
}

// Listeners returns the bound local listeners in the order they were supplied.
func (f *Forwarder) Listeners() []Listener {
	out := make([]Listener, len(f.listeners))
	copy(out, f.listeners)
	return out
}

// Wait blocks until all accept loops have exited. Intended for callers who
// want to treat Start+Wait as a foreground operation, equivalent to the
// `slicer vm forward` CLI.
func (f *Forwarder) Wait() error {
	<-f.closed
	return nil
}

// Close tears down all listeners and in-flight tunnel sockets. Safe to call
// multiple times.
func (f *Forwarder) Close() error {
	f.cancel()

	for _, ln := range f.tcps {
		_ = ln.Close()
	}
	for _, ln := range f.unixes {
		_ = ln.Close()
	}

	f.mu.Lock()
	for c := range f.conns {
		_ = c.Close()
	}
	f.conns = map[io.Closer]struct{}{}
	f.mu.Unlock()

	// Best-effort removal of unix-socket files we created.
	for _, m := range f.mappings {
		if m.ListenUnix() {
			_ = os.Remove(m.ListenUnixPath)
		}
	}

	f.wg.Wait()
	select {
	case <-f.closed:
	default:
		close(f.closed)
	}
	return nil
}

// bind opens one local listener and records it in f.listeners.
func (f *Forwarder) bind(m *AddressMapping) error {
	if m.ListenUnix() {
		// Remove any stale socket file first.
		_ = os.Remove(m.ListenUnixPath)
		if err := os.MkdirAll(filepath.Dir(m.ListenUnixPath), 0o755); err != nil {
			return fmt.Errorf("forward: mkdir for %s: %w", m.ListenUnixPath, err)
		}
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: m.ListenUnixPath, Net: "unix"})
		if err != nil {
			return fmt.Errorf("forward: listen unix %s: %w", m.ListenUnixPath, err)
		}
		_ = os.Chmod(m.ListenUnixPath, 0o660)
		f.unixes = append(f.unixes, ln)
		f.listeners = append(f.listeners, Listener{
			Spec:   m.RawSpec,
			Local:  ln.Addr().String(),
			Remote: m.RemoteTarget(),
		})
		return nil
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", fmt.Sprintf("%s:%d", m.ListenAddr, m.ListenPort))
	if err != nil {
		return fmt.Errorf("forward: resolve %s:%d: %w", m.ListenAddr, m.ListenPort, err)
	}
	ln, err := net.ListenTCP("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("forward: listen tcp %s: %w", tcpAddr, err)
	}
	resolved := ln.Addr().(*net.TCPAddr)
	f.tcps = append(f.tcps, ln)
	f.listeners = append(f.listeners, Listener{
		Spec:   m.RawSpec,
		Local:  fmt.Sprintf("%s:%d", m.ListenAddr, resolved.Port),
		Remote: m.RemoteTarget(),
		Port:   resolved.Port,
	})
	return nil
}

// acceptLoop runs one listener's accept loop until the listener is closed.
func (f *Forwarder) acceptLoop(m *AddressMapping, accept func() (net.Conn, error)) {
	defer f.wg.Done()
	for {
		c, err := accept()
		if err != nil {
			if f.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			f.logf("forward: accept error on %s: %v", m.RawSpec, err)
			return
		}
		go f.handleConn(m, c)
	}
}

// handleConn opens a fresh WebSocket tunnel for one accepted local
// connection and pipes bytes in both directions until either side closes.
func (f *Forwarder) handleConn(m *AddressMapping, local net.Conn) {
	f.track(local)
	defer f.untrack(local)
	defer local.Close()

	dialCtx, cancel := context.WithTimeout(f.ctx, f.opts.DialTimeout)
	defer cancel()

	wsConn, err := f.dial(dialCtx, m)
	if err != nil {
		f.logf("forward: dial %s: %v", m.RemoteTarget(), err)
		return
	}
	defer wsConn.Close(websocket.StatusNormalClosure, "closing")

	// Wrap the WebSocket as a net.Conn using binary frames, then io.Copy in
	// both directions. ctx tied to the parent cancels the pipe cleanly.
	pipeCtx, pipeCancel := context.WithCancel(f.ctx)
	defer pipeCancel()

	up := websocket.NetConn(pipeCtx, wsConn, websocket.MessageBinary)
	down := websocket.NetConn(pipeCtx, wsConn, websocket.MessageBinary)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(up, local) // local -> ws
		pipeCancel()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, down) // ws -> local
		pipeCancel()
	}()
	wg.Wait()
}

// dial opens the per-connection WebSocket to the Slicer daemon. For
// unix-socket base URLs the HTTP client's transport is overridden to dial
// the socket; otherwise the default transport is used.
func (f *Forwarder) dial(ctx context.Context, m *AddressMapping) (*websocket.Conn, error) {
	u, unixPath, err := tunnelURL(f.opts.BaseURL, f.opts.VMName)
	if err != nil {
		return nil, err
	}

	h := http.Header{}
	if f.opts.Token != "" {
		h.Set("Authorization", "Bearer "+f.opts.Token)
	}
	h.Set("X-Inlets-Client-ID", f.opts.ClientID)
	h.Set("X-Inlets-Mode", "local")
	h.Set("X-Inlets-Upstream", m.RemoteTarget())
	h.Set("User-Agent", f.opts.UserAgent)

	opts := &websocket.DialOptions{HTTPHeader: h}
	if unixPath != "" {
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", unixPath)
				},
			},
		}
	}

	ws, res, err := websocket.Dial(ctx, u, opts)
	if err != nil {
		return nil, err
	}
	if res != nil && res.StatusCode != http.StatusSwitchingProtocols {
		ws.Close(websocket.StatusProtocolError, "unexpected status")
		return nil, fmt.Errorf("unexpected upgrade status: %d", res.StatusCode)
	}
	return ws, nil
}

func (f *Forwarder) track(c io.Closer) {
	f.mu.Lock()
	f.conns[c] = struct{}{}
	f.mu.Unlock()
}

func (f *Forwarder) untrack(c io.Closer) {
	f.mu.Lock()
	delete(f.conns, c)
	f.mu.Unlock()
}

func (f *Forwarder) logf(format string, args ...any) {
	if f.opts.Logger != nil {
		f.opts.Logger.Printf(format, args...)
	}
}

// tunnelURL converts the SLICER_URL into the ws/wss URL the forward endpoint
// expects. If the source is a unix socket path, the returned unixPath is
// non-empty and the caller must dial that socket for the HTTP upgrade.
func tunnelURL(baseURL, vmName string) (url string, unixPath string, err error) {
	trimmed := strings.TrimSpace(baseURL)
	if strings.HasPrefix(trimmed, "unix://") {
		unixPath = strings.TrimPrefix(trimmed, "unix://")
		return fmt.Sprintf("ws://localhost/vm/%s/forward", vmName), unixPath, nil
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") {
		return fmt.Sprintf("ws://localhost/vm/%s/forward", vmName), trimmed, nil
	}
	u, parseErr := parseURL(trimmed)
	if parseErr != nil {
		return "", "", parseErr
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = fmt.Sprintf("/vm/%s/forward", vmName)
	return u.String(), "", nil
}

func parseURL(s string) (*url.URL, error) {
	return url.Parse(s)
}

func findTCPIndex(slice []*net.TCPListener, local string) int {
	for i, ln := range slice {
		if ln.Addr().String() == local {
			return i
		}
	}
	return len(slice) - 1
}

func findUnixIndex(slice []*net.UnixListener, local string) int {
	for i, ln := range slice {
		if ln.Addr().String() == local {
			return i
		}
	}
	return len(slice) - 1
}
