# tern 计划 1 · 地基 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一个能启动的 tern 二进制：SQLite 存储、凭据保险箱、首次启动向导、登录与解锁接口。

**Architecture:** 单进程 Go 服务。`store` 管 SQLite，`vault` 做信封加密，`auth` 管密码、会话和限速，`api` 用标准库路由暴露 `/api/v1`，`app` 负责装配。本计划不碰任何外部工具。

**Tech Stack:** Go 1.26、`modernc.org/sqlite`、`golang.org/x/crypto`（chacha20poly1305、argon2）。

**Spec:** `2026-11-10-nas-backup-sync-design.md` §4、§9.1、§9.4。执行者同时读 `docs/superpowers/plans/2026-11-10-tern-0-index.md`。

## Global Constraints

索引第 2 节全部适用。本计划另加：

- 数据库文件 `<DataDir>/tern.db`，权限 0600；密钥文件 `<SecretsDir>/master.key`，恰好 32 字节，权限 0600。
- 管理员密码至少 10 个字符。任何密码或口令超过 1024 字节直接拒绝，不送进 Argon2。
- 会话 cookie 名 `tern_session`，属性 `HttpOnly; SameSite=Strict; Path=/`；请求经 TLS 到达或带 `X-Forwarded-Proto: https` 时再加 `Secure`。
- CSRF 请求头名 `X-CSRF-Token`。除 `POST /api/v1/setup` 和 `POST /api/v1/login` 外，所有非 GET/HEAD 请求必须带。
- 所有响应带 `Content-Security-Policy: default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'` 和 `X-Content-Type-Options: nosniff`。
- `meta` 表的键：`admin.password_hash`、`node.id`、`node.name`、`vault.mode`、`vault.dek`、`vault.kdf`。

## Review Focus

规格没有明说、但真实使用里最可能咬人的五种情况。每条已经在所属任务里配了测试。

1. **降级。** 新版本写过的数据库被旧版本二进制打开 → 拒绝启动并说明原因，不带着看不懂的表结构继续跑。（任务 2）
2. **双开。** 同一个数据目录被两个容器同时打开 → 第二个拒绝启动。（任务 2）
3. **数据目录不可写或磁盘已满** → 启动时给出带路径的错误后退出，不 panic，不留半初始化状态。（任务 2）
4. **密钥文件存在但不对劲**（长度不是 32 字节、是个目录、读不了）→ 状态为 `keyfile_mismatch`，绝不覆盖它。（任务 3）
5. **向导被并发提交，或提交超长密码** → 只有一次成功；超长直接 400，不消耗 Argon2 的 CPU 和内存。（任务 5）

---

### Task 1: 工程骨架与配置

**Files:**
- Create: `go.mod`、`Makefile`、`.gitignore`、`cmd/tern/main.go`
- Create: `internal/config/config.go`、`internal/model/id.go`
- Test: `internal/config/config_test.go`、`internal/model/id_test.go`

**Interfaces:**
- Produces:

```go
// internal/config
type Config struct {
    Listen     string // TERN_LISTEN，默认 ":8080"
    DataDir    string // TERN_DATA_DIR，默认 "/data"
    SecretsDir string // TERN_SECRETS_DIR，默认 "/secrets"
    RuntimeDir string // TERN_RUNTIME_DIR，默认 "/dev/shm/tern"
    TLSCert    string // TERN_TLS_CERT
    TLSKey     string // TERN_TLS_KEY；两个要么都给要么都不给
}
func Load(getenv func(string) string) (Config, error)

// internal/model
func NewID() string // 32 个小写十六进制字符，来自 crypto/rand
```

- [ ] **Step 1: 提交现有文档**（仓库目前没有任何提交）

```bash
git add 2026-11-10-nas-backup-sync-design.md DESIGN-notion.md docs/superpowers/plans
git commit -m "docs: add spec, design language and implementation plans"
```

- [ ] **Step 2: 写失败的测试**

