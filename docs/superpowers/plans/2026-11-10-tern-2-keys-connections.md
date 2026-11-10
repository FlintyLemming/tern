# tern 计划 2 · 密钥与连接 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 能管理 SSH 密钥和连接、人工确认主机指纹、测试连接；并且能把钥匙安全地交给 OpenSSH 子进程。

**Architecture:** `sshx` 用 `golang.org/x/crypto/ssh` 处理密钥、探测指纹、做预检，这些都只握手、不传数据。真正的传输由 OpenSSH 子进程完成：钥匙放在进程内的 ssh-agent 里（unix socket 位于 tmpfs 运行目录），私钥不落任何文件；每次运行生成一份临时 `ssh_config`，后面三个引擎共用它。`access` 把“一个连接”变成子进程可用的这些临时物料。

**Tech Stack:** Go 1.26、`golang.org/x/crypto/ssh` 及其 `agent` 子包、标准库 `net/http`（WebDAV 预检）、OpenSSH 客户端（仅集成测试）。

**Spec:** `2026-11-10-nas-backup-sync-design.md` §5、§9.2、§9.3。执行者同时读 `docs/superpowers/plans/2026-11-10-tern-0-index.md`。

## Global Constraints

索引第 2 节全部适用。本计划另加：

- AAD 取值：`ssh_keys/<id>/private`、`ssh_keys/<id>/passphrase`、`connections/<id>/password`。
- 本计划所有路由都带 `NeedSession` 和 `NeedUnlocked`。
- 生成的 `ssh_config` 必须与任务 5 给出的模板逐字一致。
- 输入校验（在 `model.Connection.Validate` 里）：
  - `host`：RFC 1123 主机名（总长 ≤ 253）或 IP 字面量（IPv6 不带方括号）；不含空白和控制字符；不以 `-` 开头。
  - `port`：1–65535，缺省 22。
  - `username`：匹配 `^[A-Za-z0-9._][A-Za-z0-9._-]{0,63}$`。
  - WebDAV `url`：scheme 为 `http` 或 `https`；有主机；不含 userinfo；不含控制字符。
- 测试里现生成密钥，不提交任何私钥夹具。
- unix socket 路径有长度上限（约 104 字节）。测试里的运行目录用短路径：`os.MkdirTemp("/tmp", "t")`，不要用 `t.TempDir()`。
- 新增错误码：`passphrase_required` 400、`bad_passphrase` 400、`unsupported_key` 400、`host_key_required` 400、`unreachable` 502。

## Review Focus

1. **导入的私钥带 CRLF、首尾空行、缺末尾换行** → 正常解析。（任务 1）
2. **主机是 IPv6 字面量**（组网工具常给这种地址）→ 探测、预检、`ssh_config` 都能用。（任务 2、5）
3. **`host` 或 `username` 里塞了换行、空格、`-oProxyCommand=…`** → 保存时 400，绝不写进 `ssh_config`。（任务 2）
4. **对端 TCP 通了但不回话** → 探测和预检在超时内返回 `network` 错误，不挂住请求。（任务 2、3）
5. **WebDAV 地址里内嵌了凭据**（`https://u:p@host/`）→ 拒绝，凭据只能走密码字段。（任务 2）

---

### Task 1: SSH 密钥：生成、导入、存储、接口

**Files:**
- Create: `internal/sshx/keys.go`、`internal/model/sshkey.go`
- Create: `internal/store/migrations/0002_keys_connections.sql`、`internal/store/keys.go`
- Create: `internal/api/keys.go`
- Modify: `internal/app/app.go`（挂路由）
- Test: `internal/sshx/keys_test.go`、`internal/store/keys_test.go`、`internal/api/keys_test.go`

**Interfaces:**
- Consumes: `vault.Vault.Seal/Open`、`api.API.Handle`、`store.Store`
- Produces:

```go
// internal/sshx
type Parsed struct {
    Raw         any        // ssh.ParseRawPrivateKey 的结果，交给 agent 用
    Signer      ssh.Signer // 交给预检用
    Authorized  string     // "ssh-ed25519 AAAA…"，不带注释
    Fingerprint string     // "SHA256:…"
}
func Generate() (privPEM []byte, p Parsed, err error)       // ed25519，OpenSSH 私钥格式，无口令
func ParsePrivate(privPEM, passphrase []byte) (Parsed, error) // 先规整：CRLF→LF、去首尾空白、补末尾换行
var ErrPassphraseRequired, ErrBadPassphrase, ErrUnsupportedKey error

// internal/model
type SSHKey struct {
    ID string; Version int; Name, PublicKey, Fingerprint string
    HasPassphrase bool; CreatedAt, UpdatedAt time.Time
}

// internal/store
func (s *Store) CreateKey(ctx context.Context, k model.SSHKey, privSealed, passSealed string) error // 重名 → ErrDuplicate
func (s *Store) ListKeys(ctx context.Context) ([]model.SSHKey, error)
func (s *Store) GetKey(ctx context.Context, id string) (model.SSHKey, error)
func (s *Store) KeySecrets(ctx context.Context, id string) (privSealed, passSealed string, err error)
func (s *Store) RenameKey(ctx context.Context, id string, version int, name string) error // 版本不符 → ErrVersion
func (s *Store) DeleteKey(ctx context.Context, id string) error                           // 被连接引用 → ErrInUse
```

