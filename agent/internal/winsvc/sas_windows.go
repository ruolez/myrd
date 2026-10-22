package winsvc

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// The "Disable or enable software Secure Attention Sequence" policy.
const sasPolicyKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`

// sendSAS simulates Ctrl+Alt+Delete on the console session: SendSAS (sas.dll,
// Windows 7+) works only from a service and only while the policy allows
// services to, so the policy is turned on first if it is not.
func sendSAS() error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, sasPolicyKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open %s: %w", sasPolicyKey, err)
	}
	cur, _, err := k.GetIntegerValue("SoftwareSASGeneration")
	exists := err == nil
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		k.Close()
		return fmt.Errorf("read SoftwareSASGeneration: %w", err)
	}
	if v, change := softwareSASGeneration(cur, exists); change {
		if err := k.SetDWordValue("SoftwareSASGeneration", v); err != nil {
			k.Close()
			return fmt.Errorf("allow services to send Ctrl+Alt+Delete: %w", err)
		}
	}
	k.Close()
	proc := windows.NewLazySystemDLL("sas.dll").NewProc("SendSAS")
	if err := proc.Find(); err != nil {
		return err
	}
	proc.Call(0) // AsUser = FALSE: the service's own (SYSTEM) context
	return nil
}
