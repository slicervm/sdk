package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	slicer "github.com/slicervm/sdk"
)

// egress-filter is a single Go process that is the process supervisor for a real
// Slicer daemon and its egress proxy. It boots both as child processes, then
// configures the proxy entirely through the SDK (client, secret, allow rules)
// and proves the rules hold by running curl from inside an isolated microVM:
//
//   - an allowed upstream returns 200 and carries the injected credential
//   - an upstream with no allow rule is refused (default deny)
//   - revoking the allow rule blocks the previously-allowed upstream
//
// Run it on the host that will run the daemon (e.g. a Slicer box). The daemon
// API binds to 127.0.0.1 by default; pass -expose to open it on 0.0.0.0:<api-port>
// so it can be inspected from elsewhere. On exit the supervisor tears down the
// VM, the proxy client, and its child daemon/proxy processes. --keep leaves the
// whole stack running for debugging until a signal is received.

func main() {
	var (
		bin     string
		sudo    bool
		group   string
		storage string
		gateway string
		cidr    string
		keep    bool
		apiPort int
		apiHost string
		license string
		localIP string
		expose  bool
	)
	flag.StringVar(&bin, "bin", envOr("SLICER_BIN", "slicer"), "path to the slicer binary")
	flag.BoolVar(&sudo, "sudo", true, "prefix daemon and proxy commands with sudo")
	// Host group names max out at 12 chars for isolated networking (Firecracker
	// interface limit: s{hostgroup}-{num}), so keep the default short even though
	// the example is called egress-filter.
	const defaultGroup = "egress"
	flag.StringVar(&group, "group", defaultGroup, "host group name (max 12 chars for isolated networking)")
	flag.StringVar(&storage, "storage", "devmapper", "storage backend for the host group (devmapper or image)")
	flag.StringVar(&gateway, "gateway", defaultGateway, "isolated-network gateway IP the VM uses to reach the proxy")
	flag.StringVar(&cidr, "cidr", "192.168.141.0/24", "isolated-network CIDR for the host group")
	flag.BoolVar(&keep, "keep", false, "leave the daemon, proxy and VM running after passing; Ctrl-C tears down")
	flag.IntVar(&apiPort, "api-port", 8080, "TCP API port the daemon listens on")
	flag.StringVar(&apiHost, "api-host", "127.0.0.1", "address the local SDK client uses to reach the daemon")
	flag.StringVar(&license, "license-file", "", "path to the slicer license file")
	flag.StringVar(&localIP, "local-ip", "", "this host's LAN IP (auto-detected if empty)")
	flag.BoolVar(&expose, "expose", false, "bind the (unauthenticated) dev daemon API to 0.0.0.0 so it can be inspected from other hosts")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	sup, err := boot(ctx, supervisorConfig{
		bin: bin, sudo: sudo, group: group, storage: storage, gateway: gateway, cidr: cidr,
		apiPort: apiPort, apiHost: apiHost, licenseFile: license, expose: expose,
	})
	if err != nil {
		log.Fatalf("boot: %v", err)
	}
	log.Printf("daemon API at %s:%d (workdir %s)", apiHost, apiPort, sup.workdir)

	if localIP == "" {
		ip, dErr := outboundIP()
		if dErr == nil {
			localIP = ip
		} else {
			localIP = "127.0.0.1"
		}
	}

	// Self-hosted upstreams (plain HTTP) on this host's loopback/LAN so the
	// co-located proxy can dial them via absolute-form requests.
	allowed, err := startUpstream(localIP, "allowed")
	if err != nil {
		fatal(sup, "start allowed upstream: %v", err)
	}
	denied, err := startUpstream(localIP, "denied")
	if err != nil {
		fatal(sup, "start denied upstream: %v", err)
	}

	clientToken, err := retry(func() (string, error) { return configureProxy(ctx, sup.client, "egress-filter", allowed, denied) }, 60*time.Second)
	if err != nil {
		fatal(sup, "configure proxy: %v", err)
	}

	node, err := retry(func() (*slicer.SlicerCreateNodeResponse, error) {
		return sup.client.CreateVMWithOptions(ctx, group, slicer.SlicerCreateNodeRequest{
			CPUs:     1,
			RamBytes: slicer.GiB(1),
			Tags:     []string{"example=egress-filter"},
		}, slicer.SlicerCreateNodeOptions{Wait: slicer.SlicerCreateNodeWaitAgent, Timeout: 2 * time.Minute})
	}, 90*time.Second)
	if err != nil {
		fatal(sup, "create VM: %v", err)
	}
	log.Printf("VM %s ready (ip=%s)", node.Hostname, node.IP)
	sup.vm = node.Hostname

	// One teardown path. Registered unconditionally (idempotent): for -keep it
	// still runs only after the park block returns, so the stack stays up until
	// a signal, and an early failure or Ctrl-C still cleans everything up.
	cleanup := func() {
		if sup.vm != "" {
			_, _ = sup.client.DeleteVM(context.Background(), group, sup.vm)
		}
		allowed.Close()
		denied.Close()
		sup.stop()
	}
	defer cleanup()

	// A Ctrl-C/SIGTERM at any point must tear the stack down too, not just leave
	// it for the -keep park: the process owns privileged child process groups and
	// a VM.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("interrupt received, tearing down")
		cleanup()
		os.Exit(0)
	}()

	// The guest may only reach the gateway under the --drop 0.0.0.0/0 policy;
	// the plaintext proxy data-plane listens there on 3128.
	proxyURL := "http://:" + clientToken + "@" + gateway + ":3128"

	// 1. Allowed upstream returns 200 and carries the injected credential.
	out, statusCode := guestCurl(ctx, sup.client, node.Hostname, proxyURL, allowed)
	if statusCode != 200 {
		fatal(sup, "assert 1 FAILED (allowed + secret): expected HTTP 200, got %d\n%s", statusCode, strings.TrimSpace(out))
	}
	if !strings.Contains(out, "Bearer banshee") {
		fatal(sup, "assert 1 FAILED: proxy did not inject the bound secret\nbody: %s", out)
	}
	log.Printf("PASS  1 allowed upstream reachable, secret injected")
	log.Printf("      upstream saw: %s", strings.TrimSpace(strings.ReplaceAll(out, "\n", " ")))

	// 2. An unknown upstream is refused with the proxy's 403 (default deny).
	out, statusCode = guestCurl(ctx, sup.client, node.Hostname, proxyURL, denied)
	if statusCode != 403 {
		fatal(sup, "assert 2 FAILED: expected proxy 403 for un-allowed upstream, got %d\n%s", statusCode, strings.TrimSpace(out))
	}
	log.Printf("PASS  2 un-allowed upstream refused by proxy (403)")

	// 3. Revoking the rule blocks the previously-allowed upstream.
	if err := sup.client.RemoveProxyAllow(ctx, "egress-filter", allowed.ip); err != nil {
		fatal(sup, "remove allow rule: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	out, statusCode = guestCurl(ctx, sup.client, node.Hostname, proxyURL, allowed)
	if statusCode != 403 {
		fatal(sup, "assert 3 FAILED: expected proxy 403 after revoke, got %d\n%s", statusCode, strings.TrimSpace(out))
	}
	log.Printf("PASS  3 revoked upstream refused after RemoveProxyAllow (403)")

	fmt.Println("All egress rules proved.")

	if keep {
		log.Printf("-keep set: leaving VM %s, proxy config and daemon up for inspection (API %s:%d); Ctrl-C/SIGTERM tears the stack down", node.Hostname, sup.cfg.apiBind(), apiPort)
		select {} // park; the interrupt handler tears the stack down
	}
}

// fatal logs, deletes any created VM, tears the supervised stack down, then
// exits. It deletes the VM explicitly because os.Exit bypasses defers.
func fatal(sup *supervisor, format string, a ...any) {
	log.Printf(format, a...)
	if sup != nil {
		if sup.vm != "" && sup.client != nil {
			_, _ = sup.client.DeleteVM(context.Background(), sup.cfg.group, sup.vm)
		}
		sup.stop()
	}
	os.Exit(1)
}

// retry runs fn, retrying on any error with backoff for up to timeout.
func retry[T any](fn func() (T, error), timeout time.Duration) (T, error) {
	deadline := time.Now().Add(timeout)
	delay := 500 * time.Millisecond
	var (
		zero T
		v    T
		err  error
	)
	for {
		if v, err = fn(); err == nil {
			return v, nil
		}
		if time.Now().After(deadline) {
			return zero, err
		}
		time.Sleep(delay)
		if delay < 3*time.Second {
			delay *= 2
		}
	}
}

const defaultGateway = "192.168.141.1"

// newCmd builds the command for a supervised slicer subcommand, prefixing sudo
// when requested. Not context-bound, so its lifecycle is owned by the
// supervisor rather than the caller's context.
func newCmd(cfg supervisorConfig, args ...string) *exec.Cmd {
	full := append([]string{}, cfg.bin)
	full = append(full, args...)
	c := exec.Command(full[0], full[1:]...)
	if cfg.sudo {
		c = exec.Command("sudo", append([]string{"-E", cfg.bin}, args...)...)
	}
	// Own process group so stop can kill the whole tree (sudo exec'd the slicer
	// process into the same group as the sudo wrapper).
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

type supervisorConfig struct {
	bin, group, storage, gateway, cidr, apiHost, licenseFile string
	apiPort                                                  int
	sudo, expose                                             bool
}

// apiBind returns the daemon's API bind address: loopback by default, all
// interfaces only when explicitly requested, since the dev API is unauthenticated.
func (c supervisorConfig) apiBind() string {
	if c.expose {
		return "0.0.0.0"
	}
	return "127.0.0.1"
}

// supervisor owns the child processes that run the daemon and proxy, so one Go
// process starts and stops the whole stack.
type supervisor struct {
	cfg     supervisorConfig
	workdir string
	client  *slicer.SlicerClient
	vm      string
	daemon  *exec.Cmd
	proxy   *exec.Cmd
}

// boot launches a fresh Slicer daemon and egress data-plane as child processes
// in a temp dir.
func boot(ctx context.Context, cfg supervisorConfig) (*supervisor, error) {
	sup := &supervisor{cfg: cfg}

	workdir, err := os.MkdirTemp("", "slicer-egress-filter-")
	if err != nil {
		return nil, err
	}
	sup.workdir = workdir
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(workdir)
		}
	}()

	license := []string{}
	if cfg.licenseFile != "" {
		license = []string{"--license-file", cfg.licenseFile}
	}

	// `slicer new` prints the config to stdout; capture it and write the file.
	newArgs := append(append([]string{}, license...), []string{
		"new", cfg.group,
		"--min", "--count=0",
		"--net=isolated",
		"--isolated-range", cfg.cidr,
		"--drop", "0.0.0.0/0",
		"--allow", cfg.gateway,
		"--no-dns",
		"--api-auth=false",
		"--find-ssh-keys=false",
		"--storage", cfg.storage,
		"--cpu", "1", "--ram=1",
		"--api-bind", cfg.apiBind(),
		"--api-port", strconv.Itoa(cfg.apiPort),
	}...)
	yaml, err := exec.CommandContext(ctx, cfg.bin, newArgs...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("slicer new: %w\n%s", err, ee.Stderr)
		}
		return nil, fmt.Errorf("slicer new: %w", err)
	}
	yamlPath := filepath.Join(workdir, "slicer.yaml")
	if err := os.WriteFile(yamlPath, yaml, 0o600); err != nil {
		return nil, err
	}

	// Start the daemon as a supervised child process. These are plain exec.Cmd
	// (not CommandContext) so the -keep path can hold them up for inspection;
	// the supervisor kills them explicitly via stop.
	upArgs := append(append([]string{}, license...), "up", yamlPath)
	daemon := newCmd(cfg, upArgs...)
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	if err := daemon.Start(); err != nil {
		return nil, fmt.Errorf("slicer up: %w", err)
	}
	sup.daemon = daemon

	// Start the proxy data-plane as a second supervised child process.
	proxyArgs := append(append([]string{}, license...),
		"proxy", "up", "--bind", cfg.gateway, "--hostgroup", cfg.group)
	proxy := newCmd(cfg, proxyArgs...)
	proxy.Stdout = os.Stderr
	proxy.Stderr = os.Stderr
	if err := proxy.Start(); err != nil {
		sup.signalGroup(daemon)
		return nil, fmt.Errorf("slicer proxy up: %w", err)
	}
	sup.proxy = proxy

	go func() {
		_ = daemon.Wait()
		log.Printf("supervisor: daemon child exited")
	}()
	go func() {
		_ = proxy.Wait()
		log.Printf("supervisor: proxy child exited")
	}()

	sup.client = slicer.NewSlicerClient(fmt.Sprintf("http://%s:%d", cfg.apiHost, cfg.apiPort), "", "slicer-sdk-egress-filter", nil)
	waitForAPI(ctx, sup.client, 90*time.Second)
	ok = true
	return sup, nil
}

