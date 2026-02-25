package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CGA1123/krun-api/internal/cli"
)

func main() {
	var (
		image    string
		name     string
		apiURL   string
		cpus     int
		memory   int
		proxy    string
		noStart  bool
		initPath string
	)

	flag.StringVar(&image, "image", "", "Container image ref (e.g. alpine:3.18)")
	flag.StringVar(&name, "n", "", "VM name (default: image basename)")
	flag.StringVar(&name, "name", "", "VM name (default: image basename)")
	flag.StringVar(&apiURL, "api", "http://localhost:8080", "krun-api server URL")
	flag.IntVar(&cpus, "cpus", 2, "Number of vCPUs")
	flag.IntVar(&memory, "memory", 512, "RAM in MiB")
	flag.StringVar(&proxy, "proxy", "", "Route all VM traffic through this SOCKS5 proxy")
	flag.BoolVar(&noStart, "no-start", false, "Create the VM but don't start it")
	flag.StringVar(&initPath, "init", "", "Path to vminit binary for graceful shutdown")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: krun-run --image <image-ref> [flags]\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if image == "" {
		flag.Usage()
		os.Exit(1)
	}
	if flag.NArg() != 0 {
		flag.Usage()
		os.Exit(1)
	}

	if name == "" {
		name = imageBasename(image)
	}

	ctx := context.Background()
	if err := run(ctx, image, name, apiURL, cpus, memory, proxy, initPath, noStart); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, image, name, apiURL string, cpus, memory int, proxy, initPath string, noStart bool) error {
	platform := &v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
	fmt.Printf("Inspecting image %s (%s/%s)...\n", image, platform.OS, platform.Architecture)

	img, err := crane.Pull(image, crane.WithContext(ctx), crane.WithPlatform(platform))
	if err != nil {
		return fmt.Errorf("pull image config: %w", err)
	}

	cfgFile, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("read image config: %w", err)
	}

	imgCfg := &cli.ImageConfig{
		Cmd:        cfgFile.Config.Cmd,
		Entrypoint: cfgFile.Config.Entrypoint,
		Env:        cfgFile.Config.Env,
		WorkingDir: cfgFile.Config.WorkingDir,
	}

	execPath := imgCfg.ExecPath()
	execArgs := imgCfg.ExecArgs()
	env := imgCfg.EnvMap()

	if initPath != "" {
		execArgs = append([]string{execPath}, execArgs...)
		execPath = "/usr/local/bin/vminit"
		env["KRUN_INIT_PID1"] = "1"
		env["KRUN_HOSTNAME"] = name
	}

	client := cli.NewClient(apiURL)

	req := cli.CreateMachineRequest{
		Name:      name,
		BaseImage: image,
		Config: cli.MachineConfig{
			VCPUs:     cpus,
			MemoryMiB: memory,
			ExecPath:  execPath,
			Args:      execArgs,
			Env:       env,
			Workdir:   imgCfg.WorkingDir,
		},
	}

	if proxy != "" {
		req.Network = cli.NetworkConfig{ProxyAddr: proxy}
	}

	machine, err := client.CreateMachine(req)
	if err != nil {
		return fmt.Errorf("create machine: %w", err)
	}
	fmt.Printf("Created machine: id=%s name=%s state=%s\n", machine.ID, machine.Name, machine.State)

	if !noStart {
		machine, err = client.StartMachine(machine.ID)
		if err != nil {
			return fmt.Errorf("start machine: %w", err)
		}
		fmt.Printf("Started machine: id=%s name=%s state=%s\n", machine.ID, machine.Name, machine.State)
	}

	return nil
}

// imageBasename returns the repository name from an image ref.
// "alpine:3.18" → "alpine", "docker.io/library/alpine:latest" → "alpine"
func imageBasename(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	ref, _, _ = strings.Cut(ref, ":")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return ref
}
