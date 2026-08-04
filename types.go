package slicer

import (
	"encoding/json"
	"math"
	"net"
	"strings"
	"time"
)

const NonRootUser = uint32(math.MaxUint32)

// SlicerNode represents a node managed by the slicer REST API.
type SlicerNode struct {
	Hostname   string    `json:"hostname"`
	HostGroup  string    `json:"hostgroup,omitempty"`
	IP         string    `json:"ip"`
	RamBytes   int64     `json:"ram_bytes,omitempty"` // RAM size in bytes
	CPUs       int       `json:"cpus,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	Arch       string    `json:"arch,omitempty"`
	Tags       []string  `json:"tags,omitempty"`
	Status     string    `json:"status,omitempty"` // "Running", "Paused", or "Stopped"
	Persistent bool      `json:"persistent,omitempty"`
}

// SlicerCreateNodeRequest contains parameters for creating a node
type SlicerCreateNodeRequest struct {
	RamBytes   int64                          `json:"ram_bytes,omitempty"` // RAM size in bytes (must not exceed host group limit)
	CPUs       int                            `json:"cpus,omitempty"`      // Number of CPUs (must not exceed host group limit)
	GPUCount   int                            `json:"gpu_count,omitempty"`
	Persistent bool                           `json:"persistent,omitempty"`
	DiskImage  string                         `json:"disk_image,omitempty"`
	ImportUser string                         `json:"import_user,omitempty"`
	SSHKeys    []string                       `json:"ssh_keys,omitempty"`
	Userdata   string                         `json:"userdata,omitempty"`
	IP         string                         `json:"ip,omitempty"`
	Tags       []string                       `json:"tags,omitempty"`
	Secrets    []string                       `json:"secrets,omitempty"`
	Network    *SlicerCreateNodeNetworkPolicy `json:"network,omitempty"`
}

// SlicerCreateNodeNetworkPolicy optionally overrides the host group's
// isolated-network allow/drop firewall lists for this VM launch.
type SlicerCreateNodeNetworkPolicy struct {
	Allow []string `json:"allow,omitempty"`
	Drop  []string `json:"drop,omitempty"`
}

// MarshalJSON preserves an explicitly empty list while omitting a nil list.
// This keeps the original slice-based public API while retaining policy
// inheritance semantics on the wire.
func (p SlicerCreateNodeNetworkPolicy) MarshalJSON() ([]byte, error) {
	body := make(map[string]any, 2)
	if p.Allow != nil {
		body["allow"] = p.Allow
	}
	if p.Drop != nil {
		body["drop"] = p.Drop
	}
	return json.Marshal(body)
}

// SlicerVMDescription contains the configured and effective state of one VM.
type SlicerVMDescription struct {
	SlicerNode
	Storage        string                     `json:"storage,omitempty"`
	Image          string                     `json:"image,omitempty"`
	CommitID       string                     `json:"commit_id,omitempty"`
	ParentCommitID string                     `json:"parent_commit_id,omitempty"`
	Network        SlicerVMNetworkDescription `json:"network"`
}

// SlicerVMNetworkDescription explains policy inheritance for one VM.
type SlicerVMNetworkDescription struct {
	Mode      string                         `json:"mode,omitempty"`
	Source    string                         `json:"policy_source,omitempty"`
	HostGroup SlicerVMNetworkPolicy          `json:"host_group"`
	Override  *SlicerCreateNodeNetworkPolicy `json:"override,omitempty"`
	Effective SlicerVMNetworkPolicy          `json:"effective"`
}

// SlicerVMNetworkPolicy is a resolved allow/drop policy.
type SlicerVMNetworkPolicy struct {
	Allow []string `json:"allow"`
	Drop  []string `json:"drop"`
}

// SlicerCreateNodeWaitFor controls how far the server should wait before returning.
type SlicerCreateNodeWaitFor string

const (
	// SlicerCreateNodeWaitAgent returns once the guest agent is reachable.
	SlicerCreateNodeWaitAgent SlicerCreateNodeWaitFor = "agent"
	// SlicerCreateNodeWaitUserdata returns once agent readiness and userdata completion are observed.
	SlicerCreateNodeWaitUserdata SlicerCreateNodeWaitFor = "userdata"
)

// SlicerCreateNodeOptions allows typed create query params.
type SlicerCreateNodeOptions struct {
	// Wait controls server-side readiness waiting (agent/userdata). Empty means no wait.
	Wait SlicerCreateNodeWaitFor `json:"-"`
	// Timeout is optional wait timeout when Wait is set. Parsed as Go duration.
	Timeout time.Duration `json:"-"`
}

// SlicerRestoreVMWaitFor controls server-side readiness waiting for restore.
// Userdata is intentionally not exposed here: snapshot resume returns to a
// guest that has already booted, so the userdata-ran sentinel is irrelevant.
type SlicerRestoreVMWaitFor string

const (
	SlicerRestoreVMWaitNone  SlicerRestoreVMWaitFor = ""
	SlicerRestoreVMWaitAgent SlicerRestoreVMWaitFor = "agent"
)

// SlicerRestoreVMOptions mirrors SlicerCreateNodeOptions for the /vm/{h}/restore
// endpoint.
type SlicerRestoreVMOptions struct {
	// Wait controls server-side readiness waiting. Empty means no wait.
	Wait SlicerRestoreVMWaitFor `json:"-"`
	// Timeout is optional wait timeout when Wait is set.
	Timeout time.Duration `json:"-"`
}

type slicerForkVMOptions struct {
	Timeout time.Duration              `json:"-"`
	Wait    SlicerForkVMWaitFor        `json:"-"`
	Network *SlicerForkVMNetworkPolicy `json:"-"`
	Tags    []string                   `json:"-"`
	TagMode SlicerForkVMTagMode        `json:"-"`
	Secrets []string                   `json:"-"`
	// Persistent is nil for the server's current persistent default.
	Persistent *bool `json:"-"`
	// Fixups is nil for the correctness-first default. A non-nil empty slice
	// disables post-clone guest fix-ups.
	Fixups   []SlicerForkVMFixup `json:"-"`
	VCPU     int                 `json:"-"`
	RAMBytes int64               `json:"-"`
}

type SlicerForkVMWaitFor string

const (
	// SlicerForkWaitDefault preserves the historical SDK behaviour and waits
	// for agent readiness and selected guest fix-ups.
	SlicerForkWaitDefault SlicerForkVMWaitFor = ""
	SlicerForkWaitNone    SlicerForkVMWaitFor = "none"
	SlicerForkWaitAgent   SlicerForkVMWaitFor = "agent"
)

type SlicerForkVMTagMode string

const (
	SlicerForkTagsAppend  SlicerForkVMTagMode = "append"
	SlicerForkTagsReplace SlicerForkVMTagMode = "replace"
)

type SlicerForkVMFixup string

const (
	SlicerForkFixupHostname    SlicerForkVMFixup = "hostname"
	SlicerForkFixupMachineID   SlicerForkVMFixup = "machine-id"
	SlicerForkFixupSSHHostKeys SlicerForkVMFixup = "ssh-host-keys"
)

// SlicerForkVMOption configures a committed VM fork.
type SlicerForkVMOption interface {
	applyFork(*slicerForkVMOptions)
}

type slicerForkVMOptionFunc func(*slicerForkVMOptions)

func (option slicerForkVMOptionFunc) applyFork(options *slicerForkVMOptions) {
	option(options)
}

// WithFixups selects post-clone guest identity fix-ups. Calling it without
// arguments disables all fix-ups; omitting it keeps the server default.
func WithFixups(fixups ...SlicerForkVMFixup) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) {
		options.Fixups = append([]SlicerForkVMFixup{}, fixups...)
	})
}

// WithVCPU sets the child's vCPU count within the source host-group limit.
func WithVCPU(vcpu int) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.VCPU = vcpu })
}

// WithRAMBytes sets the child's RAM in bytes within the source host-group limit.
func WithRAMBytes(ramBytes int64) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.RAMBytes = ramBytes })
}

// WithTags appends tags to those inherited from the committed VM.
func WithTags(tags ...string) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) {
		options.Tags = append([]string{}, tags...)
	})
}

// WithReplaceTags replaces inherited tags. Calling it without arguments clears
// all inherited tags.
func WithReplaceTags(tags ...string) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) {
		options.Tags = append([]string{}, tags...)
		options.TagMode = SlicerForkTagsReplace
	})
}

// WithSecrets replaces inherited secret grants. Calling it without arguments
// clears all inherited grants and blots their files from the child.
func WithSecrets(secrets ...string) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) {
		options.Secrets = append([]string{}, secrets...)
	})
}

// WithPersistent controls whether the child survives shutdown for relaunch.
func WithPersistent(persistent bool) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.Persistent = &persistent })
}

// WithEphemeral discards the child's cloned storage when it stops or is deleted.
func WithEphemeral() SlicerForkVMOption {
	return WithPersistent(false)
}

// WithWait controls whether the fork waits for agent readiness and finalisation.
// A no-wait response acknowledges launch only; the daemon removes the child if
// requested asynchronous finalisation later fails.
func WithWait(wait SlicerForkVMWaitFor) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.Wait = wait })
}

// WithTimeout sets the readiness and finalisation timeout.
func WithTimeout(timeout time.Duration) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.Timeout = timeout })
}

// WithNetwork overrides the source host group's isolated network policy.
func WithNetwork(network *SlicerForkVMNetworkPolicy) SlicerForkVMOption {
	return slicerForkVMOptionFunc(func(options *slicerForkVMOptions) { options.Network = network })
}

// SlicerForkVMNetworkPolicy optionally overrides the host group's isolated
// network allow/drop lists. Nil fields inherit; non-nil empty slices clear a
// list.
type SlicerForkVMNetworkPolicy struct {
	Allow *[]string `json:"allow,omitempty"`
	Drop  *[]string `json:"drop,omitempty"`
}

type SlicerCommitVMOptions struct {
	Tags     []string          `json:"tags,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
	CacheKey string            `json:"cache_key,omitempty"`
}

