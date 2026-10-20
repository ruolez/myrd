package update

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// Windows installer logic kept free of build tags so it is tested everywhere.

const (
	installedMSI = "installed.msi" // the installer of the running build, kept for rollback
	markerName   = "update-pending.json"
)

func msiName(version string) string    { return "RemoteAgent-" + version + ".msi" }
func msiLogName(version string) string { return "msi-" + version + ".log" }

func msiArgs(msi, logPath string) []string {
	return []string{"/i", msi, "/qn", "/norestart", "REBOOT=ReallySuppress", "/l*v", logPath}
}

var (
	reMainEngine = regexp.MustCompile(`MainEngineThread is returning (\d+)`)
	reInstStatus = regexp.MustCompile(`Installation success or error status: (\d+)`)
)

// parseMSILog finds msiexec's result in a verbose log (UTF-16LE, with or
// without BOM, or ANSI): the last "MainEngineThread is returning N", else
// the product's "Installation success or error status: N".
func parseMSILog(b []byte) (exit int, ok bool) {
	text := decodeLog(b)
	for _, re := range []*regexp.Regexp{reMainEngine, reInstStatus} {
		if all := re.FindAllStringSubmatch(text, -1); len(all) > 0 {
			n, err := strconv.Atoi(all[len(all)-1][1])
			return n, err == nil
		}
	}
	return 0, false
}

func decodeLog(b []byte) string {
	utf16le := len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE
	if utf16le {
		b = b[2:]
	} else if len(b) >= 4 && b[1] == 0 && b[3] == 0 {
		utf16le = true
	}
	if !utf16le {
		return string(b)
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[2*i:])
	}
	return string(utf16.Decode(u))
}

// msiResult maps msiexec's exit code to an error (3010/1641: installed,
// restart pending; REBOOT=ReallySuppress keeps the machine up).
func msiResult(exit int, ok bool) error {
	switch {
	case !ok:
		return errors.New("installer did not finish")
	case exit == 0 || exit == 3010 || exit == 1641:
		return nil
	}
	return fmt.Errorf("installer exited with %d", exit)
}

// msiSettle is how long a failed-looking install may still be msiexec at
// work: a rollback restarts the old service before msiexec finishes its log.
const msiSettle = 10 * time.Minute

// awaitInstaller reports whether msiexec may still be running an install
// started sinceApplied ago, so its outcome should not be judged yet.
func awaitInstaller(log []byte, haveLog bool, sinceApplied time.Duration) bool {
	if sinceApplied >= msiSettle {
		return false
	}
	if !haveLog {
		return true
	}
	_, done := parseMSILog(log)
	return !done
}

var reMSIError = regexp.MustCompile(`Error (\d+)\.\s+([^\r\n]*)`)

// msiFailure explains a failed install from its log: msiResult's error plus
// the first "Error NNNN." message, cut at its first sentence ("" on success).
func msiFailure(b []byte) string {
	exit, ok := parseMSILog(b)
	err := msiResult(exit, ok)
	if err == nil {
		return ""
	}
	m := reMSIError.FindStringSubmatch(decodeLog(b))
	if !ok || m == nil {
		return err.Error()
	}
	detail, _, _ := strings.Cut(m[2], "  ") // MSI separates sentences with two spaces
	return err.Error() + ": Error " + m[1] + ". " + strings.TrimSpace(detail)
}

var (
	errUnsignedUpdate = errors.New("this agent is Authenticode-signed but the update is not")
	errOtherSigner    = errors.New("the update is Authenticode-signed by a different publisher")
)

// sigPolicy requires Authenticode on the update only when the running build
// carries a valid signature, and then from the same signer.
func sigPolicy(runningSigned, payloadSigned, sameSigner bool) error {
	switch {
	case !runningSigned:
		return nil
	case !payloadSigned:
		return errUnsignedUpdate
	case !sameSigner:
		return errOtherSigner
	}
	return nil
}

// parseAuthenticode reads "<Status>|<signer thumbprint>" printed by the
// Get-AuthenticodeSignature probe.
func parseAuthenticode(out string) (valid bool, thumbprint string) {
	status, thumb, _ := strings.Cut(strings.TrimSpace(out), "|")
	return status == "Valid", strings.TrimSpace(thumb)
}

// pruneInstallers lists the installer leftovers (RemoteAgent-<v>.msi and
// msi logs) among files that are not in keep; installed.msi and downloads
// are not leftovers.
func pruneInstallers(files []string, keep ...string) []string {
	var out []string
	for _, f := range files {
		lower := strings.ToLower(f)
		artifact := (strings.HasPrefix(lower, "remoteagent-") && strings.HasSuffix(lower, ".msi")) ||
			(strings.HasPrefix(lower, "msi-") && strings.HasSuffix(lower, ".log"))
		if !artifact || containsFold(keep, f) {
			continue
		}
		out = append(out, f)
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func rollbackReason(m Marker) string {
	if m.Starts >= MaxStarts {
		return fmt.Sprintf("%s restarted %d times without reaching the server", m.To, m.Starts)
	}
	return fmt.Sprintf("%s did not reach the server within %d minutes", m.To, int(HealthWindow.Minutes()))
}