`0002_keys_connections.sql`：

```sql
CREATE TABLE ssh_keys (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL, name TEXT NOT NULL UNIQUE,
  public_key TEXT NOT NULL, fingerprint TEXT NOT NULL,
  private_key TEXT NOT NULL, passphrase TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
CREATE TABLE connections (
  id TEXT PRIMARY KEY, version INTEGER NOT NULL,
  type TEXT NOT NULL CHECK (type IN ('ssh','webdav')), name TEXT NOT NULL UNIQUE,
  host TEXT NOT NULL DEFAULT '', port INTEGER NOT NULL DEFAULT 0, username TEXT NOT NULL DEFAULT '',
  ssh_key_id TEXT REFERENCES ssh_keys(id) ON DELETE RESTRICT,
  host_key TEXT NOT NULL DEFAULT '', url TEXT NOT NULL DEFAULT '', password TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL
) STRICT;
```

路由：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `GET /api/v1/ssh-keys` | — | 200，数组，元素为 `{id, version, name, public_key, fingerprint, has_passphrase, created_at, updated_at}` |
| `POST /api/v1/ssh-keys` | `{name, mode:"generate"}` 或 `{name, mode:"import", private_key, passphrase?}` | 201，单个对象 |
| `PUT /api/v1/ssh-keys/{id}` | `{name, version}` | 200，单个对象 |
| `DELETE /api/v1/ssh-keys/{id}` | — | 204 |

`name` 长度 1–64。重名 → 409 `conflict`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/sshx/keys_test.go
func TestGenerateParses(t *testing.T)            // Generate 的 PEM 经 ParsePrivate(pem, nil) 成功；Authorized 以 "ssh-ed25519 " 开头；Fingerprint 以 "SHA256:" 开头
func TestParseKinds(t *testing.T)                // 现生成 ed25519、ecdsa-p256、rsa-3072，各自无口令和带口令 "pp-123456"：都能解析，Authorized 与原公钥一致
func TestParseNeedsPassphrase(t *testing.T)      // 带口令的密钥不给口令 → errors.Is(err, ErrPassphraseRequired)
func TestParseBadPassphrase(t *testing.T)        // 给错口令 → ErrBadPassphrase
func TestParseGarbage(t *testing.T)              // "not a key" → ErrUnsupportedKey
func TestParseTolerantOfWhitespace(t *testing.T) // 换成 CRLF / 首尾加空行 / 去掉末尾换行 → 都能解析    ← Review Focus 1

// internal/api/keys_test.go
func TestCreateKeyGenerate(t *testing.T)         // 201；有 public_key 和 fingerprint；原始响应体不含 "PRIVATE KEY"
func TestCreateKeyImport(t *testing.T)           // 导入带口令的密钥 → 201，has_passphrase true
func TestImportNeedsPassphrase(t *testing.T)     // → 400 passphrase_required；ssh_keys 表没有新行
func TestImportBadPassphrase(t *testing.T)       // → 400 bad_passphrase
func TestImportGarbage(t *testing.T)             // → 400 unsupported_key
func TestKeysNeverReturnPrivate(t *testing.T)    // GET 列表的原始响应体不含 "PRIVATE KEY"，也不含口令原文
func TestKeyStoredSealed(t *testing.T)           // 直接查库：private_key 和 passphrase 两列都以 "v1." 开头
func TestKeyNameDuplicate(t *testing.T)          // 同名 → 409 conflict
func TestRenameVersionConflict(t *testing.T)     // PUT 带旧 version → 409 conflict
func TestKeysNeedUnlockedVault(t *testing.T)     // 保险箱锁定 → 503 vault_locked
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/sshx/ ./internal/api/ -run 'Key|Parse|Generate'`
Expected: FAIL，`undefined: Generate`

- [ ] **Step 3: 实现**

`Generate` 用 `ed25519.GenerateKey` 加 `ssh.MarshalPrivateKey`。`ParsePrivate` 用 `ssh.ParseRawPrivateKey` / `ssh.ParseRawPrivateKeyWithPassphrase`；`*ssh.PassphraseMissingError` 映射为 `ErrPassphraseRequired`，`x509.IncorrectPasswordError` 映射为 `ErrBadPassphrase`，其余解析错误映射为 `ErrUnsupportedKey`。入库的是规整后的 PEM 原文（带口令的仍带口令），口令单独加密存一列。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/sshx/ ./internal/store/ ./internal/api/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sshx internal/model internal/store internal/api internal/app
git commit -m "feat(keys): generate, import and store ssh keys"
```

