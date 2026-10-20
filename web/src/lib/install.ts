export type InstallTarget = "darwin" | "windows";

export type InstallInput = {
  target: InstallTarget;
  token: string;
  /** Control plane origin the agent enrolls against. */
  serverUrl: string;
  /** Absolute installer URL, when one is available. */
  downloadUrl: string | null;
  /** Installer file name; defaults to RemoteAgent.pkg / RemoteAgent.msi. */
  filename: string | null;
};

const shSafe = /^[A-Za-z0-9._/:=@%+-]+$/;
const sh = (s: string) => (shSafe.test(s) ? s : `'${s.replace(/'/g, `'\\''`)}'`);
const ps = (s: string) => `'${s.replace(/'/g, "''")}'`;

/** The copy-paste install + enroll commands for the Add device page. */
export function installSnippet(i: InstallInput): string {
  return (i.target === "darwin" ? macSnippet(i) : windowsSnippet(i)).join("\n");
}

function macSnippet(i: InstallInput): string[] {
  const file = i.filename ?? "RemoteAgent.pkg";
  const lines = i.downloadUrl ? [`curl -fL -o ${sh(file)} ${sh(i.downloadUrl)}`] : [];
  if (!file.toLowerCase().endsWith(".pkg")) {
    return [
      ...lines,
      `open ${sh(file)}  # copy RemoteAgent to Applications; it asks for the token`,
    ];
  }
  return [
    ...lines,
    "# the installer enrolls with this token, then deletes it",
    `sudo defaults write /Library/Preferences/com.remotectl.agent EnrollToken ${i.token}`,
    `sudo installer -pkg ${sh(file)} -target /`,
  ];
}

function windowsSnippet(i: InstallInput): string[] {
  const file = i.filename ?? "RemoteAgent.msi";
  const lines = [
    "# PowerShell as Administrator",
    ...(i.downloadUrl ? [`Invoke-WebRequest -Uri ${ps(i.downloadUrl)} -OutFile ${ps(file)}`] : []),
  ];
  const ext = file.toLowerCase().slice(file.lastIndexOf("."));
  if (ext === ".exe") {
    return [
      ...lines,
      `& ${ps(`.\\${file}`)} enroll --token ${i.token} --server ${i.serverUrl}`,
      `& ${ps(`.\\${file}`)} install-service`,
    ];
  }
  if (ext === ".zip") {
    return [
      ...lines,
      `Expand-Archive ${ps(file)} -DestinationPath RemoteAgent`,
      `& '.\\RemoteAgent\\remote-agent.exe' enroll --token ${i.token} --server ${i.serverUrl}`,
      `& '.\\RemoteAgent\\remote-agent.exe' install-service`,
    ];
  }
  return [...lines, `msiexec /i ${ps(file)} ENROLL_TOKEN=${i.token} SERVER_URL=${i.serverUrl} /qn`];
}
