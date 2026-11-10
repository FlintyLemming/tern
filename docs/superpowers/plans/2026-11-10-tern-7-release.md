# tern 计划 7 · 发布 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 可以交给人部署：多架构镜像、compose 文件、README，并且规格 §16 的九条验收标准每一条都有自动化测试对应。

**Architecture:** 三段式 Dockerfile（前端构建 → Go 构建 → Alpine 运行时）。入口脚本负责 `PUID`/`PGID` 降权。验收测试分两层：在集成环境的 runner 容器里跑的（弱网），和在宿主机上驱动真实镜像的（泄露扫描、重启恢复、端到端）。

**Tech Stack:** Docker buildx、Alpine 3.24、`su-exec`、`tc netem`（弱网模拟）、Playwright（端到端）。

**Spec:** `2026-11-10-nas-backup-sync-design.md` §9.5、§13、§16，以及 §15 的四个待确认项。执行者同时读 `docs/superpowers/plans/2026-11-10-tern-0-index.md`。

## Global Constraints

索引第 2 节全部适用。本计划另加：

- 运行时镜像只装：`rsync`、`openssh-client-default`、`su-exec`、`tzdata`、`ca-certificates`，加上 `restic`、`rclone`、`tern` 三个二进制。不留构建工具。
- 三个基础镜像（`node:26-alpine`、`golang:1.27-alpine`、`alpine:3.24`）都按摘要固定：`FROM <镜像>:<标签>@sha256:<摘要>`。
- 容器默认以 root 运行，compose 里 `cap_drop: [ALL]`，只加回六项：`CHOWN`、`DAC_OVERRIDE`、`DAC_READ_SEARCH`、`FOWNER`、`SETUID`、`SETGID`。设了非零的 `PUID`/`PGID` 时整个进程降权到该用户。
- `deploy/compose.yaml` 与规格 §13 的示例一致，只新增：`stop_grace_period: 2m`、`cap_drop`、`cap_add`、被注释掉的 `PUID`/`PGID` 两行。
- 停机时间预算：`docker stop` 的宽限 2 分钟 > 进程自己的收尾上限 100 秒 > 子进程终止宽限 30 秒 + 停机时任务后命令的上限 60 秒。
- 本计划不向任何镜像仓库推送。镜像名先用本地的 `tern:<版本>`，发布到哪里由人决定。
- 多架构构建需要构建机已启用 buildx 和 arm64 模拟。没有的话停下来问人，不要自己装。
- **验收测试的断言不许为了通过而放宽。** 跑不过就修代码；修不了就停下来报告现象。
- 新增错误码：`data_not_mounted` 400、`secrets_not_mounted` 400。新增环境变量 `TERN_ALLOW_UNMOUNTED`（值为 `1` 时跳过挂载检查，只给开发和测试用）。

## Review Focus

1. **compose 里漏挂了 `/data` 或 `/secrets`** → 数据库或密钥文件会写进容器可写层，重建即丢。向导要拦住，不能等丢了才发现。（任务 2）
2. **设了 `PUID` 但该用户写不了 `/data`**（NAS 上常见的 ACL 问题）→ 入口脚本打印带路径和 uid 的错误后退出，不带着半坏的状态跑。（任务 1）
3. **arm64 镜像里装进了 amd64 的 restic 或 rclone** → 两个架构都要实际执行一次版本命令。（任务 1）
4. **`docker stop` 时任务正在跑** → 宽限期内收尾并记为“被中断”；万一被强杀，下次启动也要把记录改成“被中断”、清掉运行目录残留，下一次备份能自己清锁继续。（任务 2、4）
5. **宿主机没有 `tcp_bbr`，或用了 host 网络** → 界面显示未生效和怎么开，其余功能不受影响；README 写明带着 `sysctls` 那行会起不来。（任务 4、6）

---

### Task 1: 生产镜像与入口脚本

**Files:**
- Create: `deploy/Dockerfile`、`deploy/entrypoint.sh`、`.dockerignore`
- Modify: `cmd/tern/main.go`（加 `--healthcheck`）、`deploy/fetch-tools.sh`（只依赖 busybox 自带的 `wget`、`unzip`、`bunzip2`、`sha256sum`）、`Makefile`
- Test: `cmd/tern/main_test.go`、`test/image/image_test.go`（`//go:build image`，在宿主机上用 docker 命令行驱动）

