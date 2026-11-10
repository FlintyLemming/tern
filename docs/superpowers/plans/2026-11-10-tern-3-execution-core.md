# tern 计划 3 · 执行核心 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用假引擎就能完整演示“任务 → 排队 → 任务前命令 → 引擎（含重试）→ 任务后命令 → 运行记录”，并通过接口和 SSE 看到全过程。

**Architecture:** `runner` 执行一次运行的管线；`scheduler` 决定何时把哪次运行交给 `runner`，并管并发名额。引擎通过 `engine.Engine` 接口接入，本计划只有测试用的假引擎，真引擎在计划 4、5。所有时间来自可注入的 `Clock`。

**Tech Stack:** Go 1.26、`github.com/robfig/cron/v3`（只用它的表达式解析）、标准库 `os/exec`。

**Spec:** `2026-11-10-nas-backup-sync-design.md` §5、§8、§10、§11、§14。执行者同时读 `docs/superpowers/plans/2026-11-10-tern-0-index.md`。

## Global Constraints

索引第 2 节全部适用。本计划另加：

- 测试不许用 `time.Sleep` 等待真实时间，一律推进假时钟。只有 `proc` 的测试例外，且要把 `proc.KillGrace` 调到 200 毫秒。
- 子进程环境是白名单：`PATH`、`HOME`、`TZ`、`LANG=C.UTF-8`，加上调用方显式传入的。绝不继承 tern 进程自己的环境。
- 运行记录进入终态后不可改：对 `runs` 的每条 `UPDATE` 都带 `WHERE seq IS NULL`。
- 钩子环境变量名：`TERN_TASK_ID`、`TERN_TASK_NAME`、`TERN_TASK_TYPE`、`TERN_TRIGGER`；任务后命令另有 `TERN_RESULT`、`TERN_DURATION_SECONDS`、`TERN_BYTES`、`TERN_FILES`、`TERN_ERROR`。
- 只有 `backup` 和 `sync` 两种运行执行前后置命令；`dry_run`、`restore`、`prune`、`check` 不执行。
- cron 表达式是标准 5 段，按容器时区（`time.Local`）解释。
- 本计划所有路由都带 `NeedSession` 和 `NeedUnlocked`。新增错误码：`task_active` 409、`paused` 503。
- `meta` 新增键：`sched.last_tick`、`runs.seq`、`settings.max_concurrent`（默认 1）、`settings.log_keep_days`（默认 90）、`settings.log_keep_runs`（默认 200）。

## Review Focus

1. **NAS 开机后 NTP 校时，时钟向后跳 1 小时或向前跳 1 天** → 同一个计划时刻不重复触发；向前跳按“错过”处理，最多补一次。（任务 5）
2. **cron 表达式合法但永不触发**（`0 0 31 2 *`）→ 保存时 400，调度器不空转不卡死。（任务 1、5）
3. **任务正在排队或运行时被编辑、停用或删除** → 这次运行用入队时的快照跑完；删除返回 409 `task_active`。（任务 5、6）
4. **引擎或钩子狂打日志、输出没有换行的超长行** → 单行截断到 64 KiB，单次运行日志封顶 64 MiB，不撑爆 `/data`。（任务 1、2）
5. **`/data` 写满，运行记录落库失败** → 这次运行照常收尾（任务后命令照跑），调度器继续工作，错误进进程日志。（任务 4）

---

### Task 1: 任务与运行记录：类型和存储

**Files:**
- Create: `internal/model/task.go`、`internal/model/task_validate.go`、`internal/model/run.go`
- Create: `internal/store/migrations/0003_tasks_runs.sql`、`internal/store/tasks.go`、`internal/store/runs.go`
- Create: `internal/runlog/runlog.go`、`internal/clock/clock.go`
- Test: `internal/model/task_test.go`、`internal/store/tasks_test.go`、`internal/store/runs_test.go`、`internal/runlog/runlog_test.go`

**Interfaces:**
- Produces:

```go
// internal/model/task.go
type TaskType string // "backup" | "sync"
type Task struct {
    ID string; Version int; Type TaskType; Name string; Enabled bool
    Schedule      string // 5 段 cron；空 = 只能手动运行
    CatchUp       bool   // 启动后补跑一次
    StaleAfterSec int    // 0 = 自动（2 × 计划周期）
    MaxRuntimeSec int    // 0 = 不限
    Retry         RetryPolicy
    Pre, Post     Hook
    Backup        *BackupSpec
    Sync          *SyncSpec
    CreatedAt, UpdatedAt time.Time
}
type RetryPolicy struct { Retries int; DelaysSec []int } // 第 n 次重试前等 DelaysSec[min(n, len)-1] 秒
type Hook struct {
    Script        string
    Where         string // "local" | "ssh"
    ConnectionID  string // Where == "ssh" 时必填
    TimeoutSec    int
    IgnoreFailure bool   // 只对 Pre 有效
}
type BackupSpec struct {
    ConnectionID, RepoPath string
    Host           string   // restic 的 --host；创建时定下，之后不可改
    Sources, Excludes []string
    LimitUploadKiB int      // 0 = 不限
    Connections    int      // restic 的 sftp.connections
    Keep           KeepPolicy
    PruneEveryDays, CheckEveryDays int // 0 = 不自动跑
}
type KeepPolicy struct { Last, Daily, Weekly, Monthly int }
type SyncSpec struct {
    Protocol     string // "sftp" | "rsync" | "webdav"
    ConnectionID string
    Direction    string // "push" | "pull"
    Pairs        []FolderPair
    DeleteMode   string // "keep" | "mirror"
    MaxDeletes   int
    Excludes     []string
    Compare      string // "size_mtime" | "checksum"
    LimitKiB     int    // 0 = 不限
    Transfers    int    // 并发文件数；rsync 必须为 0
}
type FolderPair struct { Local, Remote string }

func (t *Task) ApplyDefaults()
func (t Task) Validate() map[string]string // 键形如 "schedule"、"sync.transfers"、"sync.pairs[1].local"
func (t Task) ConnectionIDs() []string     // 主连接加钩子引用的连接，去重

// internal/model/run.go
type RunKind string // "backup" | "sync" | "dry_run" | "restore" | "prune" | "check"
type Trigger string // "schedule" | "manual" | "catch_up"
type Status string  // 进行中："queued" | "running" | "retry_wait"；终态：索引里的七种
func (s Status) Terminal() bool
type Run struct {
    ID string; Seq int64 // 进入终态时分配，之前为 0
    TaskID, TaskName string; Kind RunKind; Trigger Trigger; Status Status
    StartedAt, FinishedAt time.Time
    Attempts int; Bytes, Files int64
    ErrorKind ErrKind; ErrorSummary string
    Units []UnitResult
}
type UnitResult struct { Name string; Status Status; Bytes, Files int64; Error string } // 同步：每组文件夹对一个；其余：一个
type Progress struct { Unit string; BytesDone, BytesTotal, FilesDone, FilesTotal int64; SpeedBps float64; ETASec int64 }

// internal/clock
type Clock interface { Now() time.Time; After(d time.Duration) <-chan time.Time }
var Real Clock
func NewFake(start time.Time) *Fake
func (f *Fake) Advance(d time.Duration) // 到期的 After 通道按时间顺序触发
func (f *Fake) Set(t time.Time)          // 可以往回拨

// internal/store
type TaskState struct { LastFireAt, LastSuccessAt, LastPruneAt, LastCheckAt time.Time }
func (s *Store) CreateTask(ctx context.Context, t model.Task, repoPasswordSealed string) error
func (s *Store) UpdateTask(ctx context.Context, t model.Task) error // 按 t.Version 乐观锁；不符 → ErrVersion
func (s *Store) GetTask(ctx context.Context, id string) (model.Task, error)
func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error)
func (s *Store) DeleteTask(ctx context.Context, id string) error
func (s *Store) TaskRepoPassword(ctx context.Context, id string) (sealed string, err error)
func (s *Store) TasksUsingConnection(ctx context.Context, connID string) (taskNames []string, err error)
func (s *Store) GetTaskState(ctx context.Context, id string) (TaskState, error)
func (s *Store) SetTaskState(ctx context.Context, id string, fn func(*TaskState)) error

type RunFilter struct { TaskID string; Before time.Time; Limit int }
func (s *Store) CreateRun(ctx context.Context, r model.Run) error
func (s *Store) UpdateRun(ctx context.Context, id string, st model.Status, attempts int) error // 只在进行中状态之间流转
func (s *Store) FinalizeRun(ctx context.Context, r model.Run) (seq int64, err error)           // 已是终态 → ErrFinalized
func (s *Store) InterruptLeftovers(ctx context.Context, now time.Time) (int, error)
func (s *Store) GetRun(ctx context.Context, id string) (model.Run, error)
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]model.Run, error)                 // 按 started_at 倒序
func (s *Store) LastRun(ctx context.Context, taskID string) (model.Run, bool, error)
func (s *Store) PruneRuns(ctx context.Context, olderThan time.Time, keepPerTask int) (removedIDs []string, err error)
var ErrFinalized error

// internal/runlog
func Create(dataDir, runID string) (*Writer, error)  // <dataDir>/logs/<runID>.log，0600
func (w *Writer) Write(p []byte) (int, error)        // 单行超过 64 KiB 截断；累计超过 64 MiB 后写一行 "[log truncated]" 并丢弃后续
func (w *Writer) Close() error
func Read(dataDir, runID string, offset int64, max int) (data []byte, next int64, err error)
func Remove(dataDir string, runIDs []string) error
```

`0003_tasks_runs.sql`：

```sql
CREATE TABLE tasks (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('backup','sync')),
  name TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL,
  spec TEXT NOT NULL,                      -- model.Task 的 JSON，不含任何凭据
  repo_password TEXT NOT NULL DEFAULT '',  -- 密文，仅备份任务
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE task_state (
  task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
  last_fire_at INTEGER NOT NULL DEFAULT 0, last_success_at INTEGER NOT NULL DEFAULT 0,
  last_prune_at INTEGER NOT NULL DEFAULT 0, last_check_at INTEGER NOT NULL DEFAULT 0
) STRICT;
CREATE TABLE runs (
  id TEXT PRIMARY KEY, seq INTEGER UNIQUE,
  task_id TEXT NOT NULL, task_name TEXT NOT NULL,
  kind TEXT NOT NULL, triggered_by TEXT NOT NULL, status TEXT NOT NULL,
  started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0,
  attempts INTEGER NOT NULL DEFAULT 0, bytes INTEGER NOT NULL DEFAULT 0, files INTEGER NOT NULL DEFAULT 0,
  error_kind TEXT NOT NULL DEFAULT '', error_summary TEXT NOT NULL DEFAULT '',
  units TEXT NOT NULL DEFAULT '[]'
) STRICT;
CREATE INDEX runs_by_task ON runs(task_id, started_at DESC);
```

`runs.task_id` 故意不设外键：任务删了，运行记录还在。

默认值（`ApplyDefaults`）：`Retry` 的 `DelaysSec` 为空时整体取 `{3, [60, 300, 900]}`；钩子 `Where` 空 → `local`，`TimeoutSec` 0 → 600；备份 `Connections` 0 → 5，`PruneEveryDays` 0 → 7，`CheckEveryDays` 0 → 30（仅新建时）；同步 `DeleteMode` 空 → `keep`，`MaxDeletes` 0 → 100，`Compare` 空 → `size_mtime`，`Transfers` 0 且协议不是 rsync → 4。

校验规则（`Validate`）：
- `name` 1–64 个字符。
- `schedule` 为空，或能被 `cron.ParseStandard` 解析且从现在起 5 年内至少触发一次。
- 本机路径：绝对路径、`filepath.Clean` 后不变、不含控制字符。远端路径：非空、不含控制字符。
- `type` 与 `Backup`/`Sync` 恰好对应一个非空。
- 备份：`Sources` 至少一项；`RepoPath` 非空。
- 同步：`Protocol`、`Direction`、`DeleteMode`、`Compare` 取值合法；`Pairs` 至少一组；`Protocol == "rsync"` 时 `Transfers` 必须为 0；`MaxDeletes` ≥ 0。
- 钩子：`Where == "ssh"` 必须有 `ConnectionID`；`TimeoutSec` 1–86400。
- 重试：`Retries` 0–10；`DelaysSec` 每项 1–86400。

