# tern 计划 4 · 备份 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 备份任务可用：restic over SSH 备份、保留策略、清理与校验周期、快照列表与恢复、恢复信息。脱离本工具，只用原生 restic 和密码就能恢复。

**Architecture:** `engine/restic` 把 `model.Task` 翻译成 restic 子进程调用，实现计划 3 的 `engine.Engine`。SSH 走计划 2 生成的 `ssh_config`（通过 restic 的 `sftp.command` 选项），仓库密码写到 tmpfs 运行目录里的文件，经 `RESTIC_PASSWORD_FILE` 传入。排除规则由 `engine/filter` 统一翻译，计划 5 的同步引擎也用它。

**Tech Stack:** Go 1.26、restic 0.19.1（子进程）。

**Spec:** `2026-11-10-nas-backup-sync-design.md` §6、§7.4（排除规则）、§8 第 5、6 条。执行者同时读 `docs/superpowers/plans/2026-11-10-tern-0-index.md`。

## Global Constraints

索引第 2 节全部适用。本计划另加：

- 每次 restic 调用的固定部分：
  - 参数：`-r sftp:tern-target:<RepoPath>`、`-o`、`sftp.command=ssh -F "<ssh_config 路径>" tern-target -s sftp`、`-o`、`sftp.connections=<Connections>`
  - 环境：`RESTIC_PASSWORD_FILE=<运行目录>/repo-password`、`RESTIC_CACHE_DIR=<DataDir>/restic-cache`、`RESTIC_PROGRESS_FPS=1`
- `backup` 和 `forget` 固定带 `--host <BackupSpec.Host>` 和 `--tag tern-<任务ID>`。`Host` 来自任务里存的值，绝不取 `os.Hostname()`。
- 清锁只用 `restic unlock`，任何地方都不许出现 `--remove-all`。
- AAD：`tasks/<id>/repo_password`。
- 仓库密码：1–1024 字节；不含控制字符；首尾不得是空白。
- 单元测试用假的 `restic` 可执行文件（测试里现写的 shell 脚本：把 argv 和 `RESTIC_*` 环境变量追加到记录文件，按预设顺序输出内容并返回退出码）。真 restic 只出现在任务 8。
- 新增错误码：`recovery_not_confirmed` 400、`wrong_repo_password` 400。

restic 退出码（0.17 起稳定）：

| 码 | 含义 | 处理 |
|---|---|---|
| 0 | 成功 | — |
| 3 | 备份完成，但个别源文件读不了 | 单元状态 `warning`，不重试 |
| 10 | 仓库不存在 | `config` |
| 11 | 仓库被锁 | `unknown`（会重试；重试前 `Cleanup` 清失效锁） |
| 12 | 仓库密码错误 | `auth` |
| 其他非零 | | `engine.ClassifyStderr` 命中则用它的结果，否则 `unknown` |

## Review Focus

1. **源目录存在但是空的**（宿主机共享没挂上，只剩空挂载点）→ 备份失败，不产生空快照。否则保留策略会把好快照轮换掉。（任务 4）
2. **同一个仓库被两个任务或另一台机器同时操作** → 退出码 11 走重试；只清失效锁，不碰别人的活锁。（任务 2、4）
3. **仓库密码首尾带空白、含引号、美元符、中文** → 首尾空白和控制字符在保存时拒绝；其余原样可用，原生 restic 用同一密码能打开。（任务 5、8）
4. **恢复目标目录已存在且非空、在只读挂载上、或落在源目录、数据目录里面** → 提交时 400，不开始恢复。（任务 7）
5. **仓库里有几千个快照** → 列表接口只返回最新 200 条和总数。（任务 7）

---

### Task 1: 排除规则

**Files:**
- Create: `internal/engine/filter/filter.go`
- Modify: `internal/model/task_validate.go`（对 `Excludes` 逐条调 `filter.Validate`）
- Test: `internal/engine/filter/filter_test.go`、`internal/model/task_test.go`

**Interfaces:**
- Produces:

```go
func Validate(rule string) error
func Match(rules []string, relPath string, isDir bool) bool // 参考实现；relPath 相对同步根，用 "/" 分隔，不带前导 "/"
func ToRclone(rules []string) []string                      // --filter-from 文件的行
func ToRsync(rules []string) []string                       // --exclude-from 文件的行
func ToRestic(rules []string, sources []string) []string    // --exclude-file 文件的行
```