type SlicerCommitVMResponse struct {
	Hostname     string            `json:"hostname"`
	CommitID     string            `json:"commit_id"`
	Status       string            `json:"status"`
	ParentStatus string            `json:"parent_status,omitempty"`
	Mode         string            `json:"mode"`
	Tags         []string          `json:"tags,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	CacheKey     string            `json:"cache_key,omitempty"`
}

type SlicerCommitListOptions struct {
	Tags     []string
	CacheKey string
	Source   string
	Mode     string
}

type SlicerCommitInfo struct {
	CommitID        string            `json:"commit_id"`
	SourceHostname  string            `json:"source_hostname"`
	SourceHostGroup string            `json:"source_host_group"`
	CreatedAt       time.Time         `json:"created_at"`
	Mode            string            `json:"mode"`
	Tags            []string          `json:"tags,omitempty"`
	Labels          map[string]string `json:"labels,omitempty"`
	CacheKey        string            `json:"cache_key,omitempty"`
}

type SlicerCommitDeleteResponse struct {
	CommitID string `json:"commit_id"`
	Status   string `json:"status"`
}

type SlicerCommittedVM struct {
	SlicerCommitVMResponse
	client *SlicerClient
}

type SlicerForkVMResponse struct {
	Hostname       string `json:"hostname"`
	SourceHostname string `json:"source_hostname"`
	CommitID       string `json:"commit_id,omitempty"`
	Status         string `json:"status"`
	ParentStatus   string `json:"parent_status,omitempty"`
	ChildStatus    string `json:"child_status,omitempty"`
	Mode           string `json:"mode"`
	Persistent     bool   `json:"persistent"`
}

// MB converts megabytes to bytes
func MiB(mb int64) int64 {
	return mb * 1024 * 1024
}

// GB converts gigabytes to bytes
func GiB(gb int64) int64 {
	return gb * 1024 * 1024 * 1024
}

// SlicerCreateNodeResponse is the response from the REST API when creating a node.
type SlicerCreateNodeResponse struct {
	///{"hostname":"api-1","ip":"192.168.137.2/24","created_at":"2025-11-14T13:28:34.218182826Z"}

	Hostname  string    `json:"hostname"`
	HostGroup string    `json:"hostgroup,omitempty"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
	Arch      string    `json:"arch,omitempty"`
}

