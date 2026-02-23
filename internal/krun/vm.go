package krun

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"time"
)

// VMConfig holds the configuration for a new VM.
type VMConfig struct {
	LibkrunPath string
	LogLevel    uint32
	VmmBinPath  string

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

	// Vsock shutdown agent
	ShutdownSocketPath string
}

// vmmConfig is the JSON sent to the krun-vmm child process via stdin.
type vmmConfig struct {
	LibkrunPath        string   `json:"libkrun_path"`
	LogLevel           uint32   `json:"log_level"`
	VCPUs              uint8    `json:"vcpus"`
	MemoryMiB          uint32   `json:"memory_mib"`
	RootfsPath         string   `json:"rootfs_path"`
	ExecPath           string   `json:"exec_path"`
	Args               []string `json:"args"`
	Env                []string `json:"env"`
	Workdir            string   `json:"workdir"`
	ConsolePath        string   `json:"console_path"`
	NetSocketPath      string   `json:"net_socket_path"`
	MAC                []uint8  `json:"mac"`
	ShutdownSocketPath string   `json:"shutdown_socket_path"`
}

// VM represents a running VM managed as a child process.
type VM struct {
	cfg                VMConfig
	cmd                *exec.Cmd
	errC               chan error
	shutdownSocketPath string
}

// NewVM creates a VM from the given config. No child process is spawned yet.
func NewVM(cfg VMConfig) *VM {
	return &VM{
		cfg:                cfg,
		shutdownSocketPath: cfg.ShutdownSocketPath,
	}
}

// Start spawns the krun-vmm child process and monitors it.
func (vm *VM) Start() error {
	childCfg := vmmConfig{
		LibkrunPath:        vm.cfg.LibkrunPath,
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
		ShutdownSocketPath: vm.cfg.ShutdownSocketPath,
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

// Stop attempts a graceful shutdown via the vsock shutdown agent.
// The agent calls sync() + reboot(POWER_OFF), which causes libkrun's
// _exit() to terminate the child process.
func (vm *VM) Stop() error {
	fmt.Fprintf(os.Stderr, "[vm.Stop] starting shutdown\n")

	if vm.shutdownSocketPath != "" {
		fmt.Fprintf(os.Stderr, "[vm.Stop] connecting to vsock socket %s\n", vm.shutdownSocketPath)
		conn, err := net.DialTimeout("unix", vm.shutdownSocketPath, 2*time.Second)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[vm.Stop] vsock connect failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "[vm.Stop] vsock connected, waiting for agent to sync\n")
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var buf [16]byte
			n, err := conn.Read(buf[:])
			conn.Close()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[vm.Stop] vsock read failed: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "[vm.Stop] agent responded: %s\n", string(buf[:n]))
			}

			// Agent will call reboot(POWER_OFF) → child exits via _exit.
			// Wait for the child process to actually exit.
			fmt.Fprintf(os.Stderr, "[vm.Stop] waiting for child process to exit\n")
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