```go
// internal/config/config_test.go
func TestLoadDefaults(t *testing.T)     // 空环境 → {":8080", "/data", "/secrets", "/dev/shm/tern", "", ""}
func TestLoadOverrides(t *testing.T)    // 六个变量逐项覆盖后与输入相等
func TestLoadTLSNeedsBoth(t *testing.T) // 只给 TERN_TLS_CERT → error，信息含 "TERN_TLS_KEY"；反之亦然

// internal/model/id_test.go
func TestNewID(t *testing.T)            // 匹配 ^[0-9a-f]{32}$；连续 1000 个互不相同
```

- [ ] **Step 3: 运行，确认失败**

Run: `go mod init tern && go test ./internal/...`
Expected: FAIL，`undefined: Load`、`undefined: NewID`

- [ ] **Step 4: 实现 `config.Load`、`model.NewID`；写 `Makefile` 和 `.gitignore`；`cmd/tern/main.go` 先只打印版本**

`Makefile` 目标：`test`（`go test ./...`）、`vet`（`go vet ./...`）、`build`（`CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/tern ./cmd/tern`）。
`.gitignore`：`/bin/`、`/.dev/`、`/web/node_modules/`。
`main.go`：包级变量 `version = "dev"`；带 `--version` 参数时打印 `tern <version>` 后退出 0。

- [ ] **Step 5: 运行，确认通过**

Run: `make test && make build && ./bin/tern --version`
Expected: PASS；输出 `tern dev`

- [ ] **Step 6: 提交**

```bash
git add go.mod Makefile .gitignore cmd internal
git commit -m "feat: project skeleton and config loading"
```

---

### Task 2: SQLite 存储与迁移

**Files:**
- Create: `internal/store/store.go`、`internal/store/migrate.go`、`internal/store/meta.go`、`internal/store/errors.go`
- Create: `internal/store/migrations/0001_init.sql`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:

```go
func Open(ctx context.Context, dataDir string) (*Store, error)
func (s *Store) Close() error
func (s *Store) DB() *sql.DB
func (s *Store) GetMeta(ctx context.Context, key string) (value string, ok bool, err error)
func (s *Store) SetMeta(ctx context.Context, key, value string) error
func (s *Store) Tx(ctx context.Context, fn func(*sql.Tx) error) error

var ErrLockHeld, ErrNewerSchema error             // Open 用
var ErrNotFound, ErrVersion, ErrInUse, ErrDuplicate error // 后续各表读写用
```

`0001_init.sql`：

```sql
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL) STRICT;
CREATE TABLE sessions (
  token_hash   TEXT PRIMARY KEY,
  csrf         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL
) STRICT;
```

- [ ] **Step 1: 写失败的测试**

```go
func TestOpenCreatesAndMigrates(t *testing.T)    // PRAGMA user_version == 迁移文件数；journal_mode == "wal"；foreign_keys == 1
func TestOpenIsIdempotent(t *testing.T)          // Close 后再 Open：user_version 不变，之前写的 meta 还在
func TestMetaRoundTrip(t *testing.T)             // Set("a","1") → Get == ("1", true)；Get("none") == ("", false)；重复 Set 覆盖
func TestDBFileMode(t *testing.T)                // tern.db 权限 == 0600
func TestSecondOpenFailsWithLock(t *testing.T)   // 同目录第二次 Open → errors.Is(err, ErrLockHeld)          ← Review Focus 2
func TestNewerSchemaRefused(t *testing.T)        // 手工 PRAGMA user_version = 999 → errors.Is(err, ErrNewerSchema) ← Review Focus 1
func TestUnwritableDirFailsCleanly(t *testing.T) // 目录权限 0500 → 返回 error（不 panic），信息含该目录路径；目录里没有新文件 ← Review Focus 3
func TestTxRollsBackOnError(t *testing.T)        // fn 返回 error → 事务内的写入不可见
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/store/`
Expected: FAIL，`undefined: Open`

- [ ] **Step 3: 实现**

