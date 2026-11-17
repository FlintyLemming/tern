# 验收对照

规格（`2026-11-10-nas-backup-sync-design.md`）§16 的九条，每条由哪个自动化测试证明、怎么跑；以及 §15 留给实现者实测的几项，实测看到了什么。

运行前提：

- `make itest`、`make acceptance` 在 `test/integration/compose.yaml` 的环境里跑（真实的 sshd、WebDAV、restic、rclone、rsync）。拉不到 Docker Hub 时加 `TERN_BASE_REGISTRY=<官方镜像的镜像站>`，例如 `public.ecr.aws/docker/library`。
- `make acceptance-host`、`make e2e` 在宿主机上驱动真实镜像，先 `make image VERSION=acceptance`（或设 `TERN_VERSION` 指向别的标签）。下载不了 Playwright 自带的 Chromium 时，`TERN_E2E_CHANNEL=chrome make e2e` 改用本机装的 Chrome。
- `make acceptance` 需要 runner 容器有 `NET_ADMIN`（compose 文件里已加），宿主机内核要有 `sch_netem`。

## 规格 §16 逐条对照

| # | 验收标准 | 证明它的测试 | 怎么跑 |
|---|---|---|---|
| 1 | 从向导到跑通不进容器 | `test/e2e/first-run.spec.ts` | `make e2e` |
| 2 | 原生 restic 能列出并恢复 | `TestBackupThenNativeRestore` | `make itest` |
| 3 | 三协议、推拉、多组文件夹对 | `TestSyncMatrix` | `make itest` |
| 4 | 5% 丢包加断网后最终成功、不重传、rsync 续传 | `TestWeakNetRsync`、`TestWeakNetSFTP`、`TestWeakNetRestic`、`TestRsyncResumesBigFile` | `make acceptance`、`make itest` |
| 5 | 镜像模式源为空或没挂上时目的地完好 | `TestMirrorEmptySourceKeepsDestination` | `make itest` |
| 6 | 数据库、日志、接口、`ps` 里搜不到明文凭据 | `TestNoSecretLeak` | `make acceptance-host` |
| 7 | 重启后计划任务自动恢复 | `TestRestartResumesSchedule` | `make acceptance-host` |
| 8 | 限速和并发文件数实测生效 | `TestLimitUpload`、`TestBwlimit`、`TestTransfersConcurrency` | `make itest` |
| 9 | 前后置命令的行为符合 §10 | `TestHooksEndToEnd` 及计划 3 任务 3、4 的单元测试 | `make acceptance-host`、`make test` |

第 4 条的断网时长：规格写的是 1 分钟，测试用 2 分钟。实测（2026-11-11）1 分钟的断网会被直接扛过去：rsync 的 SSH 连接没断（保活 15 秒 × 4 要约 75 秒才判定断线，rsync 的 I/O 超时是 60 秒），传输暂停后原地继续，一次尝试成功，一个字节也没多传；rclone 靠自己的整轮重跑（当时是 `--retries 3`）在同一次尝试里恢复，只多传了断网时在传的那个文件；restic 在约 75 秒时被 SSH 保活断开，重试后成功。结果对用户是好的，但重试和续传的路径没被走到，计划里“至少两次尝试”的断言过不了。

按 2026-11-12 的决定：