---

### Task 2: 主机指纹探测与连接管理

**Files:**
- Create: `internal/model/errkind.go`、`internal/model/connection.go`
- Create: `internal/sshx/sshtest/server.go`（仅供测试的进程内 SSH 服务器）
- Create: `internal/sshx/probe.go`、`internal/store/connections.go`、`internal/api/connections.go`
- Test: `internal/model/connection_test.go`、`internal/sshx/probe_test.go`、`internal/api/connections_test.go`

**Interfaces:**
- Produces:

```go
// internal/model
type ErrKind string
const (
    KindNetwork ErrKind = "network"  // 重试
    KindUnknown ErrKind = "unknown"  // 重试
    KindAuth    ErrKind = "auth"     // 以下都不重试
    KindHostKey ErrKind = "host_key"
    KindConfig  ErrKind = "config"
    KindData    ErrKind = "data"     // 个别文件失败，重跑也没用
    KindGuard   ErrKind = "guard"    // 保护性中止
)
type KindError struct { Kind ErrKind; Msg string; Err error } // 实现 Error() 和 Unwrap()
func KindOf(err error) ErrKind      // nil → ""；不是 *KindError → KindUnknown
func (k ErrKind) Retryable() bool   // 仅 KindNetwork、KindUnknown 为 true

type Connection struct {
    ID string; Version int; Type string // "ssh" | "webdav"
    Name, Host string; Port int; Username, SSHKeyID string
    HostKey, HostKeyFingerprint string  // HostKey 为 authorized 格式；指纹由它算出，不入库
    URL string; HasPassword bool
    CreatedAt, UpdatedAt time.Time
}
func (c *Connection) ApplyDefaults()          // ssh 且 Port == 0 → 22
func (c Connection) Validate() map[string]string // 字段名 → 错误码；空 map 表示合法

// internal/sshx/sshtest
func Start(t testing.TB, opts ...Option) *Server
func (s *Server) Host() string; func (s *Server) Port() int
func (s *Server) HostPublicKey() ssh.PublicKey
func WithAuthorized(pub ssh.PublicKey) Option // 不给则任何钥匙都被拒
func WithoutSFTP() Option                     // 拒绝 sftp 子系统请求
func WithHang() Option                        // 接受 TCP 后一个字节都不回
func WithListen(addr string) Option           // 默认 "127.0.0.1:0"
func WithExec(fn func(cmd string) (stdout string, exit int)) Option

// internal/sshx
type HostKey struct { Type, Authorized, Fingerprint string }
func ProbeHostKey(ctx context.Context, host string, port int) (HostKey, error) // 10 秒超时；拿到主机公钥即断开

// internal/store
func (s *Store) CreateConnection(ctx context.Context, c model.Connection, passwordSealed string) error
func (s *Store) UpdateConnection(ctx context.Context, c model.Connection, passwordSealed *string) error // nil 表示不改密码
func (s *Store) GetConnection(ctx context.Context, id string) (model.Connection, error)
func (s *Store) ListConnections(ctx context.Context) ([]model.Connection, error)
func (s *Store) ConnectionPassword(ctx context.Context, id string) (sealed string, err error)
func (s *Store) DeleteConnection(ctx context.Context, id string) error
```

路由：

| 方法与路径 | 请求体 | 成功响应 |
|---|---|---|
| `POST /api/v1/connections/probe` | `{host, port}` | 200 `{type, authorized, fingerprint}` |
| `GET /api/v1/connections` | — | 200，数组 |
| `POST /api/v1/connections` | SSH：`{type, name, host, port, username, ssh_key_id, host_key}`；WebDAV：`{type, name, url, username, password}` | 201 |
| `GET /api/v1/connections/{id}` | — | 200 |
| `PUT /api/v1/connections/{id}` | 同创建，另加 `version` | 200 |
| `DELETE /api/v1/connections/{id}` | — | 204 |

响应对象：`{id, version, type, name, host, port, username, ssh_key_id, host_key, host_key_fingerprint, url, has_password, created_at, updated_at}`。

规则：
- **服务端从不替人确认指纹。** 创建 SSH 连接必须带 `host_key`，值是 `probe` 返回的 `authorized` 原样回传。缺了 → 400 `host_key_required`。
- `PUT` 改了 `host` 或 `port` 而请求里的 `host_key` 与库里的相同 → 400 `host_key_required`（必须重新探测并确认）。
- WebDAV 的 `password` 只写不读。`PUT` 不带 `password` 字段表示不改。
- 探测连不上 → 502 `unreachable`。
- 探测时主机密钥算法的优先顺序：ed25519、ecdsa（256、384、521）、rsa-sha2-512、rsa-sha2-256。