**Interfaces:**
- Produces:
  - `tern --healthcheck`：按当前环境变量读配置，请求本机的 `/api/v1/status`（配了 TLS 就走 https 并跳过证书校验，因为是回环地址），200 → 退出 0，否则退出 1。
  - `make image VERSION=<v>`：`docker buildx build --platform linux/amd64,linux/arm64 -f deploy/Dockerfile --build-arg VERSION=<v> -t tern:<v> .`
  - `make image-test`：先为当前架构构建并加载镜像，再 `go test -tags image -count=1 ./test/image/...`

`deploy/Dockerfile` 的三段：

| 段 | 基础镜像 | 做什么 |
|---|---|---|
| `web` | `node:26-alpine`，`--platform=$BUILDPLATFORM` | `npm ci`、`npm run build` |
| `build` | `golang:1.27-alpine`，`--platform=$BUILDPLATFORM` | 拷入 `web/dist`；`CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -tags embedweb -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/tern ./cmd/tern` |
| 运行时 | `alpine:3.24` | `apk add --no-cache` 约束里列的五个包；`TARGETARCH=$TARGETARCH sh fetch-tools.sh /usr/local/bin`；拷入 `tern` 和 `entrypoint.sh` |

运行时段另有：`ENV TERN_DATA_DIR=/data TERN_SECRETS_DIR=/secrets TERN_RUNTIME_DIR=/dev/shm/tern`、`EXPOSE 8080`、`HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["tern","--healthcheck"]`、`ENTRYPOINT ["/entrypoint.sh"]`。

`deploy/entrypoint.sh` 的行为：
1. `umask 077`。`PUID`、`PGID` 缺省为 0。
2. 建 `TERN_DATA_DIR` 和 `TERN_RUNTIME_DIR`。
3. `PUID` 为 0 → `exec tern "$@"`。
4. `PUID` 非 0：
   - 把 `TERN_DATA_DIR`（递归）和 `TERN_RUNTIME_DIR` 的属主改成 `PUID:PGID`。改不了不算错，留给下一步判断。
   - 以该用户身份在 `TERN_DATA_DIR` 里创建并删除一个探针文件。失败 → 向 stderr 打印 `tern: uid <PUID> cannot write <TERN_DATA_DIR>`，退出码 1。
   - `TERN_SECRETS_DIR` 存在时同样探测，失败信息格式相同。
   - `exec su-exec "$PUID:$PGID" tern "$@"`。

- [ ] **Step 1: 取三个基础镜像的摘要**

```bash
docker buildx imagetools inspect node:26-alpine   --format '{{.Manifest.Digest}}'
docker buildx imagetools inspect golang:1.27-alpine --format '{{.Manifest.Digest}}'
docker buildx imagetools inspect alpine:3.24       --format '{{.Manifest.Digest}}'
```

Expected: 各输出一行 `sha256:…`。抄进 Dockerfile 的 `FROM` 行。

- [ ] **Step 2: 写失败的测试**

```go
// cmd/tern/main_test.go
func TestHealthcheckOK(t *testing.T)            // 起一个 app，按同样的环境变量跑 healthcheck 逻辑 → 0
func TestHealthcheckDown(t *testing.T)          // 没有服务在听 → 1
func TestHealthcheckTLS(t *testing.T)           // 自签证书的 TLS 服务 → 0

// test/image/image_test.go
func TestToolsOnBothArchitectures(t *testing.T) // 对 linux/amd64 和 linux/arm64 各一次，`docker run --rm --platform <p> --entrypoint <工具> <镜像> …`：
                                                //   restic version 含 "0.19.1"；rclone version 含 "v1.76.0"；rsync --version、ssh -V、tern --version 退出 0    ← Review Focus 3
func TestStartsHealthy(t *testing.T)            // 挂两个临时卷启动 → 60 秒内健康检查为 healthy；宿主机访问 /api/v1/status → 200
func TestRootByDefault(t *testing.T)            // 容器里 tern 进程的 uid 为 0
func TestPUID(t *testing.T)                     // -e PUID=1000 -e PGID=1000 → tern 进程 uid 1000、gid 1000；走完向导后 /data/tern.db 属主 1000:1000、权限 0600；/secrets/master.key 同
func TestPUIDUnwritableData(t *testing.T)       // /data 只读挂载 + PUID=1000 → 容器退出码 1；日志含 "cannot write"、"/data"、"1000"                         ← Review Focus 2
func TestRuntimeDirMustBeTmpfs(t *testing.T)    // -e TERN_RUNTIME_DIR=/data/run → 容器退出码非 0；日志含 "tmpfs"
func TestNoBuildToolsInImage(t *testing.T)      // 镜像里 `which go node npm gcc` 全部找不到
func TestStopIsGraceful(t *testing.T)           // docker stop → 10 秒内退出，退出码 0（没有任务在跑时）
func TestRunsWithDroppedCaps(t *testing.T)      // 按约束里的 cap_drop/cap_add 和 no-new-privileges 启动，PUID 为 0 和 1000 两种 → 都能 healthy
```

