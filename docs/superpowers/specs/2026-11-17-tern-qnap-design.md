# tern for QNAP: design

Date: 2026-11-17. Test machine: `hf-qts` (TS-X53B, x86_64, QTS 5.2.11).

## Intent

A QNAP edition of tern that is installed and used like QNAP's own apps: an
icon in the QTS main menu that opens tern **in a QTS desktop window**, not
in a new browser tab. The Docker edition stays as it is.

## Goals, non-goals

- Goals: a `.qpkg` per architecture (x86_64, arm_64) built from this repo;
  the window opens on the QTS origin; a QTS administrator who is signed in
  to QTS does not sign in to tern again; tern sees NAS paths (`/share/...`)
  directly; the same tool versions as the image.
- Non-goals: App Center listing, code signing, QuTS hero specifics, a
  container-based package, `PUID`/`PGID` (the service runs as admin, like
  every QPKG service), changing the NAS's TCP congestion control for the
  user.
- Anti-goals: anything that leaves tern reachable on a port of its own from
  the LAN; a second login inside the QTS window when QTS already knows the
  user is an administrator.

## Approaches considered

1. **Native QPKG (chosen).** tern, restic and rclone (static Go builds) plus
   statically linked rsync and OpenSSH client, run as a QTS service.
   No dependency on Container Station, starts with the NAS, sees `/share`
   as is. Cost: rsync and ssh are built from source (QTS's rsync is 3.0.7,
   too old for `--info=progress2`).
2. QPKG that drives Container Station and runs the published image. Reuses
   the tested runtime, but needs Container Station, host networking for the
   QTS session check, and a bind mount of all of `/share`.
3. CGI bridge like the storage-analyzer package (QTS runs a CGI per
   request). Does not fit a long-running scheduler with an event stream.

## How it works

- **Window.** `qpkg.conf`: `Desktop = 1`, `WebUI = /tern/`, `Use_Proxy = 1`,
  `Proxy_Path = /tern`, `Win_Width`/`Win_Height`. QTS then writes
  `ProxyPass "/tern" "http://127.0.0.1:<port>/tern"` into
  `/etc/app_proxy.conf`, which both its HTTP and HTTPS front ends include.
  The window is an iframe on the QTS origin, so it also works over HTTPS and
  myQNAPcloud. tern listens on `127.0.0.1:<port>` only (default 17180, set
  in the package's `tern.conf`).
- **Base path** (`TERN_BASE_PATH`, generic): every route lives under the
  prefix, anything outside it is 404. `index.html` is rewritten once at
  startup: asset URLs get the prefix and a `<meta name="tern-base">` tells
  the UI where it lives. The UI's router, API client and event stream use
  that base. Vite builds with `base: './'` so nothing inside JS or CSS is
  absolute. The session cookie's path is the base path.
- **Platform** (`TERN_PLATFORM=qts`):
  - CSP `frame-ancestors 'self'` instead of `'none'` (same origin as QTS).
  - QTS sign-in: a request without a tern session but with QTS's `NAS_SID`
    cookie is checked against `http://127.0.0.1:58080/cgi-bin/authLogin.cgi`
    (the storage-analyzer package does the same). Only `isAdmin = 1` counts.
    Results are cached for 30 s per sid. Such a request is authenticated;
    its CSRF token is an HMAC of the sid under a per-process key, returned
    by `GET /session` as usual. No session row is stored, so signing out of
    QTS ends access within 30 s.
  - tern's own admin password stays: setup still sets it, sensitive actions
    (recovery info, password change) still confirm it, and the
    login page still takes it as a fallback when the QTS check fails. In
    QTS mode the login page first says that no QTS administrator session
    was found. The logout button is hidden for QTS sign-ins.
  - `/api/v1/status` reports `platform`; the UI swaps the handful of texts
    that talk about containers, compose files and `/data` for NAS wording
    (absolute NAS paths, hooks run on the NAS as admin, BBR via `sysctl`,
    time zone from QTS).
- **Service script** (`tern.sh`): reads `tern.conf`, sets `TERN_*`,
  `TZ` from QTS's `Time Zone`, `PATH` with the bundled tools first, starts
  `tern` in the background with a pid file, waits for `tern --healthcheck`;
  `stop` sends SIGTERM and waits up to 110 s (tern's shutdown budget is
  100 s), then SIGKILL. QPKG timeout `60,120`.
- **Layout on the NAS:** `<qpkg>/bin` (tern, restic, rclone, rsync, ssh),
  `<qpkg>/data`, `<qpkg>/secrets` (0700), `<qpkg>/tern.conf`, runtime in
  `/dev/shm/tern`. Upgrades keep `data`, `secrets` and `tern.conf`.
  Uninstalling removes them (QDK default); the README says to keep the
  recovery info elsewhere.
- **Mount check:** QTS's `/` is a RAM disk, so `mounts.IsMounted` already
  says yes for anything on a data volume and no for paths that would vanish
  on reboot. No special case.

## Build

`make qpkg` → `deploy/qnap/build.sh`: one `docker buildx build` of
`deploy/qnap/Dockerfile` per architecture produces the `.qpkg` with
`--output`. Stages: web (same as the image), Go (`-tags embedweb timetzdata`),
tools (Alpine 3.24: rsync 3.5.2 and OpenSSH 10.3p1 from pinned, SHA256
checked source tarballs, linked statically; restic and rclone through
`fetch-tools.sh`), QDK (pinned commit) running `qbuild`. Same
`BASE_REGISTRY`/`GOPROXY` knobs as `make image`.

## Tests

- Go: config, base-path routing and `index.html` rewrite, CSP per platform,
  cookie path, QTS sign-in against a fake `authLogin.cgi` (admin, non-admin,
  bad sid, cache, CSRF), status fields, healthcheck with a base path.
- Web: base detection, API/event URLs, router basename, login page and
  texts per platform.
- On `hf-qts`: install, start/stop/restart, upgrade over an install keeps
  data, the app proxy entry, API and event stream through QTS's Apache, the
  bundled tools run, and the QTS window opens tern signed in.

## Calls made without asking (with what they cost if wrong)

- Native over container: if the user wanted Container Station, the
  packaging changes, the Go/UI work stays.
- QTS sign-in only for administrators: non-admin QTS users cannot use tern
  (tern has one admin anyway).
- Keeping tern's admin password for sensitive actions: one more secret to
  remember; removing it later is a UI change.
- Port 17180 default: changeable in `tern.conf`.

## What the test machine showed (changes to the above)

- QTS writes the proxy line only when App Center enables the app, takes the
  port from `Web_Port`, always ends the target with `/` (so tern gets
  `/tern//x`), and never removes a line after `Proxy_Path` changes. The
  file is on the RAM disk. So `tern.sh start` checks the line itself and
  replaces any stale `/tern` line, then reloads QTS's Apache (gracefully);
  tern serves non-canonical paths in place instead of letting ServeMux
  redirect them out of the base path. The window loads `Proxy_Path`
  (`/tern?v=…`), not `WebUI`.
- Setup on QTS answers with the QTS session's CSRF token and sets no tern
  cookie: a tern session would outlive signing out of QTS and show a logout
  button. Logging out of a tern session refetches the status, so a QTS
  sign-in underneath takes over instead of the login page.
- App Center runs the service script from a temporary folder it deletes;
  tern and rsync inherited it (`getcwd(): No such file or directory`). The
  script starts with `cd /`.
- rsync 3.5 has IDN support that needs `--disable-idn` for the minimal
  static build.
- `qpkg_cli -m` refuses unsigned packages even with `-A 1` unless App
  Center allows them; `deploy/qnap/install.sh` runs the package directly.