// signalGroup stops one supervised child and its whole process group (the
// slicer process is exec'd into the same group as the sudo wrapper). As a
// non-root supervisor we need sudo to signal the root daemon.
func (s *supervisor) signalGroup(c *exec.Cmd) {
	if c == nil || c.Process == nil {
		return
	}
	if s.cfg.sudo && os.Geteuid() != 0 {
		_ = exec.Command("sudo", "-E", "kill", "-KILL", "--", "-"+strconv.Itoa(c.Process.Pid)).Run()
	} else {
		_ = syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}

// stop tears down the supervised daemon and proxy and removes the temp dir.
func (s *supervisor) stop() {
	stopGroup := func(c *exec.Cmd) {
		s.signalGroup(c)
		if c != nil && c.Process != nil {
			_, _ = c.Process.Wait() // reap the local wrapper
		}
	}
	stopGroup(s.proxy)
	stopGroup(s.daemon)
	_ = os.RemoveAll(s.workdir)
	log.Printf("supervisor: daemon + proxy stopped, workdir removed")
}

// upstream is a plain HTTP server bound to this host's LAN IP (reachable by the
// co-located proxy) that echoes back what the proxy presented.
type upstream struct {
	ip      string
	portNum int
	srv     *http.Server
}

func startUpstream(ip, tag string) (*upstream, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "hosttag=%s\nauth=%s\n", tag, r.Header.Get("Authorization"))
	})
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return &upstream{ip: ip, portNum: ln.Addr().(*net.TCPAddr).Port, srv: srv}, nil
}