- [ ] **Step 3: 运行，确认失败**

Run: `go test ./cmd/tern/ && make image-test`
Expected: FAIL，`deploy/Dockerfile` 不存在

- [ ] **Step 4: 实现**

`.dockerignore` 排除 `.git`、`.dev`、`bin`、`web/node_modules`、`web/dist`、`test`、`docs`。

- [ ] **Step 5: 运行，确认通过**

Run: `go test ./cmd/tern/ && make image-test && make image VERSION=dev`
Expected: PASS；多架构构建成功

- [ ] **Step 6: 提交**

```bash
git add deploy .dockerignore cmd/tern Makefile test/image
git commit -m "feat(deploy): multi-arch image, entrypoint with PUID/PGID and healthcheck"
```

---

### Task 2: compose、挂载检查、停机预算

**Files:**
- Create: `deploy/compose.yaml`、`internal/sysinfo/mounts.go`
- Modify: `internal/config/config.go`（`AllowUnmounted`）、`internal/api/api.go`（`Deps.Mounts`）、`internal/api/setup.go`、`internal/api/settings.go`、`internal/app/app.go`、`cmd/tern/main.go`（收尾上限改为 100 秒）、`internal/runner/runner.go`（停机时任务后命令的上限）
- Modify: `web/src/pages/setup/SetupPage.tsx`、`web/src/api/types.ts`、`web/src/i18n/zh.json`、`web/src/i18n/en.json`
- Test: `internal/sysinfo/mounts_test.go`、`internal/api/setup_test.go`、`internal/runner/runner_test.go`、`web/src/pages/setup/SetupPage.test.tsx`、`test/image/compose_test.go`

**Interfaces:**
- Consumes: `mounts.IsMounted`
- Produces:

```go
// internal/config
// Config 新增：AllowUnmounted bool // TERN_ALLOW_UNMOUNTED == "1"

// internal/sysinfo
type Mounts struct { Data, Secrets bool }
func CheckMounts(cfg config.Config, isMounted func(string) (bool, error)) Mounts // AllowUnmounted 为真 → 两项都为真；检查出错按未挂载算

// internal/runner
var ShutdownHookCap = 60 * time.Second // 取消原因是 ErrShutdown 时，任务后命令的超时取 min(钩子自己的超时, ShutdownHookCap)
```

接口改动：
- `GET /api/v1/status` 和 `GET /api/v1/system` 的响应新增 `mounts: {data, secrets}`。
- `POST /api/v1/setup`：`mounts.data` 为假 → 400 `data_not_mounted`；`unlock_mode` 为 `keyfile` 且 `mounts.secrets` 为假 → 400 `secrets_not_mounted`。两种情况都不写任何东西。
- `POST /api/v1/settings/unlock-mode` 切到 `keyfile` 时同样检查 `secrets`。

向导改动：
- `mounts.data` 为假 → 整个向导换成一张说明卡：`/data 没有挂载到宿主机。现在继续的话，所有设置会在容器重建时丢失。在 compose 文件里加上 ./data:/data 后重启。` 没有表单。
- `mounts.secrets` 为假 → “密钥文件”选项禁用，旁边说明：`/secrets 没有挂载。加上 ./secrets:/secrets 后重启，或者改用口令解锁。`

`deploy/compose.yaml`：