规则语法，三种工具一致：
- `*` 匹配除 `/` 外的任意字符；`**` 可以跨目录；`?` 匹配单个非 `/` 字符；`[abc]` 字符集。
- **名字规则**：不含 `/`（末尾的 `/` 不算）。匹配任意层级的文件名或目录名。例：`*.tmp`、`node_modules/`。
- **锚定规则**：以 `/` 开头。从同步根（备份时是每个源目录）开始匹配。例：`/cache/`、`/a/**/x.log`。
- 以 `/` 结尾表示只匹配目录，连同其下全部内容。
- 拒绝：空串；中间含 `/` 却不以 `/` 开头（`a/b`）；以 `!`、`#`、`;`、`+ `、`- ` 开头；含 `{`、`}`、`\`、控制字符。

翻译：
- rclone：每条前面加 `- `，其余原样。
- rsync：原样。
- restic：名字规则原样；锚定规则对每个源目录各出一行 `<源目录><规则>`；末尾的 `/` 去掉；`$` 写成 `$$`（restic 会对排除文件做环境变量展开）。

已知差异，写进代码注释：restic 不区分“只匹配目录”，`name/` 在备份里按 `name` 处理。

- [ ] **Step 1: 写失败的测试**

```go
func TestValidate(t *testing.T)   // 合法："*.tmp"、"node_modules/"、"/cache/"、"/a/b/*.log"、"/a/**/x.log"、"[Tt]humbs.db"
                                  // 非法：""、"a/b"、"!x"、"#x"、";x"、"- x"、"+ x"、"a{b,c}"、"a\\b"、"a\nb"
func TestMatch(t *testing.T)      // 表驱动：
                                  //   ("*.tmp", "a/b/x.tmp", 文件) → 真；("*.tmp", "x.tmp.bak", 文件) → 假
                                  //   ("node_modules/", "a/node_modules", 目录) → 真；同路径但是文件 → 假；("node_modules/", "a/node_modules/x.js", 文件) → 真
                                  //   ("/cache/", "cache", 目录) → 真；("/cache/", "a/cache", 目录) → 假
                                  //   ("/a/**/x.log", "a/b/c/x.log", 文件) → 真；("/a/*/x.log", "a/b/c/x.log", 文件) → 假
                                  //   ("?.txt", "ab.txt", 文件) → 假
func TestToRclone(t *testing.T)   // ["*.tmp", "/cache/"] → ["- *.tmp", "- /cache/"]
func TestToRsync(t *testing.T)    // ["*.tmp", "/cache/"] → 原样
func TestToRestic(t *testing.T)   // 源 ["/sources/a", "/sources/b"]，规则 ["*.tmp", "/cache/", "pri$ce"] → ["*.tmp", "/sources/a/cache", "/sources/b/cache", "pri$$ce"]
func TestTaskValidateExcludes(t *testing.T) // 任务里的 Excludes 含 "a/b" → fields["sync.excludes[0]"] 或 fields["backup.excludes[0]"]
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/filter/`
Expected: FAIL，`undefined: Validate`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/engine/filter/ ./internal/model/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine/filter internal/model
git commit -m "feat(filter): exclude rules with one syntax for restic, rclone and rsync"
```

---

### Task 2: restic 调用与错误分类

**Files:**
- Create: `internal/engine/classify.go`、`internal/engine/access.go`
- Create: `internal/engine/restic/invoke.go`、`internal/engine/restic/exit.go`、`internal/engine/restic/fakebin_test.go`
- Test: `internal/engine/classify_test.go`、`internal/engine/restic/invoke_test.go`

**Interfaces:**
- Consumes: `proc.Cmd`、`rundir.Dir`、`access.Access`
- Produces:

```go
// internal/engine
func ClassifyStderr(tail string) (model.ErrKind, bool) // 第二个返回值为假表示没认出来
type AccessOpener interface { // access.Opener 满足它；测试里用假实现
    Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (access.Access, func(), error)
    Check(ctx context.Context, connID string) error
}

// internal/engine/restic
type Invocation struct {
    Bin         string // 默认 "restic"
    SSHConfig   string
    Dir         *rundir.Dir
    DataDir     string
    RepoPath    string
    Connections int
}
func (iv Invocation) Cmd(args ...string) proc.Cmd     // 固定参数在前，args 在后
func (iv Invocation) WritePassword(pw []byte) error   // <Dir>/repo-password，0600
func ExitKind(code int, stderrTail string) (kind model.ErrKind, warning bool)

// 测试辅助（fakebin_test.go）
func fakeRestic(t *testing.T, steps ...fakeStep) (bin string, calls func() []fakeCall)
type fakeStep struct { Stdout string; Exit int }
type fakeCall struct { Args []string; Env map[string]string }
```

`ClassifyStderr` 不区分大小写，按下面的顺序找第一个命中的：

| 顺序 | 包含任一子串 | 结果 |
|---|---|---|
| 1 | `host key verification failed`、`remote host identification has changed` | `host_key` |
| 2 | `permission denied (publickey`、`too many authentication failures`、`401 unauthorized`、`403 forbidden` | `auth` |
| 3 | `rsync: command not found`、`rsync: not found` | `config` |
| 4 | `connection timed out`、`connection refused`、`connection reset`、`connection closed`、`broken pipe`、`no route to host`、`network is unreachable`、`could not resolve hostname`、`temporary failure in name resolution`、`not responding`、`i/o timeout`、`unexpected eof`、`connection lost` | `network` |

- [ ] **Step 1: 写失败的测试**

