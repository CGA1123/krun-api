package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CGA1123/krun-api/internal/cli"
)

type allowListFlag []string

func (f *allowListFlag) String() string { return strings.Join(*f, ",") }
func (f *allowListFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func main() {
	var (
		dockerfile string
		name       string
		apiURL     string
		cpus       int
		memory     int
		rootfsDir  string
		proxy      string
		allow      allowListFlag
		noStart    bool
		initPath   string
	)

	flag.StringVar(&dockerfile, "f", "", "Dockerfile path (default: <context>/Dockerfile)")
	flag.StringVar(&dockerfile, "file", "", "Dockerfile path (default: <context>/Dockerfile)")
	flag.StringVar(&name, "n", "", "VM name (default: directory name of build context)")
	flag.StringVar(&name, "name", "", "VM name (default: directory name of build context)")
	flag.StringVar(&apiURL, "api", "http://localhost:8080", "krun-api server URL")
	flag.IntVar(&cpus, "cpus", 2, "Number of vCPUs")
	flag.IntVar(&memory, "memory", 512, "RAM in MiB")
	flag.StringVar(&rootfsDir, "rootfs-dir", "", "Where to extract rootfs")
	flag.StringVar(&proxy, "proxy", "", "Route all VM traffic through this proxy")
	flag.Var(&allow, "allow", "Allow-list rules (repeatable)")
	flag.BoolVar(&noStart, "no-start", false, "Create the VM but don't start it")
	flag.StringVar(&initPath, "init", "", "Path to vminit binary for graceful shutdown")

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: krun-run [flags] <build-context-path>\n\nFlags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() != 1 {
		flag.Usage()
		os.Exit(1)
	}

	contextDir := flag.Arg(0)

	// Resolve defaults.
	if name == "" {
		name = filepath.Base(contextDir)
	}
	if dockerfile == "" {
		dockerfile = filepath.Join(contextDir, "Dockerfile")
	}
	if rootfsDir == "" {
		rootfsDir = filepath.Join("/tmp/krun-api", "rootfs-"+name)
	}

	imageTag := "krun-" + name
	ctx := context.Background()

	if err := run(ctx, runOpts{
		contextDir: contextDir,
		dockerfile: dockerfile,
		name:       name,
		imageTag:   imageTag,
		apiURL:     apiURL,
		cpus:       cpus,
		memory:     memory,
		rootfsDir:  rootfsDir,
		proxy:      proxy,
		allow:      allow,
		noStart:    noStart,
		initPath:   initPath,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type runOpts struct {
	contextDir string
	dockerfile string
	name       string
	imageTag   string
	apiURL     string
	cpus       int
	memory     int
	rootfsDir  string
	proxy      string
	allow      []string
	noStart    bool
	initPath   string
}

func run(ctx context.Context, opts runOpts) error {
	// 1. Detect container runtime.
	rt, err := cli.DetectRuntime()
	if err != nil {
		return err
	}
	fmt.Printf("Using runtime: %s\n", rt.Name())

	// 2. Build image.
	fmt.Printf("Building image %s...\n", opts.imageTag)
	if err := rt.Build(ctx, opts.contextDir, opts.dockerfile, opts.imageTag); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}

	// 3. Inspect image.
	imgCfg, err := rt.InspectImage(ctx, opts.imageTag)
	if err != nil {
		return err
	}

	// 4-6. Extract rootfs.
	fmt.Printf("Extracting rootfs to %s...\n", opts.rootfsDir)
	if err := cli.ExtractRootfs(ctx, rt, opts.imageTag, opts.rootfsDir); err != nil {
		return fmt.Errorf("extract rootfs: %w", err)
	}

	// 7. Fix resolv.conf.
	if err := cli.FixResolvConf(opts.rootfsDir); err != nil {
		return fmt.Errorf("fix resolv.conf: %w", err)
	}

	// 7b. Inject init if provided.
	execPath := imgCfg.ExecPath()
	execArgs := imgCfg.ExecArgs()
	if opts.initPath != "" {
		fmt.Println("Injecting init...")
		if err := cli.InjectInit(opts.rootfsDir, opts.initPath); err != nil {
			return fmt.Errorf("inject init: %w", err)
		}
		// vminit runs as PID 1 and spawns the user command as a child.
		execArgs = append([]string{execPath}, execArgs...)
		execPath = "/usr/local/bin/vminit"
	}

	// 8. Create VM via API.
	client := cli.NewClient(opts.apiURL)

	req := cli.CreateMachineRequest{
		Name: opts.name,
		Config: cli.MachineConfig{
			VCPUs:      opts.cpus,
			MemoryMiB:  opts.memory,
			RootfsPath: opts.rootfsDir,
			ExecPath:   execPath,
			Args:       execArgs,
			Env:        imgCfg.EnvMap(),
			Workdir:    imgCfg.WorkingDir,
		},
	}

	if opts.proxy != "" || len(opts.allow) > 0 {
		req.Network = cli.NetworkConfig{
			ProxyAddr: opts.proxy,
			AllowList: opts.allow,
		}
	}

	machine, err := client.CreateMachine(req)
	if err != nil {
		return fmt.Errorf("create machine: %w", err)
	}
	fmt.Printf("Created machine: id=%s name=%s state=%s\n", machine.ID, machine.Name, machine.State)

	// 9. Start unless --no-start.
	if !opts.noStart {
		machine, err = client.StartMachine(machine.ID)
		if err != nil {
			return fmt.Errorf("start machine: %w", err)
		}
		fmt.Printf("Started machine: id=%s name=%s state=%s\n", machine.ID, machine.Name, machine.State)
	}

	return nil
}
