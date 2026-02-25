package api

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sync/singleflight"

	"github.com/google/go-containerregistry/pkg/crane"
	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CGA1123/krun-api/internal/cli"
)

// ImageCache pulls and caches container images as extracted rootfs directories.
// Each image is identified by its digest and stored at:
//
//	<cacheDir>/<image-name>/<short-sha>/
//
// The cached directory is never modified; per-VM clones are created by the
// machine manager using APFS copy-on-write.
type ImageCache struct {
	cacheDir   string
	vminitPath string
	group      singleflight.Group
}

// NewImageCache creates an ImageCache rooted at cacheDir.
func NewImageCache(cacheDir, vminitPath string) (*ImageCache, error) {
	if err := os.MkdirAll(cacheDir, 0700); err != nil {
		return nil, fmt.Errorf("create image cache dir: %w", err)
	}
	return &ImageCache{cacheDir: cacheDir, vminitPath: vminitPath}, nil
}

// vmPlatform returns the platform to pull for: linux with the host
// architecture. libkrun always creates VMs with the same CPU architecture as
// the host process, so runtime.GOARCH is the correct target.
func vmPlatform() *v1.Platform {
	return &v1.Platform{OS: "linux", Architecture: runtime.GOARCH}
}

// Ensure returns the cached rootfs directory for imageRef, pulling and
// extracting it if not already present.
func (ic *ImageCache) Ensure(ctx context.Context, imageRef string) (string, error) {
	platform := vmPlatform()
	digest, err := crane.Digest(imageRef, crane.WithContext(ctx), crane.WithPlatform(platform))
	if err != nil {
		return "", fmt.Errorf("resolve digest: %w", err)
	}

	dir := filepath.Join(ic.cacheDir, imageName(imageRef), shortSHA(digest))
	if _, err := os.Stat(dir); err == nil {
		return dir, nil // cache hit
	}

	_, err, _ = ic.group.Do(digest, func() (interface{}, error) {
		// Re-check after acquiring the group lock — another goroutine may
		// have already extracted while we were waiting.
		if _, err := os.Stat(dir); err == nil {
			return nil, nil
		}

		img, err := crane.Pull(imageRef, crane.WithContext(ctx), crane.WithPlatform(platform))
		if err != nil {
			return nil, fmt.Errorf("pull image: %w", err)
		}

		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("create cache dir: %w", err)
		}

		pr, pw := io.Pipe()
		go func() { pw.CloseWithError(crane.Export(img, pw)) }()

		if err := extractTar(pr, dir); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("extract image: %w", err)
		}

		if err := cli.InjectInit(dir, ic.vminitPath); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("inject init: %w", err)
		}

		if err := cli.FixResolvConf(dir); err != nil {
			os.RemoveAll(dir)
			return nil, fmt.Errorf("fix resolv.conf: %w", err)
		}

		return nil, nil
	})
	if err != nil {
		return "", err
	}
	return dir, nil
}

// extractTar extracts a tar stream into destDir.
func extractTar(r io.Reader, destDir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		target := filepath.Join(destDir, filepath.Clean("/"+hdr.Name))
		// Guard against path traversal.
		if !strings.HasPrefix(target+string(os.PathSeparator), destDir+string(os.PathSeparator)) {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(hdr.Mode))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, cerr := io.Copy(f, tr)
			f.Close()
			if cerr != nil {
				return cerr
			}
		case tar.TypeSymlink:
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Symlink(hdr.Linkname, target)
		case tar.TypeLink:
			linkTarget := filepath.Join(destDir, filepath.Clean("/"+hdr.Linkname))
			os.MkdirAll(filepath.Dir(target), 0755)
			os.Link(linkTarget, target)
		}
	}
}

// imageName extracts the repository basename from an image ref.
// "alpine:3.18" → "alpine", "docker.io/library/alpine:latest" → "alpine"
func imageName(ref string) string {
	ref, _, _ = strings.Cut(ref, "@")
	ref, _, _ = strings.Cut(ref, ":")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return ref
}

// shortSHA strips "sha256:" and returns the first 12 hex chars.
func shortSHA(digest string) string {
	s := strings.TrimPrefix(digest, "sha256:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