```go
// internal/engine/classify_test.go
func TestClassifyStderr(t *testing.T)  // 表驱动：
                                       //   "Host key verification failed." → host_key
                                       //   "backup@nas2: Permission denied (publickey)." → auth
                                       //   "Timeout, server 10.0.0.2 not responding." → network
                                       //   "ssh: connect to host nas2 port 22: Connection timed out" → network
                                       //   "client_loop: send disconnect: Broken pipe" → network
                                       //   "ssh: Could not resolve hostname nas2: Temporary failure in name resolution" → network
                                       //   "bash: line 1: rsync: command not found" → config
                                       //   "something else entirely" → ("", false)
func TestClassifyOrder(t *testing.T)   // "Connection closed by 10.0.0.2\nHost key verification failed." → host_key
func TestClassifyCaseInsensitive(t *testing.T) // "CONNECTION RESET BY PEER" → network

// internal/engine/restic/invoke_test.go
func TestCmdGlobals(t *testing.T)      // Cmd("snapshots") 的 Args == ["-r","sftp:tern-target:/srv/repo","-o",`sftp.command=ssh -F "<dir>/ssh_config" tern-target -s sftp`,"-o","sftp.connections=5","snapshots"]
func TestCmdEnv(t *testing.T)          // Env 恰好含 RESTIC_PASSWORD_FILE=<dir>/repo-password、RESTIC_CACHE_DIR=<DataDir>/restic-cache、RESTIC_PROGRESS_FPS=1
func TestPasswordFile(t *testing.T)    // WritePassword 后文件权限 0600、内容逐字节相等、没有多加换行
func TestNoPasswordInCmd(t *testing.T) // Args 和 Env 的任何元素都不含密码原文
func TestExitKind(t *testing.T)        // 0→("",假)；3→("",真)；10→(config,假)；11→(unknown,假)；12→(auth,假)；1+"Connection reset by peer"→network；1+""→unknown
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/ ./internal/engine/restic/`
Expected: FAIL，`undefined: ClassifyStderr`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/engine/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine
git commit -m "feat(restic): invocation builder and error classification"
```

---

### Task 3: 进度与摘要解析

**Files:**
- Create: `internal/engine/restic/parse.go`
- Test: `internal/engine/restic/parse_test.go`

**Interfaces:**
- Produces:

```go
type Summary struct {
    SnapshotID string
    FilesNew, FilesChanged, FilesUnmodified int64
    DataAdded, BytesProcessed int64
    FilesRestored, BytesRestored int64 // 仅 restore
}
func NewParser(onProgress func(model.Progress), onItemError func(item, msg string)) *Parser
func (p *Parser) Line(b []byte)        // 不是 JSON 的行忽略
func (p *Parser) Summary() (Summary, bool)
```

`SpeedBps` 由 `bytes_done / seconds_elapsed` 算出（`seconds_elapsed` 为 0 时取 0）。

- [ ] **Step 1: 写失败的测试**（输入就是下面这些字面量）

```go
func TestBackupStatus(t *testing.T)
// {"message_type":"status","seconds_elapsed":10,"seconds_remaining":40,"percent_done":0.2,"total_files":10,"files_done":2,"total_bytes":1000,"bytes_done":200}
// → Progress{BytesDone:200, BytesTotal:1000, FilesDone:2, FilesTotal:10, SpeedBps:20, ETASec:40}

func TestBackupSummary(t *testing.T)
// {"message_type":"summary","files_new":3,"files_changed":1,"files_unmodified":6,"data_added":4096,"total_files_processed":10,"total_bytes_processed":1000,"snapshot_id":"abcd1234"}
// → Summary{SnapshotID:"abcd1234", FilesNew:3, FilesChanged:1, FilesUnmodified:6, DataAdded:4096, BytesProcessed:1000}

func TestItemError(t *testing.T)
// {"message_type":"error","error":{"message":"open /x: permission denied"},"during":"archival","item":"/x"}
// → onItemError("/x", "open /x: permission denied")

func TestRestoreStatus(t *testing.T)
// {"message_type":"status","seconds_elapsed":5,"percent_done":0.5,"total_files":4,"files_restored":2,"total_bytes":800,"bytes_restored":400}
// → Progress{BytesDone:400, BytesTotal:800, FilesDone:2, FilesTotal:4, SpeedBps:80}

func TestRestoreSummary(t *testing.T)
// {"message_type":"summary","total_files":4,"files_restored":4,"total_bytes":800,"bytes_restored":800}
// → Summary{FilesRestored:4, BytesRestored:800}

func TestGarbageIgnored(t *testing.T)   // "not json"、""、`{"message_type":"status",`、`{"message_type":"future_type"}` → 不 panic，没有回调
func TestNoSummary(t *testing.T)        // 只喂 status 行 → Summary() 第二个返回值为假
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/restic/ -run 'Status|Summary|ItemError|Garbage'`
Expected: FAIL，`undefined: NewParser`

- [ ] **Step 3: 实现 `Parser`**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/engine/restic/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine/restic
git commit -m "feat(restic): json progress and summary parsing"
```

---

### Task 4: 备份引擎

**Files:**
- Create: `internal/engine/restic/engine.go`、`internal/engine/restic/backup.go`
- Modify: `internal/app/app.go`（注册 `model.TaskType("backup")` 的 Builder）
- Test: `internal/engine/restic/backup_test.go`

**Interfaces:**
- Consumes: 任务 1–3；`engine.Engine`、`engine.Request`、`engine.Env`；`store.TaskRepoPassword`、`store.GetConnection`；`vault.Open`
- Produces:

