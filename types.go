package slicer

import (
	"net"
	"strings"
)

// SlicerNode represents a node managed by the slicer REST API.
type SlicerNode struct {
	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	CreatedAt string `json:"created_at"`
	Arch      string `json:"arch,omitempty"`
}

// SlicerCreateNodeRequest is the payload for creating a node via the REST API.
type SlicerCreateNodeRequest struct {
	RAMGB      int      `json:"ram_gb"`
	CPUs       int      `json:"cpus"`
	ImportUser string   `json:"import_user"`
	Userdata   string   `json:"userdata,omitempty"`
	SSHKeys    []string `json:"ssh_keys,omitempty"`
}

// SlicerCreateNodeResponse is the response from the REST API when creating a node.
type SlicerCreateNodeResponse struct {
	///{"hostname":"api-1","ip":"192.168.137.2/24","created_at":"2025-11-14T13:28:34.218182826Z"}

	Hostname  string `json:"hostname"`
	IP        string `json:"ip"`
	CreatedAt string `json:"created_at"`
	Arch      string `json:"arch,omitempty"`
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
	RAMGB    int    `json:"ram_gb"`
	VCPU     int    `json:"vcpu"`
	Arch     string `json:"arch,omitempty"`
	GPUCount int    `json:"gpu_count,omitempty"`
}