- 测试把断网拉长到 2 分钟（超过断线判定时间），断言一条不改。
- rclone 不再自己整轮重跑（`--retries 1`），操作级重试减到 `--low-level-retries 3`，整轮重试交给 tern 的任务级重试（有间隔、界面显示“等待重试”、只重跑失败的文件夹对）。rclone 约 1 分 40 秒报出 `connection lost`、在传的文件失败，但为了规格 §8.3 保留的操作级重试，它还会逐个去试剩下的文件，每次新开的 ssh 都要等 15 秒连接超时，进程要到约 3 分钟才退出。所以 `TestDisconnectDetectedInTime` 对 rsync 和 restic 仍要求进程 2 分钟内退出，对 rclone 改为要求 2 分钟内在运行日志里报出断线。
- rsync 断线后，对端的 `rsync --server` 还会活好几分钟：它自己发的保活让它的 60 秒超时永远不触发，要等 TCP 最终重置才退出，退出时才把收了一半的文件存进 `.tern-partial`。2 分钟断网的第一次实测里，重试在链路恢复 7 秒后就开始了，没找到半截文件，200 MiB 的大文件从头重传，旧进程随后又把半截文件留在了目的地的 `.tern-partial/` 里成了垃圾。现在 tern 启动对端 rsync 时让它先报告自己的 PID（`--rsync-path`），重试一组文件夹对之前，先用同一份 SSH 配置确认那个进程还是 `rsync --server`，再给它和它的子进程发 SIGTERM（它会存好半截文件）并等它退出，然后才重跑 rsync。

## §15 待确认项的结论

| 待确认 | 结论 | 来源 |
|---|---|---|
| rclone 对 SFTP、WebDAV 的单文件续传 | 不续传，整个文件重传。2026-11-12：SFTP 上传 64 MiB 的文件到一半时取消，再跑一次时 rclone 又传了完整的 67108864 字节；已经传完的文件不再传（100 个 1 MiB 文件传到 60 MiB 时取消，第二次传的不超过 45 个）。WebDAV：2026-11-11 断网 1 分钟的那次，300 个 1 MiB 文件一共传了 316788736 字节，比总量多 2215936 字节，约两个文件的大小。 | `TestRcloneSkipsCompleted` 的日志、`TestWeakNetWebDAV` |
| 目标 NAS 内核是否带 `tcp_bbr`、容器内 sysctl 能否生效 | **未验证：需要在目标 NAS 上确认。** 部署后看概览页的 BBR 卡片。开发机（OrbStack 虚拟机内核，2026-11-11）只有 `reno cubic`，带 `sysctls` 那一行时容器起不来（`failed to write sysctl net.ipv4.tcp_congestion_control`），去掉后正常运行，`/api/v1/system` 报告当前算法 cubic、BBR 未生效（`TestBBRReported`）。 | 在目标 NAS 上部署后看概览页的 BBR 卡片 |
| restic 的 SFTP 后端断线后的表现 | 断网后 restic 先对写失败的数据包按秒级间隔重试（`Save(<data/…>) returned error, retrying after 1.5s: … connection lost`），约 75 秒时 SSH 保活让 ssh 以 255 退出，restic 随即退出码 1（`Fatal: unable to save snapshot: ssh command exited: exit status 255`），进程在断网开始后 75～76 秒消失（2026-11-11、2026-11-12 各一次）。它的锁留在仓库里（退出前的解锁也连不上）。tern 重试时先 `restic unlock` 清掉这把失效锁，再 `restic repair index` 把断网前已传完的数据包编进索引（2026-11-12 那次是 4 个），之后的备份只传剩下的：最后一次尝试新增 140720007 / 209715200 字节（2026-11-12，断网 2 分钟），另一次是 118822610 / 209715200（2026-11-11，断网 1 分钟）。 | `TestWeakNetRestic` 的日志 |
| WebDAV 服务器对修改时间的支持 | 对测试用的服务器（`rclone serve webdav`）保留得住：2026-11-12 同一任务不改源再跑一次，传输 0 个文件。其他 WebDAV 服务器（NAS 自带的、nginx、Apache）未测。 | `TestSecondRunTransfersNothing` 的日志 |
| rclone 走外部 ssh 时的并发 | 正常。2026-11-12：`transfers` 设 4，对端同时出现的 `*.partial` 文件最多正好 4 个。 | `TestTransfersConcurrency` |
| restic 退出码 | 与计划 4 的表一致（restic 0.19.1，2026-11-12）：仓库密码对 → 0；密码错 → 12；路径下没有仓库 → 10；有备份在跑时 `prune` 拿不到锁 → 11；有一个源读不了 → 3。 | `TestExitCodesAsDocumented` |