- 驱动名 `sqlite`。连接串带 `_pragma=journal_mode(WAL)`、`_pragma=synchronous(FULL)`、`_pragma=foreign_keys(1)`、`_pragma=busy_timeout(5000)`。`SetMaxOpenConns(1)`。
- 双开保护：`Open` 先对 `<dataDir>/tern.lock` 取非阻塞独占 `flock`，拿不到返回 `ErrLockHeld`；`Close` 释放。
- 迁移：`//go:embed migrations/*.sql`，按文件名顺序，每个文件一个事务，做完把 `PRAGMA user_version` 设为已应用的文件数。库里的 `user_version` 大于内置文件数时返回 `ErrNewerSchema`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS（8 个）

- [ ] **Step 5: 提交**

```bash
git add internal/store go.mod go.sum
git commit -m "feat(store): sqlite store with embedded migrations and single-instance lock"
```

---

### Task 3: 凭据保险箱

**Files:**
- Create: `internal/vault/vault.go`、`internal/vault/keyfile.go`、`internal/vault/passphrase.go`
- Test: `internal/vault/vault_test.go`

**Interfaces:**
- Consumes: `store.Store` 的 `GetMeta`、`SetMeta`、`Tx`（在本包里声明一个只含这三个方法的小接口 `MetaStore`）
- Produces:

```go
type Mode string  // "keyfile" | "passphrase"
type State string // "uninitialized" | "unlocked" | "locked" | "keyfile_missing" | "keyfile_mismatch"

func New(st MetaStore, secretsDir string) *Vault
func (v *Vault) Load(ctx context.Context) (State, error)                    // 启动时调用；keyfile 模式在这里自动解锁
func (v *Vault) Init(ctx context.Context, mode Mode, passphrase string) error // 生成数据密钥；只能成功一次
func (v *Vault) Unlock(ctx context.Context, passphrase string) error
func (v *Vault) State() State
func (v *Vault) Mode() Mode
func (v *Vault) Seal(aad string, plaintext []byte) (string, error)          // 返回 "v1." + base64url(24 字节随机 nonce ‖ 密文)
func (v *Vault) Open(aad string, sealed string) ([]byte, error)
func (v *Vault) SwitchMode(ctx context.Context, mode Mode, passphrase string) error // 只重新包裹数据密钥

var ErrLocked, ErrWrongPassphrase, ErrAlreadyInitialized error
```

存储约定：

| meta 键 | 内容 |
|---|---|
| `vault.mode` | `keyfile` 或 `passphrase` |
| `vault.dek` | 数据密钥被主密钥加密后的密文，格式同 `Seal`，AAD 固定为 `tern/dek/v1` |
| `vault.kdf` | 仅口令模式：`{"salt":"<base64，16 字节>","time":3,"memory_kib":65536,"threads":4}` |

密钥文件规则：
- `Init(keyfile)`：`master.key` 不存在 → 用 `O_CREATE|O_EXCL`、0600 写入 32 个随机字节；已存在且恰好 32 字节 → 沿用；已存在但不是 32 字节的普通文件 → 报错，不动它。
- `Load`：`vault.mode` 不存在 → `uninitialized`。keyfile 模式下文件不存在 → `keyfile_missing`；读不了、不是 32 字节、或解不开 `vault.dek` → `keyfile_mismatch`。这两种状态下**绝不创建或改写任何文件**。口令模式 → `locked`。

- [ ] **Step 1: 写失败的测试**