func (u *upstream) Close() { _ = u.srv.Close() }

// configureProxy mints a client, registers a bearer credential for the allowed
// upstream, and grants the client one allow rule bound to that secret. It is
// idempotent: each attempt starts from a clean slate so a retry after a partial
// failure converges instead of colliding. Returns the minted client token.
func configureProxy(ctx context.Context, c *slicer.SlicerClient, clientName string, allowed, denied *upstream) (string, error) {
	_ = c.DeleteProxyClient(ctx, clientName) // ignore not-found
	_ = c.DeleteProxySecret(ctx, "banshee")  // ignore not-found

	created, err := c.CreateProxyClient(ctx, clientName, "")
	if err != nil {
		return "", err
	}
	log.Printf("proxy client %s minted", created.Name)

	if err := c.CreateProxySecret(ctx, slicer.CreateProxySecretRequest{
		Name:  "banshee",
		Host:  allowed.ip,
		Value: "banshee",
	}); err != nil {
		return "", fmt.Errorf("create secret: %w", err)
	}
	if err := c.AddProxyAllow(ctx, slicer.AddProxyAllowRequest{
		Client: clientName,
		Host:   allowed.ip,
		Secret: "banshee",
		Ports:  []int{allowed.portNum},
	}); err != nil {
		return "", fmt.Errorf("add allow rule: %w", err)
	}

	// The denied upstream intentionally gets no rule; default-deny covers it.
	log.Printf("allow rule registered: %s:%d -> secret banshee (denied %s:%d left rule-less)",
		allowed.ip, allowed.portNum, denied.ip, denied.portNum)
	return created.Token, nil
}