func (n *SlicerCreateNodeResponse) IPAddress() net.IP {
	if strings.Contains(n.IP, "/") {
		ip, _, _ := net.ParseCIDR(n.IP)
		return ip
	}
	return net.ParseIP(n.IP)
}

// SlicerHostGroup represents a host group from the /hostgroup endpoint.
type SlicerHostGroup struct {
	Name     string `json:"name,omitempty"`
	Count    int    `json:"count,omitempty"`
	RamBytes int64  `json:"ram_bytes,omitempty"` // RAM size in bytes
	CPUs     int    `json:"cpus,omitempty"`
	Arch     string `json:"arch,omitempty"`
	GPUCount int    `json:"gpu_count,omitempty"`
}

// ExecWriteResult represents output from commands executing within a microVM.
type SlicerExecWriteResult struct {
	Timestamp time.Time `json:"timestamp,omitempty,omitzero"`
	Type      string    `json:"type,omitempty"`
	Pid       int       `json:"pid,omitempty"`
	Encoding  string    `json:"encoding,omitempty"`
	Data      string    `json:"data,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitempty,omitzero"`
	Signal    string    `json:"signal,omitempty"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`

	// Background-exec extensions. The foreground /exec streaming API never sets
	// these; they are emitted by the background-exec log stream.
	ID            uint64 `json:"id,omitempty"`
	DroppedBytes  int64  `json:"dropped_bytes,omitempty"`
	DroppedFrames int    `json:"dropped_frames,omitempty"`
	Message       string `json:"message,omitempty"`
}

