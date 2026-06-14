# retroarch-romm-bridge

A small WebDAV server that lets **RetroArch's cloud sync** store its save files and save states in **[RomM](https://romm.app)** via RomM's save-sync API.

RetroArch only knows how to sync to a WebDAV server.
This bridge presents a WebDAV façade and translates the handful of verbs RetroArch actually uses into RomM API calls.
It holds almost no state — the only thing it persists is a small pairing file (device credentials → RomM token); everything else is derived live from RomM.

## How it works

RetroArch's WebDAV cloud sync is much narrower than full WebDAV.
It only issues:

| Verb | Purpose | This bridge |
|------|---------|-------------|
| `OPTIONS` | capability probe | returns `DAV: 1, 2` |
| `GET /manifest.server` | read the server's file list (JSON `[{path,hash}]`, hashes are MD5) | **generated live** from RomM's current saves + states |
| `GET /<key>` | download a file | streamed from RomM |
| `PUT /<key>` | upload a file | multipart POST/PUT to RomM |
| `MKCOL` / `MOVE` / `DELETE` / `COPY` | dirs / deletes | acknowledged as no-ops (deletions are never propagated) |

It also serves two unauthenticated helpers: `GET /healthz` (liveness probe) and `GET /ping` (a browser-friendly connectivity check from a device).
Every request is logged (method, path, status, client) for troubleshooting.

The sync intelligence (a three-way merge) lives entirely inside RetroArch.
The one thing it needs from the server is `manifest.server`, a list of `{path, hash}` where `hash` is the MD5 of each file.
RetroArch compares those hashes against the MD5s of its local files to decide what to upload/download, so **the manifest hash must equal the real bytes' MD5**:

- **Saves** (`.srm`, `.rtc`): RomM stores an MD5 `content_hash`, used directly.
- **States** (`.state`): RomM does *not* hash states, so the bridge computes the MD5 itself.
  It downloads and hashes each state once, caches it by `(state id, updated_at)`, and also seeds the cache at upload time so a state the bridge just stored never needs to be re-downloaded.

This makes sync naturally bidirectional: change a save/state in RomM (or another device) and RetroArch pulls it; save on the device and it's pushed up.

### Key mapping

RetroArch names a save after the **local ROM file's basename** (`Pokemon - Odyssey [v4.1.1].srm`).
RomM joins saves to games by integer `rom_id`.
Bridging the two is the trickiest part:

- **Shared ROM index.**
  The bridge caches your RomM library (`GET /api/roms`), mapping `(platform fs-slug, normalized name) → rom_id`, indexing each ROM's `fs_name_no_ext`, `fs_name_no_tags`, and display name.
  It refreshes periodically and is shared across all devices (see `ROMM_API_TOKEN`).
- **Folders → platform.**
  With RetroArch's *"sort saves/states into folders by content directory"* on, keys arrive as `saves/<dir>/<game>.srm`.
  `<dir>` is matched to a RomM platform fs-slug (e.g. `gba`), disambiguating same-named games across platforms.
  Override odd folder names with `PLATFORM_MAP`.