// guestCurl runs curl inside the guest, proxying to targetURL, and returns the
// captured output and the emitted final HTTP status code (0 if curl got no
// response). Unknown/revoked upstreams reach the proxy denial, so they surface
// as a genuine proxy 403 rather than a transport error.
func guestCurl(ctx context.Context, c *slicer.SlicerClient, hostname, proxyURL string, target *upstream) (string, int) {
	cmd := c.Command(ctx, hostname, "curl", "-sS",
		"-x", proxyURL,
		"-w", "\nHTTP_STATUS:%{http_code}",
		fmt.Sprintf("http://%s:%d/echo", target.ip, target.portNum))
	cmd.Env = []string{"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL}
	out, _ := cmd.CombinedOutput()
	s := string(out)
	return s, parseHTTPStatus(s)
}

// parseHTTPStatus extracts the code from the "HTTP_STATUS:<code>" trailer curl
// emits, defaulting to 0 when absent (i.e. curl got no HTTP response).
func parseHTTPStatus(out string) int {
	const marker = "HTTP_STATUS:"
	if i := strings.LastIndex(out, marker); i >= 0 {
		if code, err := strconv.Atoi(strings.TrimSpace(out[i+len(marker):])); err == nil {
			return code
		}
	}
	return 0
}

func waitForAPI(ctx context.Context, c *slicer.SlicerClient, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := c.GetInfo(ctx); err == nil {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// outboundIP returns this host's LAN IP via a UDP dial that needs no packets.
func outboundIP() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	addr, err := (&net.Dialer{}).DialContext(ctx, "udp", "1.1.1.1:80")
	if err != nil {
		return "", err
	}
	defer addr.Close()
	return addr.LocalAddr().(*net.UDPAddr).IP.String(), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
