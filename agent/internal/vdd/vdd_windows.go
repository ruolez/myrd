//go:build windows && !win7

package vdd

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/ruolez/remotectl/proto"
)

//go:embed driver/MttVDD.inf driver/MttVDD.dll driver/mttvdd.cat
//go:embed driver/signpath-foundation.cer driver/globalsign-code-signing-root-r45.cer
var files embed.FS

const (
	hardwareID  = `Root\MttVDD`
	settingsKey = `SOFTWARE\MikeTheTech\VirtualDisplayDriver`
)

// The display device class.
var displayClass = windows.GUID{Data1: 0x4d36e968, Data2: 0xe325, Data3: 0x11ce,
	Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

var (
	newdev   = windows.NewLazySystemDLL("newdev.dll")
	setupapi = windows.NewLazySystemDLL("setupapi.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")

	procUpdateDriver        = newdev.NewProc("UpdateDriverForPlugAndPlayDevicesW")
	procUninstallOEMInf     = setupapi.NewProc("SetupUninstallOEMInfW")
	procEnumDisplayDevices  = user32.NewProc("EnumDisplayDevicesW")
	procEnumDisplaySettings = user32.NewProc("EnumDisplaySettingsExW")
	procChangeDisplay       = user32.NewProc("ChangeDisplaySettingsExW")
)

// settingsDir is where the driver reads vdd_settings.xml (VDDPATH). The
// driver host runs as LocalService, which cannot read the agent's data
// directory; this is the upstream installer's folder.
func settingsDir() string {
	drive := os.Getenv("SystemDrive")
	if drive == "" {
		drive = "C:"
	}
	return drive + `\VirtualDisplayDriver`
}

// Available reports whether this Windows can run the driver.
func Available() bool {
	v := windows.RtlGetVersion()
	return Supported(v.MajorVersion, v.BuildNumber, runtime.GOARCH)
}

// Info is the helper's view for device.info: what the desktop shows and
// whether the driver's device exists.
func Info() *proto.VirtualDisplayInfo {
	if !Available() {
		return &proto.VirtualDisplayInfo{Driver: proto.VDriverAbsent}
	}
	headless, active := Classify(monitorIDs())
	driver := proto.VDriverAbsent
	if installed() {
		driver = proto.VDriverInstalled
	}
	return &proto.VirtualDisplayInfo{Supported: true, Headless: headless, Driver: driver, Active: active}
}

// Install puts the driver on the device: the settings, trust for the
// driver's signer, a root-enumerated device and the driver on it.
func Install(log *slog.Logger) error {
	if !Available() {
		return errors.New("this Windows cannot run the virtual display (64-bit Windows 10 1803 or later)")
	}
	dir := settingsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"MttVDD.inf", "MttVDD.dll", "mttvdd.cat"} {
		b, err := files.ReadFile("driver/" + name)
		if err == nil {
			err = os.WriteFile(filepath.Join(dir, name), b, 0o644)
		}
		if err != nil {
			return fmt.Errorf("driver files: %w", err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "vdd_settings.xml"), SettingsXML(Modes), 0o644); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, settingsKey, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("settings key: %w", err)
	}
	err = k.SetStringValue("VDDPATH", dir)
	k.Close()
	if err != nil {
		return fmt.Errorf("settings key: %w", err)
	}
	// A driver install from a service never prompts: its publisher must be
	// trusted, and its root present on servers that never fetched it.
	for _, c := range []struct{ store, file string }{
		{"Root", "globalsign-code-signing-root-r45.cer"},
		{"TrustedPublisher", "signpath-foundation.cer"},
	} {
		if err := trust(c.store, c.file); err != nil {
			return fmt.Errorf("trusting %s: %w", c.file, err)
		}
	}
	if !installed() {
		if err := createDevice(); err != nil {
			return fmt.Errorf("device: %w", err)
		}
	}
	reboot, err := updateDriver(filepath.Join(dir, "MttVDD.inf"))
	if err != nil {
		return fmt.Errorf("driver: %w", err)
	}
	log.Info("vdd: installed", "reboot_needed", reboot)
	return nil
}

// Remove takes the device and its driver package off, and the settings.
func Remove(log *slog.Logger) error {
	set, err := windows.SetupDiGetClassDevsEx(&displayClass, "", 0, 0, 0, "")
	if err != nil {
		return err
	}
	defer set.Close()
	var infs []string
	var firstErr error
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) {
			break
		}
		if err != nil || !isVirtual(set, data) {
			continue
		}
		if inf := driverInf(set, data); inf != "" {
			infs = append(infs, inf)
		}
		if err := set.CallClassInstaller(windows.DIF_REMOVE, data); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("removing the device: %w", err)
		}
	}
	for _, inf := range infs {
		p, _ := windows.UTF16PtrFromString(inf)
		const suoiForceDelete = 1
		if r, _, e := procUninstallOEMInf.Call(uintptr(unsafe.Pointer(p)), suoiForceDelete, 0); r == 0 {
			log.Warn("vdd: driver package not removed", "inf", inf, "err", e)
		}
	}
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, settingsKey)
	_ = os.RemoveAll(settingsDir())
	if firstErr == nil {
		log.Info("vdd: removed", "packages", infs)
	}
	return firstErr
}

