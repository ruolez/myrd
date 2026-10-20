# remotectl

Browser-based remote control for macOS and Windows machines. You sign in on the web, pick an enrolled device, and control it live. One server runs the control plane: accounts, device registry, signaling, TURN relay and audit log. The session itself is a peer-to-peer WebRTC connection, encrypted end to end. Each side signs its DTLS fingerprint with a key the server never holds, so the server never sees screen content or keystrokes.

Design doc: `~/Desktop/Remote Device Control Platform Architecture & Project Plan.md`. The wire contract is in [`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Quick start (local, Docker)

```bash
make up          # generates .env with fresh secrets, builds and starts everything
```

| URL | What |
|---|---|
| http://localhost:5560 | Web app and admin dashboard. The admin login is `ADMIN_EMAIL` / `ADMIN_INITIAL_PASSWORD` from `.env` (printed by `make env`). |
| http://localhost:5562 | Device console of the **Docker demo device**: the "physical screen" where you enroll it and approve sessions |
| http://127.0.0.1:9190/metrics | Prometheus metrics (localhost only) |

To try a session:

1. Sign in, go to **Devices → Add device → Docker demo**, and copy the token.
2. Open the device console at http://localhost:5562, paste the token and click **Enroll**. The device shows as online.
3. Click **Connect**. The device console asks *"Allow this new browser?"*; choose **Allow and remember**.
4. You now control a 1080p XFCE desktop. Use the toolbar for the HUD, **Stream** settings, **Files**, fullscreen (adds pointer and keyboard lock), clipboard and **Reconnect via relay** (forces TURN).

### Session controls

- **Stream** (toolbar, or Ctrl+Alt+Shift+S; in fullscreen also from the pill at the top edge):
  - **Presets.** Auto lowers frame rate first, then resolution, when the network is congested, and raises them again once it recovers. It never reacts to an idle screen. Speed is 720p at the maximum fps with a 4 Mbps cap; Balanced is 1080p30 at 8 Mbps; Quality is native at 30 fps and 20 Mbps.
  - **Frame rate:** 5–60 fps, capped at the device encoder's maximum (30 on software encoders).
  - **Resolution:** Native, 1440p, 1080p, 720p or 50%. The cap applies to the short edge, and the stream is never upscaled.
  - **Bitrate:** Auto, or a manual cap of 1–20 Mbps. Relayed sessions are capped at 6 Mbps.
  - Settings are remembered per device. A hidden tab drops to 5 fps.
  - The HUD shows target vs sent fps, target bitrate and bandwidth estimate, encoded size and Auto's state.
- **Files:**
  - Browse the device as its signed-in user, upload by button or by dragging files onto the session, and download any file.
  - No size limit. Transfers stream to and from disk, flow-controlled and SHA-256 verified. Uploads go to the open folder, or to the user's Downloads.
  - Downloads save through the browser's save dialog (Chromium), a streamed download (Firefox and Safari), or memory as a last resort.
  - Every transfer is shown on the device (tray, macOS notification, demo console) and written to the audit log as `file.*` events.
  - The agent's own data directory is never accessible. On Windows, file operations impersonate the signed-in user and are refused when nobody is signed in.
  - On the demo device, `/home/device/Downloads` lives in the container, so recreating the container empties it.

`make down` stops the stack, `make clean` also deletes its volumes, and `make logs` follows the logs.

If host port 3478 is taken (another TURN server), set `TURN_PORT=3479` in `.env`; this checkout already does.

`ADMIN_EMAIL` may be a plain username: this checkout's `.env` signs in as `admin` / `admin`. The bootstrap admin is only created when the users table is empty, so changing `.env` later does not change an existing account. Change the password under **Settings → Password** (at least 10 characters). `install.sh` always generates a random password for production.

## Layout

```
proto/            Go: signaling message types + fingerprint/auth/release signing helpers (shared)
server/           Go control plane: REST + WebSocket signaling, auth, enrollment, TURN creds, admin, audit, metrics
web/              React 19 + Vite + Tailwind SPA: devices, session view, settings, admin dashboard
agent/            Go agent core (pion WebRTC): signaling, sessions, input, consent, pins, updates, tray
native/include/   rc_native.h: the one C ABI each OS backend implements
native/linux/     X11/XShm → x264, XTest      (Docker demo device)
native/macos/     ScreenCaptureKit → VideoToolbox, CGEvent (Objective-C)
native/windows/   DXGI dup / WGC → Media Foundation H.264, SendInput (C++)
packaging/        macOS .app/.pkg (LaunchAgent), Windows WiX v5 MSI (service)
deploy/           Caddyfile, coturn entrypoint, demo-device image
docs/             PROTOCOL.md + byte-exact test vectors shared by web and agent
```

Services: `postgres` (17), `api` (Go, port 8080 inside the network), `web` (Caddy: SPA + `/v1` proxy, strict CSP), `coturn` (STUN/TURN with HMAC credentials) and `demo-device` (profile `demo`).

## Tests

```bash
make test          # all of the below
make test-proto    # signing helpers
make test-server   # unit + integration tests against a throwaway Postgres 17
make test-agent    # agent unit tests incl. an in-process two-peer WebRTC session
make test-web      # lint (eslint + prettier), vitest, production build
```

The host Go toolchain (`.tools/go`) cannot reach the module proxy on this Mac because of a local firewall. Use `scripts/godocker.sh` to download modules and run Linux builds and tests; host builds via `scripts/gohost.sh` then run offline from the shared cache in `.tools/gomod`. Network tests in the agent must run in Docker for the same reason.

## Agents

| OS | Build | Status |
|---|---|---|
| Linux (demo) | `docker compose --profile demo build demo-device` | Verified end to end in Chromium: video, mouse, keyboard, relay path, consent, kill switch |
| macOS | `SERVER_URL=… packaging/macos/build-app.sh` → `dist/macos/RemoteAgent.app`; `packaging/macos/build-pkg.sh` → `.pkg` | Live session against the stack verified on this Mac: enrollment dialog, consent + pinning, ScreenCaptureKit → VideoToolbox video, direct (srflx) path. |
| Windows | `native/windows/xbuild.sh` (mingw cross-compile in Docker) → `agent/dist/windows/remote-agent.exe`; `packaging/windows/build.ps1` for the MSI on Windows | Compiles and links. **Never run on Windows.** |

macOS notes:

- **Server URL.** `build-app.sh` bakes `SERVER_URL` into `Info.plist` (`LSEnvironment`), so the app knows its server when launched from Finder. The `.pkg` LaunchAgent uses the same value. For the local stack, build with `SERVER_URL=http://localhost:5560`.
- **Permissions.** Grant **Screen Recording** and **Accessibility** to `RemoteAgent` under System Settings → Privacy & Security. The app appears in those lists after its first start, which asks for both.
- **Rebuilds.** An ad-hoc signed build is a new app to macOS, so every rebuild invalidates the grants. Turning an old entry back on does not help. Reset and re-grant:

  ```bash
  tccutil reset ScreenCapture com.remotectl.agent; tccutil reset Accessibility com.remotectl.agent
  ```

  Developer ID signing (`DEVELOPER_ID_APP` / `DEVELOPER_ID_INSTALLER` plus the notary credentials) keeps grants across updates.
- **Logs.** The agent logs to `~/Library/Logs/RemoteAgent.log` (rotated at 10 MB); set `RC_LOG_LEVEL=debug` for more.
- **Self-test.** Run it as the app, so that the app's own grants apply:

  ```bash
  open -W -n /Applications/RemoteAgent.app --stdout /tmp/selftest.txt --stderr /tmp/selftest.txt --args selftest; cat /tmp/selftest.txt
  ```

**Distributing the agent.** Upload the installers under **Admin → System → Client downloads**:

- macOS: `.pkg`, `.dmg` or `.zip`. Build it with `SERVER_URL=<PUBLIC_URL>`.
- Windows: `.msi`, `.exe` or `.zip`. Double-clicking the MSI shows an **Add this PC** page for the server address and the enrollment token. The address is pre-filled when it is built with `SERVER_URL` (`build.ps1 -ServerUrl`, the repo variable `SERVER_URL`, or the `server_url` input of the `agent-windows` workflow). `msiexec … /qn` skips that page.
- At most 512 MB each.

Files are stored in the `downloads` volume and survive updates. **Add device** then shows a download button and a public link (`/v1/downloads/macos`, `/v1/downloads/windows`) that works without signing in, so you can open it on the machine being enrolled. Enrolling still needs the one-time token. With no upload, the page falls back to the external installer URLs set on the same Admin page.

Add device also shows a ready-to-paste scripted install for the uploaded file:
- macOS: `curl`, then `sudo defaults write /Library/Preferences/com.remotectl.agent EnrollToken …`, then `sudo installer -pkg …`.
- Windows (PowerShell): `Invoke-WebRequest`, then `msiexec /i … ENROLL_TOKEN=… SERVER_URL=… /qn`. For an `.exe` it uses `remote-agent.exe enroll --token … --server …` followed by `install-service`.

Otherwise the agent asks for the token on first run.

Agent subcommands: `run` (default), `enroll --token`, `status`, `selftest`, `uninstall`, `version`. Windows also has `service`, `helper`, `install-service` and `uninstall-service`.

## Admin

Admins are users with `is_admin`. The bootstrap admin comes from `ADMIN_EMAIL` / `ADMIN_INITIAL_PASSWORD` when the users table is empty. The admin section has:

- **Overview**: agents online, active sessions, relay share, failures, reconnects/min and a 24 h chart.
- **Users**: create, disable, admin toggle, password and MFA reset, force logout.
- **All devices** and **Sessions**: disable devices, force-end sessions.
- **Audit log**: append-only, partitioned by month.
- **Releases**: signed update manifests with rollout %.
- **System**: client downloads (installer uploads), signup, default auto-accept, max session length, external download URLs.

Admins can end sessions but cannot watch or join them: the end-to-end pinning makes that impossible by design.

Release signing (offline key):

```bash
cd server && ../scripts/godocker.sh run ./cmd/remotectl-release keygen     # keep the private key offline
../scripts/godocker.sh run ./cmd/remotectl-release sign --key <priv> --version 0.2.0 --os darwin --arch arm64 --url <cdn url> --sha256 <hex>
```

Set `RELEASE_PUBLIC_KEY` in `.env` (the server verifies uploads against it), and build the agents with `-X main.releasePublicKey=<pub>`.

## Production (dedicated Ubuntu 24.04 server)

Point a DNS A record at the server, then run:

```bash
curl -fsSL https://raw.githubusercontent.com/ruolez/myrd/main/install.sh | sudo bash
```

The menu offers **Install**, **Update**, **SSL** and **Remove**. Each is also a subcommand: `sudo bash /opt/myrd/install.sh update|ssl|remove`.

| Command | What it does |
|---|---|
| `install` | Installs Docker, certbot and ufw. Asks for the domain, a Let's Encrypt email, the admin login and the public IP, and checks that DNS points at this server. Clones the repo to `/opt/myrd` and writes `.env`: fresh secrets, public URLs, TURN settings, and Postgres memory sized from RAM. Opens the firewall, builds and starts the stack, gets the certificate, and enables renewal. Prints the URL and admin password. |
| `update` | Backs up the database and `.env` to `/opt/myrd/backups` (keeps the last 10). Pulls `main` from GitHub, pulls new base images and rebuilds. Removes dangling images and build cache older than 24 h. Data, settings and the certificate are not touched. Migrations run when the API starts. |
| `ssl` | Shows the certificate status and repairs whatever is broken: a missing or expired certificate, Caddy stuck without one, a missing renewal hook or timer. It can also force a renewal or move to a new domain. It finishes with a `certbot renew --dry-run`. |
| `remove` | Backs up the database to `/root/myrd-backup-<time>`, then deletes the containers, images, volumes and `/opt/myrd`. It asks before deleting the certificate. |

How TLS works:

1. certbot runs on the host using the webroot method. Caddy serves `/.well-known/acme-challenge/` from `data/acme`; it serves plain HTTP until the first certificate exists.
2. Ubuntu's `certbot.timer` renews twice a day when a certificate is due.
3. After each renewal, the deploy hook `/etc/letsencrypt/renewal-hooks/deploy/myrd.sh` copies the certificate to `data/tls` (key readable by coturn) and restarts Caddy and coturn.
4. Caddy then serves HTTPS, HTTP/2 and HTTP/3, and coturn serves TURNS on port 5349.

Ports: 80/443 tcp+udp, 3478 tcp+udp, 5349/tcp, and 49152–65535/udp for the TURN relay.

`make test-install` runs the whole installer in a throwaway privileged Ubuntu 24.04 container with its own Docker. Pebble (Let's Encrypt's test server) does real HTTP-01 validation. The run covers install, renewal through the hook, update, `ssl` repairing a broken TLS state, and remove.

## Deviations from the design doc

- **sqlc** → hand-written pgx queries (no codegen step).
- **Swift** → Objective-C for the macOS native layer, so cgo links it without the Swift runtime.
- **App/API split** → one origin (`app` + `/v1`) behind Caddy. This is simpler for cookies and CSP.
- **systemd units** → Docker Compose, as requested. Postgres tuning lives in the compose files.
- **Grafana / Uptime Kuma** → not shipped. The admin overview plus `/metrics` cover the "one dashboard"; point Prometheus at `127.0.0.1:9190`.
- **Append-only audit** → enforced by a trigger, since the API uses a single DB role.
- **Windows helper** → `remote-agent.exe helper` instead of a separate `agent-session.exe`. IPC uses two one-way named pipes.
- **Linux demo device** → added; it was out of scope in v1. It is the test harness that makes the whole flow runnable in Docker.

## Known gaps

- Windows: runtime untested (hardware MFT negotiation, secure-desktop capture, SYSTEM helper spawn, WiX build).
- macOS: tested only by controlling this same Mac, not from a second machine. There is no app icon, and the bundle-swap auto-update is untested.
- Email alerts on login and new devices are not implemented; those events are only audited. (TOTP codes are single-use: the accepted time step is stored per user.)
- Passkey ceremonies are implemented on both sides but were not exercised with a real authenticator.
- PNG clipboard is ignored; text clipboard works both ways.
- File transfer: no folder transfer, resume, or delete/rename on the device. It has not been tried on a real Windows PC (impersonation), and the macOS privacy-prompt handling is untested.
- The Linux x264 backend keeps a constant-rate setup. Variable-frame-rate input corrupted frames on the demo device, so the agent instead enforces a keyframe at least every 2 s.
- On the Linux demo, the red session border is visible in the captured video. On macOS and Windows it is excluded from capture.
