# remotectl

Browser-based remote control for macOS and Windows machines. You sign in on the web, pick an enrolled device, and control it live. One server runs the control plane: accounts, device registry, signaling, TURN relay and audit log. The session itself is a peer-to-peer WebRTC connection, encrypted end to end. Each side signs its DTLS fingerprint with a key the server never holds, so the server never sees screen content or keystrokes.

Design doc: `~/Desktop/Remote Device Control Platform Architecture & Project Plan.md`. The wire contract is in [`docs/PROTOCOL.md`](docs/PROTOCOL.md).

## Quick start (local, Docker)

```bash
make up          # generates .env with fresh secrets, builds and starts everything
```

| URL | What |
|---|---|
| http://localhost:5560 | Web app and admin dashboard. The admin login is printed by `make env` and stored in `.env`. |
| http://localhost:5562 | Device console of the **Docker demo device**: the "physical screen" where you enroll it and approve sessions |
| http://127.0.0.1:9190/metrics | Prometheus metrics (localhost only) |

To try a session:

1. Sign in, go to **Devices → Add device → Docker demo**, and copy the token.
2. Open the device console at http://localhost:5562, paste the token and click **Enroll**. The device shows as online.
3. Click **Connect**. The device console asks *"Allow this new browser?"*; choose **Allow and remember**.
4. You now control a 1080p XFCE desktop. Use the toolbar for the HUD, quality, fullscreen (adds pointer and keyboard lock), clipboard and **Reconnect via relay** (forces TURN).

`make down` stops the stack, `make clean` also deletes its volumes, and `make logs` follows the logs.

If host port 3478 is taken (another TURN server), set `TURN_PORT=3479` in `.env`; this checkout already does.

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
| macOS | `packaging/macos/build-app.sh` → `dist/macos/RemoteAgent.app`; `packaging/macos/build-pkg.sh` → `.pkg` | Capture + VideoToolbox encode verified on this Mac (selftest, ffmpeg-decodable stream). Input needs the Accessibility permission. A live session against the stack has not been run yet. |
| Windows | `native/windows/xbuild.sh` (mingw cross-compile in Docker) → `agent/dist/windows/remote-agent.exe`; `packaging/windows/build.ps1` for the MSI on Windows | Compiles and links. **Never run on Windows.** |

macOS permissions: grant `RemoteAgent.app` **Screen Recording** and **Accessibility** under System Settings → Privacy & Security. Ad-hoc signed rebuilds reset these grants; Developer ID signing (set `DEVELOPER_ID_APP` / `DEVELOPER_ID_INSTALLER` and the notary credentials) keeps them stable. Check with:

```bash
open -W -n dist/macos/RemoteAgent.app --stdout /dev/stdout --args selftest
```

Scripted installs: `msiexec /i RemoteAgent.msi ENROLL_TOKEN=XXXX-XXXX /qn`. On macOS, write the token to `/Library/Preferences/com.remotectl.agent` (`EnrollToken`) before running the `.pkg`. Otherwise the agent asks for the token on first run.

Agent subcommands: `run` (default), `enroll --token`, `status`, `selftest`, `uninstall`, `version`. Windows also has `service`, `helper`, `install-service` and `uninstall-service`.

## Admin

Admins are users with `is_admin`. The bootstrap admin comes from `ADMIN_EMAIL` / `ADMIN_INITIAL_PASSWORD` when the users table is empty. The admin section has:

- **Overview**: agents online, active sessions, relay share, failures, reconnects/min and a 24 h chart.
- **Users**: create, disable, admin toggle, password and MFA reset, force logout.
- **All devices** and **Sessions**: disable devices, force-end sessions.
- **Audit log**: append-only, partitioned by month.
- **Releases**: signed update manifests with rollout %.
- **System**: signup, default auto-accept, max session length, download URLs.

Admins can end sessions but cannot watch or join them: the end-to-end pinning makes that impossible by design.

Release signing (offline key):

```bash
cd server && ../scripts/godocker.sh run ./cmd/remotectl-release keygen     # keep the private key offline
../scripts/godocker.sh run ./cmd/remotectl-release sign --key <priv> --version 0.2.0 --os darwin --arch arm64 --url <cdn url> --sha256 <hex>
```

Set `RELEASE_PUBLIC_KEY` in `.env` (the server verifies uploads against it), and build the agents with `-X main.releasePublicKey=<pub>`.

## Production (one VPS)

On Ubuntu 24.04:

```bash
curl -fsSL https://raw.githubusercontent.com/ruolez/remotectl/main/install.sh | sudo bash   # Install / Update / Remove
```

The installer:

1. Installs Docker.
2. Asks for the domain, admin email and public IP.
3. Writes `.env` with `PUBLIC_URL`, `TURN_URLS` (incl. `turns:` on 5349), `TURN_EXTERNAL_IP` and `TURN_ALLOW_PRIVATE_PEERS=false`.
4. Opens ufw for 80/443 tcp+udp, 3478 tcp+udp, 5349/tcp and 49152–65535/udp.
5. Runs `docker compose -f docker-compose.yml -f docker-compose.prod.yml up -d`.

Caddy gets the TLS certificate automatically, and coturn reuses it for TURNS. Updates take a `pg_dump` backup first.

The repo URL in `install.sh` assumes `github.com/ruolez/remotectl`, which has not been created yet.

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
- macOS: no live session against the server yet, no app icon, and the bundle-swap auto-update is untested.
- Email alerts on login and new devices are not implemented; those events are only audited. There is no TOTP replay cache yet.
- Passkey ceremonies are implemented on both sides but were not exercised with a real authenticator.
- PNG clipboard is ignored; text clipboard works both ways.
- On the Linux demo, the red session border is visible in the captured video. On macOS and Windows it is excluded from capture.