```go
func TestSealOpenRoundTrip(t *testing.T)         // Open(aad, Seal(aad, p)) == p；密文以 "v1." 开头
func TestSealIsRandomized(t *testing.T)          // 同一 aad、同一明文 Seal 两次，结果不同
func TestOpenRejectsOtherAAD(t *testing.T)       // Seal("ssh_keys/a/private", p) 后 Open("ssh_keys/b/private", …) → error
func TestOpenRejectsTampering(t *testing.T)      // 翻转密文任意一位 → error
func TestSealWhenLocked(t *testing.T)            // 口令模式 Load 后未 Unlock → errors.Is(err, ErrLocked)
func TestInitTwice(t *testing.T)                 // 第二次 Init → errors.Is(err, ErrAlreadyInitialized)

func TestKeyfileInitCreatesFile(t *testing.T)    // master.key 存在、32 字节、权限 0600
func TestKeyfileAutoUnlock(t *testing.T)         // 同一 store 新建 Vault，Load → "unlocked"，能解开之前的密文
func TestKeyfileMissing(t *testing.T)            // 删掉 master.key 后 Load → "keyfile_missing"；secretsDir 里没有新文件
func TestKeyfileMismatch(t *testing.T)           // 换成另外 32 个随机字节 → "keyfile_mismatch"；文件内容没被改
func TestKeyfileWrongShape(t *testing.T)         // 31 字节的文件 / 同名目录 / 权限 0000 → 都是 "keyfile_mismatch"，原物未动 ← Review Focus 4
func TestKeyfileInitReusesExisting(t *testing.T) // 预先放好 32 字节文件再 Init → 文件内容不变，能正常 Seal/Open

func TestPassphraseLockedAfterLoad(t *testing.T) // 新建 Vault，Load → "locked"
func TestPassphraseUnlock(t *testing.T)          // 错口令 → ErrWrongPassphrase，状态仍 "locked"；对口令 → "unlocked"
func TestKDFParamsStored(t *testing.T)           // vault.kdf 里 time==3、memory_kib==65536、threads==4，salt 解码后 16 字节

func TestSwitchKeyfileToPassphrase(t *testing.T) // 切换后旧密文仍能 Open；vault.dek 的值变了；新实例 Load → "locked"
func TestSwitchPassphraseToKeyfile(t *testing.T) // 切换后新实例 Load → "unlocked"；vault.kdf 被删除
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/vault/`
Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现**

数据密钥是 32 个随机字节，只存在于内存和 `vault.dek` 的密文里。`Init` 和 `SwitchMode` 对 meta 的多项写入放在同一个 `Tx` 里。口令派生用 `argon2.IDKey(passphrase, salt, 3, 64*1024, 4, 32)`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/vault/ -v`
Expected: PASS（17 个）

- [ ] **Step 5: 提交**

```bash
git add internal/vault go.mod go.sum
git commit -m "feat(vault): envelope encryption with keyfile and passphrase unlock"
```

---

### Task 4: 管理员密码、会话、登录限速

**Files:**
- Create: `internal/auth/password.go`、`internal/auth/session.go`、`internal/auth/limiter.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: `store.Store.DB()`（`sessions` 表）
- Produces:

```go
const MinPasswordLen = 10
const MaxSecretLen = 1024

func HashPassword(pw string) (string, error) // "$argon2id$v=19$m=65536,t=3,p=4$<盐 base64>$<哈希 base64>"
func VerifyPassword(encoded, pw string) bool // 常数时间比较

type Sessions struct{ /* db、now */ }
func NewSessions(db *sql.DB, now func() time.Time) *Sessions
func (s *Sessions) Create(ctx context.Context) (token, csrf string, err error) // 各 32 字节随机，base64url
func (s *Sessions) Lookup(ctx context.Context, token string) (csrf string, ok bool, err error) // 命中时刷新 last_seen_at
func (s *Sessions) Delete(ctx context.Context, token string) error
func (s *Sessions) DeleteAll(ctx context.Context) error

type Limiter struct{ /* 按来源 IP */ }
func NewLimiter() *Limiter
func (l *Limiter) Allow(ip string, now time.Time) (ok bool, retryAfter time.Duration)
func (l *Limiter) Fail(ip string, now time.Time)
func (l *Limiter) Reset(ip string)
```

- 超长输入：`HashPassword` 对超过 `MaxSecretLen` 字节的输入返回 error，`VerifyPassword` 返回 false，两者都不做任何派生计算。
- 会话：库里只存 `sha256(token)` 的十六进制。空闲超过 24 小时或创建超过 7 天即失效，失效的行在 `Lookup` 时顺手删掉。
- 限速：同一 IP 连续失败 5 次后锁 1 分钟；锁定期满后每再失败一次，锁定时长翻倍，15 分钟封顶；`Reset` 清零。

