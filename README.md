# tern

## 1. 这是什么

tern 是跑在 Docker 里（在 QNAP 上也可以装成应用，见第 3 节）的备份与同步节点：用 restic 把本机目录备份到远端，用 SFTP、rsync over SSH 或 WebDAV 单向同步文件夹，全部操作都在自带的网页界面里完成。它是 QNAP HBS3 的替代品，针对的是两个痛点：

1. 备份用厂商私有格式（qdff），数据被锁在 QNAP 生态里。tern 的备份是标准的 restic 仓库，离开 tern 也能用原生 restic 恢复。
2. 同步不支持 SFTP 和 rsync over SSH，对端必须是厂商认识的服务。tern 只要求对端有 SSH 或 WebDAV。

## 2. 快速开始

镜像发布在 `ghcr.io/flintylemming/tern`，amd64 和 arm64 都有，不用登录就能拉。每个版本（如 `v0.1.0`）有同名的镜像标签；`latest` 跟着最新的正式版本走，`v1.3.0-rc1` 这种带 `-` 的预发布版本不会动它。已有的版本见 [GitHub 上的镜像页](https://github.com/FlintyLemming/tern/pkgs/container/tern)。

把下面的内容存成 `compose.yaml`（仓库里的 `deploy/compose.yaml` 就是它），按自己的目录改好 `volumes` 里的后两行：

```yaml
services:
  tern:
    image: ghcr.io/flintylemming/tern:${TERN_VERSION:-latest}
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
      - ./secrets:/secrets                   # tern 自动生成的密钥文件，和 data 分开保管
      - /share/Photos:/sources/photos:ro     # 只推送或备份的目录，只读挂载
      - /share/Inbox:/targets/inbox          # 拉取同步的落地目录
    sysctls:
      net.ipv4.tcp_congestion_control: bbr   # 宿主机内核不支持时删掉这一行
    security_opt:
      - no-new-privileges:true
    cap_drop: [ALL]
    cap_add: [CHOWN, DAC_OVERRIDE, DAC_READ_SEARCH, FOWNER, SETUID, SETGID]
```

然后：

```bash
docker compose up -d
```

浏览器打开 `http://<地址>:8080`，按向导设置管理员密码和节点名。之后在“连接与密钥”里生成密钥、添加连接，在“任务”里新建备份或同步任务。全程不需要进容器敲命令。

向导会检查 `/data` 和 `/secrets` 是不是真的挂载到了宿主机，任何一个没挂载都不让继续：否则重建容器时，设置会丢（`/data`），或者保存的凭据再也解不开（`/secrets`）。

要固定在某个版本，在 `compose.yaml` 旁边建一个 `.env` 文件，写一行 `TERN_VERSION=v0.1.0`（docker compose 会自动读它）。升级时改这一行（用 `latest` 就不用改），然后：

```bash
docker compose pull
```

```bash
docker compose up -d
```

想自己构建镜像：`make image VERSION=latest` 在本机构建两种架构的 `tern:latest`（不推送到任何仓库），再把 `image` 那一行改成 `tern:latest`。

## 3. QNAP 版本

QNAP 上也可以不用 Docker，直接装 tern 的 QPKG。装好后它和 QNAP 自带的应用一样：主菜单里有图标，点开是 QTS 桌面里的一个窗口，不另开网页；已经用管理员账号登录了 QTS，就不用再登录 tern。

- 要求 QTS 5.0 或更新，x86_64 机型用 `tern_<版本>_x86_64.qpkg`，ARM 64 位机型用 `tern_<版本>_arm_64.qpkg`，从 [GitHub Releases](https://github.com/FlintyLemming/tern/releases) 下载（附 `SHA256SUMS`）。不需要 Container Station。
- 安装：这个包没有 QNAP 的数字签名，先在 App Center 的设置里打开“允许安装没有有效数字签名的应用程序”，再点右上角的“手动安装”选 .qpkg 文件。
- 自己构建：`make qpkg VERSION=v0.2.0`，两个包在 `build/qnap/` 里；只要一种架构就加 `QPKG_PLATFORMS=linux/amd64`。
- 窗口走 QTS 自己的端口和证书：QTS 把 `/tern/` 转给 tern，tern 只监听本机回环地址（默认端口 17180，在 `tern.conf` 里改）。所以 `http(s)://<NAS 地址>:<QTS 端口>/tern/` 在浏览器里也能直接打开。
- 登录：登录着 QTS 的管理员直接进入 tern，不是管理员的 QTS 用户进不去；初始化也要先以管理员身份登录 QTS。初始化时仍要设一个 tern 管理员密码：查看恢复信息、修改密码时要输入它；检查 QTS 登录出了问题时，登录页也能用它登录。
- 和 Docker 版不同的地方：
  - 路径直接写 NAS 上的路径，比如 `/share/Photos`，没有挂载这回事。
  - tern 以 QTS 的 admin 身份运行，拉取同步落地的文件属主是 admin；没有 `PUID`、`PGID`。任务前后置命令也在 NAS 上以 admin 身份执行。
  - 时区跟着 QTS 的设置走，改了时区要在 App Center 里停用再启用 tern。
  - BBR 要用 SSH 登录 NAS 执行 `sysctl -w net.ipv4.tcp_congestion_control=bbr`，它对整台 NAS 生效，NAS 重启后要重新执行。
  - 自带 restic、rclone，以及静态编译的 rsync 和 OpenSSH 客户端，版本和镜像里的一样，不用 QTS 自带的（QTS 的 rsync 太旧）。
- 文件都在安装目录（`/share/<数据卷>/.qpkg/tern`）里：`data`（数据库、日志 `tern.log`、restic 缓存）、`secrets`（tern 自动生成的密钥文件 `master.key`）、`tern.conf`（端口和额外的环境变量）。升级不会动它们，**卸载会把它们一起删掉**。每个备份任务的恢复信息要另外保存到这台 NAS 以外（第 4 节）。

## 4. 密码、凭据和你要自己保管的东西

tern 只有一个密码：初始化时设的管理员密码。数据库里只存它的 Argon2id 哈希。

tern 保存的凭据（SSH 私钥和私钥口令、WebDAV 密码、restic 仓库密码）在数据库里全是密文（XChaCha20-Poly1305，每条密文绑定它所属的记录）。加密用的密钥是初始化时自动生成的 `secrets/master.key`，不用记，也没有导出这回事。所以：

- 只拿到 `data` 目录（或它的备份、拷贝）的人，看不到任何一条凭据。
- `secrets` 和 `data` 一起落到别人手里（包括能以 root 登录这台机器的人），凭据就等于明文：tern 要在没人值守时自己跑计划任务，就必须能自己解开它们。所以 `secrets` 不要和 `data` 放在同一份备份里。
- `secrets` 丢了，`data` 里的凭据就解不开：连接要重新建、私钥要重新导入、备份任务要用恢复信息重新接入仓库。
- 用旧版本以“口令”解锁方式初始化的节点，升级后输入一次当时的口令，之后就改用密钥文件自动解锁，不再需要那个口令。

你必须自己保管的只有一样：**每个备份任务的恢复信息。** 新建备份任务时 tern 会显示仓库地址和仓库密码，并且要你确认已经保存到这台机器以外的地方才让创建。节点坏了，仓库密码就只在你保存的那份恢复信息里；没有它，远端的备份就是一堆打不开的加密数据。以后也可以在“快照与恢复”里输入管理员密码再看一次。

## 5. 对端要求

- restic 备份和 SFTP 同步：对端只要开着 SSH，并且带 sftp 子系统（绝大多数 NAS 和 Linux 默认就有）。
- rsync 同步：对端还要装有 rsync（绝大多数 NAS 和 Linux 自带）。
- WebDAV 同步：对端要有 WebDAV 服务。

## 6. 三种同步协议怎么选

| | SFTP | rsync over SSH | WebDAV |
|---|---|---|---|
| 引擎 | rclone | rsync + OpenSSH | rclone |
| 限速 | 支持 | 支持 | 支持 |
| 并发文件数 | 支持 | **不支持** | 支持 |
| 单个大文件断点续传 | 不支持，整个文件重传 | **支持** | 不支持，整个文件重传 |
| 保留权限、属主、硬链接 | 只保留修改时间 | 支持 | 看服务器 |
| 对端要求 | SSH | SSH + rsync | WebDAV 服务 |

**大文件多、网络差，优先选 rsync。**

## 7. BBR

- BBR 是**节点级**开关，不是任务级。它是内核的 TCP 拥塞算法，在容器里只能对整个容器生效，靠 compose 文件里的 `sysctls` 那一行在部署时打开。
- BBR 只对**本机发出**的数据起作用：推送和备份受益；拉取任务取决于远端机器自己的内核设置。
- 要求宿主机内核已经加载 `tcp_bbr` 模块，容器自己加载不了。可以在宿主机上看 `cat /proc/sys/net/ipv4/tcp_available_congestion_control` 里有没有 `bbr`。
- **宿主机不支持 BBR 时，带着 `sysctls` 那一行容器会启动失败**（报错里有 `failed to write sysctl net.ipv4.tcp_congestion_control`）。删掉那一行即可，其余功能不受影响。
- 用宿主机网络模式（`network_mode: host`）时不能写 `sysctls`，此时沿用宿主机自己的设置。
- 概览页的 BBR 卡片会显示内核是否支持、当前生效的算法，以及没生效时怎么开。
- 如果组网工具自己用 TCP 或 KCP 做隧道，外层会把丢包藏起来，里面这层 BBR 的效果会打折扣。

## 8. 文件属主

默认 tern 以 root 运行（只保留 6 项内核能力），拉取同步落地的文件属主是 root。设了 `PUID`、`PGID`（compose 里注释掉的那两行）后，整个进程降权到这个用户：落地文件属主就是它，`data`、`secrets` 目录也会改成它的。

- 设了之后，**源目录必须对这个用户可读**，否则备份和推送会读不到文件。
- 这个用户写不了 `data` 或 `secrets`（NAS 上常见的 ACL 问题）时，容器启动即退出，日志里是 `tern: uid <PUID> cannot write <目录>`。

## 9. 安全须知

- **能登录界面的人，就能通过任务前后置命令在容器里执行任意命令，并读到所有挂载进容器的数据。** QNAP 版里是在 NAS 上以 admin 身份执行、读到整台 NAS 的数据，所以只有 QTS 管理员能进。 管理员密码要够强；端口建议只暴露在组网地址上，不要直接放到公网。
- SSH 只支持密钥认证。tern 生成或导入的私钥加密保存，任何界面和接口都不会再显示。
- 第一次连接时要人工核对对端的主机指纹。指纹之后变了，tern 拒绝连接，要在界面里重新获取并确认——如果你没重装过对端，这可能是有人在中间冒充。
- 想要 HTTPS：可以用内置 TLS（设 `TERN_TLS_CERT` 和 `TERN_TLS_KEY`，指向挂载进容器的证书和私钥），也可以放在反向代理后面。

## 10. 脱离本工具恢复备份

备份是标准的 restic 仓库。在任何装了 restic 的机器上，用恢复信息里的四行命令：

```bash
export RESTIC_REPOSITORY='sftp:用户@主机:/仓库路径'
export RESTIC_PASSWORD='仓库密码'
restic snapshots
restic restore latest --target /path/to/restore
```

这台机器要能用 SSH 登录对端（把对应的私钥配进 `~/.ssh/config`，或者给 restic 加 `-o sftp.args='-i <私钥文件>'`）。tern 的快照都带着 `tern-<任务ID>` 标签，`restic snapshots --tag tern-<任务ID>` 只列一个任务的快照。

## 11. 通知

第一版没有内置通知。可以在任务后命令里按 `$TERN_RESULT` 用镜像自带的 `curl`（QNAP 版用 QTS 自带的）自己发：

```sh
case "$TERN_RESULT" in
  success|warning) ;;
  *) msg=$(printf 'tern: %s %s: %s' "$TERN_TASK_NAME" "$TERN_RESULT" "$TERN_ERROR" | tr -d '"\\' | tr '\n' ' ')
     curl -fsS -m 30 -H 'Content-Type: application/json' \
       -d "{\"text\":\"$msg\"}" \
       https://example.com/your-webhook ;;
esac
```

- `tr` 去掉引号、反斜杠和换行，免得错误摘要把 JSON 弄坏。
- `-m 30` 让卡住的通知地址最多拖 30 秒，而不是拖到任务后命令的超时（默认 10 分钟）。
- `-f` 让对方返回 HTTP 错误时 curl 也以非 0 退出，运行日志里会多一条 `post-task command failed`，看得出通知没发出去。
- 容器上设的 `HTTPS_PROXY` 等变量不会传给任务后命令。通知地址要走代理时，在命令里写 `curl -x http://代理地址:端口 ...`。

任务后命令能用的变量：`TERN_TASK_ID`、`TERN_TASK_NAME`、`TERN_TASK_TYPE`、`TERN_TRIGGER`、`TERN_RESULT`（`success`、`warning`、`partial`、`failed`、`canceled`、`skipped`、`interrupted` 之一）、`TERN_DURATION_SECONDS`、`TERN_BYTES`、`TERN_FILES`、`TERN_ERROR`。执行位置选“某个 SSH 连接的远端”时，命令在对端执行，用的是对端的 curl。

## 12. 环境变量一览

| 变量 | 默认值 | 作用 |
|---|---|---|
| `TERN_LISTEN` | `:8080` | 监听地址 |
| `TERN_DATA_DIR` | `/data` | 数据库、日志、restic 缓存 |
| `TERN_SECRETS_DIR` | `/secrets` | tern 自动生成的密钥文件 |
| `TERN_RUNTIME_DIR` | `/dev/shm/tern` | 运行时临时目录，必须是 tmpfs，否则拒绝启动 |
| `TERN_TLS_CERT` | 空 | 内置 TLS 的证书文件，和 `TERN_TLS_KEY` 要么都设、要么都不设 |
| `TERN_TLS_KEY` | 空 | 内置 TLS 的私钥文件 |
| `TERN_BASE_PATH` | 空 | 在这个路径前缀下提供服务，比如 `/tern`，给保留前缀转发的反向代理用。QNAP 版固定是 `/tern` |
| `TERN_PLATFORM` | `docker` | 设为 `qts` 时信任 QTS 管理员登录，并允许 QTS 把界面放进窗口。只给 QNAP 版用 |
| `TERN_QTS_AUTH_URL` | `http://127.0.0.1:58080/cgi-bin/authLogin.cgi` | QNAP 版检查 QTS 登录的地址 |
| `TERN_ALLOW_UNMOUNTED` | 空 | 设为 `1` 时跳过 `/data`、`/secrets` 的挂载检查。只给开发和测试用 |
| `PUID` | `0` | 运行 tern 的用户 ID，0 为 root |
| `PGID` | `0` | 运行 tern 的组 ID |
| `TZ` | 空 | 节点时区，计划任务按它解释，界面按它显示 |

## 13. 第一版不做什么

- 双向同步、远端到远端同步。
- 自带组网或 VPN。tern 只认 IP 或主机名。
- 多用户和权限划分。只有一个管理员。
- 实时同步（文件一变就触发）。只按计划或手动触发。
- S3 等其他存储后端。
- 备份远端机器上的数据。只备份挂载进容器的本机目录。
- 集中管理控制台。每个节点独立工作，以后再做。