- [ ] **Step 1: 写失败的测试**

```go
// internal/model/connection_test.go
func TestKindRetryable(t *testing.T)            // network、unknown → true；auth、host_key、config、data、guard → false
func TestKindOf(t *testing.T)                   // nil → ""；errors.New("x") → unknown；包了两层的 *KindError 仍能取出 Kind
func TestValidateSSHOK(t *testing.T)            // "nas2.example"、"10.0.0.2"、"fd7a:115c::1" 都合法
func TestValidateRejectsInjection(t *testing.T) // host: "a\nProxyCommand x"、"-oProxyCommand=x"、"a b"、"[::1]"；username: "root\n"、"-x"、"a b" → 各自报对应字段 ← Review Focus 3
func TestValidatePort(t *testing.T)             // 0（应用默认值后为 22）合法；-1、65536 → fields["port"]
func TestValidateWebDAV(t *testing.T)           // "https://u:p@h/" → fields["url"]=="embedded_credentials"；"ftp://h/" → "scheme"；"https://h/dav" 合法 ← Review Focus 5

// internal/sshx/probe_test.go
func TestProbeReturnsServerKey(t *testing.T)    // Fingerprint == ssh.FingerprintSHA256(srv.HostPublicKey())
func TestProbeIPv6(t *testing.T)                // WithListen("[::1]:0")，host 传 "::1" → 成功          ← Review Focus 2
func TestProbeRefused(t *testing.T)             // 没人监听的端口 → KindOf(err) == network
func TestProbeHangTimesOut(t *testing.T)        // WithHang，ctx 超时 2s → KindOf(err) == network，耗时 < 3s ← Review Focus 4

// internal/api/connections_test.go
func TestProbeEndpoint(t *testing.T)            // 对 sshtest 服务器 → 200，fingerprint 正确
func TestProbeUnreachable(t *testing.T)         // → 502 unreachable
func TestCreateSSHRequiresHostKey(t *testing.T) // 不带 host_key → 400 host_key_required
func TestCreateSSHOK(t *testing.T)              // 201；host_key_fingerprint 与探测到的一致
func TestUpdateHostNeedsNewHostKey(t *testing.T)// 只改 host → 400 host_key_required
func TestWebDAVPasswordWriteOnly(t *testing.T)  // 响应体不含密码原文，has_password true；库里 password 列以 "v1." 开头
func TestWebDAVUpdateKeepsPassword(t *testing.T)// PUT 不带 password → 密文列不变
func TestDeleteKeyInUse(t *testing.T)           // 被连接引用的密钥 DELETE → 409 in_use
func TestConnectionVersionConflict(t *testing.T)// PUT 带旧 version → 409 conflict
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/model/ ./internal/sshx/ ./internal/api/ -run 'Kind|Validate|Probe|SSH|WebDAV|Connection|InUse'`
Expected: FAIL，`undefined: ProbeHostKey`

- [ ] **Step 3: 实现**

`ProbeHostKey` 用一个会失败的认证方式发起握手，在 `HostKeyCallback` 里记下公钥后返回一个哨兵错误来中止。所有拨号用 `net.JoinHostPort`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/model internal/sshx internal/store internal/api
git commit -m "feat(connections): host key probing and connection management"
```

---

### Task 3: 预检：SSH 与 WebDAV

**Files:**
- Create: `internal/sshx/check.go`、`internal/webdavx/check.go`
- Test: `internal/sshx/check_test.go`、`internal/webdavx/check_test.go`

**Interfaces:**
- Consumes: `model.KindError`、`sshx.Parsed`、`sshtest`
- Produces:

```go
// internal/sshx
type Target struct { Host string; Port int; User string; HostKey string } // HostKey 为 authorized 格式
func Check(ctx context.Context, t Target, signer ssh.Signer) error         // 拨号超时 15 秒

