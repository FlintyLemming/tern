# tern

## 1. 这是什么

tern 是跑在 Docker 里的备份与同步节点：用 restic 把本机目录备份到远端，用 SFTP、rsync over SSH 或 WebDAV 单向同步文件夹，全部操作都在自带的网页界面里完成。它是 QNAP HBS3 的替代品，针对的是两个痛点：

1. 备份用厂商私有格式（qdff），数据被锁在 QNAP 生态里。tern 的备份是标准的 restic 仓库，离开 tern 也能用原生 restic 恢复。
2. 同步不支持 SFTP 和 rsync over SSH，对端必须是厂商认识的服务。tern 只要求对端有 SSH 或 WebDAV。

## 2. 快速开始

先构建镜像（多架构，amd64 和 arm64；不会推送到任何仓库）：

```bash
make image VERSION=latest
```

把下面的内容存成 `compose.yaml`（仓库里的 `deploy/compose.yaml` 就是它），按自己的目录改好 `volumes` 里的后两行：

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

然后：

```bash
docker compose up -d
```

浏览器打开 `http://<地址>:8080`，按向导设置管理员密码、节点名和解锁方式。之后在“连接与密钥”里生成密钥、添加连接，在“任务”里新建备份或同步任务。全程不需要进容器敲命令。

向导会检查 `/data` 和 `/secrets` 是不是真的挂载到了宿主机：`/data` 没挂载时向导不让继续（否则重建容器所有设置都会丢）；`/secrets` 没挂载时只能选口令解锁。

## 3. 你必须自己保管的两样东西

- **`secrets` 目录。** 密钥文件解锁模式下，里面的 `master.key` 加密着 tern 保存的所有凭据（SSH 私钥、WebDAV 密码、restic 仓库密码）。丢了它，`data` 目录里的凭据就再也解不开：连接要重新建、私钥要重新导入、备份任务要用恢复信息重新接入仓库。反过来，`secrets` 和 `data` 一起落到别人手里，凭据就等于明文。所以两者分开保管，`secrets` 不要和 `data` 放在同一份备份里。口令解锁模式下不用这个文件，但口令要自己记住，忘了后果相同。
- **每个备份任务的恢复信息。** 新建备份任务时 tern 会显示仓库地址和仓库密码，并且要你确认已经保存到这台机器以外的地方才让创建。节点坏了，仓库密码就只在你保存的那份恢复信息里；没有它，远端的备份就是一堆打不开的加密数据。以后也可以在“快照与恢复”里输入管理员密码再看一次。

## 4. 对端要求

- restic 备份和 SFTP 同步：对端只要开着 SSH，并且带 sftp 子系统（绝大多数 NAS 和 Linux 默认就有）。
- rsync 同步：对端还要装有 rsync（绝大多数 NAS 和 Linux 自带）。
- WebDAV 同步：对端要有 WebDAV 服务。

## 5. 三种同步协议怎么选

| | SFTP | rsync over SSH | WebDAV |
|---|---|---|---|
| 引擎 | rclone | rsync + OpenSSH | rclone |
| 限速 | 支持 | 支持 | 支持 |
| 并发文件数 | 支持 | **不支持** | 支持 |
| 单个大文件断点续传 | 不支持，整个文件重传 | **支持** | 不支持，整个文件重传 |
| 保留权限、属主、硬链接 | 只保留修改时间 | 支持 | 看服务器 |
| 对端要求 | SSH | SSH + rsync | WebDAV 服务 |

**大文件多、网络差，优先选 rsync。**

## 6. BBR

- BBR 是**节点级**开关，不是任务级。它是内核的 TCP 拥塞算法，在容器里只能对整个容器生效，靠 compose 文件里的 `sysctls` 那一行在部署时打开。
- BBR 只对**本机发出**的数据起作用：推送和备份受益；拉取任务取决于远端机器自己的内核设置。
- 要求宿主机内核已经加载 `tcp_bbr` 模块，容器自己加载不了。可以在宿主机上看 `cat /proc/sys/net/ipv4/tcp_available_congestion_control` 里有没有 `bbr`。
- **宿主机不支持 BBR 时，带着 `sysctls` 那一行容器会启动失败**（报错里有 `failed to write sysctl net.ipv4.tcp_congestion_control`）。删掉那一行即可，其余功能不受影响。
- 用宿主机网络模式（`network_mode: host`）时不能写 `sysctls`，此时沿用宿主机自己的设置。
- 概览页的 BBR 卡片会显示内核是否支持、当前生效的算法，以及没生效时怎么开。
- 如果组网工具自己用 TCP 或 KCP 做隧道，外层会把丢包藏起来，里面这层 BBR 的效果会打折扣。