`FinalizeRun` 在一个事务里做三件事：把 `meta` 的 `runs.seq` 加 1 并写入该行的 `seq`；写终态各字段；若 `Kind` 是 `backup` 或 `sync` 且状态是 `success` 或 `warning`，更新 `task_state.last_success_at`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/model/task_test.go
func TestApplyDefaults(t *testing.T)            // 空 sync 任务 → Retry {3,[60,300,900]}、DeleteMode "keep"、MaxDeletes 100、Compare "size_mtime"、Transfers 4、钩子 TimeoutSec 600
func TestDefaultsRsyncNoTransfers(t *testing.T) // Protocol "rsync" → Transfers 保持 0
func TestRetryZeroRespected(t *testing.T)       // {Retries:0, DelaysSec:[60]} 经 ApplyDefaults 后不变
func TestValidateOK(t *testing.T)               // 一个完整的 sync 任务和一个完整的 backup 任务 → 空 map
func TestValidateRsyncTransfers(t *testing.T)   // rsync + Transfers 4 → fields["sync.transfers"]
func TestValidatePaths(t *testing.T)            // "relative/x"、"/a/../b"、"/a\nb" → fields["sync.pairs[0].local"]
func TestValidateSchedule(t *testing.T)         // "0 3 * * *" 合法；"nope" → fields["schedule"]；"0 0 31 2 *" → fields["schedule"]=="never_fires" ← Review Focus 2
func TestValidateHook(t *testing.T)             // Where "ssh" 无 ConnectionID → fields["pre.connection_id"]
func TestStatusTerminal(t *testing.T)           // 七种终态 → true；queued、running、retry_wait → false
func TestTaskJSONHasNoSecrets(t *testing.T)     // json.Marshal(task) 的键集合里没有 password、private、secret 字样

// internal/store/runs_test.go
func TestFinalizeAssignsMonotonicSeq(t *testing.T) // 三次 Finalize → seq 为 1、2、3
func TestSeqSurvivesPrune(t *testing.T)            // 清掉全部记录后再 Finalize → seq 为 4，不回退
func TestFinalizeTwice(t *testing.T)               // 第二次 → errors.Is(err, ErrFinalized)，记录内容不变
func TestUpdateAfterFinalizeNoop(t *testing.T)     // 终态后 UpdateRun → error，状态不变
func TestLastSuccessUpdated(t *testing.T)          // sync/success 与 backup/warning 更新 last_success_at；partial、failed、以及 kind dry_run/restore/prune/check 都不更新
func TestInterruptLeftovers(t *testing.T)          // 两条 running、一条 queued、一条 success → 返回 3；那三条变 interrupted 且有 seq
func TestListRunsPaging(t *testing.T)              // Limit 2 + Before 翻页，顺序为 started_at 倒序
func TestPruneRuns(t *testing.T)                   // olderThan 之前的终态记录被删；每任务超出 keepPerTask 的最旧记录被删；进行中的不动

// internal/store/tasks_test.go
func TestTaskRoundTrip(t *testing.T)               // Create → Get 深度相等（含 Pairs、Hook）
func TestUpdateTaskVersion(t *testing.T)           // 成功后 Version+1；带旧 Version → ErrVersion
func TestTasksUsingConnection(t *testing.T)        // 主连接和钩子连接都算
func TestDeleteTaskKeepsRuns(t *testing.T)         // 删任务后 ListRuns 仍能查到它的记录

// internal/runlog/runlog_test.go
func TestLogFileMode(t *testing.T)                 // 0600
func TestLongLineTruncated(t *testing.T)           // 写一行 200 KiB → 文件里该行 ≤ 64 KiB + 换行        ← Review Focus 4
func TestLogCap(t *testing.T)                      // 写入 70 MiB → 文件 ≤ 64 MiB + 一行 "[log truncated]"；之后的 Write 仍返回成功 ← Review Focus 4
func TestReadPaging(t *testing.T)                  // offset 续读拼起来等于全文；next 单调不减
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/model/ ./internal/store/ ./internal/runlog/ ./internal/clock/`
Expected: FAIL，`undefined: Task`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/model internal/store internal/runlog internal/clock go.mod go.sum
git commit -m "feat: task and run models, storage and run logs"
```

---

### Task 2: 子进程运行器

**Files:**
- Create: `internal/proc/proc.go`
- Test: `internal/proc/proc_test.go`

**Interfaces:**
- Produces:

```go
type Cmd struct {
    Path  string
    Args  []string
    Env   []string            // 追加在 BaseEnv() 之后
    Dir   string
    Stdin io.Reader
    OnStdout, OnStderr func(line []byte) // 以 \n 或 \r 分行；单行超过 64 KiB 截断
}
type Result struct {
    ExitCode   int
    StderrTail string // stderr 的最后 4 KiB
    Signaled   bool   // 被本包的终止流程结束
}
var KillGrace = 30 * time.Second
func BaseEnv() []string                              // PATH、HOME、TZ（取本进程的值）、LANG=C.UTF-8
func Run(ctx context.Context, c Cmd) (Result, error) // error 只表示没能启动；非零退出码不算 error
```

`ctx` 取消时：对整个进程组发 SIGTERM，等 `KillGrace`，还没退就对进程组发 SIGKILL。子进程放进自己的进程组，这样 rsync、restic 拉起的 `ssh` 也一并结束。

- [ ] **Step 1: 写失败的测试**

```go
func TestExitCode(t *testing.T)               // sh -c 'exit 7' → ExitCode 7，err == nil
func TestStartFailure(t *testing.T)           // 不存在的可执行文件 → err != nil
func TestLinesSplitOnCRAndLF(t *testing.T)    // printf 'a\rb\nc' → OnStdout 依次收到 "a"、"b"、"c"
func TestLongLineTruncated(t *testing.T)      // 输出 200 KiB 无换行 → 回调收到的每一段 ≤ 64 KiB     ← Review Focus 4
func TestStderrTail(t *testing.T)             // stderr 输出 10 KiB → StderrTail 长 4096，是最后那一段
func TestEnvIsAllowlisted(t *testing.T)       // 本进程设 TERN_TEST_CANARY=1；子进程 `env` 的输出不含它，含 PATH、LANG=C.UTF-8 和显式传入的 X=1
func TestStdin(t *testing.T)                  // Stdin "hello" + `cat` → OnStdout 收到 "hello"
func TestCancelGraceful(t *testing.T)         // 子进程收到 TERM 后 exit 0 → 取消后立刻返回，没等满宽限期
func TestCancelTermThenKill(t *testing.T)     // 子进程 trap '' TERM 后 sleep → 取消后约 200ms 返回，Signaled true
func TestKillsWholeProcessGroup(t *testing.T) // sh -c 'sleep 60 & echo $!; wait'，取消后那个 sleep 的 pid 已不存在
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/proc/`
Expected: FAIL，`undefined: Run`

