# remotectl protocol contract (v1)

Source-of-truth companions:
- Go wire types: `proto/messages.go`, signing helpers: `proto/fingerprint.go`
- Native ABI: `native/include/rc_native.h`, Go side: `agent/internal/platform/platform.go`
- Byte-exact test vectors: `docs/test-vectors.json` (both the web encoder and the agent decoder MUST pass them)

Everything lives under one origin. In dev that is `http://localhost:5560` (Caddy). Caddy serves the SPA and proxies `/v1/*` to the API (`api:8080`).

---

## 1. Conventions (REST)

- JSON in and out, `Content-Type: application/json`. State-changing requests are `POST/PATCH/PUT/DELETE` only; no state-changing GET.
- Browser auth: cookie `rc_session` (32 random bytes, base64url; server stores SHA-256). `HttpOnly; SameSite=Strict; Path=/`; `Secure` when `PUBLIC_URL` is https. Login sessions last 30 days sliding.
- CSRF: SameSite=Strict + the server rejects state-changing requests whose `Origin` header is present and not equal to `PUBLIC_URL`.
- Agent auth: `Authorization: Bearer <jwt>` (HS256, claims `sub`=device_id, `typ`="agent", `exp` = now+10 min).
- Errors: HTTP status + `{"error":{"code":"<code>","message":"<human text>"}}`. Codes: `bad_request`, `unauthorized`, `forbidden`, `not_found`, `conflict`, `rate_limited`, `totp_required`, `invalid_credentials`, `internal`.
- Timestamps RFC3339 (UTC). IDs are UUIDv4 strings. `null` for absent optional values.
- Lists: `{"items":[...], "next_before": "<cursor>|null"}`; query `?limit=50&before=<cursor>` (cursor = opaque string from the previous page).

### Objects

```jsonc
User      { "id","email","is_admin":bool,"disabled":bool,"totp_enabled":bool,"has_password":bool,
            "passkey_count":int,"created_at","last_login_at":ts|null }
Device    { "id","name","hostname","os":"darwin|windows|linux","arch","agent_version",
            "public_key":"<b64 raw ed25519>","status":"online|offline|disabled",
            "online":bool,"busy":bool,            // busy = has a pending/active session
            "auto_accept":bool,"last_seen_at":ts|null,"created_at",
            "monitors":[Monitor],"permissions":{"screen":bool,"input":bool},"encoder":"x264|videotoolbox|mf-hw|..."|null,
            "pinned_clients":[{"client_key_id","label","approved_at"}],   // informational mirror
            "latest_version":string|null,"update_available":bool,      // newest published release for its os/arch
            "update_state":""|"checking|downloading|waiting_idle|installing|up_to_date|failed|rolled_back",
            "update_from","update_version","update_error","update_at":ts|null }
Monitor   { "id":int,"name","x","y","width","height","scale":float,"primary":bool }
Session   { "id","device_id","device_name","user_id","user_email","client_key_id","client_key_label",
            "state":"pending|active|ended|failed","created_at","connected_at","ended_at","end_reason",
            "ice_path":"host|srflx|relay"|null,"bytes_sent","bytes_recv","client_ip","agent_ip","duration_s":int|null }
AuditEvent{ "id":int,"ts","actor":"user|agent|admin|system","event","user_id","user_email",
            "device_id","device_name","session_id","ip","meta":{} }
ClientKey { "id","label","public_key":"<b64 SPKI>","created_at","last_used_at","revoked_at" }
Passkey   { "id","name","created_at","last_used_at","transports":[string] }
LoginSession { "id","ip","user_agent","created_at","last_seen_at","expires_at","current":bool }
Release   { "id","version","os":"darwin|windows","arch":"amd64|arm64|universal|amd64-win7","url":"/v1/releases/<id>/file",
            "filename","size","sha256","signature","key_id","rollout_pct":0..100,"published_at":ts|null,"created_at",
            "installer":bool,                 // also served as the Add-device download for its os
            "devices":int }                   // devices currently reporting this version
```

## 2. REST endpoints

### Public
| Method | Path | Body → Response |
|---|---|---|
| GET | `/healthz` (via Caddy also `/v1/healthz`) | `{"ok":true,"db":true}` |
| GET | `/v1/releases/{id}/file` | the installer of a published release (`Content-Disposition` = its filename, `X-Content-SHA256`, Range supported); 404 when unpublished, except for admins |
| GET | `/v1/config` | `{"product_name":"remotectl","version","signup_allowed":bool,"webauthn":{"rp_id","origin"},"downloads":{"macos":url\|null,"windows":url\|null}}` |

### Auth
| Method | Path | Body → Response |
|---|---|---|
| POST | `/v1/auth/signup` | `{email,password}` → `{user}` + cookie. 403 unless signup allowed. |
| POST | `/v1/auth/login` | `{email,password}` → `{user}` + cookie, **or** `{"totp_required":true,"ticket":"..."}` (ticket TTL 5 min) |
| POST | `/v1/auth/login/totp` | `{ticket,code}` → `{user}` + cookie |
| POST | `/v1/auth/passkey/begin` | `{}` → `{ticket, publicKey: <PublicKeyCredentialRequestOptions JSON, base64url buffers>}` (discoverable login) |
| POST | `/v1/auth/passkey/finish` | `{ticket, credential: <PublicKeyCredential JSON (toJSON form)>}` → `{user}` + cookie |
| POST | `/v1/auth/logout` | → `{}` |
| GET | `/v1/auth/me` | → `{user}` or 401 |

Rate limit: 10 failed attempts / 15 min per IP and per email → 429.

