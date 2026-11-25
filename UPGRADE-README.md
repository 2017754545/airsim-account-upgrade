# AirSIM 账号层改造包

本仓库是 AirSIM「账号层改造」的完整落地：Relay 服务端、iOS / Android 客户端、
MU300 守护进程与内置 Agent 四端同步改造，并按需求**去除了全部组网（Tailscale/overlay）功能**。

## 目录结构

| 目录 | 内容 | 来源基线 |
|---|---|---|
| `relay/` | 自建 workerd Relay（账号层改造版） | Milk-lpy/airsim-relay（2a22320） |
| `clients/` | iOS + Android 客户端（v0.9.8.27/28 现行基线） | 内网 airsim-android-relay-20261125/source |
| `mu300/daemon/` | MU300 OpenWrt 守护进程（无需改动，随包发布） | 内网 AirSIM-Tailscale-Handoff F50 分支 |
| `mu300/agent/` | MU300 内置 Go Agent（battery-agent 改造版） | 内网 airsim-7.3-continuation |

## Relay 服务端改动（relay/）

新增模块 `push-relay-worker/src/accounts.mjs`（邀请码语义参考 OpenHanako
alpha-gate-worker，Apache-2.0，已在 NOTICE 标注）：

1. **账号与邀请**：账号记录、一次性邀请码（无偏生成、哈希存储、TTL、同设备幂等核销、
   固定窗口限流、统一拒绝话术防猜码）。`registerDevice` 新设备必须持有效邀请码；
   注册响应新增 `approval_status`（approved/pending/revoked）与 `account_bound`。
2. **审批门禁**：pending/revoked 设备除完成注册与状态查询外，所有控制类路由一律 403。
3. **媒体短期票据**：`POST /v1/calls/:uuid/media-ticket`（设备密钥鉴权）签发约 10 分钟
   票据；挂断/撤销即失效。迁移期 `MEDIA_TICKET_REQUIRED=0` 时旧 `call_secret` 仍可用。
4. **feedback 鉴权**：`/v1/feedback` 需要 `x-airsim-device-id` + 设备密钥头，附大小限制
   与按设备/IP 限流。
5. **Dashboard 账号 API**：`/dashboard/api/accounts`、`/invitations`、`/devices`（DASHBOARD_TOKEN 保护）：
   建账号、发/撤邀请码、批准/撤销设备、审计事件列表。
6. **注册限流**：按 IP 固定窗口限流，防邀请码暴力枚举。

配置新增（`self-hosted/config.example.json` + configure.mjs 校验）：
- `ACCOUNT_MODE`：`grace`（默认，旧设备继续可用）/ `strict`（无账号绑定一律拒绝）
- `MEDIA_TICKET_REQUIRED`：`0`（迁移期）/ `1`（强制票据）
- `INVITATION_TTL_HOURS`（默认 72）、`REGISTER_RATE_LIMIT`（默认 30/小时/IP）

测试：`test/accounts.test.mjs` 7 项全过（注册幂等、重复邀请拒绝、pending 门禁、
feedback 鉴权、票据签发/过期/重放、撤销联动、限流）。原有套件 13/15，2 项失败为
Windows 环境限制（symlink/workerd 二进制），需在 Linux 部署机复跑确认。

## iOS 客户端改动（clients/iOS/）

- `VoIPPushController.swift`：注册载荷新增 `invitation_code`；解析响应 `approval_status`
  并在 approved 后立即清除本地邀请码；来电/拨出推送优先使用 `media_token`（回退 call_secret）。
- `MediaTicketClient.swift`（新增）：媒体票据换取客户端；本地键位 `AccountKeys`。
- `IPhoneCloudCallSession.swift`：媒体重连前先换新票据，失败沿用旧 URL（迁移期兼容）。
- `AirSIMSetupView.swift`：设置页新增「邀请码输入 + 重新注册」入口与审批状态提示。
- 组网剥离：`NetworkRoutePreferences` / `VoWLANController` 文案去除 Tailscale 语义
  （Android 端已不再通告 VPN 网关，相关路径自然失效）。