- [ ] **Step 3: 实现 `proc.Run`**

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/proc/ -v`
Expected: PASS（10 个）

- [ ] **Step 5: 提交**

```bash
git add internal/proc
git commit -m "feat(proc): subprocess runner with allowlisted env and group termination"
```

---

### Task 3: 任务前、任务后命令

**Files:**
- Create: `internal/hooks/hooks.go`
- Test: `internal/hooks/hooks_test.go`、`test/integration/hooks_test.go`

**Interfaces:**
- Consumes: `proc.Run`、`access.Opener`、`rundir`、`redact`、`model.Hook`
- Produces:

```go
type Vars struct {
    TaskID, TaskName, TaskType, Trigger string
    Result *ResultVars // 只有任务后命令有
}
type ResultVars struct { Status string; DurationSec, Bytes, Files int64; Error string }

type Runner struct { Access access.Opener; RuntimeDir string; SSHBin string /* 默认 "ssh" */ }
func (r Runner) Run(ctx context.Context, h model.Hook, v Vars, log io.Writer) error // Script 为空 → nil，不起进程
func Environ(v Vars) []string
func shellQuote(s string) string // 单引号包裹，内部单引号写作 '\''
```

执行方式：
- `local`：`/bin/sh -s`，脚本文本走 stdin，`Env` 为 `Environ(v)`。
- `ssh`：`ssh -F <ssh_config> tern-target sh -s`。stdin 先是每个变量一行 `export NAME=<shellQuote(值)>`，再是脚本原文。运行目录用 `rundir.New(RuntimeDir, model.NewID())`，结束即删。
- 超时：`h.TimeoutSec` 秒后走 `proc` 的终止流程，返回的错误信息含 `timeout`。
- 非零退出：返回的错误信息含 `exit <码>`。
- stdout 和 stderr 逐行写进 `log`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/hooks/hooks_test.go
func TestEnviron(t *testing.T)                 // Vars{"t1","照片","sync","manual",nil} → 恰好四项：TERN_TASK_ID=t1、TERN_TASK_NAME=照片、TERN_TASK_TYPE=sync、TERN_TRIGGER=manual
func TestEnvironWithResult(t *testing.T)       // 加 Result{"failed",12,34,5,"boom"} → 另有 TERN_RESULT=failed、TERN_DURATION_SECONDS=12、TERN_BYTES=34、TERN_FILES=5、TERN_ERROR=boom
func TestLocalHookSeesVars(t *testing.T)       // 脚本 `echo "id=$TERN_TASK_ID"` → log 含 "id=t1"
func TestLocalHookNoInheritedEnv(t *testing.T) // 本进程设 TERN_TEST_CANARY=leak；脚本 `env` → log 不含 "leak"
func TestHookNonZero(t *testing.T)             // `exit 3` → error 含 "exit 3"
func TestHookTimeout(t *testing.T)             // TimeoutSec 1、脚本 `sleep 30`、KillGrace 200ms → error 含 "timeout"，总耗时 < 3s
func TestEmptyScriptIsNoop(t *testing.T)       // Script "" → nil
func TestOutputGoesToLog(t *testing.T)         // `echo out; echo err >&2` → log 两行都有
func TestShellQuote(t *testing.T)              // "it's" → `'it'\''s'`；含换行、$、反引号、双引号的值经 `sh -c "printf %s <引用后>"` 还原后与原值相等
func TestRemoteCommandShape(t *testing.T)      // SSHBin 指向一个记录 argv 和 stdin 的假脚本：argv == ["-F", <cfg>, "tern-target", "sh", "-s"]；stdin 以 "export TERN_TASK_ID='t1'\n" 开头，以脚本原文结尾

// test/integration/hooks_test.go（//go:build integration）
func TestRemoteHookRealSSH(t *testing.T)       // 脚本 `printf %s "$TERN_TASK_NAME" > <remotePath>/hook.txt`，TaskName "it's 照片" → 对端该文件内容逐字相等
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/hooks/`
Expected: FAIL，`undefined: Environ`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/hooks/ && make itest`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/hooks test/integration/hooks_test.go
git commit -m "feat(hooks): pre and post task commands, local and over ssh"
```

---

### Task 4: 引擎接口与执行管线

**Files:**
- Create: `internal/engine/engine.go`、`internal/engine/registry.go`、`internal/engine/fake/fake.go`
- Create: `internal/events/hub.go`
- Create: `internal/runner/runner.go`、`internal/runner/status.go`
- Test: `internal/engine/registry_test.go`、`internal/events/hub_test.go`、`internal/runner/runner_test.go`

**Interfaces:**
- Consumes: 任务 1–3 的全部导出符号
- Produces:

```go
// internal/engine
type Env struct {
    Run      model.Run
    Dir      *rundir.Dir
    Log      io.Writer              // 已经过脱敏
    Redact   *redact.Redactor
    Progress func(model.Progress)
}
type Request struct { Kind model.RunKind; AllowDeletes bool; Params map[string]string }
type Engine interface {
    Units() []string                                  // 本次运行的工作单元名，按执行顺序
    Preflight(ctx context.Context, env Env) error     // 返回 *model.KindError
    RunUnit(ctx context.Context, env Env, i int) (model.UnitResult, error) // error 非空 = 该单元失败；为空时 Status 是 success 或 warning
    Cleanup(ctx context.Context, env Env) error       // 每次重试前调用
    Describe() []string                               // 等价命令行，凭据用占位符
}
type Builder func(t model.Task, req Request) (Engine, error)
func NewRegistry() *Registry
func (r *Registry) Register(tt model.TaskType, b Builder)
func (r *Registry) Build(t model.Task, req Request) (Engine, error) // 没注册 → *KindError{KindConfig}

// internal/engine/fake —— 可编排的假引擎，runner、scheduler、api 的测试共用
type Engine struct {
    UnitNames  []string
    Preflights []error           // 第 n 次 Preflight 的返回；用完后返回 nil
    Results    map[int][]Outcome // 单元 i 第 n 次 RunUnit 的结果；用完后重复最后一个
    Block      chan struct{}     // 非 nil 时 RunUnit 阻塞到它关闭或 ctx 结束
}
type Outcome struct { Err error; Status model.Status; Bytes, Files int64 }
func (e *Engine) Calls() []string // 如 ["preflight","unit:0","cleanup","preflight","unit:0"]
func Builder(e *Engine) engine.Builder

// internal/events
type Event struct { Type string; Data any } // "run.updated" | "run.progress" | "task.updated"
func NewHub() *Hub
func (h *Hub) Publish(e Event)                             // 永不阻塞
func (h *Hub) Subscribe(ctx context.Context) <-chan Event  // 缓冲 64；满了先丢该订阅者的 run.progress，再满则关闭它的通道