```go
type Deps struct {
    Access  engine.AccessOpener
    Store   *store.Store
    Vault   *vault.Vault
    DataDir string
    Bin     string // 默认 "restic"
}
func NewBuilder(d Deps) engine.Builder // req.Kind：backup → 本任务；prune、check → 任务 6；restore → 任务 7；其他 → KindConfig
```

备份引擎的行为：

- `Units()`：`["backup"]`。
- `Preflight`，按顺序，任何一步失败即返回：
  1. 每个 `Source` 必须存在且是目录，否则 `KindConfig`，Msg `source missing: <路径>`。
  2. 每个 `Source` 至少有一个目录项，否则 `KindConfig`，Msg `source is empty: <路径> (is the share mounted?)`。
  3. `Access.Check`。
  4. 打开访问物料、写密码文件、把密码 `Redact.Add`，然后 `restic cat config`，退出码按表处理。
- `RunUnit`，按顺序：
  1. `restic unlock`（失败只记日志，继续）。
  2. `restic backup --json --host <Host> --tag tern-<任务ID> --exclude-file <运行目录>/excludes [--limit-upload <KiB>] <各源目录…>`。排除文件内容是 `filter.ToRestic(Excludes, Sources)`。stdout 逐行喂 `Parser`，stderr 逐行进日志；`onItemError` 的每一项写一行日志。
  3. 退出码 0 或 3 时，若 `Keep` 任一项大于 0：`restic forget --host <Host> --tag tern-<任务ID> --group-by host,tags` 加上非零的 `--keep-last N`、`--keep-daily N`、`--keep-weekly N`、`--keep-monthly N`。`forget` 失败 → 单元状态 `warning`，不算备份失败。
  4. 返回 `UnitResult{Name:"backup", Bytes: DataAdded, Files: FilesNew + FilesChanged}`。退出码 3 → 状态 `warning`。
- `Cleanup`：`restic unlock`。
- `Close`（实现 `io.Closer`）：关闭 `Preflight` 里打开的访问物料。访问物料在一次运行里只打开一次，重试时复用。
- `Describe()`，三行，`<…>` 处填真实值，密码用占位符：

```
export RESTIC_REPOSITORY='<RecoveryRepository 的结果，见任务 5>'
export RESTIC_PASSWORD='<仓库密码>'
restic backup --host <Host> --tag tern-<任务ID> <源目录…>
```

- [ ] **Step 1: 写失败的测试**（假 restic、假 `AccessOpener`）

```go
func TestPreflightMissingSource(t *testing.T) // → config，Msg 含 "source missing: /sources/photos"；restic 零调用
func TestPreflightEmptySource(t *testing.T)   // 源是空目录 → config，Msg 含 "source is empty"；restic 零调用       ← Review Focus 1
func TestPreflightRepoMissing(t *testing.T)   // cat config 退出 10 → config
func TestPreflightWrongPassword(t *testing.T) // 退出 12 → auth
func TestPreflightNetwork(t *testing.T)       // Access.Check 返回 network → network；restic 零调用
func TestBackupArgs(t *testing.T)             // backup 那次调用含 "--json"、"--host","nas-1"、"--tag","tern-<id>"、"--exclude-file",<路径>、"--limit-upload","1024"，末尾是各源目录
func TestNoLimitFlagWhenZero(t *testing.T)    // LimitUploadKiB 0 → 没有 "--limit-upload"
func TestHostFromSpec(t *testing.T)           // 不论 os.Hostname() 是什么，--host 恒为 Spec.Host
func TestExcludeFileContent(t *testing.T)     // 文件内容等于 filter.ToRestic 的结果按行拼接
func TestCallOrder(t *testing.T)              // 子命令顺序：unlock、backup、forget
func TestForgetArgs(t *testing.T)             // Keep{Last:7, Daily:0, Weekly:4, Monthly:0} → 含 "--keep-last","7"、"--keep-weekly","4"，不含 daily 和 monthly；含 "--group-by","host,tags"
func TestNoForgetWhenKeepUnset(t *testing.T)  // Keep 全 0 → 没有 forget 调用
func TestForgetFailureIsWarning(t *testing.T) // forget 退出 1 → 单元 warning，error 为 nil
func TestExit3IsWarning(t *testing.T)         // backup 退出 3 → 单元 warning，error 为 nil，之后仍调用 forget
func TestExit11Retryable(t *testing.T)        // backup 退出 11 → KindOf(err).Retryable() 为真                    ← Review Focus 2
func TestNeverRemoveAll(t *testing.T)         // 所有调用的 argv 都不含 "--remove-all"                            ← Review Focus 2
func TestUnitResultNumbers(t *testing.T)      // 摘要 data_added 4096、files_new 3、files_changed 1 → Bytes 4096、Files 4
func TestPasswordHandling(t *testing.T)       // 假 restic 记到的 argv 和 env 都不含密码原文；RESTIC_PASSWORD_FILE 指向的文件权限 0600；往 env.Log 写密码 → 被打码
func TestCleanupUnlocks(t *testing.T)         // Cleanup → 一次 "unlock" 调用
func TestAccessOpenedOnceClosedOnce(t *testing.T) // Preflight 调两次（模拟重试）→ AccessOpener.Open 只被调一次；Close 后它返回的关闭函数被调一次
func TestDescribe(t *testing.T)               // 第三行 == "restic backup --host nas-1 --tag tern-<id> /sources/photos"；三行里都没有真实密码
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/restic/ -run 'Preflight|Backup|Forget|Exit|Unit|Password|Cleanup|Describe|Host|Order|Limit|Exclude|Never'`
Expected: FAIL，`undefined: NewBuilder`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/engine/restic/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine/restic internal/app
git commit -m "feat(restic): backup engine with retention and stale lock cleanup"
```

---

### Task 5: 创建备份任务：仓库接入与恢复信息

**Files:**
- Create: `internal/engine/restic/repo.go`、`internal/api/tasks_backup.go`
- Modify: `internal/api/tasks.go`（`POST /api/v1/tasks` 遇到备份类型时转给本任务的逻辑）
- Test: `internal/engine/restic/repo_test.go`、`internal/api/tasks_backup_test.go`

**Interfaces:**
- Consumes: 任务 2、4
- Produces:

```go
// internal/engine/restic
type RepoState string // "new" | "existing"
func Attach(ctx context.Context, iv Invocation, password []byte) (RepoState, error) // cat config：0 → existing；10 → 执行 init → new；12 → KindAuth
func RecoveryRepository(c model.Connection, repoPath string) string
type Recovery struct { Repository, Password string; Commands []string }
func RecoveryInfo(c model.Connection, repoPath, password string) Recovery
func ValidateRepoPassword(pw string) error
func GenerateRepoPassword() string // 32 个字符，取自 [A-Za-z0-9]，crypto/rand
```

`RecoveryRepository` 的格式：
- 端口 22 且主机不是 IPv6：`sftp:<用户>@<主机>:<RepoPath>`
- 否则：`sftp://<用户>@<主机>:<端口>/<RepoPath>`，IPv6 主机加方括号。`RepoPath` 是绝对路径时结果里会出现 `//`，这是 restic 的写法。