```yaml
services:
  tern:
    image: tern:${TERN_VERSION:-latest}
    restart: unless-stopped
    init: true
    stop_grace_period: 2m
    ports:
      - "8080:8080"
    environment:
      TZ: Asia/Shanghai
      # PUID: "1000"                         # 拉取同步落地文件的属主；不设则为 root
      # PGID: "1000"
    volumes:
      - ./data:/data                         # 数据库、日志、restic 缓存
      - ./secrets:/secrets                   # 主密钥文件，和 data 分开保管
      - /share/Photos:/sources/photos:ro     # 只推送或备份的目录，只读挂载
      - /share/Inbox:/targets/inbox          # 拉取同步的落地目录
    sysctls:
      net.ipv4.tcp_congestion_control: bbr   # 宿主机内核不支持时删掉这一行
    security_opt:
      - no-new-privileges:true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, DAC_READ_SEARCH, FOWNER, SETUID, SETGID]
```

- [ ] **Step 1: 写失败的测试**

```go
// internal/sysinfo/mounts_test.go
func TestCheckMounts(t *testing.T)                 // 注入的 isMounted：/data 真、/secrets 假 → {true, false}；出错 → 假；AllowUnmounted → {true, true}

// internal/api/setup_test.go
func TestSetupRefusedWithoutDataMount(t *testing.T)   // Mounts{Data:false} → 400 data_not_mounted；meta 表为空；secrets 目录里没有新文件     ← Review Focus 1
func TestSetupKeyfileNeedsSecretsMount(t *testing.T)  // Mounts{Data:true, Secrets:false} + keyfile → 400 secrets_not_mounted                ← Review Focus 1
func TestSetupPassphraseWithoutSecretsMount(t *testing.T) // 同样的挂载情况 + passphrase → 201
func TestStatusReportsMounts(t *testing.T)
func TestSwitchToKeyfileNeedsSecretsMount(t *testing.T)

// internal/runner/runner_test.go
func TestShutdownCapsPostHook(t *testing.T)        // 任务后命令 `sleep 600`、钩子超时 600；以 ErrShutdown 取消；ShutdownHookCap 调到 1 秒 → Execute 在约 1 秒后返回，状态 interrupted  ← Review Focus 4

// test/image/compose_test.go（//go:build image）
func TestComposeFileValid(t *testing.T)            // `docker compose -f deploy/compose.yaml config -q` 退出 0
func TestComposeMatchesSpec(t *testing.T)          // 解析后的服务含：init true、restart unless-stopped、端口 8080:8080、四个卷、sysctls 那一项、no-new-privileges、stop_grace_period 2m、cap_drop [ALL]、六项 cap_add
```

```ts
// web/src/pages/setup/SetupPage.test.tsx
it('blocks the wizard when /data is not mounted')        // mounts.data false → 页面含 "./data:/data"，没有任何输入框
it('disables keyfile when /secrets is not mounted')      // 密钥文件选项 disabled，默认选中口令
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/sysinfo/ ./internal/api/ ./internal/runner/ -run 'Mount|ShutdownCaps' ; npm --prefix web test`
Expected: FAIL

- [ ] **Step 3: 实现**

`app.Start` 里用真的 `mounts.IsMounted` 调 `CheckMounts`，结果放进 `api.Deps`；任一项为假时往进程日志写一行警告。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./... && npm --prefix web test && make image-test`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add deploy/compose.yaml internal web test/image cmd/tern
git commit -m "feat(deploy): compose file, mount checks in setup and bounded shutdown"
```

---

### Task 3: 验收：弱网

**Files:**
- Modify: `test/integration/compose.yaml`（`runner` 加 `cap_add: [NET_ADMIN]`）、`Makefile`（加 `acceptance`）
- Create: `test/acceptance/netem_test.go`、`test/acceptance/weaknet_test.go`（都带 `//go:build acceptance`）

**Interfaces:**
- Consumes: 计划 4 任务 8、计划 5 任务 5 的测试辅助函数（把它们所在的文件加上 `acceptance` 标签，或挪到两个包都能引用的 `test/testkit` 包里）
- Produces:

```go
func netem(t *testing.T, spec string)             // tc qdisc replace dev eth0 root netem <spec>；t.Cleanup 里删除
func blackout(t *testing.T, d time.Duration)      // 把规则换成 loss 100%，d 之后换回原来的 spec
```