// internal/runner
var ErrCanceled, ErrShutdown error // 作为 context 的取消原因
type Slot interface { Release(); Acquire(ctx context.Context) error }
type Job struct {
    RunID   string
    Task    model.Task // 入队那一刻的快照
    Req     engine.Request
    Trigger model.Trigger
}
type Runner struct {
    Store      *store.Store
    Registry   *engine.Registry
    Hooks      hooks.Runner
    Events     *events.Hub
    Clock      clock.Clock
    DataDir, RuntimeDir string
    Log        *slog.Logger
}
func (r *Runner) Execute(ctx context.Context, j Job, slot Slot) model.Run
func FinalStatus(units []model.UnitResult, warn bool) model.Status
```

`Execute` 的管线。进入时该运行在库里已是 `queued`，调用方已持有一个执行名额：

1. 状态改 `running`，发布 `run.updated`。建运行目录、日志、脱敏器。`MaxRuntimeSec > 0` 时给 `ctx` 加截止时间。`Registry.Build` 失败 → 直接到第 6 步，状态 `failed`，不跑任何钩子。
2. 任务前命令（仅 `backup`、`sync`）。失败且不是 `IgnoreFailure` → 跳到第 5 步，状态 `failed`，错误摘要以 `pre-task command failed` 开头。
3. 尝试循环，最多 `1 + Retries` 次。每次：
   1. `Attempts` 加 1 并落库。
   2. `Preflight`。失败则本次尝试失败，所有未成功的单元记这个错误，转 3.4。
   3. 按顺序对每个“尚未成功”的单元调 `RunUnit`。一个单元失败不影响后面的单元。
   4. 没有失败单元 → 跳出循环。有失败单元、其中至少一个的 `ErrKind.Retryable()` 为真、且还有重试次数：`Cleanup` → 状态 `retry_wait` → `slot.Release()` → 等 `DelaysSec` → `slot.Acquire()` → 状态 `running` → 下一次尝试。否则跳出循环。
4. `ctx` 结束时按取消原因定状态：`ErrCanceled` → `canceled`；`ErrShutdown` → `interrupted`；超过截止时间 → `failed`，错误摘要 `max runtime exceeded`。
5. 任务后命令（仅 `backup`、`sync`）**总是执行**，用一个不受取消影响的新 `ctx`，只受钩子自己的超时约束。`TERN_RESULT` 是执行它之前算出的状态。失败 → `warn = true`。
6. 状态取第 4 步的结果，否则 `FinalStatus(units, warn)`。`Bytes`、`Files` 是各单元之和。`ErrorKind` 取第一个失败单元的。`FinalizeRun`，发布 `run.updated`，关日志；引擎若实现了 `io.Closer` 就调它的 `Close`（真引擎用它关 agent 等运行期资源）；最后删运行目录。`FinalizeRun` 报错时记进程日志、再试一次，仍失败就带着内存里的结果返回，不 panic。

`FinalStatus`：没有单元或全部失败 → `failed`；有成功有失败 → `partial`；全部成功但有 `warning` 单元或 `warn` 为真 → `warning`；否则 `success`。

进度发布限流：同一运行每秒最多一条 `run.progress`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/engine/registry_test.go
func TestBuildUnregistered(t *testing.T)        // → KindOf(err) == config

// internal/events/hub_test.go
func TestPublishReachesSubscribers(t *testing.T)
func TestSlowSubscriberDoesNotBlock(t *testing.T) // 不读的订阅者 + 发布 10000 条 run.progress → Publish 全部立即返回
func TestUnsubscribeOnCtxDone(t *testing.T)       // ctx 取消后通道关闭，Hub 内部订阅者数回到 0

// internal/runner/runner_test.go（假引擎、假时钟、临时目录里的真 store）
func TestHappyPath(t *testing.T)                // 1 个单元成功 → success，Attempts 1，Seq > 0
func TestRetriesNetworkThenOK(t *testing.T)     // 两次 network 失败后成功 → success，Attempts 3；两次等待分别是 60s、300s
func TestSlotReleasedWhileWaiting(t *testing.T) // 等待重试期间 Release 被调用一次，继续前 Acquire 被调用一次
func TestNoRetryOnNonRetryable(t *testing.T)    // 表驱动：auth、host_key、config、data、guard → failed，Attempts 1
func TestRetryExhausted(t *testing.T)           // 4 次全 network → failed，Attempts 4，ErrorKind network
func TestUnknownIsRetried(t *testing.T)         // unknown 一次后成功 → success，Attempts 2
func TestOnlyFailedUnitsRerun(t *testing.T)     // 单元 0 成功、单元 1 先 network 后成功 → Calls 里 unit 的顺序是 0、1、1
func TestPartial(t *testing.T)                  // 单元 0 成功、单元 1 config 失败 → partial，Units[1].Error 非空
func TestLaterUnitsRunAfterFailure(t *testing.T)// 单元 0 config 失败 → 单元 1 仍被执行
func TestPreflightFailureRetries(t *testing.T)  // Preflight 第一次 network → 那次没有 RunUnit；第二次成功
func TestCleanupBeforeRetry(t *testing.T)       // Calls 里每次重试前有 "cleanup"
func TestHooksOncePerRun(t *testing.T)          // 重试 2 次的运行里，pre、post 各执行 1 次
func TestPreFailSkipsEngine(t *testing.T)       // pre `exit 1` → 引擎零调用，failed，post 仍执行
func TestPreFailIgnored(t *testing.T)           // IgnoreFailure → 引擎照常运行
func TestPostFailIsWarning(t *testing.T)        // 引擎成功 + post `exit 1` → warning
func TestPostSeesResult(t *testing.T)           // 引擎 config 失败时 post 里 $TERN_RESULT == "failed"，$TERN_ERROR 非空
func TestCancelRunsPost(t *testing.T)           // 运行中以 ErrCanceled 取消 → canceled；post 执行过，$TERN_RESULT == "canceled"
func TestShutdownIsInterrupted(t *testing.T)    // 以 ErrShutdown 取消 → interrupted
func TestMaxRuntime(t *testing.T)               // MaxRuntimeSec 60，引擎阻塞，时钟推进 61s → failed，ErrorSummary 含 "max runtime exceeded"
func TestMaxRuntimeCoversRetryWait(t *testing.T)// MaxRuntimeSec 90，network 失败后在等 300s 的途中到点 → failed
func TestNoHooksForOtherKinds(t *testing.T)     // 表驱动 dry_run、restore、prune、check → pre、post 都没执行
func TestRunDirRemoved(t *testing.T)            // 成功、失败、取消三种结局后 RuntimeDir 下都没有残留
func TestEngineClosed(t *testing.T)             // 假引擎实现 io.Closer → 三种结局下 Close 都恰好被调用一次，且在删运行目录之前
func TestFinalizeFailureStillCompletes(t *testing.T) // 运行途中关掉 store → Execute 仍返回，post 已执行，不 panic ← Review Focus 5
func TestFinalStatus(t *testing.T)              // 表驱动覆盖上面四条规则
func TestProgressThrottled(t *testing.T)        // 引擎 1 秒内报 1000 次进度 → 发布的 run.progress ≤ 2 条
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/engine/... ./internal/events/ ./internal/runner/`
Expected: FAIL，`undefined: Runner`