type ExecResult struct {
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	Encoding  string    `json:"encoding,omitempty"`
	Pid       int       `json:"pid,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty,omitzero"`
	EndedAt   time.Time `json:"ended_at,omitempty,omitzero"`
	Signal    string    `json:"signal,omitempty"`
	ExitCode  int       `json:"exit_code"`
	Error     string    `json:"error,omitempty"`
}

// SlicerExecRequest contains parameters for invoking a command
// within a VM.
type SlicerExecRequest struct {
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	Env         []string `json:"env,omitempty"`
	UID         uint32   `json:"uid,omitempty"`
	GID         uint32   `json:"gid,omitempty"`
	Stdin       bool     `json:"stdin,omitempty"`
	Stdout      bool     `json:"stdout,omitempty"`
	Stderr      bool     `json:"stderr,omitempty"`
	Stdio       string   `json:"stdio,omitempty"`
	Shell       string   `json:"shell,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Permissions string   `json:"permissions,omitempty"`
}

// SlicerCpRequest contains parameters for copying files to/from a VM
type SlicerCpRequest struct {
	VM   string // VM name
	Path string // Path on the VM
}

// SlicerFSInfo represents file system entry metadata returned by VM fs endpoints.
type SlicerFSInfo struct {
	Name  string    `json:"name"`
	Type  string    `json:"type"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
	Mode  string    `json:"mode"`
}

// SlicerFSMkdirRequest contains parameters for mkdir on a VM.
type SlicerFSMkdirRequest struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
	Mode      string `json:"mode,omitempty"`
}

// SlicerNodeStat represents stats for a VM node
type SlicerNodeStat struct {
	Hostname  string          `json:"hostname"`
	IP        string          `json:"ip"`
	CreatedAt time.Time       `json:"created_at"`
	Snapshot  *SlicerSnapshot `json:"snapshot"`
	Error     string          `json:"error"`
}

