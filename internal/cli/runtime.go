package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// Runtime wraps a container runtime (docker or podman).
type Runtime struct {
	cmd string
}

// DetectRuntime probes for docker, then podman, returning the first one found.
func DetectRuntime() (*Runtime, error) {
	for _, name := range []string{"docker", "podman"} {
		if err := exec.Command(name, "info").Run(); err == nil {
			return &Runtime{cmd: name}, nil
		}
	}
	return nil, fmt.Errorf("neither docker nor podman found")
}

// Build builds a container image from the given context directory.
func (r *Runtime) Build(ctx context.Context, contextDir, dockerfile, tag string) error {
	args := []string{"build", "-t", tag, "-f", dockerfile, contextDir}
	cmd := exec.CommandContext(ctx, r.cmd, args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

// InspectImage returns the parsed image configuration.
func (r *Runtime) InspectImage(ctx context.Context, tag string) (*ImageConfig, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.cmd, "inspect", tag)
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("inspect %s: %w", tag, err)
	}

	var results []struct {
		Config struct {
			Cmd        []string `json:"Cmd"`
			Entrypoint []string `json:"Entrypoint"`
			Env        []string `json:"Env"`
			WorkingDir string   `json:"WorkingDir"`
		} `json:"Config"`
	}
	if err := json.Unmarshal(buf.Bytes(), &results); err != nil {
		return nil, fmt.Errorf("parsing inspect output: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("no inspect results for %s", tag)
	}

	cfg := results[0].Config
	return &ImageConfig{
		Cmd:        cfg.Cmd,
		Entrypoint: cfg.Entrypoint,
		Env:        cfg.Env,
		WorkingDir: cfg.WorkingDir,
	}, nil
}

// Create creates a container from the given image tag and returns the container ID.
func (r *Runtime) Create(ctx context.Context, tag string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, r.cmd, "create", tag)
	cmd.Stdout = &buf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("create container from %s: %w", tag, err)
	}
	return string(bytes.TrimSpace(buf.Bytes())), nil
}

// Export exports the container filesystem as a tar stream into destDir.
func (r *Runtime) Export(ctx context.Context, cid, destDir string) error {
	exportCmd := exec.CommandContext(ctx, r.cmd, "export", cid)
	tarCmd := exec.CommandContext(ctx, "tar", "xf", "-", "-C", destDir)

	pipe, err := exportCmd.StdoutPipe()
	if err != nil {
		return err
	}
	tarCmd.Stdin = pipe

	if err := exportCmd.Start(); err != nil {
		return fmt.Errorf("export start: %w", err)
	}
	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("tar start: %w", err)
	}

	if err := exportCmd.Wait(); err != nil {
		return fmt.Errorf("export: %w", err)
	}
	if err := tarCmd.Wait(); err != nil {
		return fmt.Errorf("tar extract: %w", err)
	}
	return nil
}

// Remove removes a container by ID.
func (r *Runtime) Remove(ctx context.Context, cid string) error {
	return exec.CommandContext(ctx, r.cmd, "rm", cid).Run()
}

// Name returns the runtime command name.
func (r *Runtime) Name() string {
	return r.cmd
}