- [ ] **Step 1: 写失败的测试**

```go
func TestHashVerify(t *testing.T)             // Verify(Hash("correct horse battery"), 原串) == true；改一个字符 == false
func TestHashFormat(t *testing.T)             // 前缀 "$argon2id$v=19$m=65536,t=3,p=4$"
func TestHashesDiffer(t *testing.T)           // 同一密码两次哈希不同
func TestVerifyGarbage(t *testing.T)          // Verify("not-a-hash", "x") == false，不 panic
func TestHashRejectsHuge(t *testing.T)        // 1025 字节 → error；调用前后 runtime.MemStats.TotalAlloc 的增量 < 1 MiB（没进 Argon2） ← Review Focus 5
func TestVerifyRejectsHuge(t *testing.T)      // 1025 字节 → false；同样的内存断言

func TestSessionLifecycle(t *testing.T)       // Create → Lookup ok 且 csrf 一致；Delete → !ok
func TestSessionTokenNotStored(t *testing.T)  // sessions 表任何列都不含 token 原文
func TestSessionIdleExpiry(t *testing.T)      // 时钟 +24h1m → !ok，且该行已被删除
func TestSessionAbsoluteExpiry(t *testing.T)  // 每隔 23h Lookup 一次，累计超过 7 天 → !ok
func TestSessionDeleteAll(t *testing.T)       // 两个会话，DeleteAll 后都 !ok

func TestLimiterLocksAfterFive(t *testing.T)  // 5 次 Fail 后 Allow == (false, 1m)；1m 后 Allow ok
func TestLimiterDoubles(t *testing.T)         // 解锁后每再 Fail 一次：2m、4m、8m、15m、15m
func TestLimiterReset(t *testing.T)           // 4 次 Fail 后 Reset，再 Fail 4 次仍 Allow
func TestLimiterPerIP(t *testing.T)           // "10.0.0.1" 被锁不影响 "10.0.0.2"
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/auth/`
Expected: FAIL，`undefined: HashPassword`

- [ ] **Step 3: 实现三个文件**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/auth/ -v`
Expected: PASS（15 个）

- [ ] **Step 5: 提交**

```bash
git add internal/auth
git commit -m "feat(auth): argon2id password hashing, sessions and login rate limiting"
```

---

### Task 5: HTTP 骨架：向导、登录、解锁

**Files:**
- Create: `internal/api/api.go`、`internal/api/middleware.go`、`internal/api/respond.go`、`internal/api/setup.go`、`internal/api/session.go`
- Test: `internal/api/api_test.go`

**Interfaces:**
- Consumes: 任务 2–4 的全部导出符号
- Produces:

```go
type Deps struct {
    Cfg      config.Config
    Store    *store.Store
    Vault    *vault.Vault
    Sessions *auth.Sessions
    Limiter  *auth.Limiter
    Version  string
    Now      func() time.Time
}
func New(d Deps) *API // *API 实现 http.Handler

// 后续计划注册路由时用
func (a *API) Handle(pattern string, h http.HandlerFunc, guards ...Guard) // pattern 形如 "POST /api/v1/tasks"
type Guard int
const ( NeedSession Guard = iota + 1; NeedUnlocked ) // 非 GET/HEAD 且带 NeedSession 的路由自动校验 CSRF

