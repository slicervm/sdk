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

// Secret represents a secret stored in the slicer system.
// Secrets can be used to store sensitive configuration data, keys, or other private information
// that can be mounted into nodes or used by services.
type Secret struct {
	// Name is the unique name of the secret
	Name string `json:"name"`
	// Size is the size of the secret data in bytes
	Size int64 `json:"size"`
	// Permissions specifies the file permissions for the secret (e.g., "0600")
	Permissions string `json:"permissions"`
	// UID is the optional user ID that should own the secret file
	UID *uint32 `json:"uid"`
	// GID is the optional group ID that should own the secret file
	GID *uint32 `json:"gid"`
}

// CreateSecretRequest is the payload for creating a new secret via the REST API.
type CreateSecretRequest struct {
	// Name is the unique name of the secret
	Name string `json:"name"`
	// Data is the secret content
	Data string `json:"data"`
	// Permissions specifies the file permissions (defaults to system default)
	Permissions string `json:"permissions,omitempty"`
	// UID is the optional user ID that should own the secret file
	UID *uint32 `json:"uid,omitempty"`
	// GID is the optional group ID that should own the secret file
	GID *uint32 `json:"gid,omitempty"`
}

// UpdateSecretRequest is the payload for updating an existing secret via the REST API.
// All fields are optional - only provided fields will be updated.
type UpdateSecretRequest struct {
	// Data is the updated secret content
	Data string `json:"data"`
	// Permissions specifies the file permissions
	Permissions string `json:"permissions,omitempty"`
	// UID is the optional user ID that should own the secret file
	UID *uint32 `json:"uid,omitempty"`
	// GID is the optional group ID that should own the secret file
	GID *uint32 `json:"gid,omitempty"`
}