`Recovery.Commands`：

```
export RESTIC_REPOSITORY='<Repository>'
export RESTIC_PASSWORD='<Password，单引号内的单引号写作 '\''>'
restic snapshots
restic restore latest --target /path/to/restore
```

路由：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `POST /api/v1/tasks/repo-password` | — | 200 `{password}` |
| `POST /api/v1/tasks`（备份） | `model.Task` 另加 `repo_password`、`recovery_confirmed` | 201，`model.Task` 另加 `repo_state` |
| `POST /api/v1/tasks/{id}/recovery-info` | `{password}`（管理员密码） | 200 `{repository, password, commands}` |

创建备份任务的顺序：`recovery_confirmed` 不为真 → 400 `recovery_not_confirmed` → `ValidateRepoPassword` → 把 `backup.host` 设为当前 `node.name`（客户端传的值忽略）→ `Validate` 与连接检查 → `Access.Check`，失败 → 502 `unreachable` → `Attach`，密码错 → 400 `wrong_repo_password` → 加密密码并落库。**前面任何一步失败，库里都不留任务。**

`recovery-info`：管理员密码错 → 403 `forbidden`，并记入登录限速。`PUT /api/v1/tasks/{id}` 的请求体带 `repo_password` → 400（第一版不支持改仓库密码）。`GET` 任务永远不返回仓库密码。

- [ ] **Step 1: 写失败的测试**

```go
// internal/engine/restic/repo_test.go
func TestAttachExisting(t *testing.T)           // cat config 退出 0 → "existing"；没有 init 调用
func TestAttachNew(t *testing.T)                // 退出 10 → 调用 init → "new"
func TestAttachWrongPassword(t *testing.T)      // 退出 12 → KindOf(err) == auth；没有 init 调用
func TestRecoveryRepository(t *testing.T)       // ("backup","nas2.example",22,"/srv/repo") → "sftp:backup@nas2.example:/srv/repo"
                                                // 端口 2222 → "sftp://backup@nas2.example:2222//srv/repo"
                                                // 主机 "fd7a:115c::1" → "sftp://backup@[fd7a:115c::1]:22//srv/repo"
func TestRecoveryCommandsQuote(t *testing.T)    // 密码 "it's" → 第二行 == `export RESTIC_PASSWORD='it'\''s'`
func TestValidateRepoPassword(t *testing.T)     // " lead"、"trail "、"a\nb"、""、1025 字节 → error；`p@$$'w"ö密码` → nil       ← Review Focus 3
func TestGenerateRepoPassword(t *testing.T)     // 长 32；匹配 ^[A-Za-z0-9]{32}$；两次不同