- [ ] **Step 3: 实现**

取消原因用 `context.WithCancelCause` 传递，用 `context.Cause(ctx)` 读取。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/engine/... ./internal/events/ ./internal/runner/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/engine internal/events internal/runner
git commit -m "feat(runner): engine interface and run pipeline with retries and hooks"
```

---

### Task 5: 调度器

**Files:**
- Create: `internal/scheduler/scheduler.go`、`internal/scheduler/cron.go`、`internal/scheduler/stale.go`
- Test: `internal/scheduler/scheduler_test.go`、`internal/scheduler/cron_test.go`

**Interfaces:**
- Consumes: `runner.Runner/Job/Slot`、`store`、`clock.Clock`、`events.Hub`
- Produces:

```go
type Deps struct { Store *store.Store; Runner *runner.Runner; Clock clock.Clock; Events *events.Hub; Log *slog.Logger }
func New(d Deps) *Scheduler
func (s *Scheduler) Start(ctx context.Context) error // InterruptLeftovers → 补跑检查 → 启动主循环
func (s *Scheduler) Reload()                         // 任务或设置变更后调用
func (s *Scheduler) Enqueue(taskID string, req engine.Request, tr model.Trigger) (runID string, err error)
func (s *Scheduler) Cancel(runID string) error
func (s *Scheduler) Active(taskID string) bool       // 该任务有排队中或运行中的运行
func (s *Scheduler) SetPaused(p bool)                // 保险箱未解锁时为 true
func (s *Scheduler) Shutdown(ctx context.Context)    // 以 ErrShutdown 取消全部运行并等它们收尾
var ErrActive, ErrPaused error

func NextRuns(expr string, from time.Time, n int) ([]time.Time, error) // 5 年内凑不够 n 个 → error
func Period(expr string, from time.Time) (time.Duration, error)        // 未来 10 次触发之间的最大间隔
func StaleAfter(t model.Task, now time.Time) time.Duration             // StaleAfterSec；为 0 时 2 × Period；没有计划 → 0
func IsStale(t model.Task, lastSuccess, now time.Time) bool            // StaleAfter 为 0 → false；从未成功时从 t.CreatedAt 起算
```

规则：

- **触发。** 主循环每次醒来，对每个启用且有计划的任务算 `f = next(lastTick)`。当 `f <= now` 且 `f > task_state.last_fire_at` 时处理这个时刻，并把 `last_fire_at` 记为 `f`；中间错过多个时刻也只处理最近的一个。处理完所有任务后把 `now` 写入 `sched.last_tick`。
- **准点还是错过。** `now - f <= 5 分钟` 算准点，触发方式 `schedule`。超过 5 分钟算错过：`CatchUp` 为真 → 触发一次，方式 `catch_up`；否则不触发。进程启动时和时钟向前大跳时都走这条规则。
- **时钟回拨。** `now < lastTick` → 把 `lastTick` 设为 `now`，本轮不触发。因为有 `last_fire_at`，已处理过的时刻不会再来一次。
- **重叠。** 计划触发时该任务已在排队或运行 → 不入队，直接写一条终态为 `skipped` 的运行记录。手动 `Enqueue` 遇到这种情况返回 `ErrActive`。同一任务同一时刻只允许一个运行，不分 `Kind`。
- **并发。** 名额数取 `settings.max_concurrent`，先进先出。运行在 `retry_wait` 期间不占名额。
- **暂停。** `SetPaused(true)` 期间不触发计划，`Enqueue` 返回 `ErrPaused`。恢复后按“错过”规则处理。
- **快照。** `Enqueue` 那一刻读出任务并放进 `Job.Task`，之后对任务的修改不影响这次运行。
- **取消。** 排队中的运行直接终结为 `canceled`；运行中的以 `runner.ErrCanceled` 取消。

- [ ] **Step 1: 写失败的测试**

```go
// internal/scheduler/cron_test.go
func TestNextRuns(t *testing.T)                 // "0 3 * * *" 从 2026-11-10T02:00 起 → 11-10T03:00、11-11T03:00、…
func TestNeverFiringCron(t *testing.T)          // "0 0 31 2 *" → error                                   ← Review Focus 2
func TestPeriod(t *testing.T)                   // "0 3 * * *" → 24h；"0 3 * * 1-5" → 72h；"*/15 * * * *" → 15m
func TestStaleAfter(t *testing.T)               // 每日任务 → 48h；StaleAfterSec 3600 → 1h；Schedule 为空 → 0
func TestIsStale(t *testing.T)                  // 每日任务：49h 前成功 → true；47h 前 → false；从未成功且 49h 前创建 → true；无计划 → false

// internal/scheduler/scheduler_test.go（假引擎、假时钟）
func TestFiresAtCronTime(t *testing.T)          // "0 3 * * *"，时钟 02:59 → 03:00 → 恰好一次运行，trigger schedule
func TestDisabledTaskDoesNotFire(t *testing.T)
func TestOverlapRecordsSkipped(t *testing.T)    // 上一次还在跑时又到点 → 多一条 status skipped 的终态记录；引擎没有第二次调用
func TestConcurrencyOne(t *testing.T)           // 两个任务同时到点 → 一个 running、一个 queued；前者结束后后者才 running
func TestConcurrencyTwo(t *testing.T)           // max_concurrent 2 → 两个同时 running
func TestRetryWaitFreesSlot(t *testing.T)       // 任务 A network 失败进入 retry_wait → 排队的任务 B 立即开始
func TestCatchUpOn(t *testing.T)                // sched.last_tick 为昨天 02:00，现在 09:00，CatchUp 真 → 恰好一次 catch_up
func TestCatchUpOff(t *testing.T)               // 同上但 CatchUp 假 → 零次；下一个 03:00 正常触发
func TestClockJumpBackNoDuplicate(t *testing.T) // 03:00 触发后把时钟拨回 02:30，再走到 03:00 → 没有第二次      ← Review Focus 1
func TestClockJumpForward(t *testing.T)         // 时钟从 02:00 一步跳到次日 09:00（跨过两个时刻）：CatchUp 假 → 零次；真 → 恰好一次 ← Review Focus 1
func TestPaused(t *testing.T)                   // 暂停期间到点 → 不触发；Enqueue → ErrPaused；恢复后按错过规则
func TestEnqueueWhileActive(t *testing.T)       // 手动入队同一任务两次 → 第二次 ErrActive
func TestEnqueueUsesSnapshot(t *testing.T)      // 入队后把任务名改掉 → 这次运行的 TaskName 仍是旧名      ← Review Focus 3
func TestCancelQueued(t *testing.T)             // 取消排队中的运行 → canceled，引擎零调用
func TestCancelRunning(t *testing.T)            // → canceled
func TestStartInterruptsLeftovers(t *testing.T) // 启动前库里有一条 running → 启动后是 interrupted
func TestShutdownWaits(t *testing.T)            // 一个运行在跑时 Shutdown → 返回时它已是 interrupted
func TestReloadPicksUpNewTask(t *testing.T)     // 运行期间新建任务并 Reload → 到点会触发
func TestLastTickPersisted(t *testing.T)        // 推进时钟后 meta 的 sched.last_tick 等于最近一次循环的时间
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/scheduler/`
Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现**

主循环的睡眠时长是“到最近一个触发时刻的间隔”和 1 分钟里较小的那个，用 `Clock.After`。`Reload` 和队列变化通过一个通道唤醒循环。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/scheduler/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/scheduler
git commit -m "feat(scheduler): cron triggers, queueing, catch-up and staleness"
```

