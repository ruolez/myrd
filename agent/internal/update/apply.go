package update

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ruolez/remotectl/agent/internal/api"
)

// macOS LaunchAgent label (packaging/macos).
const launchAgentLabel = "com.remotectl.agent"

// verifyOSSignature runs the OS code-signature check on the payload.
func verifyOSSignature(payload string) error {
	switch runtime.GOOS {
	case "darwin":
		if strings.HasSuffix(payload, ".pkg") {
			return run("pkgutil", "--check-signature", payload)
		}
		return nil // zipped .app: verified with codesign after extraction
	case "windows":
		ps := fmt.Sprintf("if ((Get-AuthenticodeSignature -LiteralPath '%s').Status -ne 'Valid') { exit 1 }", payload)
		return run("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
	}
	return nil
}

func applyDefault(payload string, m api.Manifest) (string, error) {
	switch runtime.GOOS {
	case "windows":
		// The MSI upgrades in place and restarts the service; the previous MSI
		// is kept next to it for rollback.
		cmd := exec.Command("msiexec", "/i", payload, "/qn", "/norestart")
		if err := cmd.Start(); err != nil {
			return "", err
		}
		return previousMSI(payload), nil
	case "darwin":
		return applyMacBundle(payload)
	}
	return "", errors.New("auto-update is not supported on the Linux demo device; rebuild the image")
}

func previousMSI(payload string) string {
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(payload), "*.msi"))
	for i := len(matches) - 1; i >= 0; i-- {
		if matches[i] != payload {
			return matches[i]
		}
	}
	return ""
}

// applyMacBundle swaps RemoteAgent.app for the zipped bundle in payload and
// kickstarts the LaunchAgent; the old bundle is kept for rollback.
func applyMacBundle(payload string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	bundle := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", "..")) // …/RemoteAgent.app
	if !strings.HasSuffix(bundle, ".app") {
		return "", fmt.Errorf("not running from an .app bundle (%s)", bundle)
	}
	stage := payload + ".d"
	_ = os.RemoveAll(stage)
	if err := run("ditto", "-x", "-k", payload, stage); err != nil {
		return "", err
	}
	newApp := filepath.Join(stage, filepath.Base(bundle))
	if err := run("codesign", "--verify", "--deep", "--strict", newApp); err != nil {
		return "", err
	}
	prev := filepath.Join(filepath.Dir(payload), "previous", filepath.Base(bundle))
	_ = os.RemoveAll(prev)
	_ = os.MkdirAll(filepath.Dir(prev), 0o700)
	if err := run("ditto", bundle, prev); err != nil {
		return "", err
	}
	if err := os.RemoveAll(bundle); err != nil {
		return "", err
	}
	if err := run("ditto", newApp, bundle); err != nil {
		_ = run("ditto", prev, bundle)
		return "", err
	}
	go kickstart()
	return prev, nil
}

func kickstart() {
	_ = run("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), launchAgentLabel))
}

func rollbackDefault(previous string) error {
	if previous == "" {
		return errors.New("no previous build kept")
	}
	switch runtime.GOOS {
	case "windows":
		return exec.Command("msiexec", "/i", previous, "/qn", "/norestart").Start()
	case "darwin":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		bundle := filepath.Clean(filepath.Join(filepath.Dir(exe), "..", ".."))
		if err := os.RemoveAll(bundle); err != nil {
			return err
		}
		if err := run("ditto", previous, bundle); err != nil {
			return err
		}
		go kickstart()
		return nil
	}
	return errors.New("rollback not supported on this OS")
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}
