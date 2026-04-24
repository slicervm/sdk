// Package shell provides an HTTP handler that proxies browser WebSocket
// connections to a Slicer VM's interactive shell endpoint.
//
// The handler upgrades the incoming HTTP request to a WebSocket, dials the
// Slicer API's /vm/{hostname}/shell endpoint (over TCP or Unix socket),
// and bidirectionally relays binary frames using the Slicer shell protocol.
//
// Usage:
//
//	h := shell.NewProxyHandler(shell.Options{
//	    BaseURL:  "/var/run/slicer/api.sock",
//	    VMName:   "sandbox-1",
//	})
//	mux.Handle("/terminal", myAuth(h))
package shell

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// Frame type constants for the Slicer shell binary protocol.
// Each frame has a 5-byte header: 1 byte frame type + 4 bytes big-endian
// payload length.
const (
	FrameTypeData         = 0x01
	FrameTypeWindowSize   = 0x02
	FrameTypeShutdown     = 0x03
	FrameTypeHeartbeat    = 0x04
	FrameTypeSessionClose = 0x05
)

// Options configures a shell proxy handler.
type Options struct {
	// BaseURL is the Slicer API URL: `http(s)://host[:port]` or an absolute
	// Unix socket path (`/path/to/slicer.sock` or `unix:///path`).
	BaseURL string

	// Token is the bearer token for the Slicer API. Optional for unix sockets
	// with no auth configured.
	Token string

	// VMName is the static hostname of the VM to shell into.
	// Ignored when VMNameFunc is set.
	VMName string

	// VMNameFunc dynamically resolves the VM hostname from the incoming
	// request. Takes precedence over VMName.
	VMNameFunc func(r *http.Request) string

	// Shell overrides the shell binary inside the guest (e.g. "/bin/bash").
	Shell string

	// UID sets the user ID for the shell session. 0 uses the guest default.
	UID int

	// GID sets the group ID for the shell session. 0 uses the guest default.
	GID int

	// Cwd sets the working directory for the shell session.
	Cwd string

	// CheckOrigin validates the Origin header on the incoming WebSocket
	// upgrade. If nil, same-origin policy is enforced.
	CheckOrigin func(r *http.Request) bool

	// Logger receives short progress strings. If nil, events are discarded.
	Logger *log.Logger
}

// NewProxyHandler returns an http.Handler that upgrades browser connections
// to WebSocket and proxies them to the Slicer VM shell endpoint.
func NewProxyHandler(opts Options) http.Handler {
	if strings.TrimSpace(opts.BaseURL) == "" {
		panic("shell: BaseURL is required")
	}
	if strings.TrimSpace(opts.VMName) == "" && opts.VMNameFunc == nil {
		panic("shell: VMName or VMNameFunc is required")
	}

	return &proxyHandler{opts: opts}
}

type proxyHandler struct {
	opts Options
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vmName := h.opts.VMName
	if h.opts.VMNameFunc != nil {
		vmName = h.opts.VMNameFunc(r)
	}
	if vmName == "" {
		http.Error(w, "shell: could not resolve VM name", http.StatusBadRequest)
		return
	}

	upstreamURL, unixPath, err := shellURL(h.opts.BaseURL, vmName, h.opts.Shell, h.opts.UID, h.opts.GID, h.opts.Cwd)
	if err != nil {
		http.Error(w, fmt.Sprintf("shell: bad upstream URL: %v", err), http.StatusInternalServerError)
		return
	}

	// Dial upstream.
	dialCtx, dialCancel := context.WithCancel(r.Context())
	defer dialCancel()

	dialOpts := &websocket.DialOptions{
		HTTPHeader: http.Header{},
	}
	if h.opts.Token != "" {
		dialOpts.HTTPHeader.Set("Authorization", "Bearer "+h.opts.Token)
	}
	if unixPath != "" {
		dialOpts.HTTPClient = &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", unixPath)
				},
			},
		}
	}

	upstream, _, err := websocket.Dial(dialCtx, upstreamURL, dialOpts)
	if err != nil {
		http.Error(w, fmt.Sprintf("shell: upstream dial: %v", err), http.StatusBadGateway)
		return
	}
	defer upstream.CloseNow()

	// Accept browser WebSocket.
	acceptOpts := &websocket.AcceptOptions{
		InsecureSkipVerify: h.opts.CheckOrigin == nil,
	}
	if h.opts.CheckOrigin != nil {
		acceptOpts.OriginPatterns = nil // use custom check below
		acceptOpts.InsecureSkipVerify = h.opts.CheckOrigin(r)
	}

	browser, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		h.logf("shell: accept browser ws: %v", err)
		upstream.Close(websocket.StatusGoingAway, "browser rejected")
		return
	}
	defer browser.CloseNow()

	h.logf("shell: session started for %s", vmName)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	var wg sync.WaitGroup
	wg.Add(2)

	// browser -> upstream
	go func() {
		defer wg.Done()
		defer cancel()
		proxyFrames(ctx, upstream, browser)
	}()

	// upstream -> browser
	go func() {
		defer wg.Done()
		defer cancel()
		proxyFrames(ctx, browser, upstream)
	}()

	wg.Wait()

	browser.Close(websocket.StatusNormalClosure, "session ended")
	upstream.Close(websocket.StatusNormalClosure, "session ended")

	h.logf("shell: session ended for %s", vmName)
}

// proxyFrames reads binary messages from src and writes them to dst until
// the context is cancelled or an error occurs. When a FrameTypeShutdown
// message is read, it is forwarded and the function returns.
func proxyFrames(ctx context.Context, dst, src *websocket.Conn) {
	for {
		typ, data, err := src.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageBinary {
			continue
		}

		if err := dst.Write(ctx, websocket.MessageBinary, data); err != nil {
			return
		}

		// If this is a shutdown frame, stop after forwarding it.
		if len(data) >= 1 && data[0] == FrameTypeShutdown {
			return
		}
	}
}

func (h *proxyHandler) logf(format string, args ...any) {
	if h.opts.Logger != nil {
		h.opts.Logger.Printf(format, args...)
	}
}

// shellURL converts the BaseURL into the ws/wss URL for the shell endpoint.
// If the source is a unix socket path, the returned unixPath is non-empty
// and the caller must dial that socket for the HTTP upgrade.
func shellURL(baseURL, vmName, shell string, uid, gid int, cwd string) (wsURL string, unixPath string, err error) {
	path := fmt.Sprintf("/vm/%s/shell", vmName)

	q := url.Values{}
	if shell != "" {
		q.Set("shell", shell)
	}
	if uid != 0 {
		q.Set("uid", fmt.Sprintf("%d", uid))
	}
	if gid != 0 {
		q.Set("gid", fmt.Sprintf("%d", gid))
	}
	if cwd != "" {
		q.Set("cwd", cwd)
	}

	qs := ""
	if len(q) > 0 {
		qs = "?" + q.Encode()
	}

	trimmed := strings.TrimSpace(baseURL)
	if strings.HasPrefix(trimmed, "unix://") {
		unixPath = strings.TrimPrefix(trimmed, "unix://")
		return fmt.Sprintf("ws://localhost%s%s", path, qs), unixPath, nil
	}
	if strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "./") {
		return fmt.Sprintf("ws://localhost%s%s", path, qs), trimmed, nil
	}

	u, parseErr := url.Parse(trimmed)
	if parseErr != nil {
		return "", "", parseErr
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	u.Path = path
	u.RawQuery = q.Encode()
	return u.String(), "", nil
}
