package update

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
	"unicode/utf16"
)

const msiLogTail = "=== Verbose logging started: 10/20/2026  14:02:11  Build type: SHIP UNICODE 5.00.10011.00  Calling process: C:\\Windows\\system32\\msiexec.exe ===\r\n" +
	"MSI (c) (D8:F0) [14:02:11:010]: Font created.  Charset: Req=0, Ret=0, Font: Req=MS Shell Dlg, Ret=MS Shell Dlg\r\n" +
	"MSI (s) (A4:3C) [14:02:40:123]: Product: RemoteAgent -- Installation completed successfully.\r\n" +
	"MSI (s) (A4:3C) [14:02:40:124]: Windows Installer installed the product. Product Name: RemoteAgent. Product Version: 0.3.1. " +
	"Product Language: 1033. Manufacturer: remotectl. Installation success or error status: %d.\r\n" +
	"MSI (s) (A4:3C) [14:02:40:130]: MainEngineThread is returning %d\r\n" +
	"MSI (c) (D8:F0) [14:02:40:140]: MainEngineThread is returning %d\r\n" +
	"=== Verbose logging stopped: 10/20/2026  14:02:40 ===\r\n"

func utf16le(s string, bom bool) []byte {
	var b []byte
	if bom {
		b = []byte{0xFF, 0xFE}
	}
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return b
}

func logWith(installStatus, server, client int) string {
	return fmt.Sprintf(msiLogTail, installStatus, server, client)
}

func TestParseMSILog(t *testing.T) {
	cases := []struct {
		name string
		log  []byte
		exit int
		ok   bool
	}{
		{"UTF-16LE with BOM, success", utf16le(logWith(0, 0, 0), true), 0, true},
		{"UTF-16LE with BOM, fatal error", utf16le(logWith(1603, 1603, 1603), true), 1603, true},
		{"UTF-16LE without BOM", utf16le(logWith(0, 0, 1618), false), 1618, true},
		{"ANSI, reboot required", []byte(logWith(3010, 3010, 3010)), 3010, true},
		{"client line wins over the server line", []byte(logWith(0, 0, 1602)), 1602, true},
		{"only the product status so far", []byte("MSI (s) (A4:3C) [14:02:40:124]: Windows Installer installed the product. " +
			"Installation success or error status: 1603.\r\n"), 1603, true},
		{"still running", utf16le("=== Verbose logging started ===\r\nMSI (s) (A4:3C): Doing action: InstallFiles\r\n", true), 0, false},
		{"empty", nil, 0, false},
		{"odd UTF-16 tail", append(utf16le("MainEngineThread is returning 0", true), 0x41), 0, true},
	}
	for _, c := range cases {
		exit, ok := parseMSILog(c.log)
		if exit != c.exit || ok != c.ok {
			t.Errorf("%s: parseMSILog = %d, %v; want %d, %v", c.name, exit, ok, c.exit, c.ok)
		}
	}
}

func TestMSIResult(t *testing.T) {
	cases := []struct {
		exit int
		ok   bool
		want string
	}{
		{0, true, ""},
		{3010, true, ""},
		{1641, true, ""},
		{1603, true, "installer exited with 1603"},
		{1618, true, "installer exited with 1618"},
		{0, false, "installer did not finish"},
	}
	for _, c := range cases {
		err := msiResult(c.exit, c.ok)
		got := ""
		if err != nil {
			got = err.Error()
		}
		if got != c.want {
			t.Errorf("msiResult(%d, %v) = %q, want %q", c.exit, c.ok, got, c.want)
		}
	}
}

func TestSigPolicy(t *testing.T) {
	cases := []struct {
		running, payload, same bool
		want                   error
	}{
		{false, false, false, nil},
		{false, true, false, nil},
		{true, true, true, nil},
		{true, false, false, errUnsignedUpdate},
		{true, true, false, errOtherSigner},
	}
	for _, c := range cases {
		if got := sigPolicy(c.running, c.payload, c.same); !errors.Is(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("sigPolicy(%v, %v, %v) = %v, want %v", c.running, c.payload, c.same, got, c.want)
		}
	}
}

func TestParseAuthenticode(t *testing.T) {
	cases := []struct {
		out   string
		valid bool
		thumb string
	}{
		{"Valid|A1B2C3\r\n", true, "A1B2C3"},
		{"NotSigned|\r\n", false, ""},
		{"HashMismatch|A1B2C3", false, "A1B2C3"},
		{"", false, ""},
	}
	for _, c := range cases {
		valid, thumb := parseAuthenticode(c.out)
		if valid != c.valid || thumb != c.thumb {
			t.Errorf("parseAuthenticode(%q) = %v, %q; want %v, %q", c.out, valid, thumb, c.valid, c.thumb)
		}
	}
}