func writeJSON(w http.ResponseWriter, status int, v any)
func writeError(w http.ResponseWriter, status int, code, message string, fields map[string]string)
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool // 上限 1 MiB；Content-Type 必须是 application/json；拒绝未知字段
```

路由：

| 方法与路径 | 前置条件 | 请求体 | 成功响应 |
|---|---|---|---|
| `GET /api/v1/status` | 无 | — | 200 `{version, setup_required, vault_state, authenticated}` |
| `POST /api/v1/setup` | 尚未设置过 | `{password, node_name, unlock_mode, passphrase?}` | 201，下发 cookie，`{csrf_token}` |
| `POST /api/v1/login` | 无 | `{password}` | 200，下发 cookie，`{csrf_token}` |
| `POST /api/v1/logout` | 会话 | — | 204 |
| `GET /api/v1/session` | 会话 | — | 200 `{csrf_token}` |
| `POST /api/v1/unlock` | 会话 | `{passphrase}` | 204 |

错误码与状态码：`validation` 400（带 `fields`）、`bad_request` 400、`unauthorized` 401、`forbidden` 403、`csrf` 403、`not_found` 404、`conflict` 409、`in_use` 409、`already_setup` 409、`rate_limited` 429（带 `Retry-After` 秒数）、`vault_locked` 503、`vault_unavailable` 503、`internal` 500。

`setup` 的处理顺序：校验（`password` 长度 10–1024；`node_name` 匹配 `^[a-z0-9][a-z0-9-]{0,62}$`；`unlock_mode` 为 `keyfile` 或 `passphrase`；口令模式下 `passphrase` 长度 10–1024）→ 进程内互斥 → 再查一次是否已设置 → `Vault.Init` → 写 `admin.password_hash`、`node.id`（`model.NewID()`）、`node.name` → 建会话。`setup_required` 的判据是 `admin.password_hash` 不存在。

`NeedUnlocked` 的行为：保险箱状态为 `locked` → 503 `vault_locked`；`keyfile_missing` 或 `keyfile_mismatch` → 503 `vault_unavailable`。

- [ ] **Step 1: 写失败的测试**（`httptest` + 临时目录里的真 store 和 vault）

```go
func TestStatusBeforeSetup(t *testing.T)      // setup_required true，authenticated false，vault_state "uninitialized"
func TestSetupThenStatus(t *testing.T)        // 201；Set-Cookie 含 tern_session、HttpOnly、SameSite=Strict；之后 status: setup_required false、vault_state "unlocked"、authenticated true
func TestSetupTwice(t *testing.T)             // 第二次 → 409 already_setup
func TestSetupConcurrent(t *testing.T)        // 并发 10 个 setup → 恰好 1 个 201，其余 9 个 409           ← Review Focus 5
func TestSetupWeakPassword(t *testing.T)      // 9 个字符 → 400 validation，fields.password 存在
func TestSetupHugePassword(t *testing.T)      // 1025 字节 → 400 validation；meta 表仍为空               ← Review Focus 5
func TestSetupBadNodeName(t *testing.T)       // "NAS 1" → 400，fields.node_name 存在
func TestSetupPassphraseMode(t *testing.T)    // unlock_mode passphrase 不带 passphrase → 400；带了 → 201

func TestLoginWrong(t *testing.T)             // 401 unauthorized
func TestLoginRateLimited(t *testing.T)       // 连错 5 次后第 6 次 → 429，Retry-After == "60"
func TestLoginHugePassword(t *testing.T)      // 1025 字节 → 400，不计入失败次数
func TestProtectedNeedsSession(t *testing.T)  // 不带 cookie 的 GET /api/v1/session → 401
func TestWriteNeedsCSRF(t *testing.T)         // 有会话、不带 X-CSRF-Token 的 POST /api/v1/logout → 403 csrf；带对了 → 204
func TestLogoutInvalidates(t *testing.T)      // 登出后原 cookie → 401
func TestSecureCookieBehindTLS(t *testing.T)  // 请求带 X-Forwarded-Proto: https → cookie 有 Secure；不带 → 没有

