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
| 7 | 密钥文件模式下重启后计划任务自动恢复 | `TestRestartResumesSchedule` | `make acceptance-host` |
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
