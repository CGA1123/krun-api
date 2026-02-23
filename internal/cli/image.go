package cli

import "strings"

// ImageConfig holds the parsed image metadata from docker/podman inspect.
type ImageConfig struct {
	Cmd        []string
	Entrypoint []string
	Env        []string // "KEY=VALUE" pairs
	WorkingDir string
}

// ExecPath returns the executable path following Docker convention:
// Entrypoint[0] if set, else Cmd[0], else "/bin/sh".
func (ic *ImageConfig) ExecPath() string {
	if len(ic.Entrypoint) > 0 {
		return ic.Entrypoint[0]
	}
	if len(ic.Cmd) > 0 {
		return ic.Cmd[0]
	}
	return "/bin/sh"
}

// ExecArgs returns the arguments following Docker convention:
// Entrypoint[1:] + Cmd if entrypoint set, else Cmd[1:], else ["-l"].
func (ic *ImageConfig) ExecArgs() []string {
	if len(ic.Entrypoint) > 0 {
		args := append([]string{}, ic.Entrypoint[1:]...)
		args = append(args, ic.Cmd...)
		return args
	}
	if len(ic.Cmd) > 1 {
		return ic.Cmd[1:]
	}
	if len(ic.Cmd) == 0 {
		return []string{"-l"}
	}
	return nil
}

// EnvMap converts the "KEY=VALUE" slice to a map.
func (ic *ImageConfig) EnvMap() map[string]string {
	m := make(map[string]string, len(ic.Env))
	for _, e := range ic.Env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}