`make acceptance`：`docker compose -f test/integration/compose.yaml run --rm --build runner go test -tags acceptance -count=1 -timeout 60m ./test/acceptance/...`

说明：丢包规则加在 runner 自己的网卡上。被测的 app 和它拉起的 `ssh`、`rclone`、`restic` 都在这个网络命名空间里，所以都受影响。

为了不让测试跑一小时，这里的任务把重试间隔设成 `[10, 20, 60]` 秒。断网时长仍是规格要求的 60 秒。

- [ ] **Step 1: 写测试**

```go
func TestWeakNetRsync(t *testing.T)
// netem "loss 5%"。rsync 推送：1 个 200 MiB 文件加 200 个 100 KiB 文件，限速到全程约 2 分钟。
// 总进度过 30% 时 blackout 60 秒。
// 断言：运行最终 success；attempts ≥ 2；两端 assertSameTree；
//       最后一次尝试的 bytes < 总数据量的 80%（大文件是续传的，不是重传）。           （规格 §16 第 4 条）

func TestWeakNetSFTP(t *testing.T)
// 同样的丢包和断网。sftp 推送 300 个 1 MiB 文件。
// 断言：success；attempts ≥ 2；两端一致；最后一次尝试的 files < 300（已完成的文件没有重传）。

func TestWeakNetWebDAV(t *testing.T)              // 同上，webdav。断言：success；两端一致

func TestWeakNetRestic(t *testing.T)
// 备份 200 MiB 随机数据，过 30% 时 blackout 60 秒。
// 断言：success；attempts ≥ 2；最后一次尝试的 bytes < 总数据量的 80%；原生 restic check 通过。
// 用 t.Log 记下断线那次尝试的退出码、stderr 末尾、从断网到进程退出的秒数。           （规格 §15 待确认项）

func TestDisconnectDetectedInTime(t *testing.T)   // 三种引擎各一次：blackout 开始后，引擎子进程在 120 秒内退出
func TestPullUnderLoss(t *testing.T)              // netem "loss 5%"、不断网：rsync 和 sftp 各拉取 50 MiB → success，两端一致
func TestRetryWaitVisible(t *testing.T)           // 断网期间 GET /api/v1/overview 里该任务 active_run.status 出现过 "retry_wait"
```

- [ ] **Step 2: 运行**

Run: `make acceptance`
Expected: 全部 PASS。失败时先看是不是 `ClassifyStderr` 没认出某种断线报错：把那行 stderr 加进计划 4 任务 2 的 `network` 子串表并补一条表驱动用例，再重跑。不许改这里的断言。

- [ ] **Step 3: 提交**

```bash
git add test/acceptance test/integration/compose.yaml Makefile
git commit -m "test(acceptance): weak network scenarios with packet loss and outage"
```

---

### Task 4: 验收：凭据不泄露、重启自恢复、前后置命令

**Files:**
- Create: `test/acceptance/host/compose.yaml`（被测镜像 `tern`、`sshd`、`webdav` 三个服务；`tern` 的卷都绑到宿主机的临时目录）
- Create: `test/acceptance/host/harness_test.go`、`leak_test.go`、`restart_test.go`、`hooks_test.go`（都带 `//go:build acceptance_host`，在宿主机上跑，用 docker 命令行和 HTTP 驱动）
- Modify: `Makefile`（加 `acceptance-host`）

**Interfaces:**
- Produces:

```go
// harness_test.go
func up(t *testing.T, env map[string]string) *stack      // docker compose up -d --wait；t.Cleanup 里 down -v
func (s *stack) api() *client                            // 与集成测试的 testApp 同形状，但走宿主机端口
func (s *stack) exec(service string, cmd ...string) string // 只用于 sshd、webdav，以及泄露扫描里对 tern 的只读采样
func (s *stack) restart(service string)
func (s *stack) stop(service string); func (s *stack) start(service string)
func (s *stack) dataDir() string                         // 宿主机上绑给 /data 的目录
```

`make acceptance-host`：`go test -tags acceptance_host -count=1 -timeout 30m ./test/acceptance/host/...`

- [ ] **Step 1: 写测试**

