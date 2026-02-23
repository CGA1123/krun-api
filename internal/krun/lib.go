package krun

import (
	"fmt"
	"reflect"
	"strings"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Net feature flags from libkrun.h
const (
	NetFeatureCsum      = 1 << 0
	NetFeatureGuestCsum = 1 << 1
	NetFeatureGuestTSO4 = 1 << 7
	NetFeatureGuestTSO6 = 1 << 8
	NetFeatureGuestUFO  = 1 << 10
	NetFeatureHostTSO4  = 1 << 11
	NetFeatureHostTSO6  = 1 << 12
	NetFeatureHostUFO   = 1 << 14

	CompatNetFeatures = NetFeatureCsum | NetFeatureGuestCsum |
		NetFeatureGuestTSO4 | NetFeatureGuestUFO |
		NetFeatureHostTSO4 | NetFeatureHostUFO

	NetFlagVfkit = 1 << 0
)

// Log levels
const (
	LogLevelOff   uint32 = 0
	LogLevelError uint32 = 1
	LogLevelWarn  uint32 = 2
	LogLevelInfo  uint32 = 3
	LogLevelDebug uint32 = 4
	LogLevelTrace uint32 = 5
)

type libkrun struct {
	SetLogLevel        func(level uint32) int32                                                           `C:"krun_set_log_level"`
	CreateCtx          func() int32                                                                       `C:"krun_create_ctx"`
	FreeCtx            func(ctxID uint32) int32                                                           `C:"krun_free_ctx"`
	SetVMConfig        func(ctxID uint32, cpu uint8, ram uint32) int32                                    `C:"krun_set_vm_config"`
	SetRoot            func(ctxID uint32, path string) int32                                              `C:"krun_set_root"`
	SetExec            func(ctxID uint32, path string, args, env unsafe.Pointer) int32                    `C:"krun_set_exec"`
	SetWorkdir         func(ctxID uint32, path string) int32                                              `C:"krun_set_workdir"`
	SetConsoleOutput   func(ctxID uint32, path string) int32                                              `C:"krun_set_console_output"`
	AddNetUnixgram     func(ctxID uint32, path string, fd int, mac []uint8, features, flags uint32) int32 `C:"krun_add_net_unixgram"`
	AddVsockPort2      func(ctxID uint32, port uint32, path string, listen bool) int32                     `C:"krun_add_vsock_port2"`
	GetShutdownEventFD func(ctxID uint32) int32                                                           `C:"krun_get_shutdown_eventfd"`
	StartEnter         func(ctxID uint32) int32                                                           `C:"krun_start_enter"`
}

// Lib holds a loaded libkrun library and provides safe Go wrappers.
type Lib struct {
	k *libkrun
	f uintptr

	// passedDown prevents GC of C strings while the library holds pointers.
	passedDown [][]byte
}

// Open loads libkrun from the given dylib path.
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

// SetLogLevel sets the libkrun log verbosity.
func (l *Lib) SetLogLevel(level uint32) error {
	ret := l.k.SetLogLevel(level)
	if ret != 0 {
		return fmt.Errorf("krun_set_log_level: %d", ret)
	}
	return nil
}

// CreateCtx creates a new VM configuration context.
func (l *Lib) CreateCtx() (uint32, error) {
	ret := l.k.CreateCtx()
	if ret < 0 {
		return 0, fmt.Errorf("krun_create_ctx: %d", ret)
	}
	return uint32(ret), nil
}

// FreeCtx frees a VM configuration context, causing a running VM to exit.
func (l *Lib) FreeCtx(ctxID uint32) error {
	ret := l.k.FreeCtx(ctxID)
	if ret != 0 {
		return fmt.Errorf("krun_free_ctx: %d", ret)
	}
	return nil
}

// SetVMConfig sets CPU and memory for the VM.
func (l *Lib) SetVMConfig(ctxID uint32, vcpus uint8, memMiB uint32) error {
	ret := l.k.SetVMConfig(ctxID, vcpus, memMiB)
	if ret != 0 {
		return fmt.Errorf("krun_set_vm_config: %d", ret)
	}
	return nil
}

// SetRoot sets the rootfs path for the VM.
func (l *Lib) SetRoot(ctxID uint32, path string) error {
	ret := l.k.SetRoot(ctxID, path)
	if ret != 0 {
		return fmt.Errorf("krun_set_root: %d", ret)
	}
	return nil
}

// SetExec sets the executable, args, and env for the VM.
func (l *Lib) SetExec(ctxID uint32, execPath string, args, env []string) error {
	ret := l.k.SetExec(ctxID, execPath, l.cStringArray(args), l.cStringArray(env))
	if ret != 0 {
		return fmt.Errorf("krun_set_exec: %d", ret)
	}
	return nil
}

// SetWorkdir sets the working directory inside the VM.
func (l *Lib) SetWorkdir(ctxID uint32, path string) error {
	ret := l.k.SetWorkdir(ctxID, path)
	if ret != 0 {
		return fmt.Errorf("krun_set_workdir: %d", ret)
	}
	return nil
}

// SetConsoleOutput redirects the VM console to a file.
func (l *Lib) SetConsoleOutput(ctxID uint32, path string) error {
	ret := l.k.SetConsoleOutput(ctxID, path)
	if ret != 0 {
		return fmt.Errorf("krun_set_console_output: %d", ret)
	}
	return nil
}

// AddNetUnixgram adds a virtio-net device backed by a unixgram socket.
func (l *Lib) AddNetUnixgram(ctxID uint32, sockPath string, mac []uint8, features, flags uint32) error {
	ret := l.k.AddNetUnixgram(ctxID, sockPath, -1, mac, features, flags)
	if ret != 0 {
		return fmt.Errorf("krun_add_net_unixgram: %d", ret)
	}
	return nil
}

// AddVsockPort2 maps a vsock port to a Unix domain socket.
// When listen is true, the host creates the socket; the guest connects to it via vsock.
func (l *Lib) AddVsockPort2(ctxID uint32, port uint32, socketPath string, listen bool) error {
	ret := l.k.AddVsockPort2(ctxID, port, socketPath, listen)
	if ret != 0 {
		return fmt.Errorf("krun_add_vsock_port2: %d", ret)
	}
	return nil
}

// GetShutdownEventFD returns a file descriptor that can be written to
// in order to trigger a clean VM shutdown.
func (l *Lib) GetShutdownEventFD(ctxID uint32) (int, error) {
	ret := l.k.GetShutdownEventFD(ctxID)
	if ret < 0 {
		return 0, fmt.Errorf("krun_get_shutdown_eventfd: %d", ret)
	}
	return int(ret), nil
}

// StartEnter starts the VM. This call blocks until the VM exits.
func (l *Lib) StartEnter(ctxID uint32) error {
	ret := l.k.StartEnter(ctxID)
	if ret != 0 {
		return fmt.Errorf("krun_start_enter: %d", ret)
	}
	return nil
}

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
