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
	CreatedAt time.Time `json:"created_at"`
	Arch      string    `json:"arch,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

// SlicerCreateNodeRequest is the payload for creating a node via the REST API.
type SlicerCreateNodeRequest struct {
	RamGB      int      `json:"ram_gb"`
	CPUs       int      `json:"cpus"`
	Tags       []string `json:"tags,omitempty"`
	ImportUser string   `json:"import_user"`
	Userdata   string   `json:"userdata,omitempty"`
	SSHKeys    []string `json:"ssh_keys,omitempty"`
	Secrets    []string `json:"secrets,omitempty"`
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
	Name     string `json:"name"`
	Count    int    `json:"count"`
	RamGB    int    `json:"ram_gb"`
	CPUs     int    `json:"cpus"`
	Arch     string `json:"arch,omitempty"`
	GPUCount int    `json:"gpu_count,omitempty"`
}

// ExecWriteResult represents output from commands executing within a microVM.
type SlicerExecWriteResult struct {
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

// SlicerExecRequest contains parameters for invoking a command
// within a VM.
type SlicerExecRequest struct {
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	UID     uint32   `json:"uid,omitempty"`
	GID     uint32   `json:"gid,omitempty"`
	Stdin   bool     `json:"stdin,omitempty"`
	Stdout  bool     `json:"stdout,omitempty"`
	Stderr  bool     `json:"stderr,omitempty"`
	Shell   string   `json:"shell,omitempty"`
}

// SlicerCpRequest contains parameters for copying files to/from a VM
type SlicerCpRequest struct {
	VM   string // VM name
	Path string // Path on the VM
}
