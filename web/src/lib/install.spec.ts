import { describe, expect, test } from "vitest";
import { installSnippet } from "./install";
import type { InstallInput } from "./install";

const token = "AB12-CD34";
const serverUrl = "https://myrd.example.com";
const base = {
  token,
  serverUrl,
  downloadUrl: null,
  filename: null,
} satisfies Partial<InstallInput>;

describe("installSnippet", () => {
  test("macOS .pkg: download, token pref the postinstall reads, installer", () => {
    expect(
      installSnippet({
        ...base,
        target: "darwin",
        downloadUrl: `${serverUrl}/v1/downloads/macos`,
        filename: "RemoteAgent-1.2.pkg",
      }),
    ).toBe(
      [
        `curl -fL -o RemoteAgent-1.2.pkg ${serverUrl}/v1/downloads/macos`,
        "# the installer enrolls with this token, then deletes it",
        `sudo defaults write /Library/Preferences/com.remotectl.agent EnrollToken ${token}`,
        "sudo installer -pkg RemoteAgent-1.2.pkg -target /",
      ].join("\n"),
    );
  });

  test("macOS without a download URL skips the download and uses the default name", () => {
    expect(installSnippet({ ...base, target: "darwin" })).toBe(
      [
        "# the installer enrolls with this token, then deletes it",
        `sudo defaults write /Library/Preferences/com.remotectl.agent EnrollToken ${token}`,
        "sudo installer -pkg RemoteAgent.pkg -target /",
      ].join("\n"),
    );
  });

  test("macOS file names with spaces are shell-quoted", () => {
    expect(installSnippet({ ...base, target: "darwin", filename: "Remote Agent.pkg" })).toContain(
      "sudo installer -pkg 'Remote Agent.pkg' -target /",
    );
  });

  test("macOS .dmg opens the image instead of running installer", () => {
    expect(installSnippet({ ...base, target: "darwin", filename: "RemoteAgent.dmg" })).toBe(
      "open RemoteAgent.dmg  # copy RemoteAgent to Applications; it asks for the token",
    );
  });

  test("Windows .msi passes the token and server URL as MSI properties", () => {
    expect(
      installSnippet({
        ...base,
        target: "windows",
        downloadUrl: `${serverUrl}/v1/downloads/windows`,
        filename: "RemoteAgent.msi",
      }),
    ).toBe(
      [
        "# PowerShell as Administrator",
        `Invoke-WebRequest -Uri '${serverUrl}/v1/downloads/windows' -OutFile 'RemoteAgent.msi'`,
        `msiexec /i 'RemoteAgent.msi' ENROLL_TOKEN=${token} SERVER_URL=${serverUrl} /qn`,
      ].join("\n"),
    );
  });

  test("Windows .exe enrolls, then registers the service", () => {
    expect(installSnippet({ ...base, target: "windows", filename: "remote-agent.exe" })).toBe(
      [
        "# PowerShell as Administrator",
        `& '.\\remote-agent.exe' enroll --token ${token} --server ${serverUrl}`,
        "& '.\\remote-agent.exe' install-service",
      ].join("\n"),
    );
  });

  test("PowerShell single quotes in a URL are doubled", () => {
    expect(
      installSnippet({ ...base, target: "windows", downloadUrl: "https://x/it's.msi" }),
    ).toContain("-Uri 'https://x/it''s.msi'");
  });
});
