package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/CGA1123/krun-api/internal/krun"
)

// VMConfig is the JSON configuration read from stdin.
type VMConfig struct {
	LibkrunPath        string  `json:"libkrun_path"`
	LogLevel           uint32  `json:"log_level"`
	VCPUs              uint8   `json:"vcpus"`
	MemoryMiB          uint32  `json:"memory_mib"`
	RootfsPath         string  `json:"rootfs_path"`
	ExecPath           string  `json:"exec_path"`
	Args               []string `json:"args"`
	Env                []string `json:"env"`
	Workdir            string  `json:"workdir"`
	ConsolePath        string  `json:"console_path"`
	NetSocketPath      string  `json:"net_socket_path"`
	MAC                []uint8  `json:"mac"`
	ShutdownSocketPath string  `json:"shutdown_socket_path"`
}

func main() {
	runtime.LockOSThread()

	var cfg VMConfig
	if err := json.NewDecoder(os.Stdin).Decode(&cfg); err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: failed to read config from stdin: %v\n", err)
		os.Exit(1)
	}

	lib, err := krun.Open(cfg.LibkrunPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: failed to load libkrun from %s: %v\n", cfg.LibkrunPath, err)
		os.Exit(1)
	}

	if err := lib.SetLogLevel(cfg.LogLevel); err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: failed to set log level: %v\n", err)
	}

	ctxID, err := lib.CreateCtx()
	if err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: create context: %v\n", err)
		os.Exit(1)
	}

	if err := lib.SetVMConfig(ctxID, cfg.VCPUs, cfg.MemoryMiB); err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: set vm config: %v\n", err)
		os.Exit(1)
	}

	if cfg.RootfsPath != "" {
		if err := lib.SetRoot(ctxID, cfg.RootfsPath); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: set root: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg.ExecPath != "" {
		if err := lib.SetExec(ctxID, cfg.ExecPath, cfg.Args, cfg.Env); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: set exec: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg.Workdir != "" {
		if err := lib.SetWorkdir(ctxID, cfg.Workdir); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: set workdir: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg.ConsolePath != "" {
		if err := lib.SetConsoleOutput(ctxID, cfg.ConsolePath); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: set console output: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg.NetSocketPath != "" {
		if err := lib.AddNetUnixgram(ctxID, cfg.NetSocketPath, cfg.MAC, krun.CompatNetFeatures, krun.NetFlagVfkit); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: add net unixgram: %v\n", err)
			os.Exit(1)
		}
	}

	if cfg.ShutdownSocketPath != "" {
		if err := lib.AddVsockPort2(ctxID, 10000, cfg.ShutdownSocketPath, true); err != nil {
			fmt.Fprintf(os.Stderr, "krun-vmm: add vsock port: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Fprintf(os.Stderr, "krun-vmm: starting VM (ctx=%d)\n", ctxID)
	if err := lib.StartEnter(ctxID); err != nil {
		fmt.Fprintf(os.Stderr, "krun-vmm: start enter: %v\n", err)
		os.Exit(1)
	}
}