// internal/api/tasks_backup_test.go
func TestCreateBackupNeedsConfirmation(t *testing.T) // recovery_confirmed 缺失或为假 → 400 recovery_not_confirmed；tasks 表为空；restic 零调用
func TestCreateBackupNewRepo(t *testing.T)           // → 201，repo_state "new"
func TestCreateBackupExistingRepo(t *testing.T)      // → 201，repo_state "existing"
func TestCreateBackupWrongPassword(t *testing.T)     // → 400 wrong_repo_password；tasks 表为空
func TestCreateBackupUnreachable(t *testing.T)       // Access.Check 返回 network → 502 unreachable；tasks 表为空
func TestCreateBackupBadPassword(t *testing.T)       // repo_password " x" → 400 validation
func TestHostFromNodeName(t *testing.T)              // node.name "nas-1"，请求里 backup.host 写 "evil" → 存下来的是 "nas-1"；之后把节点名改成 "nas-2"，任务里仍是 "nas-1"
func TestRepoPasswordSealed(t *testing.T)            // 直接查库：repo_password 列以 "v1." 开头；GET /tasks/{id} 的原始响应体不含密码
func TestRecoveryInfo(t *testing.T)                  // 管理员密码对 → repository == "sftp:backup@nas2.example:/srv/repo"，password 与创建时一致
func TestRecoveryInfoWrongAdminPassword(t *testing.T)// → 403；连错 5 次 → 429
func TestUpdateRejectsRepoPassword(t *testing.T)     // PUT 带 repo_password → 400
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/restic/ ./internal/api/ -run 'Attach|Recovery|RepoPassword|CreateBackup|HostFrom|UpdateRejects'`
Expected: FAIL，`undefined: Attach`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine/restic internal/api
git commit -m "feat(backup): repository attach on task creation and recovery info"
```

---

### Task 6: 清理与校验周期

**Files:**
- Create: `internal/engine/restic/maintenance.go`、`internal/scheduler/maintenance.go`
- Modify: `internal/scheduler/scheduler.go`（每次运行结束后调 `afterRun`）、`internal/api/overview.go`（加维护信息）
- Test: `internal/engine/restic/maintenance_test.go`、`internal/scheduler/maintenance_test.go`

**Interfaces:**
- Consumes: 任务 4 的 `NewBuilder`；`store.GetTaskState/SetTaskState`；`Scheduler.Enqueue`
- Produces:

```go
// internal/scheduler
func Due(last, now time.Time, everyDays int) bool // everyDays == 0 → 假；last 为零值 → 真；否则 now - last >= everyDays 天
```

引擎：
- `prune`：`Units()` 为 `["prune"]`；`Preflight` 是 `Access.Check`；`RunUnit` 依次 `restic unlock`、`restic prune`。
- `check`：`Units()` 为 `["check"]`；`RunUnit` 是 `restic check`。
- 两者 `Cleanup` 都是 `restic unlock`，退出码都按本计划的表处理。

调度规则（`afterRun`）：
- 一次 `backup` 运行以 `success` 或 `warning` 结束后：`Due(LastPruneAt, now, PruneEveryDays)` 为真 → 入队一次 `prune`，触发方式 `schedule`。
- 一次 `backup` 以 `success`/`warning` 结束且没有入队 `prune`，或一次 `prune` 结束（不论成败）后：`Due(LastCheckAt, now, CheckEveryDays)` 为真 → 入队一次 `check`。
- `prune` 成功 → `LastPruneAt = now`；`check` 成功 → `LastCheckAt = now`。失败不更新，下次备份成功后再试。
- 手动触发走 `POST /api/v1/tasks/{id}/run`，`kind` 为 `prune` 或 `check`，不受周期限制。

概览每项新增：`last_prune_at`、`last_check_at`、`last_check_status`（该任务最近一次 `check` 运行的状态，没有则为空串）。

- [ ] **Step 1: 写失败的测试**

```go
// internal/scheduler/maintenance_test.go
func TestDue(t *testing.T)                        // (零值, now, 7) → 真；(6 天前, now, 7) → 假；(恰好 7 天前, now, 7) → 真；(任意, now, 0) → 假
func TestBackupSuccessEnqueuesPrune(t *testing.T) // LastPruneAt 8 天前 → 备份成功后出现一条 kind prune、trigger schedule 的运行
func TestBackupWarningEnqueuesPrune(t *testing.T)
func TestBackupFailureNoMaintenance(t *testing.T) // 备份 failed → 没有 prune 也没有 check
func TestPruneNotDue(t *testing.T)                // LastPruneAt 1 天前 → 没有 prune
func TestCheckAfterPrune(t *testing.T)            // 两者都到期 → 顺序是 backup、prune、check，三者不重叠
func TestCheckWithoutPrune(t *testing.T)          // 只有 check 到期 → backup 之后直接 check
func TestPruneUpdatesState(t *testing.T)          // prune 成功 → LastPruneAt == now；失败 → 不变
func TestCheckFailureIsolated(t *testing.T)       // check 失败 → 该运行 failed；任务的 last_success_at 不变；概览 last_check_status == "failed"
func TestMaintenanceNoHooks(t *testing.T)         // 任务配了前后置命令 → prune、check 运行期间都没执行

// internal/engine/restic/maintenance_test.go
func TestPruneCalls(t *testing.T)                 // 子命令顺序：unlock、prune
func TestCheckCalls(t *testing.T)                 // 只有 check
func TestPruneLockedRetryable(t *testing.T)       // prune 退出 11 → Retryable                                    ← Review Focus 2
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/scheduler/ ./internal/engine/restic/ -run 'Due|Prune|Check|Maintenance'`
Expected: FAIL，`undefined: Due`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine/restic internal/scheduler internal/api
git commit -m "feat(backup): periodic prune and check as separate runs"
```

---

### Task 7: 快照与恢复

**Files:**
- Create: `internal/mounts/mounts.go`、`internal/mounts/mounts_linux.go`、`internal/mounts/mounts_other.go`
- Create: `internal/engine/restic/snapshots.go`、`internal/engine/restic/restore.go`、`internal/api/snapshots.go`
- Test: `internal/mounts/mounts_linux_test.go`、`internal/engine/restic/snapshots_test.go`、`internal/engine/restic/restore_test.go`、`internal/api/snapshots_test.go`

**Interfaces:**
- Produces:

```go
// internal/mounts
func IsMounted(path string) (bool, error) // Linux：path 最近的已存在祖先目录与 "/" 不在同一设备上 → 真；其他系统恒为真