---

### Task 6: 任务、运行、概览接口与事件推送

**Files:**
- Create: `internal/api/tasks.go`、`internal/api/runs.go`、`internal/api/overview.go`、`internal/api/events.go`
- Modify: `internal/api/api.go`（`Deps` 加 `Scheduler`、`Registry`、`Events`、`Clock`）、`internal/api/connections.go`（删除前查引用）、`internal/api/session.go`（解锁成功后 `SetPaused(false)`）、`internal/app/app.go`（装配并启动调度器；停机时先 `Scheduler.Shutdown`）
- Test: `internal/api/tasks_test.go`、`internal/api/runs_test.go`、`internal/api/events_test.go`

**Interfaces:**
- Consumes: 任务 1、4、5 的全部导出符号
- Produces（路由）：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `GET /api/v1/tasks` | — | 200，`model.Task` 数组 |
| `POST /api/v1/tasks` | `model.Task`（不含 id、version） | 201 |
| `GET /api/v1/tasks/{id}` | — | 200 |
| `PUT /api/v1/tasks/{id}` | `model.Task`（含 version） | 200 |
| `DELETE /api/v1/tasks/{id}` | — | 204 |
| `POST /api/v1/tasks/{id}/enabled` | `{enabled}` | 200 |
| `POST /api/v1/tasks/{id}/run` | `{kind?, allow_deletes_once?}` | 202 `{run_id}` |
| `GET /api/v1/tasks/{id}/command-line` | — | 200 `{lines}` |
| `POST /api/v1/schedule/preview` | `{schedule}` | 200 `{next, period_sec}`，`next` 为 5 个时间 |
| `GET /api/v1/runs?task_id=&before=&limit=` | — | 200，`model.Run` 数组，`limit` 默认 50、上限 200 |
| `GET /api/v1/runs/{id}` | — | 200 |
| `GET /api/v1/runs/{id}/log?offset=` | — | 200 `{data, next_offset, eof}`，单次最多 256 KiB |
| `POST /api/v1/runs/{id}/cancel` | — | 202 |
| `GET /api/v1/overview` | — | 200 `{tasks}` |
| `GET /api/v1/events` | — | SSE |

- `run` 的 `kind` 缺省时取任务类型对应的 `backup` 或 `sync`；可选值 `dry_run`、`prune`、`check`。`dry_run` 只对同步任务有效，`prune`、`check` 只对备份任务有效，否则 400。
- 概览每项：`{task_id, name, type, enabled, last_status, last_run_at, last_success_at, next_run_at, stale, stale_after_sec, active_run}`。`active_run` 为空或 `{run_id, status, progress}`。
- SSE：事件名即 `Event.Type`，数据为 JSON。`run.updated` 带完整 `model.Run`；`run.progress` 带 `{run_id, progress}`；`task.updated` 带 `{task_id}`。每 20 秒发一行注释 `: ping`。
- `eof` 为真的条件：运行已是终态且已读到文件末尾。
- 创建和修改任务时除 `Validate` 外还要查：引用的连接存在；`sftp`、`rsync` 配 SSH 连接，`webdav` 配 WebDAV 连接，备份配 SSH 连接；`ssh` 钩子配 SSH 连接。不符 → 400 `validation`。
- 修改备份任务时 `backup.host` 与库里不同 → 400 `validation`，`fields["backup.host"] == "immutable"`。
- 删除任务时 `Scheduler.Active` 为真 → 409 `task_active`。任务变更后调 `Scheduler.Reload` 并发布 `task.updated`。
- 删除连接时 `TasksUsingConnection` 非空 → 409 `in_use`。
- `Enqueue` 返回 `ErrActive` → 409 `task_active`；`ErrPaused` → 503 `paused`。

- [ ] **Step 1: 写失败的测试**（API 测试用假引擎注册到两种任务类型上）