```go
// leak_test.go
func TestNoSecretLeak(t *testing.T)
// 金丝雀（每个都是带随机后缀的独特字符串）：
//   管理员密码、带口令私钥的口令、该私钥 PEM 正文的中间一行、WebDAV 密码、restic 仓库密码。
// 场景：导入密钥 → 建 SSH 和 WebDAV 连接 → 测试连接 → 建备份任务并运行 → sftp、rsync、webdav 同步各运行一次
//       → 一个注定失败的同步（远端路径不存在）→ 恢复一次 → 每个任务的前后置命令都是 `env; ps aux`。
// 运行期间每 200 毫秒采样一次 tern 容器里所有进程的 /proc/<pid>/cmdline 和 /proc/<pid>/environ。
// 结束后扫描：
//   (a) dataDir 下的每个文件（数据库、-wal、-shm、日志、restic 缓存）
//   (b) `docker logs` 的全部输出
//   (c) 所有 GET 接口的响应体（逐个列出路径，不得遗漏新增的），以及运行期间录下的 SSE 流
//   (d) 每次运行的日志（经接口取）
//   (e) 全部采样
// 断言：任何一处都不含任何金丝雀。唯一的例外是 POST …/recovery-info 的响应，它本来就该返回仓库密码。
// 另外断言：全部运行结束后容器里 /dev/shm/tern 下没有任何文件。                              （规格 §16 第 6 条）

func TestNoSecretLeakPassphraseMode(t *testing.T) // 口令解锁模式下重复数据库和日志两项扫描，金丝雀多一个：保险箱口令

// restart_test.go
func TestRestartResumesSchedule(t *testing.T)
// keyfile 模式；建一个每分钟运行的同步任务，等到一次 success。
// restart tern。之后**不调用任何登录或解锁接口**。
// 断言：150 秒内，对端出现新一轮同步的结果（往源里新放的文件到了对端）。                     （规格 §16 第 7 条）

func TestPassphraseModeWaitsForUnlock(t *testing.T) // 口令模式：重启后 150 秒内没有新运行，status.vault_state == "locked"；登录并解锁后 90 秒内出现新运行
func TestStopMarksInterrupted(t *testing.T)       // 备份运行到一半时 stop → start 后那条记录是 interrupted；再运行一次 → success（残留锁被清掉）      ← Review Focus 4
func TestKillThenRecover(t *testing.T)            // 同上但用 `docker kill`（SIGKILL）：重启后记录是 interrupted；/dev/shm/tern 被清空；再运行 → success   ← Review Focus 4
func TestKeyfileRemoved(t *testing.T)             // 停容器、挪走宿主机上的 master.key、启动 → status.vault_state == "keyfile_missing"；secrets 目录里没有生成新文件；挪回来重启 → 恢复正常
func TestBBRReported(t *testing.T)                // GET /api/v1/system 的 bbr：supported 与 active 自洽（active 为真则 supported 必为真）；
                                                  // 去掉 compose 的 sysctls 行再起 → 服务照常 healthy，active 反映实际算法                              ← Review Focus 5

// hooks_test.go
func TestHooksEndToEnd(t *testing.T)
// 任务前命令在对端（SSH 执行位置）写 `<目录>/stopped`，任务后命令删掉它并把 $TERN_RESULT 写进 `<目录>/result`。
// (1) 正常运行 → 对端没有 stopped，result 内容是 "success"。
// (2) 让同步失败（远端路径不存在）→ 没有 stopped，result 是 "failed"。
// (3) 运行中取消 → 没有 stopped，result 是 "canceled"。
// (4) 任务前命令 `exit 1` → 引擎没跑（对端没有新文件），result 是 "failed"。
// (5) 带重试的运行（中途重启 sshd 一次）→ stopped 只被创建过一次（任务前命令里同时往 `<目录>/count` 追加一行，最后恰好一行）。
// (6) 试运行 → 两个命令都没执行。                                                        （规格 §16 第 9 条）
```

- [ ] **Step 2: 运行**

Run: `make image VERSION=acceptance && make acceptance-host`
Expected: 全部 PASS

- [ ] **Step 3: 提交**

```bash
git add test/acceptance/host Makefile
git commit -m "test(acceptance): secret leak scan, restart recovery and hook semantics"
```

---

### Task 5: 端到端：从向导到跑通

**Files:**
- Create: `test/e2e/package.json`、`test/e2e/playwright.config.ts`、`test/e2e/first-run.spec.ts`、`test/e2e/global-setup.ts`
- Modify: `Makefile`（加 `e2e`）