## 7. 文件属主

默认 tern 以 root 运行（只保留 6 项内核能力），拉取同步落地的文件属主是 root。设了 `PUID`、`PGID`（compose 里注释掉的那两行）后，整个进程降权到这个用户：落地文件属主就是它，`data`、`secrets` 目录也会改成它的。

- 设了之后，**源目录必须对这个用户可读**，否则备份和推送会读不到文件。
- 这个用户写不了 `data` 或 `secrets`（NAS 上常见的 ACL 问题）时，容器启动即退出，日志里是 `tern: uid <PUID> cannot write <目录>`。

## 8. 安全须知

- **能登录界面的人，就能通过任务前后置命令在容器里执行任意命令，并读到所有挂载进容器的数据。** 管理员密码要够强；端口建议只暴露在组网地址上，不要直接放到公网。
- SSH 只支持密钥认证。tern 生成或导入的私钥加密保存，任何界面和接口都不会再显示。
- 第一次连接时要人工核对对端的主机指纹。指纹之后变了，tern 拒绝连接，要在界面里重新获取并确认——如果你没重装过对端，这可能是有人在中间冒充。
- 想要 HTTPS：可以用内置 TLS（设 `TERN_TLS_CERT` 和 `TERN_TLS_KEY`，指向挂载进容器的证书和私钥），也可以放在反向代理后面。

## 9. 脱离本工具恢复备份

备份是标准的 restic 仓库。在任何装了 restic 的机器上，用恢复信息里的四行命令：

```bash
export RESTIC_REPOSITORY='sftp:用户@主机:/仓库路径'
export RESTIC_PASSWORD='仓库密码'
restic snapshots
restic restore latest --target /path/to/restore
```

这台机器要能用 SSH 登录对端（把对应的私钥配进 `~/.ssh/config`，或者给 restic 加 `-o sftp.args='-i <私钥文件>'`）。tern 的快照都带着 `tern-<任务ID>` 标签，`restic snapshots --tag tern-<任务ID>` 只列一个任务的快照。

## 10. 通知

第一版没有内置通知。可以在任务后命令里按 `$TERN_RESULT` 用镜像自带的 `curl` 自己发：

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

## 11. 环境变量一览

| 变量 | 默认值 | 作用 |
|---|---|---|
| `TERN_LISTEN` | `:8080` | 监听地址 |
| `TERN_DATA_DIR` | `/data` | 数据库、日志、restic 缓存 |
| `TERN_SECRETS_DIR` | `/secrets` | 主密钥文件 |
| `TERN_RUNTIME_DIR` | `/dev/shm/tern` | 运行时临时目录，必须是 tmpfs，否则拒绝启动 |
| `TERN_TLS_CERT` | 空 | 内置 TLS 的证书文件，和 `TERN_TLS_KEY` 要么都设、要么都不设 |
| `TERN_TLS_KEY` | 空 | 内置 TLS 的私钥文件 |
| `TERN_ALLOW_UNMOUNTED` | 空 | 设为 `1` 时跳过 `/data`、`/secrets` 的挂载检查。只给开发和测试用 |
| `PUID` | `0` | 运行 tern 的用户 ID，0 为 root |
| `PGID` | `0` | 运行 tern 的组 ID |
| `TZ` | 空 | 节点时区，计划任务按它解释，界面按它显示 |

## 12. 第一版不做什么

- 双向同步、远端到远端同步。
- 自带组网或 VPN。tern 只认 IP 或主机名。
- 多用户和权限划分。只有一个管理员。
- 实时同步（文件一变就触发）。只按计划或手动触发。
- S3 等其他存储后端。
- 备份远端机器上的数据。只备份挂载进容器的本机目录。
- 集中管理控制台。每个节点独立工作，以后再做。