// internal/webdavx
func Check(ctx context.Context, rawURL, user, pass string) error           // PROPFIND，Depth: 0，15 秒
```

两个函数的返回值要么是 `nil`，要么是 `*model.KindError`：

| SSH 情形 | Kind |
|---|---|
| 拨号失败、超时、握手中途断开 | `network` |
| 对端主机公钥与 `Target.HostKey` 不同 | `host_key` |
| 公钥认证被拒 | `auth` |
| 认证通过但 sftp 子系统请求被拒 | `config`，Msg `"remote has no sftp subsystem"` |

| WebDAV 情形 | Kind |
|---|---|
| 传输层错误、超时、502/503/504 | `network` |
| 401、403 | `auth` |
| 207 | 通过 |
| TLS 证书校验失败、404、405、其他状态码 | `config` |

- [ ] **Step 1: 写失败的测试**

```go
// internal/sshx/check_test.go
func TestCheckOK(t *testing.T)            // 授权过的钥匙 + 正确主机公钥 → nil
func TestCheckWrongHostKey(t *testing.T)  // Target.HostKey 用另一把现生成的公钥 → host_key
func TestCheckUnauthorized(t *testing.T)  // 服务器没授权这把钥匙 → auth
func TestCheckNoSFTP(t *testing.T)        // WithoutSFTP → config，Msg 含 "sftp subsystem"
func TestCheckRefused(t *testing.T)       // → network
func TestCheckHang(t *testing.T)          // WithHang，ctx 2s → network，耗时 < 3s            ← Review Focus 4
func TestCheckHostKeyBeforeAuth(t *testing.T) // 主机公钥不符且钥匙未授权 → host_key（先验主机，再谈认证）

// internal/webdavx/check_test.go（httptest.Server）
func TestWebDAVOK(t *testing.T)           // 回 207 → nil；请求方法 PROPFIND、头 Depth: 0、带 Basic 认证
func TestWebDAVAuth(t *testing.T)         // 401 → auth；403 → auth
func TestWebDAVNotDAV(t *testing.T)       // 405 → config；404 → config
func TestWebDAVGateway(t *testing.T)      // 502、503、504 → network
func TestWebDAVDown(t *testing.T)         // 服务器已关 → network
func TestWebDAVBadCert(t *testing.T)      // httptest.NewTLSServer 的自签证书 → config
func TestWebDAVNoRedirectLeak(t *testing.T) // 回 302 到另一个主机 → 不跟随，config（不把凭据带到别处）
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/sshx/ ./internal/webdavx/ -run Check`
Expected: FAIL，`undefined: Check`

- [ ] **Step 3: 实现两个 `Check`**

- [ ] **Step 4: 运行，确认通过**

Run: `go test ./internal/sshx/ ./internal/webdavx/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sshx internal/webdavx
git commit -m "feat(preflight): typed ssh and webdav connectivity checks"
```

---

### Task 4: 运行目录与日志脱敏

**Files:**
- Create: `internal/rundir/rundir.go`、`internal/rundir/tmpfs_linux.go`、`internal/rundir/tmpfs_other.go`
- Create: `internal/redact/redact.go`
- Modify: `internal/app/app.go`（启动时校验并清扫运行目录）
- Test: `internal/rundir/rundir_test.go`、`internal/rundir/tmpfs_linux_test.go`、`internal/redact/redact_test.go`

**Interfaces:**
- Produces:

```go
// internal/rundir
func VerifyTmpfs(dir string) error             // Linux：statfs 的文件系统类型不是 tmpfs → error，信息含 "tmpfs"；其他系统恒为 nil
func Sweep(base string) error                  // 删掉 base 下的全部子目录（上次进程的残留）
func New(base, runID string) (*Dir, error)     // 建 <base>/<runID>，0700
type Dir struct{ Path string }
func (d *Dir) WriteSecret(name string, data []byte) (path string, err error) // 0600；name 不得含路径分隔符
func (d *Dir) Close() error                    // 删除整个目录

// internal/redact
func New() *Redactor
func (r *Redactor) Add(secret []byte)          // 长度 < 4 的不登记
func (r *Redactor) Writer(w io.Writer) io.WriteCloser // 按行缓冲；Close 时把没有换行结尾的残余也输出
```

脱敏规则，按顺序应用到每一行：
1. 处于 PEM 私钥块内（从匹配 `-----BEGIN [A-Z ]*PRIVATE KEY-----` 的行到对应的 `END` 行）→ 整块只输出一行 `*** private key ***`。
2. 每个登记过的 secret 的所有出现 → `***`。
3. URL 里的 userinfo 密码：`://user:xxx@` → `://user:***@`。

`app.Start` 的改动：建 `RuntimeDir`（0700）→ `VerifyTmpfs`，失败则启动失败 → `Sweep`。

- [ ] **Step 1: 写失败的测试**

