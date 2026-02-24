package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ExtractRootfs creates a temporary container, exports its filesystem to
// destDir, then removes the container.
func ExtractRootfs(ctx context.Context, rt *Runtime, imageTag, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", destDir, err)
	}

	cid, err := rt.Create(ctx, imageTag)
	if err != nil {
		return err
	}

	if err := rt.Export(ctx, cid, destDir); err != nil {
		_ = rt.Remove(ctx, cid)
		return err
	}

	return rt.Remove(ctx, cid)
}

// InjectInit copies the pre-built vminit binary into the rootfs so it can
// run as PID 1 and handle graceful shutdown via vsock.
func InjectInit(rootfsDir, initBinaryPath string) error {
	binDir := filepath.Join(rootfsDir, "usr", "local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", binDir, err)
	}

	data, err := os.ReadFile(initBinaryPath)
	if err != nil {
		return fmt.Errorf("read init binary: %w", err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "vminit"), data, 0o755); err != nil {
		return fmt.Errorf("write init binary: %w", err)
	}

	return nil
}

// FixResolvConf writes a resolv.conf pointing to the gvisor-tap-vsock gateway.
func FixResolvConf(rootfsDir string) error {
	etcDir := filepath.Join(rootfsDir, "etc")
	if err := os.MkdirAll(etcDir, 0o755); err != nil {
		return err
	}

	content := "options use-vc\nnameserver 192.168.127.1\n"
	return os.WriteFile(filepath.Join(etcDir, "resolv.conf"), []byte(content), 0o644)
}