// internal/engine/restic
type Snapshot struct {
    ID, ShortID string; Time time.Time; Host string
    Paths, Tags []string
    Mine bool // 带 tern-<任务ID> 标签
}
func ListSnapshots(ctx context.Context, iv Invocation, taskID string) ([]Snapshot, error) // restic snapshots --json；按时间倒序
type RestoreParams struct { SnapshotID, Subpath, TargetDir string }
func ParseRestoreParams(m map[string]string) (RestoreParams, error) // 键 snapshot_id、subpath、target_dir
func ValidateRestoreTarget(target string, sources []string, dataDir, secretsDir string, mounted func(string) (bool, error)) error
```

路由：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `GET /api/v1/tasks/{id}/snapshots` | — | 200 `{total, snapshots, repository}`，`snapshots` 最多 200 条，最新的在前 |
| `POST /api/v1/tasks/{id}/restore` | `{snapshot_id, subpath?, target_dir}` | 202 `{run_id}` |

`repository` 是 `RecoveryRepository` 的结果，界面用它拼等价的恢复命令。

规则：
- `snapshot_id` 匹配 `^[0-9a-f]{8,64}$`。`subpath` 为空，或是不含控制字符、`Clean` 后不变的绝对路径。
- `ValidateRestoreTarget`，全部满足才通过：绝对路径且 `Clean` 后不变；不存在，或是空目录；`mounted(target)` 为真；最近的已存在祖先目录可写（在里面建一个临时文件再删掉来验证）；不等于也不位于任何源目录、`dataDir`、`secretsDir` 之内。
- 恢复引擎：`Units()` 为 `["restore"]`；`Preflight` 是 `Access.Check` 加再跑一次 `ValidateRestoreTarget`；`RunUnit` 是 `restic restore <snapshot_id>[:<subpath>] --target <target_dir> --json`，`UnitResult` 的 `Bytes`、`Files` 取 `BytesRestored`、`FilesRestored`。
- 恢复走调度器的同一个队列，`Kind` 为 `restore`，`Trigger` 为 `manual`，不执行前后置命令。
- 接口层在入队前先跑一次 `ValidateRestoreTarget`，不通过 → 400 `validation`，`fields["target_dir"]` 给出原因码：`not_absolute`、`not_empty`、`not_mounted`、`not_writable`、`inside_source`、`inside_data`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/mounts/mounts_linux_test.go（//go:build linux）
func TestIsMounted(t *testing.T)               // "/dev/shm/x/y"（不存在）→ 真；根文件系统上的临时目录 → 假

// internal/engine/restic/snapshots_test.go
func TestListSnapshotsParses(t *testing.T)     // 夹具：两个元素的 JSON 数组，其一 tags 含 "tern-<id>" → Mine 真/假；ShortID、Time、Paths 正确；较新的排在前
func TestListSnapshotsEmpty(t *testing.T)      // 输出 "[]" 或 "null" → 空切片，不报错

// internal/engine/restic/restore_test.go
func TestParseRestoreParams(t *testing.T)      // snapshot_id "latest"、"ABCD1234"、"abc" → error；"abcd1234" 通过；subpath "photos" → error，"/photos/2024" 通过
func TestRestoreTargetRules(t *testing.T)      // 表驱动（mounted 注入）：                                      ← Review Focus 4
                                               //   "restore-1"（相对）→ not_absolute
                                               //   已存在且有一个文件 → not_empty
                                               //   mounted 返回假 → not_mounted
                                               //   父目录权限 0500 → not_writable
                                               //   "/sources/photos/r"（源目录内）→ inside_source；等于源目录本身 → inside_source
                                               //   DataDir 之内 → inside_data；SecretsDir 之内 → inside_data
                                               //   不存在的新目录、已存在的空目录 → nil
func TestRestoreArgs(t *testing.T)             // 末尾 == ["restore","abcd1234:/photos/2024","--target","/targets/restore-1","--json"]；无 subpath 时为 "abcd1234"
func TestRestoreUnitResult(t *testing.T)       // 摘要 files_restored 4、bytes_restored 800 → Files 4、Bytes 800

// internal/api/snapshots_test.go
func TestSnapshotsCapped(t *testing.T)         // 假 restic 输出 3000 条 → snapshots 长 200，total 3000，第一条是最新的    ← Review Focus 5
func TestSnapshotsRepository(t *testing.T)     // repository == "sftp:backup@nas2.example:/srv/repo"
func TestRestoreBadTarget(t *testing.T)        // 非空目录 → 400，fields["target_dir"] == "not_empty"；没有新的运行记录
func TestRestoreQueued(t *testing.T)           // 202；GET /runs/{id}：kind restore、trigger manual；任务的前后置命令没被执行
func TestRestoreOnSyncTask(t *testing.T)       // 对同步任务请求恢复 → 400
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/mounts/ ./internal/engine/restic/ ./internal/api/ -run 'Mounted|Snapshot|Restore'`
Expected: FAIL，`undefined: ListSnapshots`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/mounts internal/engine/restic internal/api
git commit -m "feat(backup): snapshot listing and restore"
```

---

### Task 8: 集成测试：真 restic 对真 sshd

**Files:**
- Modify: `test/integration/helpers_test.go`（加应用级辅助）
- Create: `test/integration/backup_test.go`（`//go:build integration`）

