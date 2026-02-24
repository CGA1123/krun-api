package machine

import (
	"fmt"
	"sync"
	"time"
)

// State represents the lifecycle state of a machine.
type State string

const (
	StateCreated  State = "created"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
	StateStopped  State = "stopped"
)

// Volume maps a host directory into the guest via virtiofs.
type Volume struct {
	Tag      string `json:"tag"`
	HostPath string `json:"host_path"`
}

// Config holds the user-supplied configuration for creating a machine.
type Config struct {
	VCPUs      int               `json:"vcpus"`
	MemoryMiB  int               `json:"memory_mib"`
	RootfsPath string            `json:"rootfs_path"`
	ExecPath   string            `json:"exec_path"`
	Args       []string          `json:"args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
	Volumes    []Volume          `json:"volumes,omitempty"`
}

// NetworkConfig holds optional network routing configuration.
type NetworkConfig struct {
	ProxyAddr string   `json:"proxy_addr,omitempty"`
	AllowList []string `json:"allow_list,omitempty"`
}

// Machine represents a single microVM and its metadata.
type Machine struct {
	mu sync.RWMutex

	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Config         Config        `json:"config"`
	Network        NetworkConfig `json:"network,omitempty"`
	State          State         `json:"state"`
	ExecSocketPath string        `json:"exec_socket_path,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// SetState transitions the machine to a new state.
func (m *Machine) SetState(s State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.State = s
	m.UpdatedAt = time.Now()
}

// GetState returns the current state.
func (m *Machine) GetState() State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.State
}

// EnvSlice converts the env map to a slice of "KEY=VALUE" strings.
func (c *Config) EnvSlice() []string {
	if len(c.Env) == 0 {
		return nil
	}
	env := make([]string, 0, len(c.Env))
	for k, v := range c.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}