`global-setup.ts` 用任务 4 的 compose 文件起环境，`TERN_VERSION` 指向刚构建的镜像。`make e2e`：`npm --prefix test/e2e ci && npx --prefix test/e2e playwright install chromium && npx --prefix test/e2e playwright test`。

**铁律：测试全程不对 `tern` 容器执行任何命令。** 只允许操作浏览器，以及对 `sshd` 容器做“把公钥加进 authorized_keys”这一件事（那是对端管理员的动作）。检查文件一律看宿主机上绑定的目录。

- [ ] **Step 1: 写测试**

```ts
test('first run: from wizard to a successful sync, backup and restore', async ({ page }) => {
  // 1. 打开首页 → 被带到向导。填管理员密码和节点名，选密钥文件，完成 → 进入概览。
  // 2. 连接与密钥 → 生成密钥 → 从弹窗读出公钥 → 写进 sshd 的 authorized_keys。
  // 3. 新建 SSH 连接 → 获取指纹 → 页面显示的指纹与 sshd 的真实指纹一致 → 勾选确认 → 保存 → 测试连接显示“连接正常”。
  // 4. 新建同步任务：rsync、推、一组文件夹对、只手动 → 保存 → 试运行 → 运行详情的日志里有 "plan:"。
  // 5. 立即运行 → 运行详情最终显示“成功”；宿主机上对端目录里的文件与源一致。
  // 6. 新建备份任务 → 点保存出现恢复信息弹窗 → 未勾选时“确认并创建”不可点 → 下载恢复信息 → 勾选 → 创建成功，提示“已初始化新仓库”。
  // 7. 立即运行备份 → 成功。快照与恢复页出现一个快照。
  // 8. 恢复到 /targets/restore-e2e → 运行成功；宿主机上绑给 /targets 的目录里文件与源一致。
  // 9. 概览页：两个任务都是成功状态，没有异常横幅。
});                                                                                          // （规格 §16 第 1 条）

test('language switch', async ({ page }) => { /* 设置页切到 English → 侧栏出现 "Overview" */ });
test('narrow viewport', async ({ page }) => { /* 视口 390×844 → 侧栏不可见，点菜单按钮后出现 */ });
```

- [ ] **Step 2: 运行**

Run: `make e2e`
Expected: 3 个测试 PASS

- [ ] **Step 3: 提交**

```bash
git add test/e2e Makefile
git commit -m "test(e2e): first-run journey from wizard to backup and restore"
```

---

### Task 6: README 与验收对照表

**Files:**
- Create: `README.md`、`docs/acceptance.md`

`README.md`（中文）按此顺序，每节写到“照着做就行”的程度：

1. **这是什么。** 两句话，加规格 §1 的两个痛点。
2. **快速开始。** 原样贴出 `deploy/compose.yaml`；`docker compose up -d`；打开 `http://<地址>:8080` 走向导。
3. **你必须自己保管的两样东西。** `secrets` 目录；每个备份任务的恢复信息。各说明丢了会怎样。
4. **对端要求。** 规格 §3 的三条：restic 和 SFTP 只要对端开着带 sftp 子系统的 SSH；rsync 要对端装有 rsync；WebDAV 要对端有 WebDAV 服务。
5. **三种同步协议怎么选。** 规格 §7.2 的能力差异表，加那句提示。
6. **BBR。** 是节点级开关不是任务级；只对本机发出的数据起作用；要求宿主机内核已加载 `tcp_bbr`；宿主机不支持时带着 `sysctls` 那行容器会启动失败，删掉即可；host 网络模式不能写 `sysctls`；组网工具自己用 TCP 或 KCP 做隧道时效果会打折。
7. **文件属主。** `PUID`/`PGID` 的作用；设了之后源目录必须对该用户可读。
8. **安全须知。** 能登录界面的人就能通过前后置命令在容器里执行任意命令、读到所有挂载的数据，建议只在组网地址上暴露端口；SSH 只支持密钥；主机指纹变了会拒绝连接，要人工重新确认；可选内置 TLS（`TERN_TLS_CERT`、`TERN_TLS_KEY`）或放在反向代理后面。
9. **脱离本工具恢复备份。** 用恢复信息里的四行命令。
10. **通知。** 第一版没有内置通知；给一个在任务后命令里用 `curl` 按 `$TERN_RESULT` 发 Webhook 的例子。
11. **环境变量一览。** `TERN_LISTEN`、`TERN_DATA_DIR`、`TERN_SECRETS_DIR`、`TERN_RUNTIME_DIR`、`TERN_TLS_CERT`、`TERN_TLS_KEY`、`PUID`、`PGID`、`TZ`。
12. **第一版不做什么。** 规格 §2.2 的清单。