### Account (logged in)
| Method | Path | Body → Response |
|---|---|---|
| POST | `/v1/account/password` | `{current_password\|null, new_password}` → `{}` (min 10 chars) |
| GET | `/v1/account/passkeys` | → `{items:[Passkey]}` |
| POST | `/v1/account/passkeys/begin` | `{}` → `{ticket, publicKey:<CredentialCreationOptions JSON>}` (resident key required, UV preferred) |
| POST | `/v1/account/passkeys/finish` | `{ticket, name, credential}` → `{passkey}` |
| DELETE | `/v1/account/passkeys/{id}` | → `{}` (refused if it would leave the account with no password and no passkey) |
| POST | `/v1/account/totp/setup` | `{}` → `{secret, otpauth_url, qr_png: "data:image/png;base64,..."}` (pending until enabled) |
| POST | `/v1/account/totp/enable` | `{code}` → `{}` |
| POST | `/v1/account/totp/disable` | `{code}` → `{}` |
| GET | `/v1/account/sessions` | → `{items:[LoginSession]}` |
| DELETE | `/v1/account/sessions/{id}` | → `{}` |

### Client keys (browser signing keys)
| Method | Path | Body → Response |
|---|---|---|
| GET | `/v1/client-keys` | → `{items:[ClientKey]}` |
| POST | `/v1/client-keys` | `{public_key: b64 SPKI P-256, label}` → `{client_key}` (idempotent on public_key for the same user) |
| DELETE | `/v1/client-keys/{id}` | revoke → `{}`; server pushes `client_key.revoked` to the user's online agents |