**云编译**：仓库根 `.github/workflows/ios-unsigned-build.yml` 手动触发，产
`AirSIM-unsigned.ipa`（未签名，个人证书侧载）。

## Android 客户端改动（clients/android/phone-control-app/）

- `RelayRegistrationPayload.java`：FIELDS 新增 `invitation_code`。
- `StandaloneAgentStore.java`：邀请码存取（注册 JSON 内，消费后清除）、审批状态独立存储、
  pushStatus 暴露 `approval_status`。
- `StandaloneAgentService.java`：注册响应解析审批状态，approved 后立即清邀请码。
- `StandaloneCloudMediaManager.java`：来电/拨出优先 `media_token`；重连前换新票据
  （失败沿用旧凭证）。
- 组网剥离：`VoWLANNetworkPolicy` 不再识别/放行 VPN overlay 接口（仅局域网互通）；
  `VoWLANGatewayService` 移除 loopback/overlay 备用网关通告；`PairingServer` /
  `MainActivity` 移除 Tailscale 文案。

## MU300 改动（mu300/）

- `agent/`（battery-agent）：
  - `push.go`：注册载荷新增 `invitation_code`；响应解析 `approval_status`（空 body 兼容）；
    approved 后从配置清除邀请码并原子落盘；pushStatus 暴露审批状态。
  - `cloud_command.go` / `cloud_call.go`：命令通话优先 `media_token`；每次（重）连前向
    Relay 换取短期票据（`registrationForMediaTicket` + `refreshCloudMediaCredential`，
    任何失败回退旧凭证）。
- `daemon/`（airsim-mu300d）：无需改动——注册代理是整包透传，`invitation_code` 自动
  流向 Agent；密钥文件权限已是 0600/0700。
- 组网：mu300 代码库本身无 Tailscale 引用。

验证（内网编译机，Go 1.27.4）：`go build ./...`、`go vet ./...` 通过；
`go test ./...` 全部通过（含原有测试）。

## 部署步骤（Relay，按交接文档顺序）

1. **KV 备份**（改造前必做）：在部署机补一个定时任务，例如
   `0 2 * * * rsync -a /var/lib/airsim-relay-huawei/ /var/backups/airsim-kv/$(date +\\%F)/`
   （或对 KV 目录打 tar 快照），保留 30 天。
2. 上传 `relay/` 到部署机新 release 目录（如 `/opt/airsim-relay-huawei/releases/<新日期>`），
   在 staging 配置上先加 `ACCOUNT_MODE=grace`、`MEDIA_TICKET_REQUIRED=0`。
3. systemd 重启服务，只读冒烟：`/healthz`、一次真实设备注册（带邀请码）、
   一次 feedback 调用（带设备头）。
4. **发邀请码**：Dashboard `POST /dashboard/api/accounts` + `POST /dashboard/api/invitations`，
   把邀请码填进 iOS 设置页 / Android（重新注册）/ MU300（配对载荷）完成首绑。
5. 真机验收：通话建立、挂断后票据失效、撤销设备后 403、旧客户端（无票据）在 grace 模式下
   仍可通话。
6. 观察 24-48h 后切 `MEDIA_TICKET_REQUIRED=1`；确认无旧客户端依赖后再考虑 `ACCOUNT_MODE=strict`。

**回滚**：systemd 停服 → `current` 软链指回上一 release → 重启。KV 数据格式向后兼容
（新增键均为独立前缀），无需数据迁移。

## 已知注意点

- 令牌安全：本次改造过程中在对话里出现过 GitHub PAT，建议用完后到 GitHub Settings
  → Fine-grained tokens 吊销重建。
- Relay 生产实例（123.60.79.97）设备数为 0，是账号层上线的最佳窗口，无需迁移旧设备。
- iOS 云编译产物为未签名 IPA，需自行签名侧载；签名证书与描述文件不在本仓库范围。