`docs/acceptance.md` 两张表。第一张，规格 §16 逐条对照：

| # | 验收标准 | 证明它的测试 | 怎么跑 |
|---|---|---|---|
| 1 | 从向导到跑通不进容器 | `test/e2e/first-run.spec.ts` | `make e2e` |
| 2 | 原生 restic 能列出并恢复 | `TestBackupThenNativeRestore` | `make itest` |
| 3 | 三协议、推拉、多组文件夹对 | `TestSyncMatrix` | `make itest` |
| 4 | 5% 丢包加断网 1 分钟后最终成功、不重传、rsync 续传 | `TestWeakNetRsync`、`TestWeakNetSFTP`、`TestWeakNetRestic`、`TestRsyncResumesBigFile` | `make acceptance`、`make itest` |
| 5 | 镜像模式源为空或没挂上时目的地完好 | `TestMirrorEmptySourceKeepsDestination` | `make itest` |
| 6 | 数据库、日志、接口、`ps` 里搜不到明文凭据 | `TestNoSecretLeak` | `make acceptance-host` |
| 7 | 密钥文件模式下重启后计划任务自动恢复 | `TestRestartResumesSchedule` | `make acceptance-host` |
| 8 | 限速和并发文件数实测生效 | `TestLimitUpload`、`TestBwlimit`、`TestTransfersConcurrency` | `make itest` |
| 9 | 前后置命令的行为符合 §10 | `TestHooksEndToEnd` 及计划 3 任务 3、4 的单元测试 | `make acceptance-host`、`make test` |

第二张，待确认项的结论。每行写实测到的现象和测试日期，不写推测：

| 待确认 | 结论 | 来源 |
|---|---|---|
| rclone 对 SFTP、WebDAV 的单文件续传 | | `TestRcloneSkipsCompleted` 的日志 |
| 目标 NAS 内核是否带 `tcp_bbr`、容器内 sysctl 能否生效 | | 在目标 NAS 上部署后看概览页的 BBR 卡片 |
| restic 的 SFTP 后端断线后的表现 | | `TestWeakNetRestic` 的日志 |
| WebDAV 服务器对修改时间的支持 | | `TestSecondRunTransfersNothing` 的日志 |
| rclone 走外部 ssh 时的并发 | | `TestTransfersConcurrency` |
| restic 退出码 | | `TestExitCodesAsDocumented` |

- [ ] **Step 1: 跑全部测试并收集结论**

```bash
make vet && make test && npm --prefix web test && make itest && make acceptance && make image VERSION=rc && make image-test && make acceptance-host && make e2e
```

Expected: 全部 PASS。任何一项失败，本任务不继续。

- [ ] **Step 2: 写 `README.md` 和 `docs/acceptance.md`**

第二张表的“结论”列填 Step 1 实际观察到的内容。第 2 行需要目标 NAS，执行者没有条件验证时写“未验证：需要在目标 NAS 上确认”，并在交付时明确告诉人。

- [ ] **Step 3: 核对**

逐条检查：README 第 2 节的 compose 内容与 `deploy/compose.yaml` 逐字一致；第 11 节的变量与 `internal/config/config.go` 一致；`docs/acceptance.md` 第一张表里的每个测试名都能在仓库里 `grep` 到。

Run: `grep -rn "func TestBackupThenNativeRestore\|func TestSyncMatrix\|func TestWeakNetRsync\|func TestMirrorEmptySourceKeepsDestination\|func TestNoSecretLeak\|func TestRestartResumesSchedule\|func TestTransfersConcurrency\|func TestHooksEndToEnd" test/ | wc -l`
Expected: `8`

- [ ] **Step 4: 提交**

```bash
git add README.md docs/acceptance.md
git commit -m "docs: README and acceptance matrix"
```