**Interfaces:**
- Consumes: `app.Start`、计划 2 任务 6 的 `authorize`、`target`、`remotePath`
- Produces（供计划 5、7 用）：

```go
type testApp struct{ /* 基地址、http.Client（带 cookie jar）、csrf、临时目录 */ }
func mountedDir(t *testing.T) string                 // /dev/shm 下的一个新目录。它在挂载点里，满足 mounts.IsMounted；恢复目标和拉取落地目录都用它
func startApp(t *testing.T) *testApp                 // DataDir、SecretsDir、RuntimeDir 各取一个 mountedDir；走完向导（keyfile 模式，节点名 "nas-1"）
func (a *testApp) do(method, path string, body any, out any) int // 自动带 CSRF 头，返回状态码
func (a *testApp) sshConnection(t *testing.T) string // 生成密钥、授权到 sshd、探测并确认指纹、建连接，返回连接 ID
func (a *testApp) runAndWait(t *testing.T, taskID string, body any) model.Run // POST …/run 后轮询到终态，上限 5 分钟
func writeTree(t *testing.T, dir string, files map[string]string) // 相对路径 → 内容
func randomFile(t *testing.T, path string, size int64)
```

- [ ] **Step 1: 写测试**

```go
func TestBackupThenNativeRestore(t *testing.T)
// 用接口建备份任务并运行 → success。然后**不经过任何 tern 代码**：把 recovery-info 返回的 repository 和 password
// 交给 `restic`（配一份独立写的 ssh 配置和同一把钥匙），执行 snapshots 和 restore latest。
// 恢复出的文件与源逐字节一致。（规格 §16 第 2 条）

func TestSpecialPassword(t *testing.T)          // 仓库密码 `p@$$'w"ö密码 x`：建任务、备份、原生 restic 用同一密码能列出快照     ← Review Focus 3
func TestHostAndParent(t *testing.T)            // 连续两次备份：两个快照的 hostname 都是 "nas-1"（不是容器主机名）；第二个快照的 parent 等于第一个的 ID；第二次 files_new == 0
func TestTaggedRetention(t *testing.T)          // 仓库里先用原生 restic 造 3 个不带标签的快照；任务 Keep{Last:1} 跑两次 → 带 tern 标签的剩 1 个，不带标签的 3 个都还在
func TestStaleLockRecovered(t *testing.T)       // 备份途中 kill -9 restic 进程 → 这次运行失败或重试；再运行一次 → success
func TestUnreadableFileIsWarning(t *testing.T)  // 以非 root 身份跑、源里一个 chmod 000 的文件 → 运行状态 warning，其余文件已备份
func TestEmptySourceFails(t *testing.T)         // 把源目录清空 → failed，error_kind config；快照数没有增加                 ← Review Focus 1
func TestLimitUpload(t *testing.T)              // 32 MiB 随机数据、limit_upload_kib 4096 → 运行耗时 ≥ 6 秒（规格 §16 第 8 条）
func TestInterruptThenResume(t *testing.T)      // 64 MiB 随机数据限速传输，传到约一半时取消；再运行 → 第二次 data_added < 总量的 70%
func TestRestoreViaAPI(t *testing.T)            // 列快照 → 恢复到新目录 → 文件一致；再恢复到同一目录 → 400 not_empty
func TestPruneAndCheck(t *testing.T)            // 手动触发 prune 和 check → 都 success
func TestExitCodesAsDocumented(t *testing.T)    // 直接调 restic：不存在的仓库路径 → 退出 10；错密码 → 12；对已存在仓库 → 0
```

- [ ] **Step 2: 运行**

Run: `make itest`
Expected: 新增测试全部 PASS。有失败就修代码，不改断言；若 `TestExitCodesAsDocumented` 与本计划的退出码表不符，先停下来报告。

- [ ] **Step 3: 提交**

```bash
git add test/integration
git commit -m "test(backup): integration tests against real restic over ssh"
```
