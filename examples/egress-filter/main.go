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
	"path/filepath"
	"strconv"
	"strings"
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
// is exposed over 0.0.0.0:<api-port> so it can be poked from elsewhere; on
// exit the supervisor tears down the VM, the proxy client, and its child
// daemon/proxy processes. --keep leaves them running for debugging.

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
	)
	flag.StringVar(&bin, "bin", envOr("SLICER_BIN", "slicer"), "path to the slicer binary")
	flag.BoolVar(&sudo, "sudo", true, "prefix daemon and proxy commands with sudo")
	flag.StringVar(&group, "group", "egress-filter", "host group name")
	flag.StringVar(&storage, "storage", "devmapper", "storage backend for the host group (devmapper or image)")
	flag.StringVar(&gateway, "gateway", defaultGateway, "isolated-network gateway IP the VM uses to reach the proxy")
	flag.StringVar(&cidr, "cidr", "192.168.141.0/24", "isolated-network CIDR for the host group")
	flag.BoolVar(&keep, "keep", false, "leave the daemon, proxy and VM running on exit")
	flag.IntVar(&apiPort, "api-port", 8080, "TCP API port the daemon listens on")
	flag.StringVar(&apiHost, "api-host", "127.0.0.1", "address the local SDK client uses to reach the daemon")
	flag.StringVar(&license, "license-file", "", "path to the slicer license file")
	flag.StringVar(&localIP, "local-ip", "", "this host's LAN IP (auto-detected if empty)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	sup, err := boot(ctx, supervisorConfig{
		bin: bin, sudo: sudo, group: group, storage: storage, gateway: gateway, cidr: cidr,
		apiPort: apiPort, apiHost: apiHost, licenseFile: license,
	})
	if err != nil {
		log.Fatalf("boot: %v", err)
	}
	log.Printf("daemon API at %s:%d (workdir %s)", apiHost, apiPort, sup.workdir)

	if keep {
		log.Printf("--keep set: leaving daemon/proxy/VM running")
		return
	}
	defer sup.stop()

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
	allowed := startUpstream(localIP, "allowed")
	defer allowed.Close()
	denied := startUpstream(localIP, "denied")
	defer denied.Close()

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
	defer func() { _, _ = sup.client.DeleteVM(ctx, group, node.Hostname) }()

	// The guest may only reach the gateway under the --drop 0.0.0.0/0 policy;
	// the plaintext proxy data-plane listens there on 3128.
	proxyURL := "http://:" + clientToken + "@" + gateway + ":3128"

	// 1. Allowed upstream returns 200 and carries the injected credential.
	out, err := guestCurl(ctx, sup.client, node.Hostname, proxyURL, allowed)
	if err != nil {
		fatal(sup, "assert 1 FAILED (allowed + secret): curl: %v\nbody: %s", err, out)
	}
	if !strings.Contains(out, "Bearer banshee") {
		fatal(sup, "assert 1 FAILED: proxy did not inject the bound secret\nbody: %s", out)
	}
	log.Printf("PASS  1 allowed upstream reachable, secret injected")
	log.Printf("      upstream saw: %s", strings.TrimSpace(strings.ReplaceAll(out, "\n", " ")))

	// 2. An unknown upstream is refused (default deny).
	out, err = guestCurl(ctx, sup.client, node.Hostname, proxyURL, denied)
	if err == nil && !strings.Contains(out, "403") {
		fatal(sup, "assert 2 FAILED: un-allowed upstream was reachable\nbody: %s", out)
	}
	log.Printf("PASS  2 un-allowed upstream refused by proxy")

	// 3. Revoking the rule blocks the previously-allowed upstream.
	if err := sup.client.RemoveProxyAllow(ctx, "egress-filter", allowed.ip); err != nil {
		fatal(sup, "remove allow rule: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	out, err = guestCurl(ctx, sup.client, node.Hostname, proxyURL, allowed)
	if err == nil && !strings.Contains(out, "403") {
		fatal(sup, "assert 3 FAILED: upstream still reachable after revoke\nbody: %s", out)
	}
	log.Printf("PASS  3 revoked upstream refused after RemoveProxyAllow")

	fmt.Println("All egress rules proved.")
}

// fatal logs, tears the supervised stack down, then exits.
func fatal(sup *supervisor, format string, a ...any) {
	log.Printf(format, a...)
	if sup != nil {
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

type supervisorConfig struct {
	bin, group, storage, gateway, cidr, apiHost, licenseFile string
	apiPort                                                  int
	sudo                                                     bool
}

// supervisor owns the child processes that run the daemon and proxy, so one Go
// process starts and stops the whole stack.
type supervisor struct {
	cfg     supervisorConfig
	workdir string
	client  *slicer.SlicerClient
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
		"--api-bind", "0.0.0.0",
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

	// Start the daemon as a supervised child process.
	upArgs := append(append([]string{}, license...), "up", yamlPath)
	daemon := exec.CommandContext(ctx, cfg.bin, upArgs...)
	if cfg.sudo {
		daemon = exec.CommandContext(ctx, "sudo", append([]string{"-E", cfg.bin}, upArgs...)...)
	}
	daemon.Stdout = os.Stderr
	daemon.Stderr = os.Stderr
	if err := daemon.Start(); err != nil {
		return nil, fmt.Errorf("slicer up: %w", err)
	}
	sup.daemon = daemon

	// Start the proxy data-plane as a second supervised child process.
	proxyArgs := append(append([]string{}, license...),
		"proxy", "up", "--bind", cfg.gateway, "--hostgroup", cfg.group)
	proxy := exec.CommandContext(ctx, cfg.bin, proxyArgs...)
	if cfg.sudo {
		proxy = exec.CommandContext(ctx, "sudo", append([]string{"-E", cfg.bin}, proxyArgs...)...)
	}
	proxy.Stdout = os.Stderr
	proxy.Stderr = os.Stderr
	if err := proxy.Start(); err != nil {
		_ = daemon.Process.Kill()
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
	return sup, nil
}

// stop tears down the supervised daemon and proxy and removes the temp dir.
func (s *supervisor) stop() {
	if s.daemon != nil && s.daemon.Process != nil {
		_ = s.daemon.Process.Kill()
	}
	if s.proxy != nil && s.proxy.Process != nil {
		_ = s.proxy.Process.Kill()
	}
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

func startUpstream(ip, tag string) *upstream {
	mux := http.NewServeMux()
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "hosttag=%s\nauth=%s\n", tag, r.Header.Get("Authorization"))
	})
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		log.Fatalf("listen upstream on %s: %v", ip, err)
	}
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	return &upstream{ip: ip, portNum: ln.Addr().(*net.TCPAddr).Port, srv: srv}
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

// guestCurl runs curl inside the guest, proxying to targetURL, and returns its
// combined output.
func guestCurl(ctx context.Context, c *slicer.SlicerClient, hostname, proxyURL string, target *upstream) (string, error) {
	cmd := c.Command(ctx, hostname, "curl", "-sS",
		"-x", proxyURL,
		"-w", "\nHTTP_STATUS:%{http_code}",
		fmt.Sprintf("http://%s:%d/echo", target.ip, target.portNum))
	cmd.Env = []string{"HTTP_PROXY=" + proxyURL, "HTTPS_PROXY=" + proxyURL}
	out, err := cmd.CombinedOutput()
	return string(out), err
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