```go
// internal/api/tasks_test.go
func TestCreateSyncTask(t *testing.T)            // 201；响应里默认值已填好（retry.retries 3、sync.max_deletes 100）
func TestCreateTaskValidation(t *testing.T)      // rsync + transfers 4 → 400 validation，fields["sync.transfers"] 存在
func TestConnectionTypeMismatch(t *testing.T)    // sftp 协议配 WebDAV 连接 → 400，fields["sync.connection_id"]
func TestUnknownConnection(t *testing.T)         // 不存在的 connection_id → 400
func TestUpdateVersionConflict(t *testing.T)     // → 409 conflict
func TestBackupHostImmutable(t *testing.T)       // PUT 改 backup.host → 400，fields["backup.host"] == "immutable"
func TestDeleteActiveTask(t *testing.T)          // 运行中删除 → 409 task_active                         ← Review Focus 3
func TestEditWhileRunningAllowed(t *testing.T)   // 运行中 PUT → 200；这次运行的 task_name 仍是旧名          ← Review Focus 3
func TestDeleteConnectionInUse(t *testing.T)     // → 409 in_use
func TestSetEnabled(t *testing.T)                // 停用后到点不触发
func TestSchedulePreview(t *testing.T)           // "0 3 * * *" → next 长度 5，period_sec 86400；"nope" → 400
func TestCommandLine(t *testing.T)               // 返回假引擎 Describe 的内容

// internal/api/runs_test.go
func TestRunNow(t *testing.T)                    // 202 带 run_id；随后 GET /runs/{id} 的 status 为 success，trigger manual
func TestRunWhileActive(t *testing.T)            // → 409 task_active
func TestRunKindMismatch(t *testing.T)           // 对同步任务请求 kind "prune" → 400
func TestRunAllowDeletesOnce(t *testing.T)       // 带 allow_deletes_once → Builder 收到的 Request.AllowDeletes 为真；下一次不带则为假
func TestCancelRun(t *testing.T)                 // → 202；最终 status canceled
func TestLogPaging(t *testing.T)                 // 用 next_offset 续读拼出全文；运行结束后 eof 真
func TestRunsFilter(t *testing.T)                // task_id 过滤与 before 翻页
func TestOverview(t *testing.T)                  // 每日任务 49 小时没成功（假时钟）→ stale 真；运行中时 active_run 非空
func TestRunWhenPaused(t *testing.T)             // 保险箱锁定后（走 NeedUnlocked）→ 503 vault_locked

// internal/api/events_test.go
func TestSSEStreamsRunUpdates(t *testing.T)      // 订阅后触发运行 → 依次收到 status 为 queued、running、success 的 run.updated
func TestSSEPing(t *testing.T)                   // 假时钟推进 20s → 收到一行 ": ping"
func TestSSEClientDisconnect(t *testing.T)       // 客户端断开后 Hub 订阅者数回到 0
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/api/ -run 'Task|Run|Overview|SSE|Schedule|CommandLine|InUse'`
Expected: FAIL，编译错误（`Deps` 没有 `Scheduler` 字段）

- [ ] **Step 3: 实现**

`app.Start` 里：保险箱状态不是 `unlocked` 时先 `SetPaused(true)`；`Shutdown` 的顺序是停止接收新请求 → `Scheduler.Shutdown` → 关 store。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/api internal/app
git commit -m "feat(api): tasks, runs, overview and server-sent events"
```

---

### Task 7: 设置、系统信息、记录清理

**Files:**
- Create: `internal/sysinfo/bbr.go`、`internal/sysinfo/tools.go`
- Create: `internal/api/settings.go`、`internal/scheduler/housekeeping.go`
- Test: `internal/sysinfo/sysinfo_test.go`、`internal/api/settings_test.go`、`internal/scheduler/housekeeping_test.go`

**Interfaces:**
- Produces:

```go
// internal/sysinfo
type BBR struct {
    Available []string // tcp_available_congestion_control 的内容
    Current   string   // tcp_congestion_control 的内容
    Supported bool     // Available 含 "bbr"
    Active    bool     // Current == "bbr"
}
func ReadBBR(procRoot string) BBR // 读 <procRoot>/sys/net/ipv4/ 下两个文件；读不到 → 零值，不报错
type Tool struct { Name, Version string; Found bool }
func Tools(ctx context.Context) []Tool // restic、rclone、rsync、ssh；各执行一次版本命令，结果进程内缓存

// internal/scheduler
func (s *Scheduler) Housekeep(ctx context.Context) error // PruneRuns + runlog.Remove；主循环每 24 小时调一次，启动后先调一次
```

路由：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `GET /api/v1/settings` | — | 200 `{node_name, max_concurrent, log_keep_days, log_keep_runs, unlock_mode}` |
| `PUT /api/v1/settings` | `{node_name, max_concurrent, log_keep_days, log_keep_runs}` | 200 |
| `POST /api/v1/settings/password` | `{current_password, new_password}` | 204；作废全部会话 |
| `POST /api/v1/settings/unlock-mode` | `{current_password, mode, passphrase?}` | 204 |
| `GET /api/v1/system` | — | 200 `{version, timezone, bbr, tools}` |

取值范围：`max_concurrent` 1–8；`log_keep_days` 1–3650；`log_keep_runs` 1–10000；`node_name` 规则同向导。`current_password` 错 → 403 `forbidden`，并记入登录限速。改节点名不影响已有备份任务的 `backup.host`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/sysinfo/sysinfo_test.go
func TestReadBBR(t *testing.T)                  // 夹具：available "reno cubic bbr\n"、current "bbr\n" → Supported 真、Active 真、Available 长度 3
func TestReadBBRSupportedNotActive(t *testing.T)// current "cubic" → Supported 真、Active 假
func TestReadBBRUnsupported(t *testing.T)       // available "reno cubic" → Supported 假
func TestReadBBRMissingFiles(t *testing.T)      // 空目录 → 零值
func TestToolsMissingBinary(t *testing.T)       // PATH 设为空目录 → 四项都是 Found 假，不报错

// internal/api/settings_test.go
func TestSettingsDefaults(t *testing.T)         // {max_concurrent:1, log_keep_days:90, log_keep_runs:200}
func TestSettingsValidation(t *testing.T)       // max_concurrent 0 和 9 → 400
func TestSettingsApplyToScheduler(t *testing.T) // 改成 2 后两个任务能同时 running
func TestChangePassword(t *testing.T)           // 旧会话 → 401；旧密码登录失败；新密码登录成功
func TestChangePasswordWrongCurrent(t *testing.T) // → 403；连错 5 次后 → 429
func TestSwitchUnlockMode(t *testing.T)         // keyfile → passphrase 后，之前存的 WebDAV 连接仍能通过“测试连接”的解密步骤
func TestSystemEndpoint(t *testing.T)           // 返回 version、timezone、bbr、tools 四个键

// internal/scheduler/housekeeping_test.go
func TestHousekeepByAge(t *testing.T)           // 91 天前的终态记录和它的日志文件被删；89 天前的还在
func TestHousekeepByCount(t *testing.T)         // 某任务 205 条 → 只剩最新 200 条
func TestHousekeepKeepsActive(t *testing.T)     // 进行中的记录不动
func TestHousekeepRunsDaily(t *testing.T)       // 假时钟推进 24h → 又执行一次
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/sysinfo/ ./internal/api/ ./internal/scheduler/ -run 'BBR|Tools|Settings|Password|Unlock|System|Housekeep'`
Expected: FAIL，`undefined: ReadBBR`

- [ ] **Step 3: 实现**

`GET /api/v1/system` 调 `ReadBBR("/proc")`；`timezone` 取 `time.Local.String()`。

- [ ] **Step 4: 运行全部测试**

Run: `make vet && go test -race ./... && make itest`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sysinfo internal/api internal/scheduler
git commit -m "feat: settings, system info and run record housekeeping"
```
