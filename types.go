package slicer

import (
	"net"
	"strings"
	"time"
)

// SlicerNode represents a node managed by the slicer REST API.
type SlicerNode struct {
	Hostname  string    `json:"hostname"`
	IP        string    `json:"ip"`
	RamBytes  int64     `json:"ram_bytes,omitempty"` // RAM size in bytes
	CPUs      int       `json:"cpus,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Arch      string    `json:"arch,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

// SlicerCreateNodeRequest contains parameters for creating a node
type SlicerCreateNodeRequest struct {
	RamBytes   int64    `json:"ram_bytes,omitempty"` // RAM size in bytes (must not exceed host group limit)
	CPUs       int      `json:"cpus,omitempty"`      // Number of CPUs (must not exceed host group limit)
	GPUCount   int      `json:"gpu_count,omitempty"`
	Persistent bool     `json:"persistent,omitempty"`
	DiskImage  string   `json:"disk_image,omitempty"`
	ImportUser string   `json:"import_user,omitempty"`
	SSHKeys    []string `json:"ssh_keys,omitempty"`
	Userdata   string   `json:"userdata,omitempty"`
	IP         string   `json:"ip,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Secrets    []string `json:"secrets,omitempty"`
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
	Timestamp time.Time `json:"timestamp"`
	Stdout    string    `json:"stdout,omitempty"`
	Stderr    string    `json:"stderr,omitempty"`
	ExitCode  int       `json:"exit_code,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// SlicerExecRequest contains parameters for invoking a command
// within a VM.
type SlicerExecRequest struct {
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	UID         uint32   `json:"uid,omitempty"`
	GID         uint32   `json:"gid,omitempty"`
	Stdin       bool     `json:"stdin,omitempty"`
	Stdout      bool     `json:"stdout,omitempty"`
	Stderr      bool     `json:"stderr,omitempty"`
	Shell       string   `json:"shell,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Permissions string   `json:"permissions,omitempty"`
}

// SlicerCpRequest contains parameters for copying files to/from a VM
type SlicerCpRequest struct {
	VM   string // VM name
	Path string // Path on the VM
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
}