### Devices
| Method | Path | Body → Response |
|---|---|---|
| GET | `/v1/devices` | → `{items:[Device]}` (only the caller's) |
| GET | `/v1/devices/{id}` | → `{device}` |
| PATCH | `/v1/devices/{id}` | `{name?, auto_accept?}` → `{device}`; pushes `device.update` to the agent |
| DELETE | `/v1/devices/{id}` | → `{}`; marks removed, closes its socket, agent auth refused afterwards |
| POST | `/v1/devices/{id}/update` | `{force?:bool}` → 202 `{version}`; sends `update.check{explicit:true,force}`. 409 `device_offline` \| `up_to_date` \| `unsupported` (Linux demo). Audited `agent.update.requested` |
| POST | `/v1/enroll-tokens` | `{}` → `{token:"XXXX-XXXX", expires_at}` (charset `ABCDEFGHJKLMNPQRSTUVWXYZ23456789`, 15 min, single use, stored hashed) |
| GET | `/v1/sessions?device_id=&limit=&before=` | → paged `Session` |
| GET | `/v1/audit?device_id=&limit=&before=` | → paged `AuditEvent` for the caller |

### Agent
| Method | Path | Body → Response |
|---|---|---|
| POST | `/v1/agent/enroll` | `{token, public_key:<b64 raw ed25519>, hostname, os, arch, version}` → `{device_id, name}` (name defaults to hostname) |
| POST | `/v1/agent/challenge` | `{device_id}` → `{nonce}` (b64 32 bytes, TTL 60 s, single use) |
| POST | `/v1/agent/auth` | `{device_id, nonce, signature}` → `{token, expires_at}`; signature = Ed25519 over `proto.AuthChallengeMessage`. 403 `{"error":{"code":"device_removed"\|"device_disabled"}}` |
| GET | `/v1/agent/ws` | WebSocket, Bearer JWT. One socket per device (a new one replaces the old). |
| GET | `/v1/agent/update?os=&arch=&version=&explicit=0\|1` | Bearer. 204 when nothing newer for this device; else `{version,os,arch,url,sha256,size,signature}` with `url` relative to the server. Selection and cohort are server-side (§7); `explicit=1` ignores `rollout_pct`. |
| DELETE | `/v1/agent/self` | Bearer → `{}`; device marked removed (uninstall). |

### Admin (`is_admin` only; 403 otherwise; every mutation audited with actor `admin`)
| Method | Path | Body → Response |
|---|---|---|
| GET | `/v1/admin/overview` | see below |
| GET | `/v1/admin/users?q=&limit=&before=` | paged `User` (+ `device_count`) |
| POST | `/v1/admin/users` | `{email,password,is_admin}` → `{user}` |
| PATCH | `/v1/admin/users/{id}` | `{is_admin?, disabled?}` → `{user}` (disabling revokes login sessions, ends its live sessions). An admin cannot demote/disable themselves. |
| POST | `/v1/admin/users/{id}/password` | `{password}` → `{}` |
| POST | `/v1/admin/users/{id}/reset-mfa` | → `{}` (clears TOTP) |
| POST | `/v1/admin/users/{id}/logout` | → `{}` (revokes login sessions) |
| DELETE | `/v1/admin/users/{id}` | → `{}` |
| GET | `/v1/admin/devices?q=&status=&limit=&before=` | paged `Device` + `owner_email` |
| PATCH | `/v1/admin/devices/{id}` | `{disabled: bool}` → `{device}` (disable closes its socket + ends sessions) |
| DELETE | `/v1/admin/devices/{id}` | → `{}` |
| GET | `/v1/admin/sessions?state=&limit=&before=` | paged `Session` |
| POST | `/v1/admin/sessions/{id}/end` | → `{}` (sends `session.end` reason `admin_terminated` to both ends) |
| GET | `/v1/admin/audit?user_id=&device_id=&event=&actor=&limit=&before=` | paged `AuditEvent` |
| GET | `/v1/admin/releases` | `{items:[Release]}`, newest version first |
| POST | `/v1/admin/releases` | `multipart/form-data`: part `sig` (the `.sig` sidecar, ≤ 4 KB) **before** part `file` (the installer, ≤ 512 MB); optional field `rollout_pct` (default 100) before `file`. The sidecar must verify against the server's release key and match the uploaded bytes (sha256, size, filename extension). → `{release}` (unpublished). 400 `bad_signature` \| `mismatch` \| `bad_request`, 409 `conflict` (same version/os/arch), 413 `too_large`. Audited `release.uploaded` |
| PATCH | `/v1/admin/releases/{id}` | `{rollout_pct?, published?:bool}` → `{release}`; publishing or raising `rollout_pct` pushes `update.check` to the online devices it now applies to (spread over up to 10 min). A release that reaches 100% published (darwin `universal`, windows `amd64` or `amd64-win7`) also becomes the Add-device download (`macos`, `windows`, `windows7`), unless that is already a release at least as new; audited `release.installer_set` with `auto: true` |
| POST | `/v1/admin/releases/{id}/installer` | → `{release}`; the release's file becomes the Add-device download for its os (`/v1/downloads/{macos\|windows}`). Audited `release.installer_set` |
| DELETE | `/v1/admin/releases/{id}` | → `{}`; removes the file |
| POST | `/v1/admin/devices/{id}/update` | `{force?:bool}` → 202 `{version}`; as `/v1/devices/{id}/update` for any device |
| POST | `/v1/admin/updates/push` | `{os?:"darwin"\|"windows"}` → `{queued:int}`; "Update all outdated": `update.check{explicit:true}` (never forced) to every online outdated device, spread over up to 10 min |
| GET / PUT | `/v1/admin/settings` | `{signup_allowed:bool, default_auto_accept:bool, max_session_hours:int, download_macos_url, download_windows_url}` |

`GET /v1/admin/overview` →
```jsonc
{ "server": {"version","started_at","uptime_s","go_version","db_ok":bool},
  "users": {"total","admins","disabled"},
  "devices": {"total","online","disabled","by_os":{"darwin":n,"windows":n,"linux":n}},
  "sessions": {"active","pending","today","relay_share_24h":0..1,"avg_duration_s_24h","by_path_24h":{"host":n,"srflx":n,"relay":n},
               "failed_24h":n},
  "ws": {"agents":n,"browsers":n,"reconnects_per_min":float},
  "series": [{"hour":ts,"sessions":n,"relay":n,"failed":n}],      // last 24 hours, oldest first, 24 entries
  "recent_audit": [AuditEvent]                                    // latest 15
}
```

Prometheus text metrics at `GET /metrics` on the API's internal listener `:9090` (not proxied by Caddy): `remotectl_agents_online`, `remotectl_browsers_connected`, `remotectl_sessions_active`, `remotectl_sessions_total{path,result}`, `remotectl_ws_reconnects_total`, `remotectl_signaling_latency_seconds` (histogram), `remotectl_db_pool_conns`.

## 3. Signaling WebSocket

- Browser: `GET /v1/ws` with the cookie. Server checks `Origin == PUBLIC_URL`. Max 5 per user.
- Agent: `GET /v1/agent/ws` with Bearer JWT.
- Every message is a JSON object with `"v":1` and `"type"`. Max 64 KB (larger → close 1009). Max 20 msg/s per connection (excess → `error` `rate_limited`, sustained → close).
- Keepalive: server sends WS-level pings every 25 s; two misses = dead. Clients may also send `{"type":"ping","ts"}` and get `{"type":"pong","ts"}`.
- Close codes: 4001 unauthorized, 4003 device disabled/removed, 4008 replaced by newer connection.
- Agent reconnect: jittered backoff 1 s → 30 s (full jitter), reset after 60 s connected.

### Session sequence

```
Browser                         Server                              Agent
 ── session.create{req,device_id,client_key_id,force_relay} ─▶
                                checks owner / online / not busy / key not revoked
                                inserts sessions(state=pending), audit session.created
 ◀─ session.created{req,session_id,ice_servers,device{id,name,public_key}}
 ── session.offer{session_id,sdp,fp_sig} ─▶
                                adds client_key, user, ice_servers(agent creds)
                                ─────────── session.offer{...} ────────────────▶
                                                                     verify fp_sig over offer fingerprint
                                                                     pinned? auto_accept? else prompt on device
                                ◀── session.status{awaiting_approval} ─────────
 ◀─ session.status ───
                                ◀── session.answer{session_id,sdp,fp_sig} ─────
 ◀─ session.answer ── verify device sig with pinned device key (TOFU)
 ◀─▶ ice.candidate (trickled both ways, forwarded verbatim; agent buffers until the offer is applied)
                                ◀── session.state{connected:true,path,...} ────
                                sessions.state=active, ice_path, IPs; audit session.connected
 ◀─ session.state ───
 ── session.end{reason} ─▶   (either side)  ─── session.end ───▶
                                state=ended, end_reason, bytes; audit session.ended
```

Rules:
- One pending/active session per device (`device_busy` otherwise).
- Pending sessions not active within 90 s → `failed` / `timeout`, `session.end` to both.
- A second `session.offer` for an **active** session is an ICE restart; the server forwards it, the agent renegotiates on the existing peer connection (same fingerprint checks).
- Browser socket closes → its pending/active sessions get `session.end{peer_disconnected}` to the agent **only for pending** sessions; active P2P sessions continue (the media path does not need the server). The agent ends the session itself when ICE fails.
- Agent socket closes → pending sessions fail (`device_offline`). Active sessions are kept; on reconnect the agent's `hello.active_sessions` reconciles: any active row for that device not listed is ended `peer_disconnected`.
- Server boot: every `pending` row → `failed/server_restart`; `active` rows survive until reconciled by the agent's next `hello` (or ended after 2 min if the device never returns).
- Session `max_session_hours` (admin setting) enforced by the server sending `session.end{timeout}`.
- Server writes audit: `session.created`, `session.connected` (meta: path, client_ip, agent_ip, local/remote candidate), `session.ended` (meta: reason, duration_s, bytes), `session.failed`, `input.rejected` (when reason = `input_abuse`), and one event per file transfer reported by the agent: `file.uploaded`, `file.downloaded`, `file.upload_failed`, `file.download_failed` (meta: name, dir, size, bytes, sha256, result, error, duration_ms, xfer).
- Server→agent `session.offer` carries `ice_servers` minted for the agent (separate TURN username), `client_key` and `user{id,email}`.
- TURN credentials: coturn REST scheme — `username = "<unix_expiry>:<session_id>:<b|a>"`, `credential = base64(HMAC-SHA1(TURN_SECRET, username))`, TTL ≤ 2 h. URLs from `TURN_URLS` (browser) and `TURN_URLS_AGENT` (agent; defaults to `TURN_URLS`). STUN URL included as a separate entry without credentials. `force_relay` is passed back unchanged so the browser sets `iceTransportPolicy:"relay"`.

### Agent extras
- `hello` → `hello_ack{server_time, device{id,name,auto_accept}, update{available,version}, revoked_client_keys:[spki b64]}`. The agent drops revoked keys from its pinned list. `update` comes from the same selection as `/v1/agent/update` (cohort applied); the agent checks for an update on `hello_ack` only when `available`.
- Server pushes `update.check{explicit?:bool, force?:bool}` (§7). Agent sends `update.status{state, from, version, error}` on every state change (at most one per 2 s); the server stores it on the device, relays it to the owner's browsers and audits it: `agent.update.requested` (meta: version, force; actor user or admin), `agent.update.started` (installing), `agent.update.succeeded` (derived from the next `hello`), `agent.update.failed`, `agent.update.rolled_back` (meta: from, version, error).
- `permissions{permissions}` whenever TCC state changes; `pins.sync{pinned_client_keys}` after any pin change.
- `token.renew{}` → `token{token,expires_at}` (the agent keeps it for the next reconnect).
- Server pushes `device.update{name,auto_accept}`, `client_key.revoked{public_key}`.
- `session.file{session_id, files:[FileRecord]}` (audit only, never relayed): one record per finished file transfer, batched (≤ 50 records per message). `FileRecord = {xfer, direction: "upload"|"download", name, dir, size, bytes, sha256, result: "ok"|"cancelled"|"error", error, duration_ms}`; `upload` = browser → device. The server accepts it for a session of this device that connected, also after it ended (records are flushed just before `session.end`), and audits each record as above; batches over 50 are rejected with `bad_request`.

### Browser extras
- Server pushes `device.status{device_id,online,status,last_seen_at}` to all of the owner's browser sockets on connect/disconnect/disable/remove.
- Server pushes `device.update_status{device_id,state,from,version,error,at}` to the owner's browser sockets when a device's update state changes.

## 4. End-to-end trust (DTLS fingerprint signing)

- Fingerprint = the first `a=fingerprint:sha-256 XX:XX…` value of the **local** SDP, uppercased.
- Signed message (UTF-8): `"remotectl-fp-v1\n" + session_id + "\n" + role + "\n" + FINGERPRINT`, role ∈ `offer` (browser) | `answer` (agent). (`proto.FingerprintMessage`)
- Browser: non-extractable ECDSA P-256 key in WebCrypto, stored in IndexedDB per user id (`remotectl` DB, store `client_keys`, key = user id). Public half exported as SPKI DER → base64 → `POST /v1/client-keys`. Signature: `ECDSA {hash:SHA-256}` → 64-byte P1363 → base64 std.
- Agent: Ed25519 device key → 64-byte sig → base64 std.
- Each side recomputes the fingerprint from the **remote** SDP it received and verifies the remote's signature over it. Mismatch → `session.end{bad_signature}`, nothing is connected.
- Browser pins `device.public_key` per device id in IndexedDB store `device_pins` on first session; a different key later → hard warning screen; the user may explicitly re-trust.
- Agent pins client keys by `sha256(SPKI DER)` hex in its local store (authoritative). A pin records the `user.id` that approved it (`user_id`; pins from before 0.3.9 have none and count for the device's owner, the only user the server lets open sessions to it; a pinned key fills its in on use). A browser is **trusted** when its key is pinned or a pin was approved by the same user: "Allow and remember" trusts the account on that device, so the user's other browsers connect without the new-browser prompt. Untrusted key → device prompt "Allow this new browser: <label>, first seen <created_at>? (user <email>)" with Deny / Allow once / Allow and remember. Trusted + `auto_accept` → no prompt; trusted + !auto_accept → light prompt "Allow session from <label>?". Prompt timeout 60 s → `not_approved`. Revoking a client key drops its pin and the account trust that came with it.

## 5. Data channels

The browser creates the offer with one `recvonly` video transceiver (H.264, packetization-mode=1, preferring high 640c1f/64001f, then main 4d001f, then constrained baseline 42e01f; then VP9 `profile-id=1` for full color; then the repair formats rtx, red, ulpfec, `flexfec-03`) and five data channels (exact labels).

Video: the agent encodes at most the negotiated profile (high without B-frames, i.e. constrained high), stamps each frame's RTP timestamp with its capture time (90 kHz), sends a keyframe only when the browser asks (PLI/FIR, at most every 100 ms), and paces packets at 2.5× the bandwidth estimate (no packet waits more than 50 ms). The estimate is GCC (forked in `agent/internal/gcc`): it holds instead of climbing while the screen sends less than it allows, every large frame doubles as a bandwidth probe, and loss is judged over windows of at least 20 packets (up to 1 s or 100 packets), each packet counted once even when the browser reports it again. Once the screen has not changed for 200 ms the agent re-encodes the last frame in passes of falling quantizer (36 → 18, each paced to half the target bitrate) until text is sharp. An encoder that cannot bound its quantizer (some Windows hardware encoders) gets repeat passes instead: the same picture under the normal rate control, at most 24, until one comes back under 1500 bytes. A change to a small area (about 2% of the screen or less: a caret, a clock) does not restart the passes still to come, which cover it; after the last pass it sends the finest one again. Larger changes start over. A capture whose pixels did not change (an OS damage report without new content) is not new content.

Full color: when the browser offered VP9 `profile-id=1` (Chrome, Edge) and the agent can encode it (all three platforms, software libvpx), `"color":"full"` switches the stream to VP9 4:4:4 (BT.709 limited range) on that payload type, same SSRC, sequence and clock, starting with a keyframe; `standard` switches back to H.264 4:2:0. 4:4:4 keeps 1-pixel colored text and edges sharp (4:2:0 halves the color resolution); it is software encoded, so at most 30 fps, and its automatic ceiling is 1.25× H.264's. If the full-color encoder fails to start, the agent stays on H.264 for the session and withdraws the option.

Repair (forward error correction): when the browser offered `flexfec-03`, the agent's answer carries `a=ssrc-group:FEC-FR <media> <repair>` and, once the median loss of the last 5 s reaches 1%, sends FlexFEC-03 repair packets on the repair SSRC: per frame (at most 48 media packets per group, interleaved masks), 3 repair packets per expected lost packet, 10% to 50% of the media packets. It stops after 5 s below 0.5% loss. Repair packets carry transport-wide sequence numbers (paced and counted by the estimate), and the encoder target leaves room for them (estimate / (1 + rate)). A lost packet is rebuilt in the browser without waiting a round trip for a retransmission; NACK still covers what repair cannot. When the browser negotiates `http://www.webrtc.org/experiments/rtp-hdrext/playout-delay` (Chrome does), every packet carries min = max = 0: render each frame as soon as it is decoded.

| label | options | direction |
|---|---|---|
| `input` | ordered, reliable | browser → agent, binary |
| `move` | `ordered:false, maxRetransmits:0` | browser → agent, binary `0x07` only (feature `"move"`; agents without it close the channel) |
| `cursor` | `ordered:false, maxRetransmits:0` | agent → browser, binary |
| `control` | ordered, reliable | both; JSON text + binary clipboard frames |
| `file` | ordered, reliable | both; JSON text + binary `0x40` data frames (see below). Agents without file transfer close it. |

### `input` (binary, little-endian, first byte = type)

| type | name | layout after the type byte | total |
|---|---|---|---|
| `0x01` | MouseMove | `x u16, y u16, monitor u8` (normalized 0..65535 of the monitor's content box) | 6 |
| `0x06` | MouseMoveRel | `dx i16, dy i16` (Pointer Lock) | 5 |
| `0x07` | MouseMoveSeq | `seq u16, x u16, y u16, monitor u8`; **`move` channel only** (rejected on `input`) | 8 |
| `0x02` | MouseButton | `button u8 (0 L,1 R,2 M,3 back,4 fwd), down u8 (0/1)` | 3 |
| `0x03` | MouseWheel | `dx i16, dy i16` pixels, DOM sign (dy>0 = scroll down) | 5 |
| `0x04` | Key | `usage u16 (HID page 0x07 usage), down u8, mods u8 (HID modifier byte)` | 5 |
| `0x05` | Text | UTF-8 bytes, 1..1024 | 2..1025 |
| `0x10` | ClipboardBegin | `xfer u16, mime u8 (1 text/plain;utf-8, 2 image/png), total u32` (total ≤ 1 MiB) | 8 |
| `0x11` | ClipboardData | `xfer u16, bytes (1..16384)`; complete when `total` bytes received | 4..16387 |
| `0x20` | Control | `op u8, arg u8`: op 1 select monitor (arg = monitor index in `hello.monitors`), 2 request keyframe, 3 set quality tier (**legacy**, for agents without the `stream` feature; 0 background 5 fps, 1 balanced 30 fps, 2 high), 4 release all keys | 3 |

`move` carries absolute moves while the pointer travels; a lost one is superseded by the next. `seq` increments per message (wrapping at 65535) and the agent drops a move whose `seq` is not newer than the newest it has seen (serial-number arithmetic: `int16(seq − newest) ≤ 0`); a late move is not an input reject. Everything that must land at an exact position goes on `input` as `0x01`: a move before each button down and up, and the resting position once the pointer has been still for 80 ms. Pointer Lock moves (`0x06`) stay on `input`, since a lost delta would drift. Without the feature, the browser sends every absolute move on `input`.

HID modifier byte: bit0 LCtrl, bit1 LShift, bit2 LAlt, bit3 LGUI, bit4 RCtrl, bit5 RShift, bit6 RAlt, bit7 RGUI. The agent tracks pressed keys itself and releases every pressed key on session end, on `0x20 op 4`, and on blur-triggered release from the browser. Ctrl+Alt+Delete (either Ctrl and Alt, Delete or keypad Delete going down while both are held) is not injected on Windows, which ignores that chord from software: the helper asks the service, which sets bit 1 of the `SoftwareSASGeneration` policy (HKLM `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`) if needed and calls `SendSAS`; the Delete's key-up is dropped too. Elsewhere the chord is injected as keys. The browser's Keys menu sends such combinations (`web/src/lib/combos.ts`) as ordinary Key messages: downs in order, ups in reverse.

Validation on the agent: wrong length, unknown type, button > 4, monitor out of range, usage not in the keymap, text > 1024 or invalid UTF-8, clipboard over size, unknown xfer → message dropped and the session's abuse counter incremented; > 200 rejects in a session → `session.end{input_abuse}`.

### `cursor` (agent → browser)

| type | name | layout | total |
|---|---|---|---|
| `0x30` | CursorShape | `id u32, w u16, h u16, hot_x u16, hot_y u16, png bytes` | 13 + png |
| `0x31` | CursorPos | `x u16, y u16, visible u8` (normalized; up to 120 Hz) | 6 |

Shapes are sent on change; the browser caches by id (re-send of a known id with zero-length png means "use cached"). Shape size and hotspot are in **native** monitor pixels, whatever the encoded stream size.

The browser shows the shape as its own CSS cursor (scaled to the stream's on-screen size, at most 128 CSS px) while the controller moves the mouse, so the cursor has no network delay. It draws it at the reported position instead under Pointer Lock, while the pointer is outside the stage, and when a report differs from the last move it sent by more than ~4 native pixels once 1 s has passed since that move (the cursor moved on the device).

### `control`

JSON text messages, `{"t": "...", ...}`:

| t | dir | fields |
|---|---|---|
| `hello` | A→B on open | `monitors:[Monitor]`, `active:index`, `encoder`, `codec:"h264"`, `fps` (current target), `max_fps` (encoder cap), `features:[string]` (`"stream"`, `"move"`, `"files"`, `"wallpaper"`; absent on old agents), `permissions:{screen,input}`, `agent_version`, `os` |
| `monitor` | A→B | `active:index`, `width`, `height` (native pixels, after a switch) |
| `stream` | B→A | desired stream settings, see below |
| `stream` | A→B | stream state, see below |
| `ping` | B→A | `id`, `ts` (ms, browser clock) |
| `pong` | A→B | `id`, `ts` (echo) |
| `stats` | A→B every 2 s | `bitrate_kbps`, `fps`, `encoder` (`libvpx-vp9` while full color runs), `width`, `height` (encoded size), `keyframes`, `target_fps`, `target_kbps`, `bwe_kbps`, `loss` (0..1), `refine_qp` (quantizer of the latest static-screen refinement pass, 0 while the screen changes or for repeat passes), `refine_pass` (passes sent since new content), `rtt_ms` (transport feedback round trip), `fec_pct` (repair overhead in use, % of media packets; 0 = off), `fec_packets` (repair packets sent this session) |
| `error` | A→B | `code` (`permission_missing`, `capture_failed`, `encoder_failed`), `detail` |
| `bye` | A→B | `reason` (just before closing) |

Binary frames on `control` (A→B) carry the remote clipboard using the same `0x10`/`0x11` encoding as `input`.

Monitor sizes in `hello.monitors` and `monitor` are always native pixels (physical pixels on Retina displays); the encoded size is reported in `stream` and `stats`.

#### Stream settings (`stream`, feature `"stream"`)

B→A, the full desired state each time (idempotent; the latest wins). The browser sends it when `hello` arrives, on every change (debounced), and when the tab is hidden or shown. It never sends `0x20 op 3` to an agent with the `stream` feature.

```json
{"t":"stream","id":7,"preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false,"wallpaper":false,"color":"full","view":{"w":2880,"h":1600}}
```

| field | type | meaning |
|---|---|---|
| `id` | u32, optional | echoed in the reply |
| `preset` | `auto` \| `speed` \| `balanced` \| `quality` \| `custom` | required |
| `fps` | null \| 1..60 | null = preset default; clamped to `max_fps`. In `auto` a ceiling; null there is 30 fps (since 0.3.10) with the automatic bitrate ceiling of `max_fps`, so each frame gets the bits 60 fps would spread over two |
| `res` | null \| `window` \| `native` \| `1440p` \| `1080p` \| `720p` \| `half` | null = preset default. `window` fits `view` (one edge exactly, so the browser draws the stream pixel for pixel; native without a `view`). Named sizes cap the **short edge** (1080p on 3440×1440 → 2580×1080), `half` halves both edges, nothing upscales. In `auto` a ceiling |
| `max_kbps` | null \| 1000..50000 | null = automatic ceiling; otherwise a cap (sent bitrate = min(estimate, cap)); relay paths are capped at 6000 |
| `background` | bool | hidden tab: 5 fps / 1 Mbps at the current size, Auto frozen |
| `wallpaper` | bool, optional | show the device's wallpaper; false or absent hides it while the session runs (feature `"wallpaper"`, see below) |
| `color` | `standard` \| `full`, optional | `full` asks for 4:4:4 (see Full color above) where the agent and browser can; absent = `standard`. The browser leaves it out when standard |
| `view` | `{w, h}`, optional | the browser's video area in device pixels (2..16384, sent even). Present: presets that would encode at native size use `window` instead. Absent: the browser shows the stream 1:1 ("actual size", scrolled) or predates the field. A change of only `view` keeps Auto's place on its ladder; the browser debounces it (400 ms), since a new size restarts capture |

Presets (`max` = the encoder cap, 60 on hardware, 30 on software encoders):

| preset | fps | resolution | bitrate cap | adaptive |
|---|---|---|---|---|
| `auto` | max (ceiling) | window or native (ceiling) | automatic (8 Mbps at 1080p30, pixel-scaled and × (fps/30)^0.6, ≤ 40) | yes: the bitrate follows the estimate; a step down only when a step's floor (0.02 bit/pixel/frame) does not fit, on loss, GCC overuse or queueing delay (RTT 60 ms, or half the baseline, above the 60 s minimum): 60 → 30 fps, then resolution at 30 fps, then 15/10/5 fps; back up with hysteresis |
| `speed` | max | 720p | 4000 | no |
| `balanced` | min(30, max) | 1080p | 8000 | no |
| `quality` | min(30, max) | window or native | 50000 | no |
| `custom` | `fps` or max | `res`, else window or native | `max_kbps` or automatic | no |

An invalid message (unknown enum, wrong type, out of range) is ignored and answered with the current state and `"ok":false`.

A→B state, sent after `hello`, after every request, and when the monitor, Auto step, path or `max_fps` changes:

```json
{"t":"stream","id":7,"ok":true,"preset":"auto","fps":null,"res":null,"max_kbps":null,"background":false,"wallpaper":false,"color":null,
 "resolved":{"fps":60,"res":"native","max_kbps":null,"color":"standard"},
 "target_fps":30,"max_fps":60,"width":2560,"height":1440,"native_width":5120,"native_height":2880,
 "ceil_kbps":8000,"relay":false,
 "auto":{"rung":2,"rungs":6,"reason":"congestion"},
 "options":{"fps":[5,10,15,24,30,60],"res":[{"id":"native","width":5120,"height":2880},{"id":"1440p","width":2560,"height":1440},{"id":"1080p","width":1920,"height":1080},{"id":"720p","width":1280,"height":720},{"id":"half","width":2560,"height":1440}],"color":["standard","full"]}}
```

`resolved` is what the preset and overrides came to (so the browser can switch to `custom` without its own preset table); `width`/`height` are the encoded size; `auto` is null unless `preset` is `auto` (`reason`: `steady`, `warmup`, `congestion`, `loss`, `starved`, `latency`, `probe`); `options.fps` lists only values ≤ `max_fps`; `options.res` starts with `window` only when the request carried a `view`. `color` echoes the request (null = standard); `resolved.color` is what the device sends; `options.color` lists `full` only when this session can switch to it (older agents omit all three).

Wallpaper (feature `"wallpaper"`, advertised when the agent's platform can hide it: macOS and Windows, not the Linux demo device): a solid background costs almost nothing to encode, so the bits go to what changes. The agent hides the wallpaper from capture start until the session ends and restores it then; toggling never restarts capture. macOS leaves the wallpaper windows out of the capture only, so the device's own screen is unchanged; Windows turns it off for the user's logon session without touching the registry (logoff, or the next agent start after a crash, brings it back), so the person at the device sees a plain background during the session. The browser shows the switch only with the feature and keeps the choice per device.

Compatibility: a browser seeing no `features` in `hello` falls back to `0x20 op 3` (hidden → 0; 30-fps presets and fps ≤ 30 → 1; otherwise 2; see `legacy_tier` vectors). An agent receiving `op 3` maps 0 → background, 1 → `custom` 30 fps native, 2 → `auto`.

### `file` (feature `"files"`)

File transfer in both directions plus browsing the device's file system, as the device's signed-in user. Allowed in every approved session; the agent shows each transfer on the device and reports it for the audit log (`session.file`). There is no size limit: both sides stream to and from disk and are flow-controlled.

Binary frame (both directions):

| type | name | layout after the type byte | total |
|---|---|---|---|
| `0x40` | FileData | `xfer u32, payload 1..16384 bytes` | 6..16389 |

JSON text frames `{"t": ...}`. The browser allocates every `id` (requests) and `xfer` (transfers) from one u32 counter.

| t | dir | fields |
|---|---|---|
| `roots` | B→A | `id` |
| `roots` | A→B | `id`, `roots:[{name, path, kind: home\|downloads\|desktop\|documents\|volume\|drive}]`, `default_dir` (upload default, the user's Downloads), `sep` (`/` or `\`) |
| `list` | B→A | `id`, `path` (absolute), `offset`, `hidden` (include hidden entries) |
| `listing` | A→B | `id`, `path`, `parent` (`""` at a root), `entries:[{name, kind: dir\|file\|other, link, size, mtime (unix ms), hidden}]`, `offset`, `next_offset` (null at the end), `total`, `truncated` (directory has more than 20000 entries) |
| `get` | B→A | `xfer`, `path`: download a regular file |
| `begin` | A→B | `xfer`, `name`, `size`, `mtime`, `window`: then `0x40` frames |
| `put` | B→A | `xfer`, `dir` (`""` = `default_dir`), `name`, `size`: upload |
| `ready` | A→B | `xfer`, `name` (final, de-duplicated: `a (1).txt`), `path`, `window`: the browser may now send `0x40` frames |
| `ack` | both | `xfer`, `bytes` (cumulative bytes the receiver has written) |
| `end` | sender | `xfer`, `sha256` (hex of the whole file), and `size` from the agent; after the last data frame |
| `done` | A→B | `xfer`, `name`, `path`, `size`, `sha256`: the upload is committed under its final name |
| `cancel` | both | `xfer` |
| `error` | A→B | `id` or `xfer`, `code`, `detail` |

Error codes: `not_found`, `denied`, `os_denied` (macOS privacy permission), `not_dir`, `not_file`, `bad_name`, `bad_path`, `no_space`, `io`, `changed` (file changed while reading), `checksum`, `protocol`, `too_many`, `no_user` (Windows: nobody signed in), `audit_unavailable`, `stalled`, `cancelled`.

Rules:
- Flow control: a sender keeps at most `window` payload bytes (4 MiB) beyond the receiver's last `ack`; receivers ack after writing, at least every 256 KiB and at the end. Sending past the window is a `protocol` error. Senders also wait while the channel's `bufferedAmount` is high, and file data is paced to 4 MiB/s on relayed sessions.
- Uploads are written to a hidden `.<name>.remotectl-<xfer>.part` file and renamed to the final name (never replacing an existing file) only after the agent's SHA-256 matches `end.sha256`; a mismatch is `checksum`. Cancel, error, channel close and session end delete the partial file.
- Names are sanitized on the agent (basename only, no control or reserved characters, reserved Windows names prefixed, ≤ 255 bytes). Paths must be absolute (Windows: drive paths only; UNC, `\\?\` and alternate data streams are rejected). The agent's own state directory is denied.
- The agent runs at most 4 transfers and 2 listings at once (`too_many`); 60 s without progress cancels a transfer (`stalled`). `get` on anything but a regular file is `not_file`.

## 6. Agent device-local surfaces

- macOS/Windows: tray icon (red during a session) with Pause / End session (kill switch) / Quit, native border overlay, native consent prompt. An unenrolled macOS agent asks for the token in a dialog (before the permission prompts, which follow enrollment) and keeps an **Enroll this device…** menu item that reopens it; an agent waiting for enrollment also picks up `remote-agent enroll --server … --token …` run in a terminal by the same user (it polls its state file every 2 s).
- Linux Docker demo device: a local "device console" web page (port 5562 on the host) stands in for the physical screen: enrollment token entry, consent prompts, kill switch, pinned browsers list, session indicator.

## 7. Agent updates

Releases are signed offline; the server stores and serves them but cannot forge one.

**Release key.** One Ed25519 key. The public half is committed as `proto.ReleasePublicKey` (compiled into every agent and the server's default `RELEASE_PUBLIC_KEY`); key id = first 16 hex digits of `sha256(raw public key)`. The private half is kept offline (`~/.config/remotectl/release.key`, GitHub secret `RELEASE_PRIVATE_KEY`).

**Signed message** (`proto.ReleaseMessage`, vectors `release`):
```
"remotectl-release-v2\n" + version + "\n" + os + "\n" + arch + "\n" + lower(sha256hex)
```
The URL is not signed; the SHA-256 pins the bytes. `os` ∈ `darwin|windows`, `arch` ∈ `amd64|arm64|universal|amd64-win7`; installers are `.pkg` (darwin) and `.msi` (windows). `amd64-win7` is the Windows 7 build of the windows/amd64 agent (since 0.3.11: Go patched for Windows 7, GDI capture, CPU colour conversion, the software Media Foundation encoder): only with `os` windows, and that build enrolls and checks for updates as arch `amd64-win7`, so selection by exact arch keeps it and the normal Windows build apart. Its Add-device slot is `windows7`.

**Sidecar** `<installer>.sig`, written by `remotectl-release sign`:
```jsonc
{ "format":"remotectl-release-sig-v2", "version":"0.3.0", "os":"darwin", "arch":"universal",
  "sha256":"<64 lowercase hex>", "size":12345678, "filename":"RemoteAgent-0.3.0.pkg",
  "key_id":"<16 hex>", "signature":"<b64 Ed25519 over the message>" }
```

**Versions** follow SemVer 2.0 precedence (`proto.CompareVersions`, vectors `versions`): prerelease < release, numeric identifiers compare as numbers, build metadata ignored, an invalid version (e.g. `dev`) sorts below every valid one. Development builds are `X.Y.Z-dev.N`; releases bump `X.Y.Z` (repo `VERSION` file).

**Selection** (`/v1/agent/update`, `hello_ack.update`, "outdated"): among published releases for the device's `os` whose `arch` equals the device's or is `universal` (exact arch preferred for the same version) and whose version is newer than the agent's, take the highest one the device may have: `explicit` or `bucket < rollout_pct`, where bucket = big-endian uint32 of the first 4 bytes of `sha256(device_id)` mod 100 (vectors `rollout_bucket`). An older release still applies to a device outside the newest one's cohort. The server never offers a downgrade.

**Agent flow.** On `hello_ack.update.available`, on `update.check`, and every 6 h (± 30 min jitter):
1. `checking`: GET `/v1/agent/update` (`explicit` from the trigger). 204 → `up_to_date` (reported only when the check was explicit).
2. Refuse unless the version is newer, os/arch match, the signature verifies against the compiled-in key, and (not explicit) the version is not in the agent's skip-list of versions that failed or rolled back here.
3. `downloading`: fetch `url` (resolved against the server), at most `size` bytes, verify SHA-256.
4. `waiting_idle` while any session is pending or active. `force` ends them with `session.end{reason:"agent_update"}` instead. A newer trigger supersedes the wait.
5. `installing`: hand the verified installer to the OS installer (below). The process is replaced; the next `hello` reports the new `agent_version`, from which the server records success (`agent.update.succeeded`). An install still on `from` after 15 min is recorded as `failed`.
6. Health: the new build must reach `hello_ack` within 5 min while the server is reachable, and must not crash-loop (4 starts without `hello_ack`). Otherwise the previous build is reinstalled, the version is skip-listed, and the restored build reports `rolled_back` with the error. A server outage never causes a rollback.

**Windows.** The LocalSystem service runs the updater. `msiexec /i <msi> /qn /norestart REBOOT=ReallySuppress /l*v %ProgramData%\RemoteAgent\updates\msi-<version>.log`, started detached so it survives the service stop. The MSI allows downgrades (local rollback only). The previous installer is kept for rollback; older ones are pruned. Authenticode is required only when the running build is itself Authenticode-signed (same signer).

**macOS.** The pkg installs a root LaunchDaemon `com.remotectl.updater` (`RemoteAgent.app/Contents/MacOS/remote-agent update-daemon`, launchd `QueueDirectories` on its inbox plus a 120 s watchdog). Directory `/Library/Application Support/RemoteAgent/Updater/` (root:wheel 0755):
- `inbox/` (1733, sticky drop box): the user agent writes `.tmp-<id>` then renames to `<id>.json`:
  `{"v":1,"op":"install","id","manifest":{version,os,arch,sha256,size,signature},"payload":"<path in the user's RemoteAgent/updates/>"}` or `{"v":1,"op":"healthy","id","version"}`.
- `status.json` (0644, written atomically): `{"v":1,"id","state":"verifying|installing|installed|failed|rolled_back|healthy","from","to","error","at"}`.
- `pkgs/`, `previous/`, `logs/` (0700), `marker.json` (0600).

The daemon trusts nothing from the user: it reads requests without following symlinks, verifies the manifest with the compiled-in key, requires a version newer than the installed bundle, copies the payload into `pkgs/` while hashing and installs only that copy (`installer -pkg … -target /`). It backs up the bundle and plists to `previous/` first and restores them on rollback. A `healthy` request only clears the pending marker when its version is the installed one.