```go
// internal/rundir/rundir_test.go
func TestNewDirMode(t *testing.T)             // 目录权限 0700
func TestWriteSecretMode(t *testing.T)        // 文件权限 0600，内容一致
func TestWriteSecretRejectsPath(t *testing.T) // name "../x" 或 "a/b" → error
func TestCloseRemovesEverything(t *testing.T) // Close 后目录不存在
func TestSweepRemovesLeftovers(t *testing.T)  // base 下预置两个子目录 → Sweep 后 base 为空但仍存在

// internal/rundir/tmpfs_linux_test.go（//go:build linux）
func TestVerifyTmpfs(t *testing.T)            // /dev/shm → nil；磁盘上的临时目录 → error 含 "tmpfs"

// internal/redact/redact_test.go
func TestMasksSecret(t *testing.T)            // Add("s3cr3t-pass")；写 "pw=s3cr3t-pass x\n" → "pw=*** x\n"
func TestMasksAcrossWrites(t *testing.T)      // 分两次写 "pw=s3cr3" 和 "t-pass\n" → 输出不含原文
func TestMasksAllOccurrences(t *testing.T)    // 同一行出现两次 → 两处都是 ***
func TestMasksPEMBlock(t *testing.T)          // 一段 5 行的 OPENSSH PRIVATE KEY 块 → 恰好一行 "*** private key ***"，前后的普通行原样
func TestMasksURLUserinfo(t *testing.T)       // "GET https://u:p4ss@h/x" → "GET https://u:***@h/x"
func TestIgnoresShortSecrets(t *testing.T)    // Add("abc")；写 "abcdef\n" → 原样
func TestFlushOnClose(t *testing.T)           // 写 "tail" 不带换行，Close 后输出 "tail"
func TestConcurrentWriters(t *testing.T)      // 两个 goroutine 各写 1000 行，-race 下无报告，行不交错
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/rundir/ ./internal/redact/`
Expected: FAIL，`undefined: New`

- [ ] **Step 3: 实现**

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/rundir/ ./internal/redact/ ./internal/app/`
Expected: PASS。本机不是 Linux 时 `TestVerifyTmpfs` 不参与编译，留给任务 6 的容器跑。

说明：从这里起，本机开发要显式设 `TERN_RUNTIME_DIR=.dev/run`，因为 macOS 没有 `/dev/shm`。

- [ ] **Step 5: 提交**

```bash
git add internal/rundir internal/redact internal/app
git commit -m "feat: tmpfs run directories and log redaction"
```

---

### Task 5: 凭据交付：进程内 agent、ssh_config、access

**Files:**
- Create: `internal/sshx/agent.go`、`internal/sshx/config.go`、`internal/access/access.go`
- Modify: `internal/api/connections.go`（加“测试连接”）
- Test: `internal/sshx/agent_test.go`、`internal/sshx/config_test.go`、`internal/access/access_test.go`、`internal/api/connections_test.go`

**Interfaces:**
- Consumes: `rundir.Dir`、`redact.Redactor`、`sshx.Parsed/Target/Check`、`webdavx.Check`、`store`、`vault`
- Produces:

```go
// internal/sshx
const Alias = "tern-target"
func StartAgent(dir *rundir.Dir, raw any) (*Agent, error) // 监听 <dir>/agent.sock（0600）；keyring 里只有这一把钥匙
func (a *Agent) Socket() string
func (a *Agent) Close() error                             // 停止监听并删除 socket
func WriteConfig(dir *rundir.Dir, t Target, agentSock string) (path string, err error) // 写 <dir>/ssh_config 和 <dir>/known_hosts

// internal/access
type Access struct {
    Kind      string      // "ssh" | "webdav"
    SSHConfig string      // ssh：ssh_config 的路径。用法：ssh -F <SSHConfig> tern-target
    Target    sshx.Target // ssh
    URL, User, Pass string // webdav
}
type Opener struct { Store *store.Store; Vault *vault.Vault }
func (o Opener) Open(ctx context.Context, connID string, dir *rundir.Dir, red *redact.Redactor) (Access, func(), error)
func (o Opener) Check(ctx context.Context, connID string) error // 解密后调 sshx.Check 或 webdavx.Check
```

`ssh_config` 模板（`<…>` 处替换，其余逐字）：

```
Host tern-target
  HostName <host>
  Port <port>
  User <user>
  HostKeyAlias tern-target
  UserKnownHostsFile <dir>/known_hosts
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  HostKeyAlgorithms <algos>
  IdentityAgent <dir>/agent.sock
  IdentityFile none
  PubkeyAuthentication yes
  PasswordAuthentication no
  KbdInteractiveAuthentication no
  BatchMode yes
  ServerAliveInterval 15
  ServerAliveCountMax 4
  ConnectTimeout 15
  ControlMaster no
  ForwardAgent no
  LogLevel ERROR