func TestPruneInstallers(t *testing.T) {
	// remote-agent-*.msi(.part) belong to a download that may be running.
	files := []string{"installed.msi", "RemoteAgent-0.3.0.msi", "RemoteAgent-0.3.1.msi", "remote-agent-0.3.2.msi.part",
		"remote-agent-0.3.2.msi", "msi-0.3.0.log", "msi-rollback-0.3.0.log", "msi-0.3.1.log", "skip.json", "notes.txt"}
	got := pruneInstallers(files, "msi-0.3.1.log")
	want := []string{"RemoteAgent-0.3.0.msi", "RemoteAgent-0.3.1.msi", "msi-0.3.0.log", "msi-rollback-0.3.0.log"}
	if !slices.Equal(got, want) {
		t.Fatalf("pruneInstallers = %q, want %q", got, want)
	}
}

func TestMSIArgs(t *testing.T) {
	got := msiArgs(`C:\ProgramData\RemoteAgent\updates\RemoteAgent-0.3.1.msi`, `C:\ProgramData\RemoteAgent\updates\msi-0.3.1.log`)
	want := []string{"/i", `C:\ProgramData\RemoteAgent\updates\RemoteAgent-0.3.1.msi`, "/qn", "/norestart", "REBOOT=ReallySuppress",
		"/l*v", `C:\ProgramData\RemoteAgent\updates\msi-0.3.1.log`}
	if !slices.Equal(got, want) {
		t.Fatalf("msiArgs = %q, want %q", got, want)
	}
}

func TestRollbackReason(t *testing.T) {
	cases := []struct {
		m    Marker
		want string
	}{
		{Marker{To: "0.3.1", Starts: 1}, "0.3.1 did not reach the server within 5 minutes"},
		{Marker{To: "0.3.1", Starts: MaxStarts}, "0.3.1 restarted 4 times without reaching the server"},
	}
	for _, c := range cases {
		if got := rollbackReason(c.m); got != c.want {
			t.Errorf("rollbackReason(%+v) = %q, want %q", c.m, got, c.want)
		}
	}
}

// From the failed 0.3.0 → 0.3.1 update on a real PC (the old package's
// uninstall tried to delete the data dir holding the running log).
const msiLog1306 = "MSI (s) (F8:4C) [15:58:51:182]: Executing op: ActionStart(Name=StopServices,Description=Stopping services,Template=Service: [1])\r\n" +
	"MSI (s) (F8:4C) [15:58:51:189]: Product: RemoteAgent -- Error 1306. Another application has exclusive access to the file 'C:\\ProgramData\\RemoteAgent\\updates\\msi-0.3.1.log'.  Please shut down all other applications, then click Retry.\r\n" +
	"Error 1306. Another application has exclusive access to the file 'C:\\ProgramData\\RemoteAgent\\updates\\msi-0.3.1.log'.  Please shut down all other applications, then click Retry.\r\n" +
	"Action ended 15:58:51: InstallFinalize. Return value 3.\r\n" +
	"MSI (s) (F8:D8) [15:58:51:254]: MainEngineThread is returning 1603\r\n"

func TestMSIFailure(t *testing.T) {
	for _, c := range []struct {
		name string
		log  []byte
		want string
	}{
		{"error line from a real log", utf16le(msiLog1306, true),
			"installer exited with 1603: Error 1306. Another application has exclusive access to the file 'C:\\ProgramData\\RemoteAgent\\updates\\msi-0.3.1.log'."},
		{"no error line", []byte(logWith(1603, 1603, 1603)), "installer exited with 1603"},
		{"unfinished log", []byte("MSI (s) (F8:D8) [15:58:49:814]: Command Line: REBOOT=ReallySuppress\r\n"), "installer did not finish"},
		{"success", []byte(logWith(0, 0, 0)), ""},
	} {
		if got := msiFailure(c.log); got != c.want {
			t.Errorf("%s: msiFailure = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestAwaitInstaller(t *testing.T) {
	unfinished := []byte("MSI (s) (F8:D8) [15:58:49:814]: Command Line: REBOOT=ReallySuppress\r\n")
	for _, c := range []struct {
		name    string
		log     []byte
		haveLog bool
		since   time.Duration
		want    bool
	}{
		{"log still being written", unfinished, true, 5 * time.Second, true},
		{"msiexec not started yet", nil, false, 5 * time.Second, true},
		{"finished log", []byte(logWith(1603, 1603, 1603)), true, 5 * time.Second, false},
		{"gave up waiting", unfinished, true, msiSettle, false},
	} {
		if got := awaitInstaller(c.log, c.haveLog, c.since); got != c.want {
			t.Errorf("%s: awaitInstaller = %v, want %v", c.name, got, c.want)
		}
	}
}
