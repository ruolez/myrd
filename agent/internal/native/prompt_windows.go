//go:build cgo

package native

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ruolez/remotectl/agent/internal/platform"
)

// unlockPrompt asks UI Automation what has the keyboard focus on the input
// desktop: LogonUI's password box is a focused password field, the lock
// curtain is not. Unknown when UI Automation cannot be reached from here.

var (
	procOpenInputDesktop = windows.NewLazySystemDLL("user32.dll").NewProc("OpenInputDesktop")
	procSetThreadDesktop = windows.NewLazySystemDLL("user32.dll").NewProc("SetThreadDesktop")
	procCloseDesktop     = windows.NewLazySystemDLL("user32.dll").NewProc("CloseDesktop")
	procCoCreateInstance = windows.NewLazySystemDLL("ole32.dll").NewProc("CoCreateInstance")
	procSysFreeString    = windows.NewLazySystemDLL("oleaut32.dll").NewProc("SysFreeString")

	clsidCUIAutomation = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201,
		Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a,
		Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
)

// Vtable slots, from uiautomationclient.idl (IUnknown takes 0-2).
const (
	slotRelease           = 2
	slotGetFocusedElement = 8  // IUIAutomation
	slotControlType       = 21 // IUIAutomationElement get_CurrentControlType
	slotClassName         = 30 // get_CurrentClassName
	slotIsPassword        = 35 // get_CurrentIsPassword
)

func comCall(obj unsafe.Pointer, slot int, args ...uintptr) uintptr {
	vtbl := *(**[64]uintptr)(obj)
	r, _, _ := syscall.SyscallN(vtbl[slot], append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func unlockPrompt() (platform.UnlockPrompt, string) {
	type result struct {
		p      platform.UnlockPrompt
		detail string
	}
	ch := make(chan result, 1)
	go func() {
		// Never unlocked: the thread moved to the input desktop and leaves
		// with the goroutine.
		runtime.LockOSThread()
		p, d := focusedPasswordField()
		ch <- result{p, d}
	}()
	r := <-ch
	return r.p, r.detail
}

func focusedPasswordField() (platform.UnlockPrompt, string) {
	desk, _, err := procOpenInputDesktop.Call(0, 0, windows.MAXIMUM_ALLOWED)
	if desk == 0 {
		return platform.PromptUnknown, fmt.Sprintf("OpenInputDesktop: %v", err)
	}
	defer procCloseDesktop.Call(desk)
	if r, _, err := procSetThreadDesktop.Call(desk); r == 0 {
		return platform.PromptUnknown, fmt.Sprintf("SetThreadDesktop: %v", err)
	}
	if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
		return platform.PromptUnknown, fmt.Sprintf("CoInitializeEx: %v", err)
	}
	defer windows.CoUninitialize()
	var ua unsafe.Pointer
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, windows.CLSCTX_INPROC_SERVER,
		uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&ua))); hr != 0 || ua == nil {
		return platform.PromptUnknown, fmt.Sprintf("CUIAutomation: 0x%08x", uint32(hr))
	}
	defer comCall(ua, slotRelease)
	var el unsafe.Pointer
	if hr := comCall(ua, slotGetFocusedElement, uintptr(unsafe.Pointer(&el))); hr != 0 || el == nil {
		return platform.PromptUnknown, fmt.Sprintf("GetFocusedElement: 0x%08x", uint32(hr))
	}
	defer comCall(el, slotRelease)
	var ctype, pwd int32
	var class unsafe.Pointer
	comCall(el, slotControlType, uintptr(unsafe.Pointer(&ctype)))
	if comCall(el, slotIsPassword, uintptr(unsafe.Pointer(&pwd))) != 0 {
		return platform.PromptUnknown, "IsPassword failed"
	}
	name := ""
	if comCall(el, slotClassName, uintptr(unsafe.Pointer(&class))) == 0 && class != nil {
		name = windows.UTF16PtrToString((*uint16)(class))
		procSysFreeString.Call(uintptr(class))
	}
	detail := fmt.Sprintf("%s (control type %d)", name, ctype)
	if pwd != 0 {
		return platform.PromptReady, detail
	}
	return platform.PromptHidden, detail
}
