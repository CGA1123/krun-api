// Package krun provides Go bindings for libkrun, a lightweight VMM (Virtual
// Machine Monitor) library that uses KVM (Linux) or HVF (macOS) to run
// microVMs with minimal overhead.
//
// Usage follows a context-based pattern: create a context with [Open] and
// [Lib.CreateCtx], configure the VM, then start it with [Lib.StartEnter].
// [Lib.StartEnter] blocks and only returns on error before VM startup.
//
// Note: only one instance of libkrun should be loaded per process. The
// library maintains global state (log level, etc.) shared across all contexts.
package krun

import (
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Log level constants for use with [Lib.SetLogLevel] and [Lib.InitLog].
const (
	LogLevelOff   uint32 = 0
	LogLevelError uint32 = 1
	LogLevelWarn  uint32 = 2
	LogLevelInfo  uint32 = 3
	LogLevelDebug uint32 = 4
	LogLevelTrace uint32 = 5
)

// Log style constants for use with [Lib.InitLog].
const (
	// LogStyleAuto enables terminal colour sequences when the target fd is a TTY.
	LogStyleAuto uint32 = 0
	// LogStyleAlways forces colour sequences regardless of whether the target is a TTY.
	LogStyleAlways uint32 = 1
	// LogStyleNever disables colour sequences unconditionally.
	LogStyleNever uint32 = 2
)

// Log option flag for use with [Lib.InitLog].
const (
	// LogOptionNoEnv prevents environment variables from overriding the logging
	// settings provided to [Lib.InitLog].
	LogOptionNoEnv uint32 = 1
)

// LogTargetDefault is passed as targetFd to [Lib.InitLog] to use the library's
// default log target (stderr).
const LogTargetDefault int = -1

// Net feature flags from virtio_net.h, used with [Lib.AddNetUnixgram],
// [Lib.AddNetUnixstream], and [Lib.AddNetTap].
const (
	NetFeatureCsum      = 1 << 0
	NetFeatureGuestCsum = 1 << 1
	NetFeatureGuestTSO4 = 1 << 7
	NetFeatureGuestTSO6 = 1 << 8
	NetFeatureGuestUFO  = 1 << 10
	NetFeatureHostTSO4  = 1 << 11
	NetFeatureHostTSO6  = 1 << 12
	NetFeatureHostUFO   = 1 << 14

	// CompatNetFeatures is the set of features enabled by the deprecated
	// krun_set_passt_fd / krun_set_gvproxy_path calls. Safe default for
	// most unixgram/unixstream backends.
	CompatNetFeatures = NetFeatureCsum | NetFeatureGuestCsum |
		NetFeatureGuestTSO4 | NetFeatureGuestUFO |
		NetFeatureHostTSO4 | NetFeatureHostUFO

	// NetFlagVfkit must be passed in flags when using [Lib.AddNetUnixgram]
	// with a path to a gvproxy listen-vfkit unixgram socket.
	NetFlagVfkit = 1 << 0
)

// Disk image format constants for use with [Lib.AddDisk2] and [Lib.AddDisk3].
const (
	DiskFormatRAW   uint32 = 0
	DiskFormatQCOW2 uint32 = 1
	// DiskFormatVMDK only supports FLAT/ZERO formats without delta links.
	DiskFormatVMDK uint32 = 2
)

// Disk sync mode constants for use with [Lib.AddDisk3].
const (
	// SyncNone ignores VIRTIO_BLK_F_FLUSH. May lead to data loss on crash.
	SyncNone uint32 = 0
	// SyncRelaxed honours flush requests but relaxes strict hardware syncing on
	// macOS (flushes OS buffers only, not the drive). Recommended default.
	// Equivalent to full sync on Linux.
	SyncRelaxed uint32 = 1
	// SyncFull honours VIRTIO_BLK_F_FLUSH with strict physical-disk flushing.
	SyncFull uint32 = 2
)

// TSI (Transparent Socket Impersonation) feature flags for use with [Lib.AddVsock].
const (
	TSIHijackInet uint32 = 1 << 0
	TSIHijackUnix uint32 = 1 << 1
)

// Feature constants for use with [Lib.HasFeature].
const (
	FeatureNet               uint64 = 0
	FeatureBLK               uint64 = 1
	FeatureGPU               uint64 = 2
	FeatureSnd               uint64 = 3
	FeatureInput             uint64 = 4
	FeatureEFI               uint64 = 5
	FeatureTEE               uint64 = 6
	FeatureAMDSEV            uint64 = 7
	FeatureIntelTDX          uint64 = 8
	FeatureAWSNitro          uint64 = 9
	FeatureVirglResourceMap2 uint64 = 10
)

// virglrenderer flag constants for use with [Lib.SetGPUOptions] and [Lib.SetGPUOptions2].
const (
	VirglUseEGL          uint32 = 1 << 0
	VirglThreadSync      uint32 = 1 << 1
	VirglUseGLX          uint32 = 1 << 2
	VirglUseSurfaceless  uint32 = 1 << 3
	VirglUseGLES         uint32 = 1 << 4
	VirglUseExternalBlob uint32 = 1 << 5
	VirglVenus           uint32 = 1 << 6
	VirglNoVirgl         uint32 = 1 << 7
	VirglUseAsyncFenceCB uint32 = 1 << 8
	VirglRenderServer    uint32 = 1 << 9
	VirglDRM             uint32 = 1 << 10
)

// MaxDisplays is the maximum number of display outputs supported (VIRTIO_GPU_MAX_SCANOUTS).
const MaxDisplays = 16

// Kernel image format constants for use with [Lib.SetKernel].
const (
	KernelFormatRAW       uint32 = 0
	KernelFormatELF       uint32 = 1
	KernelFormatPEGZ      uint32 = 2
	KernelFormatImageBZ2  uint32 = 3
	KernelFormatImageGZ   uint32 = 4
	KernelFormatImageZstd uint32 = 5
)

type libkrun struct {
	// Logging
	SetLogLevel func(level uint32) int32                                 `C:"krun_set_log_level"`
	InitLog     func(targetFd int32, level, style, options uint32) int32 `C:"krun_init_log"`

	// Context management
	CreateCtx func() int32             `C:"krun_create_ctx"`
	FreeCtx   func(ctxID uint32) int32 `C:"krun_free_ctx"`

	// VM configuration
	SetVMConfig func(ctxID uint32, cpu uint8, ram uint32) int32 `C:"krun_set_vm_config"`
	SetRoot     func(ctxID uint32, path string) int32           `C:"krun_set_root"`

	// Block storage
	AddDisk            func(ctxID uint32, blockID, diskPath string, readOnly bool) int32                                               `C:"krun_add_disk"`
	AddDisk2           func(ctxID uint32, blockID, diskPath string, diskFormat uint32, readOnly bool) int32                            `C:"krun_add_disk2"`
	AddDisk3           func(ctxID uint32, blockID, diskPath string, diskFormat uint32, readOnly, directIO bool, syncMode uint32) int32 `C:"krun_add_disk3"`
	SetRootDiskRemount func(ctxID uint32, device unsafe.Pointer, fstype unsafe.Pointer, options unsafe.Pointer) int32                  `C:"krun_set_root_disk_remount"`

	// Filesystems
	AddVirtioFS  func(ctxID uint32, tag, path string) int32                 `C:"krun_add_virtiofs"`
	AddVirtioFS2 func(ctxID uint32, tag, path string, shmSize uint64) int32 `C:"krun_add_virtiofs2"`

	// Networking
	AddNetUnixgram   func(ctxID uint32, path string, fd int, mac []uint8, features, flags uint32) int32 `C:"krun_add_net_unixgram"`
	AddNetUnixstream func(ctxID uint32, path string, fd int, mac []uint8, features, flags uint32) int32 `C:"krun_add_net_unixstream"`
	AddNetTap        func(ctxID uint32, tapName string, mac []uint8, features, flags uint32) int32      `C:"krun_add_net_tap"`
	SetNetMAC        func(ctxID uint32, mac []uint8) int32                                              `C:"krun_set_net_mac"`
	SetPortMap       func(ctxID uint32, portMap unsafe.Pointer) int32                                   `C:"krun_set_port_map"`

	// GPU
	SetGPUOptions  func(ctxID uint32, virglFlags uint32) int32                 `C:"krun_set_gpu_options"`
	SetGPUOptions2 func(ctxID uint32, virglFlags uint32, shmSize uint64) int32 `C:"krun_set_gpu_options2"`

	// Display
	AddDisplay             func(ctxID uint32, width, height uint32) int32                                        `C:"krun_add_display"`
	DisplaySetEDID         func(ctxID uint32, displayID uint32, edidBlob unsafe.Pointer, blobSize uintptr) int32 `C:"krun_display_set_edid"`
	DisplaySetDPI          func(ctxID uint32, displayID, dpi uint32) int32                                       `C:"krun_display_set_dpi"`
	DisplaySetPhysicalSize func(ctxID uint32, displayID uint32, widthMM, heightMM uint16) int32                  `C:"krun_display_set_physical_size"`
	DisplaySetRefreshRate  func(ctxID uint32, displayID, refreshRate uint32) int32                               `C:"krun_display_set_refresh_rate"`
	SetDisplayBackend      func(ctxID uint32, backend unsafe.Pointer, backendSize uintptr) int32                 `C:"krun_set_display_backend"`

	// Input
	AddInputDevice   func(ctxID uint32, cfgBackend unsafe.Pointer, cfgSize uintptr, evtBackend unsafe.Pointer, evtSize uintptr) int32 `C:"krun_add_input_device"`
	AddInputDeviceFD func(ctxID uint32, inputFd int) int32                                                                            `C:"krun_add_input_device_fd"`

	// Audio
	SetSndDevice func(ctxID uint32, enable bool) int32 `C:"krun_set_snd_device"`

	// Console
	SetConsoleOutput          func(ctxID uint32, path string) int32                                          `C:"krun_set_console_output"`
	DisableImplicitConsole    func(ctxID uint32) int32                                                       `C:"krun_disable_implicit_console"`
	SetKernelConsole          func(ctxID uint32, consoleID string) int32                                     `C:"krun_set_kernel_console"`
	AddVirtioConsoleDefault   func(ctxID uint32, inputFd, outputFd, errFd int) int32                         `C:"krun_add_virtio_console_default"`
	AddSerialConsoleDefault   func(ctxID uint32, inputFd, outputFd int) int32                                `C:"krun_add_serial_console_default"`
	AddVirtioConsoleMultiport func(ctxID uint32) int32                                                       `C:"krun_add_virtio_console_multiport"`
	AddConsolePortTTY         func(ctxID uint32, consoleID uint32, name string, ttyFd int) int32             `C:"krun_add_console_port_tty"`
	AddConsolePortInOut       func(ctxID uint32, consoleID uint32, name string, inputFd, outputFd int) int32 `C:"krun_add_console_port_inout"`

	// vsock
	AddVsock             func(ctxID uint32, tsiFeatures uint32) int32                    `C:"krun_add_vsock"`
	AddVsockPort         func(ctxID uint32, port uint32, path string) int32              `C:"krun_add_vsock_port"`
	AddVsockPort2        func(ctxID uint32, port uint32, path string, listen bool) int32 `C:"krun_add_vsock_port2"`
	DisableImplicitVsock func(ctxID uint32) int32                                        `C:"krun_disable_implicit_vsock"`

	// Execution
	SetExec    func(ctxID uint32, path string, args, env unsafe.Pointer) int32 `C:"krun_set_exec"`
	SetEnv     func(ctxID uint32, env unsafe.Pointer) int32                    `C:"krun_set_env"`
	SetWorkdir func(ctxID uint32, path string) int32                           `C:"krun_set_workdir"`

	// Firmware / kernel
	SetFirmware func(ctxID uint32, firmwarePath string) int32                                                                      `C:"krun_set_firmware"`
	SetKernel   func(ctxID uint32, kernelPath string, kernelFormat uint32, initramfs unsafe.Pointer, cmdline unsafe.Pointer) int32 `C:"krun_set_kernel"`

	// Resource limits and identity
	SetRlimits          func(ctxID uint32, rlimits unsafe.Pointer) int32    `C:"krun_set_rlimits"`
	SetSMBIOSOEMStrings func(ctxID uint32, oemStrings unsafe.Pointer) int32 `C:"krun_set_smbios_oem_strings"`
	Setuid              func(ctxID uint32, uid uint32) int32                `C:"krun_setuid"`
	Setgid              func(ctxID uint32, gid uint32) int32                `C:"krun_setgid"`

	// System / misc
	SplitIRQChip    func(ctxID uint32, enable bool) int32  `C:"krun_split_irqchip"`
	SetNestedVirt   func(ctxID uint32, enabled bool) int32 `C:"krun_set_nested_virt"`
	CheckNestedVirt func() int32                           `C:"krun_check_nested_virt"`
	HasFeature      func(feature uint64) int32             `C:"krun_has_feature"`
	GetMaxVCPUs     func() int32                           `C:"krun_get_max_vcpus"`

	// TEE (libkrun-sev only)
	SetTEEConfigFile func(ctxID uint32, filepath string) int32 `C:"krun_set_tee_config_file"`

	// Shutdown / startup
	GetShutdownEventFD func(ctxID uint32) int32 `C:"krun_get_shutdown_eventfd"`
	StartEnter         func(ctxID uint32) int32 `C:"krun_start_enter"`
}

// Lib holds a loaded libkrun library and provides safe Go wrappers around its
// C API. Obtain one via [Open].
type Lib struct {
	k *libkrun
	f uintptr

	// passedDown prevents GC of C strings while the library holds pointers.
	passedDown [][]byte
}

// Open loads libkrun from the given dylib path and registers all supported
// C functions. Returns an error if the library cannot be opened or any
// required symbol is missing.
func Open(path string) (_ *Lib, retErr error) {
	f, err := purego.Dlopen(path, purego.RTLD_NOW|purego.RTLD_GLOBAL)
	if err != nil {
		return nil, fmt.Errorf("dlopen %s: %w", path, err)
	}

	defer func() {
		if p := recover(); p != nil {
			if e, ok := p.(error); ok {
				retErr = e
			} else {
				retErr = fmt.Errorf("panic while loading libkrun: %v", p)
			}
		}
		if retErr != nil {
			purego.Dlclose(f)
		}
	}()

	var k libkrun
	ik := reflect.Indirect(reflect.ValueOf(&k))
	for i := 0; i < ik.NumField(); i++ {
		cName := ik.Type().Field(i).Tag.Get("C")
		if cName == "" {
			continue
		}
		fn := ik.Field(i).Addr().Interface()
		purego.RegisterLibFunc(fn, f, cName)
	}

	return &Lib{k: &k, f: f}, nil
}

// Logging

// SetLogLevel sets the log verbosity for the library.
//
// level must be one of the LogLevel* constants (Off=0 … Trace=5).
// This is a global setting that affects all contexts created from this Lib.
func (l *Lib) SetLogLevel(level uint32) error {
	if ret := l.k.SetLogLevel(level); ret != 0 {
		return fmt.Errorf("krun_set_log_level: %d", ret)
	}
	return nil
}

// InitLog initialises logging with fine-grained control over the target
// file descriptor, verbosity level, terminal colour style, and option flags.
//
// targetFd is the file descriptor to write log output to; pass
// [LogTargetDefault] (-1) for stderr. level is one of the LogLevel*
// constants. style is one of the LogStyle* constants. options is a bitmask
// of LogOption* flags (use 0 for defaults).
//
// Note: using a file descriptor pointing to a regular file may slow down the VM.
func (l *Lib) InitLog(targetFd int, level, style, options uint32) error {
	if ret := l.k.InitLog(int32(targetFd), level, style, options); ret != 0 {
		return fmt.Errorf("krun_init_log: %d", ret)
	}
	return nil
}

// Context management

// CreateCtx creates a new VM configuration context.
//
// The returned context ID is used as the first argument to all subsequent
// configuration calls. A context must be freed with [Lib.FreeCtx] when no
// longer needed (or after [Lib.StartEnter] returns).
func (l *Lib) CreateCtx() (uint32, error) {
	ret := l.k.CreateCtx()
	if ret < 0 {
		return 0, fmt.Errorf("krun_create_ctx: %d", ret)
	}
	return uint32(ret), nil
}

// FreeCtx releases a VM configuration context. If the VM is running, freeing
// its context causes the VM to exit.
func (l *Lib) FreeCtx(ctxID uint32) error {
	if ret := l.k.FreeCtx(ctxID); ret != 0 {
		return fmt.Errorf("krun_free_ctx: %d", ret)
	}
	return nil
}

// VM configuration

// SetVMConfig sets the basic hardware parameters for the microVM.
//
// vcpus is the number of virtual CPUs; memMiB is the amount of RAM in MiB.
// Both must be set before calling [Lib.StartEnter].
func (l *Lib) SetVMConfig(ctxID uint32, vcpus uint8, memMiB uint32) error {
	if ret := l.k.SetVMConfig(ctxID, vcpus, memMiB); ret != 0 {
		return fmt.Errorf("krun_set_vm_config: %d", ret)
	}
	return nil
}

// SetRoot sets the host directory to use as the root filesystem (/) inside
// the microVM. Not available in libkrun-SEV.
func (l *Lib) SetRoot(ctxID uint32, path string) error {
	if ret := l.k.SetRoot(ctxID, path); ret != 0 {
		return fmt.Errorf("krun_set_root: %d", ret)
	}
	return nil
}

// Block storage

// AddDisk adds a raw disk image as a block device inside the microVM.
//
// blockID is a label for the partition. diskPath is the path on the host.
// readOnly mounts the device read-only (required if the caller lacks write
// permission, e.g. images in /usr/share).
//
// Only raw images are accepted; use [Lib.AddDisk2] for other formats.
// Mutually exclusive with the deprecated krun_set_root_disk / krun_set_data_disk.
func (l *Lib) AddDisk(ctxID uint32, blockID, diskPath string, readOnly bool) error {
	if ret := l.k.AddDisk(ctxID, blockID, diskPath, readOnly); ret != 0 {
		return fmt.Errorf("krun_add_disk: %d", ret)
	}
	return nil
}

// AddDisk2 adds a disk image with an explicit format as a block device.
//
// diskFormat must be one of the DiskFormat* constants. See the security note
// in the libkrun header: non-raw images can reference other host files, so
// only open images from fully trusted sources in non-raw formats.
func (l *Lib) AddDisk2(ctxID uint32, blockID, diskPath string, diskFormat uint32, readOnly bool) error {
	if ret := l.k.AddDisk2(ctxID, blockID, diskPath, diskFormat, readOnly); ret != 0 {
		return fmt.Errorf("krun_add_disk2: %d", ret)
	}
	return nil
}

// AddDisk3 adds a disk image with explicit format, direct-IO, and sync-mode
// control.
//
// directIO bypasses host OS page cache. syncMode is one of the Sync*
// constants; [SyncRelaxed] is the recommended default. See the libkrun
// security note for non-raw formats in [Lib.AddDisk2].
func (l *Lib) AddDisk3(ctxID uint32, blockID, diskPath string, diskFormat uint32, readOnly, directIO bool, syncMode uint32) error {
	if ret := l.k.AddDisk3(ctxID, blockID, diskPath, diskFormat, readOnly, directIO, syncMode); ret != 0 {
		return fmt.Errorf("krun_add_disk3: %d", ret)
	}
	return nil
}

// SetRootDiskRemount configures a previously added block device as the root
// filesystem. libkrun boots from a built-in virtiofs root, executes init,
// then pivots to this device.
//
// device must refer to a block device added with [Lib.AddDisk] (e.g.
// "/dev/vda1"). fstype is the filesystem type (e.g. "ext4"); pass "" to let
// the kernel auto-detect. options is a comma-separated mount options string;
// pass "" for none.
func (l *Lib) SetRootDiskRemount(ctxID uint32, device, fstype, options string) error {
	if ret := l.k.SetRootDiskRemount(ctxID, l.cString(device), l.cString(fstype), l.cString(options)); ret != 0 {
		return fmt.Errorf("krun_set_root_disk_remount: %d", ret)
	}
	return nil
}

// Filesystems

// AddVirtioFS adds a virtio-fs device that exposes a host directory to the
// guest. tag is the filesystem identifier used inside the guest when mounting
// (mount -t virtiofs <tag> <mountpoint>). hostPath is the absolute path on
// the host.
//
// This can be called multiple times to add multiple independent virtio-fs
// devices.
func (l *Lib) AddVirtioFS(ctxID uint32, tag, hostPath string) error {
	if ret := l.k.AddVirtioFS(ctxID, tag, hostPath); ret != 0 {
		return fmt.Errorf("krun_add_virtiofs: %d", ret)
	}
	return nil
}

// AddVirtioFS2 is like [Lib.AddVirtioFS] but additionally configures the size
// of the DAX shared-memory window in bytes. A larger window can improve
// performance for workloads with large files.
func (l *Lib) AddVirtioFS2(ctxID uint32, tag, hostPath string, shmSize uint64) error {
	if ret := l.k.AddVirtioFS2(ctxID, tag, hostPath, shmSize); ret != 0 {
		return fmt.Errorf("krun_add_virtiofs2: %d", ret)
	}
	return nil
}

// Networking

// AddNetUnixgram adds a virtio-net device backed by a unixgram socket, such
// as gvproxy or vmnet-helper.
//
// sockPath is the path to the unixgram socket. mac is the 6-byte MAC address
// to assign to the guest NIC. features is a bitmask of NetFeature* constants;
// [CompatNetFeatures] is a safe default. flags should include [NetFlagVfkit]
// when connecting to gvproxy in vfkit mode via a path.
//
// Multiple virtio-net devices can be added; guests see them as eth0, eth1, …
// in the order they were added. If no network device is added, libkrun falls
// back to the TSI (Transparent Socket Impersonation) backend automatically.
// Must be called before [Lib.SetPortMap] if port mapping is needed.
func (l *Lib) AddNetUnixgram(ctxID uint32, sockPath string, mac []uint8, features, flags uint32) error {
	if ret := l.k.AddNetUnixgram(ctxID, sockPath, -1, mac, features, flags); ret != 0 {
		return fmt.Errorf("krun_add_net_unixgram: %d", ret)
	}
	return nil
}

// AddNetUnixstream adds a virtio-net device backed by a unixstream socket,
// suitable for use with passt or socket_vmnet.
//
// sockPath is the path to the listening unixstream socket. mac is the 6-byte
// MAC address for the guest NIC. features and flags follow the same
// conventions as [Lib.AddNetUnixgram]. Must be called before [Lib.SetPortMap].
func (l *Lib) AddNetUnixstream(ctxID uint32, sockPath string, mac []uint8, features, flags uint32) error {
	if ret := l.k.AddNetUnixstream(ctxID, sockPath, -1, mac, features, flags); ret != 0 {
		return fmt.Errorf("krun_add_net_unixstream: %d", ret)
	}
	return nil
}

// AddNetTap adds a virtio-net device backed by a host TAP interface.
//
// tapName is the name of an existing TAP device on the host. mac is the
// 6-byte MAC address for the guest NIC. features and flags follow the same
// conventions as [Lib.AddNetUnixgram]. Must be called before [Lib.SetPortMap].
func (l *Lib) AddNetTap(ctxID uint32, tapName string, mac []uint8, features, flags uint32) error {
	if ret := l.k.AddNetTap(ctxID, tapName, mac, features, flags); ret != 0 {
		return fmt.Errorf("krun_add_net_tap: %d", ret)
	}
	return nil
}

// SetNetMAC sets the MAC address for the virtio-net device when using the
// passt backend. Must be called after the network backend is configured.
func (l *Lib) SetNetMAC(ctxID uint32, mac []uint8) error {
	if ret := l.k.SetNetMAC(ctxID, mac); ret != 0 {
		return fmt.Errorf("krun_set_net_mac: %d", ret)
	}
	return nil
}

// SetPortMap configures a map of host-to-guest TCP port forwarding rules.
//
// Each entry in portMap must be in "host_port:guest_port" format (e.g.
// "8080:80"). Passing nil is different from passing an empty slice: nil
// instructs libkrun to expose all guest listening ports to the host, while an
// empty slice exposes nothing. Not supported with passt networking.
func (l *Lib) SetPortMap(ctxID uint32, portMap []string) error {
	if ret := l.k.SetPortMap(ctxID, l.cStringArray(portMap)); ret != 0 {
		return fmt.Errorf("krun_set_port_map: %d", ret)
	}
	return nil
}

// GPU

// SetGPUOptions enables and configures a virtio-gpu device.
//
// virglFlags is a bitmask of Virgl* constants to pass to virglrenderer.
func (l *Lib) SetGPUOptions(ctxID uint32, virglFlags uint32) error {
	if ret := l.k.SetGPUOptions(ctxID, virglFlags); ret != 0 {
		return fmt.Errorf("krun_set_gpu_options: %d", ret)
	}
	return nil
}

// SetGPUOptions2 is like [Lib.SetGPUOptions] but additionally configures the
// size of the SHM host window that acts as vRAM in the guest.
func (l *Lib) SetGPUOptions2(ctxID uint32, virglFlags uint32, shmSize uint64) error {
	if ret := l.k.SetGPUOptions2(ctxID, virglFlags, shmSize); ret != 0 {
		return fmt.Errorf("krun_set_gpu_options2: %d", ret)
	}
	return nil
}

// Display

// AddDisplay configures a display output for the VM, returning the display ID
// (0 to [MaxDisplays]-1). A display backend must also be set via
// [Lib.SetDisplayBackend] to receive rendered frames.
func (l *Lib) AddDisplay(ctxID uint32, width, height uint32) (uint32, error) {
	ret := l.k.AddDisplay(ctxID, width, height)
	if ret < 0 {
		return 0, fmt.Errorf("krun_add_display: %d", ret)
	}
	return uint32(ret), nil
}

// DisplaySetEDID configures a custom EDID blob for the given display. This
// replaces the auto-generated EDID and makes all other display parameters
// (DPI, physical size, etc.) ignored. libkrun does not validate that the EDID
// matches the width/height set in [Lib.AddDisplay].
func (l *Lib) DisplaySetEDID(ctxID uint32, displayID uint32, edid []byte) error {
	if ret := l.k.DisplaySetEDID(ctxID, displayID, unsafe.Pointer(unsafe.SliceData(edid)), uintptr(len(edid))); ret != 0 {
		return fmt.Errorf("krun_display_set_edid: %d", ret)
	}
	return nil
}

// DisplaySetDPI sets the dots-per-inch value reported to the guest for the
// given display.
func (l *Lib) DisplaySetDPI(ctxID uint32, displayID, dpi uint32) error {
	if ret := l.k.DisplaySetDPI(ctxID, displayID, dpi); ret != 0 {
		return fmt.Errorf("krun_display_set_dpi: %d", ret)
	}
	return nil
}

// DisplaySetPhysicalSize sets the physical display dimensions (in millimetres)
// reported to the guest.
func (l *Lib) DisplaySetPhysicalSize(ctxID uint32, displayID uint32, widthMM, heightMM uint16) error {
	if ret := l.k.DisplaySetPhysicalSize(ctxID, displayID, widthMM, heightMM); ret != 0 {
		return fmt.Errorf("krun_display_set_physical_size: %d", ret)
	}
	return nil
}

// DisplaySetRefreshRate sets the refresh rate (in Hz) reported to the guest
// for the given display.
func (l *Lib) DisplaySetRefreshRate(ctxID uint32, displayID, refreshRate uint32) error {
	if ret := l.k.DisplaySetRefreshRate(ctxID, displayID, refreshRate); ret != 0 {
		return fmt.Errorf("krun_display_set_refresh_rate: %d", ret)
	}
	return nil
}

// SetDisplayBackend configures a display backend to receive rendered frames.
// backend must point to a krun_display_backend struct (defined in
// libkrun_display.h); backendSize must be sizeof that struct.
//
// This is a low-level escape hatch for callers that have linked against
// libkrun_display and populated the struct directly.
func (l *Lib) SetDisplayBackend(ctxID uint32, backend unsafe.Pointer, backendSize uintptr) error {
	if ret := l.k.SetDisplayBackend(ctxID, backend, backendSize); ret != 0 {
		return fmt.Errorf("krun_set_display_backend: %d", ret)
	}
	return nil
}

// Input

// AddInputDevice adds a virtual input device with separate config and event
// provider objects. cfgBackend must point to a krun_input_config struct and
// evtBackend must point to a krun_input_event_provider struct (both defined in
// libkrun_input.h). The size arguments must be sizeof the respective structs.
func (l *Lib) AddInputDevice(ctxID uint32, cfgBackend unsafe.Pointer, cfgSize uintptr, evtBackend unsafe.Pointer, evtSize uintptr) error {
	if ret := l.k.AddInputDevice(ctxID, cfgBackend, cfgSize, evtBackend, evtSize); ret != 0 {
		return fmt.Errorf("krun_add_input_device: %d", ret)
	}
	return nil
}

// AddInputDeviceFD creates a passthrough input device from a host
// /dev/input/* file descriptor. The device configuration is queried from the
// host device via ioctls automatically.
func (l *Lib) AddInputDeviceFD(ctxID uint32, inputFd int) error {
	if ret := l.k.AddInputDeviceFD(ctxID, inputFd); ret != 0 {
		return fmt.Errorf("krun_add_input_device_fd: %d", ret)
	}
	return nil
}

// Audio

// SetSndDevice enables or disables the virtio-snd device in the VM.
func (l *Lib) SetSndDevice(ctxID uint32, enable bool) error {
	if ret := l.k.SetSndDevice(ctxID, enable); ret != 0 {
		return fmt.Errorf("krun_set_snd_device: %d", ret)
	}
	return nil
}

// Console

// SetConsoleOutput redirects the VM's implicit console output to a file on
// the host, ignoring stdin.
//
// This only applies to the implicitly created console device. It has no
// effect if the implicit console is disabled or consoles were created via
// [Lib.AddVirtioConsoleDefault] / [Lib.AddSerialConsoleDefault].
func (l *Lib) SetConsoleOutput(ctxID uint32, path string) error {
	if ret := l.k.SetConsoleOutput(ctxID, path); ret != 0 {
		return fmt.Errorf("krun_set_console_output: %d", ret)
	}
	return nil
}

// DisableImplicitConsole suppresses creation of the automatic console device.
// After calling this, any console devices must be added explicitly via
// [Lib.AddVirtioConsoleDefault] or [Lib.AddSerialConsoleDefault].
func (l *Lib) DisableImplicitConsole(ctxID uint32) error {
	if ret := l.k.DisableImplicitConsole(ctxID); ret != 0 {
		return fmt.Errorf("krun_disable_implicit_console: %d", ret)
	}
	return nil
}

// SetKernelConsole sets the console= value in the kernel command line
// (e.g. "hvc0" or "ttyS0").
func (l *Lib) SetKernelConsole(ctxID uint32, consoleID string) error {
	if ret := l.k.SetKernelConsole(ctxID, consoleID); ret != 0 {
		return fmt.Errorf("krun_set_kernel_console: %d", ret)
	}
	return nil
}

// AddVirtioConsoleDefault adds a virtio-console device backed by the provided
// file descriptors. When all three fds are TTYs, a single console port is
// created; otherwise additional non-console ports are created for each
// non-TTY fd and the guest init process redirects stdin/stdout/stderr
// accordingly.
//
// Devices appear as hvc0, hvc1, … in guest order. If the implicit console is
// not disabled, the first device added here will be hvc1.
func (l *Lib) AddVirtioConsoleDefault(ctxID uint32, inputFd, outputFd, errFd int) error {
	if ret := l.k.AddVirtioConsoleDefault(ctxID, inputFd, outputFd, errFd); ret != 0 {
		return fmt.Errorf("krun_add_virtio_console_default: %d", ret)
	}
	return nil
}

// AddSerialConsoleDefault adds a legacy serial (ttyS*) device backed by the
// provided file descriptors. Devices appear as ttyS0, ttyS1, … in guest
// order. If the implicit console is not disabled on aarch64/macOS, the first
// device added here will be ttyS1.
func (l *Lib) AddSerialConsoleDefault(ctxID uint32, inputFd, outputFd int) error {
	if ret := l.k.AddSerialConsoleDefault(ctxID, inputFd, outputFd); ret != 0 {
		return fmt.Errorf("krun_add_serial_console_default: %d", ret)
	}
	return nil
}

// AddVirtioConsoleMultiport creates a multi-port virtio-console device with
// explicitly managed ports. Returns the console ID used with
// [Lib.AddConsolePortTTY] and [Lib.AddConsolePortInOut].
//
// Unlike [Lib.AddVirtioConsoleDefault], this function performs no automatic
// detection; all ports must be configured manually. Port 0 of each device
// appears as hvcN in the guest.
func (l *Lib) AddVirtioConsoleMultiport(ctxID uint32) (uint32, error) {
	ret := l.k.AddVirtioConsoleMultiport(ctxID)
	if ret < 0 {
		return 0, fmt.Errorf("krun_add_virtio_console_multiport: %d", ret)
	}
	return uint32(ret), nil
}

// AddConsolePortTTY adds a TTY port to a multi-port virtio-console device.
// The port is flagged as VIRTIO_CONSOLE_CONSOLE_PORT, enabling terminal
// features such as window resize. ttyFd is used for both input and output.
// name identifies the port in the guest; pass "" for an unnamed port.
func (l *Lib) AddConsolePortTTY(ctxID uint32, consoleID uint32, name string, ttyFd int) error {
	if ret := l.k.AddConsolePortTTY(ctxID, consoleID, name, ttyFd); ret != 0 {
		return fmt.Errorf("krun_add_console_port_tty: %d", ret)
	}
	return nil
}

// AddConsolePortInOut adds a generic bidirectional I/O port to a multi-port
// virtio-console device. This port does not support terminal features such as
// window resize. name identifies the port in the guest; pass "" for unnamed.
func (l *Lib) AddConsolePortInOut(ctxID uint32, consoleID uint32, name string, inputFd, outputFd int) error {
	if ret := l.k.AddConsolePortInOut(ctxID, consoleID, name, inputFd, outputFd); ret != 0 {
		return fmt.Errorf("krun_add_console_port_inout: %d", ret)
	}
	return nil
}

// vsock

// AddVsock adds a vsock device with explicit TSI (Transparent Socket
// Impersonation) feature flags. tsiFeatures is a bitmask of TSI* constants;
// pass 0 to add a vsock device with no hijacking.
//
// By default libkrun creates an implicit vsock device; call
// [Lib.DisableImplicitVsock] first before using this function. Only one vsock
// device is supported.
func (l *Lib) AddVsock(ctxID uint32, tsiFeatures uint32) error {
	if ret := l.k.AddVsock(ctxID, tsiFeatures); ret != 0 {
		return fmt.Errorf("krun_add_vsock: %d", ret)
	}
	return nil
}

// AddVsockPort maps a vsock port to a Unix domain socket on the host.
// The guest connects to port and libkrun forwards traffic to socketPath.
//
// Prefer [Lib.AddVsockPort2] which additionally controls the connection
// direction.
func (l *Lib) AddVsockPort(ctxID uint32, port uint32, socketPath string) error {
	if ret := l.k.AddVsockPort(ctxID, port, socketPath); ret != 0 {
		return fmt.Errorf("krun_add_vsock_port: %d", ret)
	}
	return nil
}

// AddVsockPort2 maps a vsock port to a Unix domain socket on the host.
//
// port is the vsock port number. socketPath is the path of the Unix socket.
// When listen is true the host creates and listens on the socket; the guest
// connects to it via vsock. When listen is false the guest listens on the
// vsock port and the host connects to the Unix socket.
func (l *Lib) AddVsockPort2(ctxID uint32, port uint32, socketPath string, listen bool) error {
	if ret := l.k.AddVsockPort2(ctxID, port, socketPath, listen); ret != 0 {
		return fmt.Errorf("krun_add_vsock_port2: %d", ret)
	}
	return nil
}

// DisableImplicitVsock disables the automatically created vsock device.
// Must be called before [Lib.AddVsock] to take explicit control of the vsock
// device configuration.
func (l *Lib) DisableImplicitVsock(ctxID uint32) error {
	if ret := l.k.DisableImplicitVsock(ctxID); ret != 0 {
		return fmt.Errorf("krun_disable_implicit_vsock: %d", ret)
	}
	return nil
}

// Execution

// SetExec sets the executable to run inside the microVM, along with its
// arguments and environment variables.
//
// execPath is relative to the root set via [Lib.SetRoot]. If env is nil,
// libkrun auto-generates the environment from the current process environment.
func (l *Lib) SetExec(ctxID uint32, execPath string, args, env []string) error {
	if ret := l.k.SetExec(ctxID, execPath, l.cStringArray(args), l.cStringArray(env)); ret != 0 {
		return fmt.Errorf("krun_set_exec: %d", ret)
	}
	return nil
}

// SetEnv sets environment variables for the executable without changing the
// executable path or arguments. If env is nil, libkrun auto-generates the
// environment from the current process environment.
func (l *Lib) SetEnv(ctxID uint32, env []string) error {
	if ret := l.k.SetEnv(ctxID, l.cStringArray(env)); ret != 0 {
		return fmt.Errorf("krun_set_env: %d", ret)
	}
	return nil
}

// SetWorkdir sets the working directory for the executable inside the microVM.
// The path is relative to the root configured with [Lib.SetRoot].
func (l *Lib) SetWorkdir(ctxID uint32, path string) error {
	if ret := l.k.SetWorkdir(ctxID, path); ret != 0 {
		return fmt.Errorf("krun_set_workdir: %d", ret)
	}
	return nil
}

// Firmware / kernel

// SetFirmware sets the path (relative to the host filesystem) to the firmware
// image to be loaded into the microVM (e.g. an EFI firmware blob).
func (l *Lib) SetFirmware(ctxID uint32, firmwarePath string) error {
	if ret := l.k.SetFirmware(ctxID, firmwarePath); ret != 0 {
		return fmt.Errorf("krun_set_firmware: %d", ret)
	}
	return nil
}

// SetKernel sets the kernel image to load into the microVM.
//
// kernelPath and initramfsPath are relative to the host filesystem.
// kernelFormat must be one of the KernelFormat* constants. initramfsPath and
// cmdline may be empty strings to omit them.
func (l *Lib) SetKernel(ctxID uint32, kernelPath string, kernelFormat uint32, initramfsPath, cmdline string) error {
	if ret := l.k.SetKernel(ctxID, kernelPath, kernelFormat, l.cString(initramfsPath), l.cString(cmdline)); ret != 0 {
		return fmt.Errorf("krun_set_kernel: %d", ret)
	}
	return nil
}

// Resource limits and identity

// SetRlimits configures resource limits applied inside the guest before the
// workload binary is executed. Each entry must be in
// "RESOURCE=RLIM_CUR:RLIM_MAX" format (e.g. "RLIMIT_NOFILE=1024:4096").
func (l *Lib) SetRlimits(ctxID uint32, rlimits []string) error {
	if ret := l.k.SetRlimits(ctxID, l.cStringArray(rlimits)); ret != 0 {
		return fmt.Errorf("krun_set_rlimits: %d", ret)
	}
	return nil
}

// SetSMBIOSOEMStrings sets SMBIOS OEM string entries for the microVM.
func (l *Lib) SetSMBIOSOEMStrings(ctxID uint32, oemStrings []string) error {
	if ret := l.k.SetSMBIOSOEMStrings(ctxID, l.cStringArray(oemStrings)); ret != 0 {
		return fmt.Errorf("krun_set_smbios_oem_strings: %d", ret)
	}
	return nil
}

// Setuid configures the UID set right before the microVM is started. Useful
// when elevated privileges are needed to open host block devices but the VM
// should not run as root throughout.
func (l *Lib) Setuid(ctxID uint32, uid uint32) error {
	if ret := l.k.Setuid(ctxID, uid); ret != 0 {
		return fmt.Errorf("krun_setuid: %d", ret)
	}
	return nil
}

// Setgid configures the GID set right before the microVM is started.
// See [Lib.Setuid] for the motivating use case.
func (l *Lib) Setgid(ctxID uint32, gid uint32) error {
	if ret := l.k.Setgid(ctxID, gid); ret != 0 {
		return fmt.Errorf("krun_setgid: %d", ret)
	}
	return nil
}

// System / misc

// SplitIRQChip enables or disables split IRQCHIP mode, distributing interrupt
// controller responsibilities between the host and the guest.
func (l *Lib) SplitIRQChip(ctxID uint32, enable bool) error {
	if ret := l.k.SplitIRQChip(ctxID, enable); ret != 0 {
		return fmt.Errorf("krun_split_irqchip: %d", ret)
	}
	return nil
}

// SetNestedVirt enables or disables nested virtualisation for the microVM.
// Supported on macOS only. A successful return does not guarantee that nested
// virt is available on the system; call [Lib.CheckNestedVirt] to verify.
func (l *Lib) SetNestedVirt(ctxID uint32, enabled bool) error {
	if ret := l.k.SetNestedVirt(ctxID, enabled); ret != 0 {
		return fmt.Errorf("krun_set_nested_virt: %d", ret)
	}
	return nil
}

// CheckNestedVirt reports whether nested virtualisation is supported on the
// current system. Supported on macOS only.
func (l *Lib) CheckNestedVirt() (bool, error) {
	ret := l.k.CheckNestedVirt()
	if ret < 0 {
		return false, fmt.Errorf("krun_check_nested_virt: %d", ret)
	}
	return ret == 1, nil
}

// HasFeature reports whether a specific feature was enabled at build time.
// feature must be one of the Feature* constants. When linking against an older
// libkrun, unknown feature constants return an error.
func (l *Lib) HasFeature(feature uint64) (bool, error) {
	ret := l.k.HasFeature(feature)
	if ret < 0 {
		return false, fmt.Errorf("krun_has_feature: %d", ret)
	}
	return ret == 1, nil
}

// GetMaxVCPUs returns the maximum number of vCPUs supported by the underlying
// hypervisor on this system.
func (l *Lib) GetMaxVCPUs() (int, error) {
	ret := l.k.GetMaxVCPUs()
	if ret < 0 {
		return 0, fmt.Errorf("krun_get_max_vcpus: %d", ret)
	}
	return int(ret), nil
}

// TEE (libkrun-sev only)

// SetTEEConfigFile sets the path to the TEE (Trusted Execution Environment)
// configuration file. Only available in libkrun-sev builds.
func (l *Lib) SetTEEConfigFile(ctxID uint32, filepath string) error {
	if ret := l.k.SetTEEConfigFile(ctxID, filepath); ret != 0 {
		return fmt.Errorf("krun_set_tee_config_file: %d", ret)
	}
	return nil
}

// Shutdown / startup

// GetShutdownEventFD returns an eventfd file descriptor that can be written
// to in order to trigger an orderly guest shutdown. Must be called before
// [Lib.StartEnter].
//
// Only available in libkrun-efi builds.
func (l *Lib) GetShutdownEventFD(ctxID uint32) (int, error) {
	ret := l.k.GetShutdownEventFD(ctxID)
	if ret < 0 {
		return 0, fmt.Errorf("krun_get_shutdown_eventfd: %d", ret)
	}
	return int(ret), nil
}

// StartEnter starts the microVM and enters it. This call blocks until the VM
// exits; it only returns on error during startup. Exit codes from the guest
// init process are: 125 (init setup failure), 126 (exec not possible),
// 127 (executable not found).
//
// The calling goroutine (and its OS thread via runtime.LockOSThread) is taken
// over for the lifetime of the VM. This should typically be called from a
// dedicated process or goroutine with a locked OS thread.
func (l *Lib) StartEnter(ctxID uint32) error {
	if ret := l.k.StartEnter(ctxID); ret != 0 {
		return fmt.Errorf("krun_start_enter: %d", ret)
	}
	return nil
}

// C string helpers

func (l *Lib) cString(a string) unsafe.Pointer {
	if a == "" {
		return nil
	}
	if !strings.HasSuffix(a, "\000") {
		a += "\000"
	}
	b := []byte(a)
	l.passedDown = append(l.passedDown, b)
	return unsafe.Pointer(unsafe.SliceData(b))
}

func (l *Lib) cStringArray(a []string) unsafe.Pointer {
	if len(a) == 0 {
		return nil
	}
	o := make([]unsafe.Pointer, len(a)+1)
	o[len(a)] = nil
	for i := range a {
		o[i] = l.cString(a[i])
	}
	return unsafe.Pointer(unsafe.SliceData(o))
}