```

- `known_hosts` 只有一行：`tern-target <密钥类型> <base64>`。
- `<algos>`：主机密钥类型为 `ssh-rsa` 时写 `rsa-sha2-512,rsa-sha2-256`；其余类型写类型名本身。
- `<host>` 为 IPv6 时不带方括号。

`Opener.Open` 的行为：
- SSH：解密私钥和口令 → `ParsePrivate` → `StartAgent` → `WriteConfig`。若密钥有口令，把口令 `red.Add`。返回的关闭函数负责关 agent。
- WebDAV：解密密码，`red.Add(密码)`，填 `URL/User/Pass`。
- 保险箱未解锁 → 返回 `vault.ErrLocked`。

路由：`POST /api/v1/connections/{id}/test` → 200 `{ok, kind, message}`。`ok` 为 false 时 `kind` 是 `ErrKind` 的值。预检失败不是 HTTP 错误。

- [ ] **Step 1: 写失败的测试**

```go
// internal/sshx/agent_test.go
func TestAgentAuthenticates(t *testing.T)     // 连 socket，用 agent.NewClient(conn).Signers() 做认证 → 能登上 sshtest（WithAuthorized 这把公钥）
func TestAgentHoldsOneKey(t *testing.T)       // agent 客户端 List() 长度 == 1，公钥与 Parsed.Authorized 一致
func TestAgentSocketMode(t *testing.T)        // agent.sock 权限 0600
func TestAgentClose(t *testing.T)             // Close 后 socket 文件不存在，再连 → error

// internal/sshx/config_test.go
func TestWriteConfigGolden(t *testing.T)      // Target{"nas2.example", 2222, "backup", ed25519 公钥} → 与模板替换后的文本逐字相等；known_hosts 恰好一行
func TestWriteConfigAlgos(t *testing.T)       // ssh-ed25519 → "ssh-ed25519"；ssh-rsa → "rsa-sha2-512,rsa-sha2-256"；ecdsa-sha2-nistp256 → 原名
func TestWriteConfigIPv6(t *testing.T)        // Host "fd7a:115c::1" → 行 "  HostName fd7a:115c::1"       ← Review Focus 2
func TestWriteConfigFileModes(t *testing.T)   // 两个文件都是 0600

// internal/access/access_test.go
func TestOpenSSH(t *testing.T)                // Open 后运行目录里恰好有 agent.sock、ssh_config、known_hosts；没有任何文件内容含 "PRIVATE KEY"
func TestOpenSSHWithPassphraseKey(t *testing.T) // 带口令的密钥：agent 能认证；往 red 的 Writer 写口令 → 被打码
func TestOpenWebDAV(t *testing.T)             // Pass 等于创建时的密码；往 red 的 Writer 写该密码 → 被打码；运行目录为空
func TestOpenCloseStopsAgent(t *testing.T)    // 调用关闭函数后 agent.sock 不存在
func TestOpenLocked(t *testing.T)             // 保险箱锁定 → errors.Is(err, vault.ErrLocked)
func TestOpenerCheck(t *testing.T)            // 对 sshtest：授权 → nil；未授权 → auth

// internal/api/connections_test.go
func TestConnectionTestOK(t *testing.T)       // 200 {ok:true}
func TestConnectionTestAuth(t *testing.T)     // 200 {ok:false, kind:"auth"}
func TestConnectionTestHostKeyChanged(t *testing.T) // 重启 sshtest（主机公钥变了）→ 200 {ok:false, kind:"host_key"}
```

- [ ] **Step 2: 运行，确认失败**

Run: `go test ./internal/sshx/ ./internal/access/ ./internal/api/ -run 'Agent|WriteConfig|Open|ConnectionTest'`
Expected: FAIL，`undefined: StartAgent`

- [ ] **Step 3: 实现**

agent 用 `agent.NewKeyring()` 加 `agent.ServeAgent`，每个进来的连接一个 goroutine。监听前先把进程 umask 的影响排除：创建 socket 后显式 `chmod 0600`。

- [ ] **Step 4: 运行，确认通过**

Run: `go test -race ./internal/...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/sshx internal/access internal/api
git commit -m "feat(access): in-process ssh agent, generated ssh_config and connection testing"
```

---

### Task 6: 集成测试环境，用真 OpenSSH 验证

**Files:**
- Create: `deploy/fetch-tools.sh`
- Create: `test/integration/compose.yaml`、`test/integration/runner.Dockerfile`
- Create: `test/integration/sshd/Dockerfile`、`test/integration/sshd/sshd_config`、`test/integration/sshd/entrypoint.sh`
- Create: `test/integration/helpers_test.go`、`test/integration/ssh_test.go`（都带 `//go:build integration`）
- Modify: `Makefile`（加 `itest`）

**Interfaces:**
- Consumes: `sshx`、`rundir`
- Produces（供计划 4、5、7 的集成测试用）：

```go
// test/integration/helpers_test.go
func authorize(t *testing.T) sshx.Parsed   // 现生成密钥，公钥追加进共享卷里的 /keys/authorized_keys
func target(t *testing.T) sshx.Target      // 主机名取环境变量 TERN_ITEST_SSH_HOST，用户 TERN_ITEST_SSH_USER；主机公钥由 ProbeHostKey 取
func openSSH(t *testing.T, p sshx.Parsed, tg sshx.Target) (cfgPath string) // StartAgent + WriteConfig，t.Cleanup 里关闭
func remotePath(t *testing.T) string       // 对端 /srv 下的一个新目录，测试间互不干扰
```

