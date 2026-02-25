package machine

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CGA1123/krun-api/krun"
	"github.com/CGA1123/krun-api/network"
)

// Manager orchestrates the lifecycle of machines (VMs + virtual networks).
type Manager struct {
	mu          sync.RWMutex
	machines    map[string]*Machine
	vms         map[string]*krun.VM
	networks    map[string]*network.VMNetwork
	libkrunPath    string
	vmmBinPath     string
	socketDir      string
	krunLogLevel   uint32
}

// NewManager creates a new machine manager.
func NewManager(libkrunPath, vmmBinPath, socketDir string, krunLogLevel uint32) (*Manager, error) {
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}

	return &Manager{
		machines:     make(map[string]*Machine),
		vms:          make(map[string]*krun.VM),
		networks:     make(map[string]*network.VMNetwork),
		libkrunPath:  libkrunPath,
		vmmBinPath:   vmmBinPath,
		socketDir:    socketDir,
		krunLogLevel: krunLogLevel,
	}, nil
}

// Create registers a new machine in the "created" state.
func (mgr *Manager) Create(name string, cfg Config, netCfg NetworkConfig) (*Machine, error) {
	id := uuid.New().String()
	now := time.Now()

	m := &Machine{
		ID:        id,
		Name:      name,
		Config:    cfg,
		Network:   netCfg,
		State:     StateCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}

	mgr.mu.Lock()
	mgr.machines[id] = m
	mgr.mu.Unlock()

	return m, nil
}

// Get returns a machine by ID.
func (mgr *Manager) Get(id string) (*Machine, error) {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	m, ok := mgr.machines[id]
	if !ok {
		return nil, fmt.Errorf("machine %s not found", id)
	}
	return m, nil
}

// List returns all machines.
func (mgr *Manager) List() []*Machine {
	mgr.mu.RLock()
	defer mgr.mu.RUnlock()

	machines := make([]*Machine, 0, len(mgr.machines))
	for _, m := range mgr.machines {
		machines = append(machines, m)
	}
	return machines
}

// Start launches the VM and its virtual network.
func (mgr *Manager) Start(id string) error {
	mgr.mu.Lock()
	m, ok := mgr.machines[id]
	if !ok {
		mgr.mu.Unlock()
		return fmt.Errorf("machine %s not found", id)
	}

	if m.GetState() != StateCreated && m.GetState() != StateStopped {
		mgr.mu.Unlock()
		return fmt.Errorf("machine %s is in state %s, cannot start", id, m.GetState())
	}
	m.SetState(StateStarting)
	mgr.mu.Unlock()

	// Generate MAC from VM ID.
	mac := MACFromID(id)
	macStr := FormatMAC(mac)

	// Create console log file.
	consolePath := filepath.Join(mgr.socketDir, id+".console")

	// Create per-VM virtual network.
	vmNet, err := network.NewVMNetwork(id, network.Opts{
		GuestMAC:  macStr,
		SocketDir: mgr.socketDir,
		ProxyAddr: m.Network.ProxyAddr,
		AllowList: m.Network.AllowList,
	})
	if err != nil {
		m.SetState(StateStopped)
		return fmt.Errorf("create network: %w", err)
	}

	// Generate shutdown socket path for vsock agent.
	// Remove any stale socket from a previous run (libkrun returns EEXIST otherwise).
	shutdownSocketPath := filepath.Join(mgr.socketDir, id+".shutdown.sock")
	os.Remove(shutdownSocketPath)

	// Generate exec socket path for vsock exec sessions.
	execSocketPath := filepath.Join(mgr.socketDir, id+".exec.sock")
	os.Remove(execSocketPath)

	// Convert machine volumes to krun volumes.
	var volumes []krun.VirtioFSVolume
	for _, v := range m.Config.Volumes {
		volumes = append(volumes, krun.VirtioFSVolume{
			Tag:      v.Tag,
			HostPath: v.HostPath,
		})
	}

	// Configure the krun VM (spawned as a child process).
	vm := krun.NewVM(krun.VMConfig{
		LibkrunPath:        mgr.libkrunPath,
		LogLevel:           mgr.krunLogLevel,
		VmmBinPath:         mgr.vmmBinPath,
		VCPUs:              uint8(m.Config.VCPUs),
		MemoryMiB:          uint32(m.Config.MemoryMiB),
		RootfsPath:         m.Config.RootfsPath,
		ExecPath:           m.Config.ExecPath,
		Args:               m.Config.Args,
		Env:                m.Config.EnvSlice(),
		Workdir:            m.Config.Workdir,
		ConsolePath:        consolePath,
		NetSocketPath:      vmNet.SocketPath(),
		MAC:                mac,
		Volumes:            volumes,
		ShutdownSocketPath: shutdownSocketPath,
		ExecSocketPath:     execSocketPath,
	})

	// Start the VM (spawns krun-vmm child process).
	if err := vm.Start(); err != nil {
		vmNet.Close()
		m.SetState(StateStopped)
		return fmt.Errorf("start VM: %w", err)
	}

	mgr.mu.Lock()
	mgr.vms[id] = vm
	mgr.networks[id] = vmNet
	mgr.mu.Unlock()

	m.ExecSocketPath = execSocketPath
	m.SetState(StateRunning)

	// Monitor the child process — clean up on exit.
	go func() {
		slog.Info("waiting for vm to exit", "vm", id)
		err := vm.Wait()
		if err != nil {
			slog.Error("vm exited with error", "vm", id, "error", err)
		} else {
			slog.Info("vm exited normally", "vm", id)
		}
		slog.Info("closing network for vm", "vm", id)
		vmNet.Close()
		slog.Info("network closed for vm", "vm", id)
		m.SetState(StateStopped)

		mgr.mu.Lock()
		delete(mgr.vms, id)
		delete(mgr.networks, id)
		mgr.mu.Unlock()
	}()

	return nil
}