## QNAP 版（2026-11-17，hf-qts：TS-X53B，x86_64，QTS 5.2.11）

没有自动化的端到端测试：QTS 的登录、桌面窗口和 App Center 只在真机上有。服务端和界面里与 QTS 有关的行为由单元测试覆盖（`internal/qts`、`internal/api/qts_test.go`、`web/src/lib/base.test.ts`、`web/src/shell/AppShell.test.tsx` 等），下面是真机上手工验证过的。重做时：`make qpkg QPKG_PLATFORMS=linux/amd64`，`deploy/qnap/install.sh <NAS>`。

| 项目 | 结果 |
|---|---|
| 安装、升级 | 直接运行 .qpkg（`install.sh` 的做法）可以装和升级，升级保留 `data`、`secrets`、`tern.conf`，升级后自动解锁。`qpkg_cli -m … -A 1` 在签名检查处停下：App Center 里没打开“允许安装没有有效数字签名的应用程序”时，不签名的包只能这样装。 |
| 窗口 | 主菜单里有 tern 图标，点开是 QTS 桌面窗口（标题栏、最小化、最大化、关闭、任务栏标签），不开新网页。QTS 给窗口加载的是 `/tern?v=<版本>&…`，即 `Proxy_Path`，不是 `WebUI`。 |
| QTS 转发 | QTS 只在 App Center 启用应用时往 `/etc/app_proxy.conf` 写 `ProxyPass "/tern" "http://127.0.0.1:17180/tern/" retry=0`（端口取 `Web_Port`），停用时换成指向“应用已停用”页面的 `RewriteRule`，改 `Proxy_Path` 后旧行不删。所以 `tern.sh start` 自己检查并修正这一行，再平滑重载 QTS 的 Apache。清空这个文件（重启后就是这样）后访问 `/tern` 是 404，`tern.sh start` 后这一行补回来。目标末尾的 `/` 让 tern 收到 `/tern//api/…`，tern 就地按规范路径处理，不重定向。HTTP 5000 和 HTTPS 5001 都通。 |
| 登录 | 登录着 QTS 的管理员打开窗口直接进入，状态里 `signed_in_via: qts`，侧栏显示“已通过 QTS 登录”而没有退出按钮；初始化只接受 QTS 管理员。退出 tern 自己的密码会话后留在应用里，回到 QTS 登录。写操作带 CSRF 令牌（由 QTS 会话号派生）。 |
| 事件流 | 经 QTS 的 Apache 不缓冲、不压缩：`: ok` 约 1 秒内到达，运行记录在窗口里实时更新。 |
| 自带工具 | restic 0.19.1、rclone 1.76.0、静态链接的 rsync 3.5.2 和 OpenSSH 10.3p1 都在 QTS 上直接运行。通过一个临时 sshd 容器（只监听 NAS 的回环地址，测完删掉）：备份 50 MiB 成功，随后的自动清理和校验成功，恢复出的文件与源逐字节相同（含中文文件名和硬链接）；rsync 推送成功，对端保留硬链接；SFTP 拉取成功，与源相同。 |
| 工作目录 | 第一次跑 rsync 失败：`rsync: [Receiver] getcwd(): No such file or directory`。tern 继承了安装程序的临时目录（`.qpkg/.tmp-tern`），装完被删了。`tern.sh` 现在先 `cd /`；由安装程序启动后，tern 和守护进程的工作目录都是 `/`。 |
| 时区、BBR | 时区取 QTS 的设置（Asia/Hong_Kong），界面按它显示。内核有 `reno bbr cubic`，当前是 cubic，概览页的 BBR 卡片给出 `sysctl -w` 的命令。 |
| 未验证 | arm_64 包只构建过，没在 ARM 机型上装过；没有真的重启 NAS；App Center 正常途径的安装（打开不签名开关后手动安装）。 |