func TestUnlockFlow(t *testing.T)             // 口令模式，用同一数据目录新建 Vault 和 API：登录成功；NeedUnlocked 的测试路由 → 503 vault_locked；unlock 错口令 → 403；对口令 → 204；测试路由 → 200
func TestVaultUnavailable(t *testing.T)       // 删掉密钥文件后重建：status.vault_state "keyfile_missing"；NeedUnlocked 的测试路由 → 503 vault_unavailable
func TestSecurityHeaders(t *testing.T)        // 任意响应带约束里写的 CSP 原文和 nosniff
func TestNoHashInResponses(t *testing.T)      // setup、login、status、session 的响应体都不含 "$argon2id$"
func TestUnknownFieldRejected(t *testing.T)   // 请求体带未知字段 → 400 bad_request
func TestBodyTooLarge(t *testing.T)           // 2 MiB 请求体 → 400 bad_request
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/api/`
Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现**

用 `http.ServeMux` 的方法加路径模式。中间件由外到内：panic 恢复（记日志，回 500 `internal`）→ 安全响应头 → 路由自带的 `guards`。来源 IP 取 `r.RemoteAddr` 的主机部分，不信任 `X-Forwarded-For`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/api/ -v`
Expected: PASS（21 个）

- [ ] **Step 5: 提交**

```bash
git add internal/api
git commit -m "feat(api): setup wizard, login, session and unlock endpoints"
```

---

### Task 6: 进程装配与优雅停机

**Files:**
- Create: `internal/app/app.go`
- Modify: `cmd/tern/main.go`
- Test: `internal/app/app_test.go`

**Interfaces:**
- Consumes: 任务 1–5 的全部导出符号
- Produces:

```go
type App struct{ /* … */ }
func Start(ctx context.Context, cfg config.Config, version string) (*App, error)
func (a *App) Addr() string                     // 实际监听地址（测试里 Listen 用 "127.0.0.1:0"）
func (a *App) API() *api.API                    // 后续计划往上挂路由
func (a *App) Shutdown(ctx context.Context) error
```

`Start` 的顺序：建 `DataDir`（0700）→ `store.Open` → `vault.New` + `Load` → `auth.NewSessions`、`auth.NewLimiter` → `api.New` → 监听。配了 `TLSCert`/`TLSKey` 就用 TLS。`http.Server` 设 `ReadHeaderTimeout: 10 * time.Second`。任何一步失败都要把已经打开的资源关掉再返回。

`main.go`：`config.Load(os.Getenv)` → `app.Start` → 等 SIGINT 或 SIGTERM → `Shutdown`（10 秒上限）。启动失败时把错误打到 stderr 并以退出码 1 结束。

- [ ] **Step 1: 写失败的测试**

```go
func TestStartServesStatus(t *testing.T)       // 临时目录启动；GET http://<Addr>/api/v1/status → 200，setup_required true
func TestShutdownReleasesLock(t *testing.T)    // Shutdown 后用同一目录再 Start 成功
func TestStartFailsOnLockedDataDir(t *testing.T) // 第一个还在跑时第二个 Start → errors.Is(err, store.ErrLockHeld)，且第二个没有占住端口
func TestStartCreatesDataDir0700(t *testing.T) // DataDir 原先不存在 → 启动后存在且权限 0700
func TestTLS(t *testing.T)                     // 测试里现生成自签证书；https 请求 status → 200
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/app/`
Expected: FAIL，`undefined: Start`

- [ ] **Step 3: 实现 `app.Start`、`Shutdown`，改写 `main.go`**

- [ ] **Step 4: 运行全部测试**

Run: `make vet && make test`
Expected: PASS

- [ ] **Step 5: 手工冒烟**

```bash
mkdir -p .dev/data .dev/secrets
TERN_LISTEN=127.0.0.1:8080 TERN_DATA_DIR=.dev/data TERN_SECRETS_DIR=.dev/secrets go run ./cmd/tern &
curl -s http://127.0.0.1:8080/api/v1/status
kill %1
```

Expected: `{"version":"dev","setup_required":true,"vault_state":"uninitialized","authenticated":false}`；`kill` 后进程在 10 秒内退出。

- [ ] **Step 6: 提交**

```bash
git add internal/app cmd/tern
git commit -m "feat(app): process wiring, optional TLS and graceful shutdown"
```
