package krun

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"time"

	"connectrpc.com/connect"
	vminitv1 "github.com/CGA1123/krun-api/gen/vminit/v1"
	"github.com/CGA1123/krun-api/gen/vminit/v1/vminitv1connect"
)

// VMConfig holds the configuration for a new VM.
type VMConfig struct {
	LogLevel   uint32
	VmmBinPath string

	VCPUs      uint8
	MemoryMiB  uint32
	RootfsPath string
	ExecPath   string
	Args       []string
	Env        []string
	Workdir    string
	ConsolePath string

	// Network
	NetSocketPath string
	MAC           []uint8

	// Virtiofs volumes
	Volumes []VirtioFSVolume

	// Vsock shutdown agent
	ShutdownSocketPath string

	// Vsock exec session
	ExecSocketPath string
}

// VirtioFSVolume describes a host directory to expose to the guest via virtiofs.
type VirtioFSVolume struct {
	Tag      string `json:"tag"`
	HostPath string `json:"host_path"`
}

// vmmConfig is the JSON sent to the krun-vmm child process via stdin.
type vmmConfig struct {
	LogLevel           uint32   `json:"log_level"`
	VCPUs              uint8    `json:"vcpus"`
	MemoryMiB          uint32   `json:"memory_mib"`
	RootfsPath         string   `json:"rootfs_path"`
	ExecPath           string   `json:"exec_path"`
	Args               []string `json:"args"`
	Env                []string `json:"env"`
	Workdir            string   `json:"workdir"`
	ConsolePath        string   `json:"console_path"`
	NetSocketPath      string           `json:"net_socket_path"`
	MAC                []uint8          `json:"mac"`
	Volumes            []VirtioFSVolume `json:"volumes,omitempty"`
	ShutdownSocketPath string           `json:"shutdown_socket_path"`
	ExecSocketPath     string           `json:"exec_socket_path"`
}

// VM represents a running VM managed as a child process.
type VM struct {
	cfg                VMConfig
	cmd                *exec.Cmd
	errC               chan error
	shutdownSocketPath string
	execSocketPath     string
}

// NewVM creates a VM from the given config. No child process is spawned yet.
func NewVM(cfg VMConfig) *VM {
	return &VM{
		cfg:                cfg,
		shutdownSocketPath: cfg.ShutdownSocketPath,
		execSocketPath:     cfg.ExecSocketPath,
	}
}

// ExecSocketPath returns the host-side Unix socket path for exec vsock connections.
func (vm *VM) ExecSocketPath() string {
	return vm.execSocketPath
}

// Start spawns the krun-vmm child process and monitors it.
func (vm *VM) Start() error {
	childCfg := vmmConfig{
		LogLevel:           vm.cfg.LogLevel,
		VCPUs:              vm.cfg.VCPUs,
		MemoryMiB:          vm.cfg.MemoryMiB,
		RootfsPath:         vm.cfg.RootfsPath,
		ExecPath:           vm.cfg.ExecPath,
		Args:               vm.cfg.Args,
		Env:                vm.cfg.Env,
		Workdir:            vm.cfg.Workdir,
		ConsolePath:        vm.cfg.ConsolePath,
		NetSocketPath:      vm.cfg.NetSocketPath,
		MAC:                vm.cfg.MAC,
		Volumes:            vm.cfg.Volumes,
		ShutdownSocketPath: vm.cfg.ShutdownSocketPath,
		ExecSocketPath:     vm.cfg.ExecSocketPath,
	}

	cfgJSON, err := json.Marshal(childCfg)
	if err != nil {
		return fmt.Errorf("marshal vmm config: %w", err)
	}

	vm.cmd = exec.Command(vm.cfg.VmmBinPath)
	vm.cmd.Stderr = os.Stderr

	stdin, err := vm.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}

	if err := vm.cmd.Start(); err != nil {
		return fmt.Errorf("start krun-vmm: %w", err)
	}

	// Write config and close stdin.
	if _, err := stdin.Write(cfgJSON); err != nil {
		vm.cmd.Process.Kill()
		return fmt.Errorf("write config to krun-vmm: %w", err)
	}
	stdin.Close()

	// Monitor child process exit.
	vm.errC = make(chan error, 1)
	go func() {
		defer close(vm.errC)
		if err := vm.cmd.Wait(); err != nil {
			vm.errC <- err
		}
	}()

	return nil
}

// Stop attempts a graceful shutdown via the vminit Connect RPC service.
// The Shutdown RPC calls sync() inside the guest, then vminit proceeds
// with reboot(POWER_OFF), which causes libkrun's _exit() to terminate
// the child process.
func (vm *VM) Stop() error {
	fmt.Fprintf(os.Stderr, "[vm.Stop] starting shutdown\n")

	if vm.shutdownSocketPath != "" {
		fmt.Fprintf(os.Stderr, "[vm.Stop] calling Shutdown RPC via %s\n", vm.shutdownSocketPath)

		httpClient := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return net.DialTimeout("unix", vm.shutdownSocketPath, 2*time.Second)
				},
			},
		}

		client := vminitv1connect.NewVMInitServiceClient(httpClient, "http://localhost")

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		_, err := client.Shutdown(ctx, connect.NewRequest(&vminitv1.ShutdownRequest{}))
		if err != nil {
			fmt.Fprintf(os.Stderr, "[vm.Stop] Shutdown RPC failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[vm.Stop] Shutdown RPC succeeded, waiting for child process to exit\n")
			if _, exited := vm.WaitTimeout(10 * time.Second); exited {
				fmt.Fprintf(os.Stderr, "[vm.Stop] child process exited cleanly\n")
				return nil
			}
			fmt.Fprintf(os.Stderr, "[vm.Stop] child process did not exit in time\n")
		}
	}

	// Fallback: kill the child process.
	if vm.cmd != nil && vm.cmd.Process != nil {
		fmt.Fprintf(os.Stderr, "[vm.Stop] killing child process\n")
		vm.cmd.Process.Kill()
		vm.WaitTimeout(5 * time.Second)
	}

	return nil
}

// Wait blocks until the child process exits and returns any error.
func (vm *VM) Wait() error {
	if vm.errC == nil {
		return nil
	}
	return <-vm.errC
}

// WaitTimeout blocks until the child exits or the timeout elapses.
// Returns the error and whether the child exited (true) or timed out (false).
func (vm *VM) WaitTimeout(d time.Duration) (error, bool) {
	if vm.errC == nil {
		return nil, true
	}
	select {
	case err := <-vm.errC:
		return err, true
	case <-time.After(d):
		return nil, false
	}
}