// Stop shuts down a running VM.
func (mgr *Manager) Stop(id string) error {
	mgr.mu.Lock()
	m, ok := mgr.machines[id]
	if !ok {
		mgr.mu.Unlock()
		return fmt.Errorf("machine %s not found", id)
	}

	vm, hasVM := mgr.vms[id]
	vmNet, hasNet := mgr.networks[id]
	mgr.mu.Unlock()

	if m.GetState() != StateRunning {
		return fmt.Errorf("machine %s is in state %s, cannot stop", id, m.GetState())
	}

	m.SetState(StateStopping)
	slog.Info("stopping machine", "vm", id, "hasVM", hasVM, "hasNet", hasNet)

	// Close the network before stopping the VM to avoid the gvisor-tap-vsock
	// read loop hitting a broken socket when the child process exits.
	// The vsock shutdown socket is independent of the network socket, so this
	// does not affect the graceful shutdown handshake.
	if hasNet {
		slog.Info("closing network", "vm", id)
		vmNet.Close()
	}

	// Signal the VM to shut down (tries graceful vsock, then kills child).
	if hasVM {
		slog.Info("calling vm.Stop()", "vm", id)
		if err := vm.Stop(); err != nil {
			slog.Error("stop vm failed", "vm", id, "error", err)
		}
		slog.Info("vm.Stop() returned, waiting for monitor goroutine cleanup", "vm", id)

		// Wait for the monitor goroutine to finish cleanup.
		// It closes the network, updates state, and removes from maps.
		deadline := time.After(10 * time.Second)
		for m.GetState() != StateStopped {
			select {
			case <-deadline:
				slog.Warn("timeout waiting for vm monitor cleanup, forcing", "vm", id)
				if hasNet {
					vmNet.Close()
				}
				m.SetState(StateStopped)
				mgr.mu.Lock()
				delete(mgr.vms, id)
				delete(mgr.networks, id)
				mgr.mu.Unlock()
				return nil
			case <-time.After(100 * time.Millisecond):
			}
		}
		return nil
	}

	// No VM running — just clean up network if present.
	if hasNet {
		vmNet.Close()
	}
	m.SetState(StateStopped)

	return nil
}

// Delete removes a stopped machine from the manager.
func (mgr *Manager) Delete(id string) error {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	m, ok := mgr.machines[id]
	if !ok {
		return fmt.Errorf("machine %s not found", id)
	}

	state := m.GetState()
	if state != StateStopped && state != StateCreated {
		return fmt.Errorf("machine %s is in state %s, must be stopped before delete", id, state)
	}

	delete(mgr.machines, id)
	return nil
}

// StopAll stops all running VMs. Used during shutdown.
func (mgr *Manager) StopAll() {
	mgr.mu.RLock()
	ids := make([]string, 0)
	for id, m := range mgr.machines {
		if m.GetState() == StateRunning {
			ids = append(ids, id)
		}
	}
	mgr.mu.RUnlock()

	for _, id := range ids {
		slog.Info("stopping machine", "vm", id)
		if err := mgr.Stop(id); err != nil {
			slog.Error("error stopping machine", "vm", id, "error", err)
		}
	}
}

// ConsolePath returns the path to the console log file for a machine.
func (mgr *Manager) ConsolePath(id string) string {
	return filepath.Join(mgr.socketDir, id+".console")
}