- **Tag-tolerant lookup.**
  A local ROM filename often carries region/version tags the RomM library entry lacks (`Pokemon - Odyssey [v4.1.1]` vs `Pokemon - Odyssey`).
  On a lookup miss the bridge strips `(...)`/`[...]` tags (mirroring RomM's own tag rules) and retries, so tagged romhacks still resolve.
- **Stable manifest keys.**
  Each manifest entry's path is built from the *save's own stored filename* (with RomM's datetime tag removed, other tags kept), so the advertised key matches the device's local filename and round-trips without re-upload churn.
  Downloads resolve a requested key back to the newest matching asset by `rom_id` + file extension.

Two consequences worth knowing:

- **The game must exist in RomM.**
  Saves attach to a `rom_id`; if a ROM on the device isn't in your RomM library (even after tag-stripping), its save can't sync — you'll see `upload skipped: no matching rom` in the logs.
  Add the game to RomM first.
- RomM sanitizes a few characters in stored filenames (e.g. `+`).
  For the rare game whose name contains one, the manifest key won't perfectly match the local filename and that one file may re-sync.

## Authentication (pairing)

There is no user list or API token in the bridge config.
Each device authenticates with a RomM **pairing code** as its WebDAV password:

1. In RomM, create a client API token with scopes **`assets.read`, `assets.write`** (add **`roms.read`** only if you don't set `ROMM_API_TOKEN` — see Configuration), then **pair** it → RomM shows an 8-char code (valid 60 seconds, single use).
   Use **one token per device** — pairing rotates the token's secret, so sharing a token across devices breaks the earlier one.
2. In RetroArch, set the WebDAV **username** to any label and the **password** to the pairing code, then trigger a sync **within 60 seconds**.
3. On that first request the bridge exchanges the code for the real token and caches it in the pairing store, keyed by `<username>:<code>`.
   RetroArch keeps sending the same username + code, which then resolve from the cache — the code is exchanged exactly once.

**Keep that same code as the WebDAV password from then on** — it's not a one-time token anymore, it's the device's durable credential.
Generating a fresh code each sync is the usual cause of failures (a new code unused for >60s expires).
Username and code are both case- and dash-insensitive (`jleight`/`JLEIGHT`, `rsu4-9x8u`/`RSU49X8U` all match the same pairing).

If the code expires before first use, or you later rotate/revoke the token in RomM, the bridge returns 401 and drops the cached pairing; generate a new code and set it as the password once.
(RetroArch sometimes forgets the saved WebDAV password across restarts — if sync starts 401ing, re-enter the paired code.)

## Configuration

All configuration is via environment variables — there is no config file.
Only `ROMM_BASE_URL` is required.

| Variable | Default | Purpose |
|---|---|---|
| `ROMM_BASE_URL` | — (required) | RomM instance base URL |
| `ROMM_API_TOKEN` | — | Service token (scope `roms.read`) that keeps the shared ROM index warm. With it set, device pair tokens only need `assets.read`+`assets.write`. If unset, the index is seeded from the first paired device (whose token then needs `roms.read`). |
| `LISTEN_ADDR` | `:8080` | HTTP listen address |
| `INDEX_REFRESH_INTERVAL` | `1h` | ROM index refresh interval |
| `STORE_PATH` | `/data/pairings.json` | Pairing store file — **mount a writable volume here** |
| `PLATFORM_MAP` | — | Optional content-dir→fs-slug overrides, e.g. `Nintendo - Game Boy Advance=gba,snes=snes`. Only needed when a RetroArch content-dir name differs from the RomM platform fs-slug. |
| `LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |

Setting `ROMM_API_TOKEN` primes the shared index at startup so the first sync after a device pairs works immediately and ROM lookups don't depend on any device's token.

### State / persistence

The bridge is *nearly* stateless: the only thing it writes is the pairing store (`STORE_PATH`), a small JSON map of `<username>:<code>` → RomM token, persisted atomically.
Everything else (the ROM index, state hashes) is an in-memory cache rebuilt from RomM.
Because the pairing store must survive restarts, run a single replica backed by a small persistent volume rather than scaling horizontally.

## RetroArch setup

Settings → Cloud Sync:

- **Cloud Sync** = On
- **Destructive Cloud Sync** = Off (recommended)
- **Sync Save Files / Save States** = On (turn everything else off)
- **Sort saves/states into folders by content directory** = On
- **Cloud Storage Backend** = WebDAV
- **WebDAV URL** = `https://<your-bridge-host>/` (with trailing slash), or `http://<host>:8080/` for clients that can't do HTTPS (see TLS below)
- **Username** = any label · **Password** = your RomM pairing code (see above)

## TLS

RetroArch needs the bridge over HTTPS for remote use.
The bridge itself serves plain HTTP; terminate TLS in front of it.

- **Compose**: the `tls` profile runs Caddy, which obtains a real Let's Encrypt cert via a **Cloudflare DNS-01 challenge** — so the service needs no inbound internet access (only LAN clients reaching `:443`).
  Set `SYNC_SITE_ADDRESS` and `CF_API_TOKEN` (see `.env.example`).
- **Kubernetes**: put your existing ingress / TLS in front of the Service.

> **HTTPS-incapable clients:** some console RetroArch ports (e.g. the PS Vita) can't establish TLS and will fail instantly on an `https://` URL.
> Point those at the bridge's plain-HTTP port directly — `http://<host>:8080/` — which the container exposes.
> Credentials then travel in cleartext on your LAN; the bridge→RomM hop stays HTTPS.

## Run locally

```sh
go build -o server ./cmd/server
ROMM_BASE_URL=https://romm.example.com \
ROMM_API_TOKEN=rmm_... \
STORE_PATH=./pairings.json \
LOG_LEVEL=debug \
  ./server
```

Or with Docker Compose (handles the store volume; TLS via the `tls` profile).
Set `ROMM_BASE_URL`/`ROMM_API_TOKEN` in your shell (e.g. mise) or `.env` so Compose can pass them through:

```sh
docker compose up --build                 # plain HTTP on :8080
docker compose --profile tls up --build   # + Caddy HTTPS on :443 (DNS-01)
```

## Troubleshooting

Watch a sync with `docker compose logs -f bridge` (or `kubectl logs`).
You'll see one line per request; a healthy no-op sync is just `OPTIONS /` + `GET /manifest.server`.

- **`device paired`** — a code was exchanged successfully.
- **`pair exchange failed … Invalid or expired`** — the code was wrong, already used, or sat unused >60s.
  Re-enter the paired code (don't generate a new one unless re-pairing from scratch).
- **`upload skipped: no matching rom`** — that game isn't in RomM; add it.
- **`401` on every request** — RetroArch is sending a different password than the paired code (it may have dropped the saved field); re-enter it.
- **Nothing in the logs during a sync** — the device isn't reaching the bridge.
  Open `http(s)://<host>/ping` in the device's browser to isolate DNS/TLS/network from RetroArch.

## Tests

```sh
go test ./...
```
