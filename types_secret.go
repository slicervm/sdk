package slicer

import "time"

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

	// GID is the user ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	UID uint32 `json:"uid,omitempty"`

	// GID is the group ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	GID uint32 `json:"gid,omitempty"`

	// ModifiedAt is the time the secret was last modified
	ModifiedAt *time.Time `json:"modified_at,omitempty"`
}

// CreateSecretRequest is the payload for creating a new secret via the REST API.
type CreateSecretRequest struct {
	// Name is the unique name of the secret
	Name string `json:"name"`
	// Data is the secret content
	Data string `json:"data"`
	// Permissions specifies the file permissions (defaults to system default)
	Permissions string `json:"permissions,omitempty"`

	// GID is the user ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	UID uint32 `json:"uid,omitempty"`

	// GID is the group ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	GID uint32 `json:"gid,omitempty"`
}

// UpdateSecretRequest is the payload for updating an existing secret via the REST API.
// All fields are optional - only provided fields will be updated.
type UpdateSecretRequest struct {
	// Data is the updated secret content
	Data string `json:"data"`
	// Permissions specifies the file permissions
	Permissions string `json:"permissions,omitempty"`

	// GID is the user ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	UID uint32 `json:"uid,omitempty"`

	// GID is the group ID that should own the secret file. If not set, the default for
	// a uint32 will be used i.e root.
	GID uint32 `json:"gid,omitempty"`
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
}