`deploy/fetch-tools.sh <目标目录>`：
- 读环境变量 `TARGETARCH`（`amd64` 或 `arm64`，其他值报错退出）。
- 下载 `https://github.com/restic/restic/releases/download/v0.19.1/restic_0.19.1_linux_<arch>.bz2` 和 `https://github.com/rclone/rclone/releases/download/v1.76.0/rclone-v1.76.0-linux-<arch>.zip`。
- 用脚本里写死的四个 SHA256（两个工具乘两个架构）校验，不符则以非零退出码结束，不留下任何可执行文件。
- 解压后把 `restic`、`rclone` 放进目标目录，权限 0755。

compose 环境：

| 服务 | 内容 |
|---|---|
| `sshd` | Alpine 3.24 加 `openssh-server`、`rsync`、`iproute2`。用户 `backup`（账号要解锁，否则 sshd 拒绝公钥登录）。`sshd_config`：`PasswordAuthentication no`、`AuthorizedKeysFile /keys/authorized_keys`、`StrictModes no`、`Subsystem sftp internal-sftp`。`cap_add: [NET_ADMIN]`（计划 7 的弱网测试要用）。卷：`keys:/keys`、`remote:/srv`。 |
| `runner` | `golang:1.27-alpine` 加 `rsync`、`openssh-client-default`、`iproute2`，再跑 `fetch-tools.sh /usr/local/bin`。挂仓库到 `/src`，卷 `keys:/keys`。环境变量 `TERN_ITEST_SSH_HOST=sshd`、`TERN_ITEST_SSH_USER=backup`。`/dev/shm` 保持 Docker 默认的 tmpfs。 |

`Makefile`：

```make
itest:
	docker compose -f test/integration/compose.yaml run --rm --build runner \
	  go test -tags integration -count=1 ./internal/... ./test/integration/...
```

带上 `./internal/...` 是为了让只在 Linux 下编译的测试（如 `TestVerifyTmpfs`、计划 4 的 `TestIsMounted`）在容器里跑到。

- [ ] **Step 1: 取四个哈希，写进 `fetch-tools.sh`**

```bash
curl -fsSL https://github.com/restic/restic/releases/download/v0.19.1/SHA256SUMS | grep -E 'linux_(amd64|arm64)\.bz2'
curl -fsSL https://github.com/rclone/rclone/releases/download/v1.76.0/SHA256SUMS | grep -E 'linux-(amd64|arm64)\.zip'
```

Expected: 各输出两行。把哈希逐字抄进脚本。

- [ ] **Step 2: 写失败的测试**

```go
// test/integration/ssh_test.go
func TestToolVersions(t *testing.T)             // `restic version` 输出含 "0.19.1"；`rclone version` 含 "v1.76.0"；`rsync --version`、`ssh -V` 退出 0
func TestRealSSHWithAgent(t *testing.T)         // exec：ssh -F <cfg> tern-target echo ok → 退出 0，stdout == "ok\n"
func TestRealSSHWrongHostKey(t *testing.T)      // Target.HostKey 换成另一把现生成的公钥 → 退出 255，stderr 含 "Host key verification failed"
func TestRealSSHUnauthorizedKey(t *testing.T)   // 没授权的钥匙 → 退出 255，stderr 含 "Permission denied"
func TestRealSSHNoKeyMaterial(t *testing.T)     // ssh 跑 `sleep 2` 期间：运行目录里只有 agent.sock、ssh_config、known_hosts；/proc/<pid>/cmdline 和 environ 都不含 "PRIVATE KEY"
func TestRealCheck(t *testing.T)                // sshx.Check 对真 sshd：正常 → nil；错主机公钥 → host_key；未授权 → auth
func TestRealSSHIPv6(t *testing.T)              // compose 网络开 IPv6；用 sshd 的 IPv6 地址作 Host → echo ok 成功     ← Review Focus 2
func TestFetchToolsRejectsBadHash(t *testing.T) // 复制脚本、改掉一个哈希的一位后执行 → 退出码非 0，目标目录里没有 restic 也没有 rclone
```

- [ ] **Step 3: 运行，确认失败**

Run: `make itest`
Expected: 镜像构建成功；测试 FAIL，`undefined: authorize`

- [ ] **Step 4: 实现 helpers，补齐环境文件**

- [ ] **Step 5: 运行，确认通过**

Run: `make itest`
Expected: PASS，其中包含在容器里才编译的 `TestVerifyTmpfs`

- [ ] **Step 6: 提交**

```bash
git add deploy/fetch-tools.sh test/integration Makefile
git commit -m "test: integration environment with real openssh, restic and rclone"
```
