# Folder picker for every path field — design

2026-11-17. Request: "目录选择都要有交互式选择，而不是只能手动填写路径" —
every field that takes a directory gets an interactive picker, not only a
text box.

## Intent

Typing paths by hand is error-prone, most of all on a NAS where the real
paths look like `/share/CACHEDEV2_DATA/...` and on remote hosts the user
cannot see from tern. The picker lets the user look at what is there and
click their way to the folder. Typing stays possible: a path that does not
exist yet (a new repository, a new restore target) can only be typed.

## The fields

| Form | Field | Where the folders are |
|---|---|---|
| Backup task | Sources (each row) | tern's own file system |
| Backup task | Repository path | the repository's SSH connection |
| Sync task | Folder pair, local | tern's own file system |
| Sync task | Folder pair, remote | the task's SSH or WebDAV connection |
| Restore | Path inside the snapshot | the snapshot |
| Restore | Target directory | tern's own file system |

Hooks take commands, not folders, and stay as they are.

## Approaches

1. **Go for local, bundled tools for the rest (chosen).** Local folders are
   read with `os.ReadDir`. Remote folders go through `rclone lsjson
   --dirs-only` with the same `rclone.conf` the sync engine writes (SFTP over
   the run's ssh_config, WebDAV with the obscured password), so SFTP, rsync
   and WebDAV connections all work and no new dependency is needed. Snapshot
   folders come from `restic ls --json <id> <dir>`, which prints the folder
   and its direct children (checked against restic 0.19.1).
2. **Hand-written SFTP and WebDAV clients in Go.** Faster per click (no
   process start) and could show the absolute home path, but means a second
   SFTP implementation to keep correct next to rclone's, and a WebDAV
   PROPFIND parser next to rclone's.
3. **`github.com/pkg/sftp` plus a WebDAV library.** Least code, but new
   dependencies, which need the user's OK.

## Design

### API (all `NeedSession`, `NeedUnlocked`; GET, `path` query parameter)

- `GET /api/v1/browse?path=` — tern's file system.
- `GET /api/v1/connections/{id}/browse?path=` — a stored connection.
- `GET /api/v1/tasks/{id}/snapshots/{snapshot}/browse?path=` — a snapshot of
  a backup task's repository.

Answer:

```json
{"path": "/share/data", "missing": false, "truncated": false,
 "entries": [{"name": "photos", "path": "/share/data/photos", "mount": true}]}
```

- Folders only, sorted by name (case-insensitive), at most 1000
  (`truncated`).
- `path` is the folder actually listed. When the asked-for folder does not
  exist, the answer lists its nearest existing ancestor and sets `missing`,
  so opening the picker on a typed path that is not there yet still lands
  close to it.
- Errors: `400 validation` for a bad path, `404` for an unknown connection,
  task or snapshot, `502 unreachable` when the remote end or repository
  cannot be reached (the redacted last line of the tool's error),
  `400 unreadable` when a local folder cannot be read.

Local: an empty path starts at `/share` on QTS and `/` on Docker. Paths must
be absolute and clean. Symbolic links to folders count as folders (QTS's
`/share/Public` is one). `mount` marks a folder that is a mount point (another
device than its parent; Linux only) — on Docker that is how the user's volumes
stand out among the container's own folders.

Remote: an empty path is the connection's starting point — the SSH user's
home directory, or the WebDAV address. Paths are as tern already stores them:
relative to the home directory unless they start with `/`. rclone exit 3
(directory not found) walks up one level and tries again. One listing is one
rclone call (a new SSH connection each time) with a 45 s limit.

Snapshot: an empty path is `/`. Paths must be absolute and clean. A folder
that is not in the snapshot walks up one level, like remote.

### UI

`PathInput`: the existing text box plus a folder button next to it. The
button opens the picker at the field's current value (or the source's
starting point). Remote fields keep the button disabled, with a hint, until a
connection is chosen.

Picker dialog:

- Title "选择文件夹", with where it is browsing (this NAS / connection name /
  snapshot ID).
- An editable path bar (Enter goes there) and an Up button; for SSH
  connections two shortcuts, home (`~`) and root (`/`).
- The folder list: click a folder to open it; mount points carry a small
  "卷" tag.
- A note when the asked-for folder does not exist and its nearest ancestor is
  shown; loading, empty, truncated and error states (error with retry).
- Footer: Cancel, and "选择此文件夹" which puts the shown path into the
  field.

### Calls made (the user can overturn any)

- **rclone/restic over hand-written clients** — no new dependency, one SFTP
  implementation. Cost if wrong: each click on a remote folder takes about a
  second (a new SSH connection); over a slow link more.
- **Folders only**, also for backup sources. restic could back up a single
  file, but the request is about folders; a file can still be typed.
- **No "new folder" button.** The picker only reads. A folder that does not
  exist yet is typed (choose the parent, then add the name); tern and the
  tools create it when the task runs.
- **The home directory and the WebDAV root cannot be chosen themselves**
  (the path would be empty, which task validation already refuses as
  "required"). Their subfolders can.
- **The SSH home directory is shown as `~`**, not its absolute path: rclone
  does not report it, and relative paths are what restic, rclone and rsync
  all accept.
- **Choosing `/` in a snapshot means the whole snapshot** (the restore's
  subpath becomes empty).
- **Everything is shown**, hidden folders and tern's own directories
  included: only names are listed, to the signed-in administrator who can
  already run hooks on the NAS.
- **The snapshot picker opens at the snapshot's backed-up path** when it has
  exactly one, otherwise at `/`.
- **A WebDAV picker opens at `/`**, so the paths it fills in are absolute
  (`/Photos`), the form the integration tests and most users write; SSH
  opens at the home directory (`~`), the way SFTP clients do.
- **Remote listings are not locked and not retried**: `restic ls` runs with
  `--no-lock` (works while a prune holds the lock, writes nothing to the
  repository), and the UI does not retry a failed listing on its own (one
  can take its full 45 s); the error shows rclone's or restic's reason and a
  retry button.

## Built (2026-11-17)

- Go: `internal/api/browse.go` (three routes, `Deps.RcloneBin`),
  `rclone.ListDirs`, `restic.ListDir`/`ValidSnapshotID`/`ErrNoSnapshot`,
  `mounts.Device`.
- UI: `web/src/browse/FolderPicker.tsx` (`PathInput`, `FolderPicker`),
  `web/src/lib/paths.ts`, `useBrowse` in `api/queries.ts`, strings under
  `browse.*` (QTS wording for `browse.where_local`), error code
  `unreadable`.
- Checked: API tests over the fake tools, integration tests against the real
  sshd, WebDAV server, rclone 1.76.0 and restic 0.19.1 (including a restore
  of a folder picked in a snapshot), vitest for the picker and all six
  fields, and a browser run of the local picker (desktop and phone width).
- On the NAS (hf-qts, QTS 5.2.11, package built from this tree): the local
  picker opens at `/share` with the NAS wording and tags only the mounted
  `CACHEDEV2_DATA` as a volume (`CACHEDEV1_DATA` and `HD*_DATA` are empty
  placeholders on the RAM disk); the SSH picker lists a test sshd on
  127.0.0.1:22022 through the bundled rclone (home `~`, walk-up from a
  missing folder, a Chinese folder name); the snapshot picker opens at the
  snapshot's path and fills the restore subpath with the restore dialog left
  open.
- The test WebDAV server of the integration environment answers a PROPFIND
  of its root in 35–58 s (99 leftover test folders, 1.3 GB, directory cache
  off); the integration test therefore does not list that root.

### Tests

- Go API tests: local listing (folders only, links, mount flag off Linux,
  missing → ancestor, default start per platform, bad path, unreadable,
  cap); remote over the fake rclone (arguments, config, exit 3 walks up,
  other failures → 502, unknown connection → 404); snapshot over the fake
  restic (filtering to direct children, missing → walk up, bad IDs).
- `restic.ListDir` and the rclone listing parser get unit tests.
- Vitest: the picker (open, enter folder, up, path bar, missing note, error,
  choose) and each of the six fields has a working browse button.
- Integration test against the real sshd, WebDAV server and restic in
  `test/integration`.