func trust(store, file string) error {
	der, err := files.ReadFile("driver/" + file)
	if err != nil {
		return err
	}
	name, _ := windows.UTF16PtrFromString(store)
	h, err := windows.CertOpenStore(windows.CERT_STORE_PROV_SYSTEM, 0, 0, windows.CERT_SYSTEM_STORE_LOCAL_MACHINE,
		uintptr(unsafe.Pointer(name)))
	if err != nil {
		return err
	}
	defer windows.CertCloseStore(h, 0)
	c, err := windows.CertCreateCertificateContext(windows.X509_ASN_ENCODING|windows.PKCS_7_ASN_ENCODING, &der[0], uint32(len(der)))
	if err != nil {
		return err
	}
	defer windows.CertFreeCertificateContext(c)
	return windows.CertAddCertificateContextToStore(h, c, windows.CERT_STORE_ADD_USE_EXISTING, nil)
}

func isVirtual(set windows.DevInfo, data *windows.DevInfoData) bool {
	v, err := set.DeviceRegistryProperty(data, windows.SPDRP_HARDWAREID)
	ids, _ := v.([]string)
	for _, id := range ids {
		if err == nil && strings.EqualFold(id, hardwareID) {
			return true
		}
	}
	return false
}

// driverInf is the published name (oemNN.inf) of a device's driver package.
func driverInf(set windows.DevInfo, data *windows.DevInfoData) string {
	v, err := set.DeviceRegistryProperty(data, windows.SPDRP_DRIVER)
	key, _ := v.(string)
	if err != nil || key == "" {
		return ""
	}
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Class\`+key, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	inf, _, _ := k.GetStringValue("InfPath")
	return inf
}

// installed reports whether the driver's device exists.
func installed() bool {
	set, err := windows.SetupDiGetClassDevsEx(&displayClass, "", 0, windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return false
	}
	defer set.Close()
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if err != nil {
			return false
		}
		if isVirtual(set, data) {
			return true
		}
	}
}

// createDevice registers a root-enumerated display device for the driver
// (what devcon / nefcon do before installing it).
func createDevice() error {
	set, err := windows.SetupDiCreateDeviceInfoListEx(&displayClass, 0, "")
	if err != nil {
		return err
	}
	defer set.Close()
	data, err := set.CreateDeviceInfo("Display", &displayClass, "", 0, windows.DICD_GENERATE_ID)
	if err != nil {
		return err
	}
	hw, _ := windows.UTF16FromString(hardwareID)
	hw = append(hw, 0) // REG_MULTI_SZ: one id, then an empty one
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&hw[0])), len(hw)*2)
	if err := set.SetDeviceRegistryProperty(data, windows.SPDRP_HARDWAREID, buf); err != nil {
		return err
	}
	return set.CallClassInstaller(windows.DIF_REGISTERDEVICE, data)
}

func updateDriver(inf string) (reboot bool, err error) {
	hw, _ := windows.UTF16PtrFromString(hardwareID)
	path, _ := windows.UTF16PtrFromString(inf)
	var rb int32
	const installFlagForce = 1
	r, _, e := procUpdateDriver.Call(0, uintptr(unsafe.Pointer(hw)), uintptr(unsafe.Pointer(path)), installFlagForce,
		uintptr(unsafe.Pointer(&rb)))
	if r == 0 {
		return false, e
	}
	return rb != 0, nil
}

// ---- the desktop (helper, console session) ----

type displayDevice struct {
	Cb           uint32
	DeviceName   [32]uint16
	DeviceString [128]uint16
	StateFlags   uint32
	DeviceID     [128]uint16
	DeviceKey    [128]uint16
}

const (
	ddAttached = 0x1 // DISPLAY_DEVICE_ATTACHED_TO_DESKTOP
	ddPrimary  = 0x4 // DISPLAY_DEVICE_PRIMARY_DEVICE
)

func enumDisplay(parent *uint16, i uint32) (displayDevice, bool) {
	d := displayDevice{}
	d.Cb = uint32(unsafe.Sizeof(d))
	r, _, _ := procEnumDisplayDevices.Call(uintptr(unsafe.Pointer(parent)), uintptr(i), uintptr(unsafe.Pointer(&d)), 0)
	return d, r != 0
}

type adapter struct {
	name     string
	flags    uint32
	monitors []string // MONITOR\<hwid>\... ids
}

func adapters() []adapter {
	var out []adapter
	for i := uint32(0); ; i++ {
		a, ok := enumDisplay(nil, i)
		if !ok {
			return out
		}
		ad := adapter{name: windows.UTF16ToString(a.DeviceName[:]), flags: a.StateFlags}
		for j := uint32(0); ; j++ {
			m, ok := enumDisplay(&a.DeviceName[0], j)
			if !ok {
				break
			}
			ad.monitors = append(ad.monitors, windows.UTF16ToString(m.DeviceID[:]))
		}
		out = append(out, ad)
	}
}

func monitorIDs() []string {
	var ids []string
	for _, a := range adapters() {
		ids = append(ids, a.monitors...)
	}
	return ids
}

// devMode is DEVMODEW (220 bytes), display fields.
type devMode struct {
	DeviceName                                                        [32]uint16
	SpecVersion, DriverVersion, Size, DriverExtra                     uint16
	Fields                                                            uint32
	PositionX, PositionY                                              int32
	DisplayOrientation, DisplayFixedOutput                            uint32
	Color, Duplex, YResolution, TTOption, Collate                     int16
	FormName                                                          [32]uint16
	LogPixels                                                         uint16
	BitsPerPel, PelsWidth, PelsHeight, DisplayFlags, DisplayFreq      uint32
	ICMMethod, ICMIntent, MediaType, DitherType, Reserved1, Reserved2 uint32
	PanningWidth, PanningHeight                                       uint32
}

var _ [220 - unsafe.Sizeof(devMode{})]byte
var _ [unsafe.Sizeof(devMode{}) - 220]byte

const (
	dmPosition    = 0x20
	dmPelsWidth   = 0x80000
	dmPelsHeight  = 0x100000
	cdsUpdateReg  = 0x1
	cdsSetPrimary = 0x10
	cdsNoReset    = 0x10000000
	enumCurrent   = 0xFFFFFFFF // ENUM_CURRENT_SETTINGS
)

func changeDisplay(name string, dm *devMode, flags uint32) error {
	var p *uint16
	if name != "" {
		p, _ = windows.UTF16PtrFromString(name)
	}
	r, _, _ := procChangeDisplay.Call(uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(dm)), 0, uintptr(flags), 0)
	if int32(r) != 0 { // DISP_CHANGE_SUCCESSFUL
		return fmt.Errorf("ChangeDisplaySettingsEx %s: %d", name, int32(r))
	}
	return nil
}

// PreferVirtual makes the virtual monitor the primary and only one on a
// headless device: Windows' generic monitor is a 1024×768 area nobody can
// see, and windows opened there would be lost. Saved in the registry, so
// it holds across sign-ins and reboots; removing the driver brings the
// generic monitor back (Windows keeps a layout per set of monitors).
func PreferVirtual() (bool, error) {
	all := adapters()
	var ids []string
	virtual := ""
	var others []string
	primary := false
	for _, a := range all {
		ids = append(ids, a.monitors...)
		_, isVirtual := Classify(a.monitors)
		switch {
		case isVirtual:
			virtual = a.name
			primary = a.flags&ddAttached != 0 && a.flags&ddPrimary != 0
			if a.flags&ddAttached == 0 {
				return false, nil // Windows has not put it on the desktop yet
			}
		case a.flags&ddAttached != 0:
			others = append(others, a.name)
		}
	}
	if headless, _ := Classify(ids); !headless || virtual == "" || (primary && len(others) == 0) {
		return false, nil
	}
	dm := devMode{}
	dm.Size = uint16(unsafe.Sizeof(dm))
	vn, _ := windows.UTF16PtrFromString(virtual)
	if r, _, _ := procEnumDisplaySettings.Call(uintptr(unsafe.Pointer(vn)), enumCurrent, uintptr(unsafe.Pointer(&dm)), 0); r == 0 {
		return false, fmt.Errorf("no current mode for %s", virtual)
	}
	dm.PositionX, dm.PositionY, dm.Fields = 0, 0, dmPosition
	if err := changeDisplay(virtual, &dm, cdsSetPrimary|cdsUpdateReg|cdsNoReset); err != nil {
		return false, err
	}
	for _, o := range others {
		off := devMode{Fields: dmPosition | dmPelsWidth | dmPelsHeight}
		off.Size = uint16(unsafe.Sizeof(off))
		if err := changeDisplay(o, &off, cdsUpdateReg|cdsNoReset); err != nil {
			return false, err
		}
	}
	return true, changeDisplay("", nil, 0)
}

// Keep runs in the helper: while the device is headless and the virtual
// monitor is on the desktop, it stays the only, primary monitor.
func Keep(ctx context.Context, log *slog.Logger) {
	if !Available() {
		return
	}
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if changed, err := PreferVirtual(); err != nil {
			log.Warn("vdd: virtual monitor not made primary", "err", err)
		} else if changed {
			log.Info("vdd: the virtual monitor is now the device's screen")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