// SlicerSnapshot represents a snapshot of VM metrics
type SlicerSnapshot struct {
	Hostname             string    `json:"hostname"`
	Arch                 string    `json:"arch"`
	Timestamp            time.Time `json:"timestamp"`
	Uptime               string    `json:"uptime"`
	TotalCPUS            int       `json:"totalCpus"`
	TotalMemory          uint64    `json:"totalMemory"`
	MemoryUsed           uint64    `json:"memoryUsed"`
	MemoryAvailable      uint64    `json:"memoryAvailable"`
	MemoryUsedPercent    float64   `json:"memoryUsedPercent"`
	LoadAvg1             float64   `json:"loadAvg1"`
	LoadAvg5             float64   `json:"loadAvg5"`
	LoadAvg15            float64   `json:"loadAvg15"`
	DiskReadTotal        float64   `json:"diskReadTotal"`
	DiskWriteTotal       float64   `json:"diskWriteTotal"`
	NetworkReadTotal     float64   `json:"networkReadTotal"`
	NetworkWriteTotal    float64   `json:"networkWriteTotal"`
	DiskIOInflight       int64     `json:"diskIOInflight"`
	OpenConnections      int64     `json:"openConnections"`
	OpenFiles            int64     `json:"openFiles"`
	Entropy              int64     `json:"entropy"`
	DiskSpaceTotal       uint64    `json:"diskSpaceTotal"`
	DiskSpaceUsed        uint64    `json:"diskSpaceUsed"`
	DiskSpaceFree        uint64    `json:"diskSpaceFree"`
	DiskSpaceUsedPercent float64   `json:"diskSpaceUsedPercent"`
}

// SlicerLogsResponse represents the response from the logs endpoint
type SlicerLogsResponse struct {
	Hostname string `json:"hostname"`
	Lines    int    `json:"lines"`
	Content  string `json:"content"`
}

// SlicerDeleteResponse represents the response from the delete endpoint
type SlicerDeleteResponse struct {
	Message     string `json:"message"`
	DiskRemoved string `json:"disk_removed"`
	Error       string `json:"error"`
}

type SlicerAgentHealthResponse struct {
	// Hostname is the hostname of the agent
	Hostname string `json:"hostname,omitempty"`

	// Uptime is the uptime of the agent
	AgentUptime time.Duration `json:"agent_uptime,omitempty"`

	// AgentVersion is the version of the agent
	AgentVersion string `json:"agent_version,omitempty"`

	// SystemUptime is the uptime of the system
	SystemUptime time.Duration `json:"system_uptime,omitempty"`

	// UserdataRan indicates whether the user data script has completed executing
	UserdataRan bool `json:"userdata_ran,omitempty"`

	// UserdataExitCode is the user data script's exit code. Nil means the
	// script is still running, or the guest has a legacy empty completion marker.
	UserdataExitCode *int `json:"userdata_exit_code,omitempty"`

	// IP is the guest's primary IPv4 address, as reported by the agent.
	IP string `json:"ip,omitempty"`
}

// SlicerShutdownRequest contains parameters for shutting down or rebooting a VM.
// Action can be "shutdown" (default) to halt the VM or "reboot" to restart it.
type SlicerShutdownRequest struct {
	// Action specifies the shutdown action: "shutdown" (halt) or "reboot" (restart).
	// If empty, defaults to "shutdown".
	Action string `json:"action,omitempty"`
}

// SlicerInfo represents version and server information from the /info endpoint
type SlicerInfo struct {
	// Version is the version of the slicer server
	Version string `json:"version,omitempty"`

	// GitCommit is the git commit hash of the slicer server
	GitCommit string `json:"git_commit,omitempty"`

	// Platform indicates the server operating system (runtime.GOOS).
	Platform string `json:"platform,omitempty"`

	// Arch is the server architecture (runtime.GOARCH).
	Arch string `json:"arch,omitempty"`
}
