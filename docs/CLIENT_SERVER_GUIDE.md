# Client/Server 结构与使用指南

本文集中说明 PortListener2DNS 的客户端、服务端、DNS 协议、环境变量、运行流程、
部署方式和常见排错方法。简要介绍见仓库根目录的 [README](../README.md)。

## 1. 总体架构

PortListener2DNS 由两个独立运行的组件组成：

| 组件 | 部署位置 | 职责 |
|---|---|---|
| Client | 受监测的隔离内网主机 | 监听多个 TCP 端口，将连接信息编码为 DNS 查询 |
| Server | 可访问 dnslog.org 和 Telegram 的公网主机 | 轮询记录、验证协议、SQLite 去重并发送告警 |

```text
┌──────────┐ TCP 连接  ┌──────────────┐  DNS A 查询  ┌──────────────┐
│ 扫描主机 │──────────>│ Client 探针  │─────────────>│ 内网递归 DNS │
└──────────┘           └──────────────┘              └──────┬───────┘
                                                            │
                                                            v
                                                     ┌──────────────┐
                                                     │ dnslog.org   │
                                                     └──────┬───────┘
                                                            │ HTTPS
                                                            v
┌──────────┐ Telegram ┌──────────────┐                ┌──────────────┐
│ 运维人员 │<─────────│ Telegram Bot │<───────────────│ Server       │
└──────────┘          └──────────────┘                │ + SQLite     │
                                                     └──────────────┘
```

Client 和 Server 不直接建立网络连接。两端只通过 dnslog.org 中出现的查询名交换事件。

## 2. 目录结构

### 2.1 Client

```text
client/
├── cmd/portlistener2dns/main.go
├── internal/
│   ├── client/
│   │   ├── config.go
│   │   ├── dns.go
│   │   └── run.go
│   └── protocol/
│       └── protocol.go
├── config/client.env.example
├── scripts/build.ps1
├── deploy/systemd/portlistener2dns-client.service
├── go.mod
└── README.md
```

模块职责：

| 文件 | 职责 |
|---|---|
| `cmd/portlistener2dns/main.go` | 读取配置、选择静默/排错日志、处理退出信号 |
| `internal/client/config.go` | 读取并清除环境变量，校验 DNS、端口、密钥和并发参数 |
| `internal/client/run.go` | 原子创建全部监听器、接受连接、构造事件和管理发送 worker |
| `internal/client/dns.go` | 构造最小 DNS A 请求、发送 UDP 查询、验证响应和超时重试 |
| `internal/protocol/protocol.go` | UUID、二进制编码、HMAC、XOR、Base32 和 DNS 标签切分 |
| `scripts/build.ps1` | 测试后使用 Garble 构建 Linux amd64/arm64 和 Windows amd64 产物 |
| `config/client.env.example` | 全部客户端参数模板 |
| `deploy/systemd/*` | Linux 动态用户、低端口 capability 和静默输出配置 |

Client 使用有界内存队列。队列已满时新事件会被丢弃，避免大量连接无限消耗内存。默认
两个 worker 并发发送 DNS 查询。

### 2.2 Server

```text
server/
├── portlistener2dns_server/
│   ├── __main__.py
│   ├── app.py
│   └── protocol.py
├── tests/
│   ├── test_app.py
│   └── test_protocol.py
├── config/server.env.example
├── deploy/systemd/portlistener2dns-server.service
├── pyproject.toml
└── README.md
```

模块职责：

| 文件/类 | 职责 |
|---|---|
| `__main__.py` | 支持 `python -m portlistener2dns_server` |
| `protocol.py` | DNS 名称校验、Base32/XOR 解码、HMAC 验证和五项连接信息解析 |
| `DNSLogClient` | 通过 HTTPS 拉取 dnslog.org JSON，限制响应大小并规范化字段 |
| `EventStore` | 创建 SQLite 表、按 UUID 去重、维护 `pending`/`sent` 状态 |
| `TelegramClient` | 调用 Telegram `sendMessage` API |
| `Collector` | 完成“拉取 → 解码 → 入库 → 发送 → 更新状态”的一轮处理 |
| `load_settings_from_env` | 读取、校验并删除服务端环境变量 |
| `tests/` | 协议互操作、篡改拒绝、持久去重、HTTP 客户端和失败重试测试 |

## 3. 一条事件的生命周期

```text
TCP Accept
   |
   v
生成 UUID + 记录源/目标端点和 UTC 时间
   |
   v
二进制编码 -> HMAC -> XOR -> Base32 -> DNS 标签
   |
   v
向 P2D_DNS_SERVER 发送 A 查询
   |
   v
dnslog.org 记录完整查询名
   |
   v
Server 轮询、匹配 marker/domain、验证 HMAC
   |
   v
SQLite INSERT OR IGNORE (UUID 主键，status=pending)
   |
   v
Telegram sendMessage
   |
   +-- 成功：status=sent
   |
   +-- 失败：保留 pending，记录 attempts/last_error，下轮重试
```

Client 超时重试始终复用同一个 UUID。递归 DNS 也可能重复查询，因此 Server 以 UUID
作为 SQLite 主键，而不是依赖 dnslog.org 的记录序号。

## 4. DNS 协议版本 2

### 4.1 查询名

```text
<uuid>.<payload-1>[.<payload-2>...].<marker>.<base-domain>
```

示意：

```text
37604be5-aa6f-439b-8c09-cd06efb5b23b.<payload>.listener-req.example.dnslog.test
```

约束：

- UUID 是小写 RFC 4122 v4 格式；
- payload 是无填充、小写 Base32；
- 每个 payload 标签最多 56 字符；
- marker 必须是单个合法 DNS 标签；
- 完整查询名最多 253 字节；
- IPv4/IPv6 payload 都可以自动拆成多个标签。

### 4.2 明文载荷

| 字段 | 长度 | 编码 |
|---|---:|---|
| 协议版本 | 1 字节 | 当前固定为 `2` |
| 事件时间 | 8 字节 | Unix UTC 秒，大端 |
| 源地址族 | 1 字节 | `4` 或 `6` |
| 源 IP | 4/16 字节 | 网络字节序 |
| 源端口 | 2 字节 | 大端 |
| 目标地址族 | 1 字节 | `4` 或 `6` |
| 目标 IP | 4/16 字节 | 网络字节序 |
| 目标端口 | 2 字节 | 大端 |
| 认证标签 | 16 字节 | HMAC-SHA256 前 16 字节 |

处理顺序：

1. Client 对版本、时间和两个端点进行二进制编码；
2. 使用 `P2D_AUTH_KEY` 计算 HMAC-SHA256，附加前 16 字节；
3. 使用 `P2D_XOR_KEY` 对整个二进制循环异或；
4. 使用无填充 Base32 编码并按 56 字符切分；
5. Server 反向处理，并使用常量时间比较验证 HMAC。

协议版本 1 没有 HMAC，版本 2 Server 不接受旧消息。升级时必须同时更新两端并配置
`P2D_AUTH_KEY`。

## 5. 共享配置

以下四项必须在 Client 和 Server 完全相同：

| Client | Server | 用途 |
|---|---|---|
| `P2D_DOMAIN` | `P2D_DOMAIN` | dnslog.org 根域名 |
| `P2D_MARKER` | `P2D_MARKER` | 从其他 DNSLog 记录中识别本项目 |
| `P2D_XOR_KEY` | `P2D_XOR_KEY` | 载荷混淆 |
| `P2D_AUTH_KEY` | `P2D_AUTH_KEY` | HMAC 消息认证 |

建议生成两把不同的 32 字节随机值：

```powershell
[Convert]::ToHexString(
    [Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
).ToLowerInvariant()
```

执行两次，一把用作 XOR key，另一把用作 auth key。十六进制字符串本身作为环境变量值，
两端必须逐字一致。

## 6. Client 配置

模板：[client/config/client.env.example](../client/config/client.env.example)

| 变量 | 必需 | 默认值 | 校验/行为 |
|---|---|---|---|
| `P2D_DOMAIN` | 是 | 无 | 去掉首尾点后必须是合法 DNS 名 |
| `P2D_DNS_SERVER` | 是 | 无 | 必须为 IP 或 `IP:端口`，不接受主机名 |
| `P2D_XOR_KEY` | 是 | 无 | 至少 8 个 UTF-8 字节 |
| `P2D_AUTH_KEY` | 是 | 无 | 至少 32 个 UTF-8 字节 |
| `P2D_BIND_ADDRESS` | 否 | `0.0.0.0` | IPv4/IPv6 地址 |
| `P2D_PORTS` | 否 | `139,445,1080,1099` | 1–65535，逗号分隔，不允许重复 |
| `P2D_MARKER` | 否 | `listener-req` | 单个 DNS 标签 |
| `P2D_QUEUE_SIZE` | 否 | `256` | 正整数 |
| `P2D_WORKERS` | 否 | `2` | 正整数 |
| `P2D_RETRIES` | 否 | `3` | 正整数，包含第一次发送 |
| `P2D_QUERY_TIMEOUT` | 否 | `3s` | Go duration，例如 `500ms`、`3s` |
| `P2D_VERBOSE` | 否 | `false` | `true` 时向 stderr 输出排错日志 |

多个端口只需一个进程：

```text
P2D_BIND_ADDRESS=0.0.0.0
P2D_PORTS=139,445,1080,1099
```

启动时会先绑定全部地址。只要其中一个失败，已打开的监听器会全部关闭，然后程序以
退出码 1 结束。

Client 退出码：

| 退出码 | 含义 |
|---:|---|
| `0` | 收到正常停止信号并完成退出 |
| `1` | 监听或运行失败 |
| `2` | 环境变量配置无效 |

默认静默时退出码是判断启动是否成功的重要依据。

## 7. Server 配置

模板：[server/config/server.env.example](../server/config/server.env.example)

| 变量 | 必需 | 默认值 | 说明 |
|---|---|---|---|
| `P2D_DNSLOG_TOKEN` | 是 | 无 | dnslog.org API token |
| `P2D_DOMAIN` | 是 | 无 | 与 Client 一致 |
| `P2D_XOR_KEY` | 是 | 无 | 与 Client 一致 |
| `P2D_AUTH_KEY` | 是 | 无 | 与 Client 一致，至少 32 字节 |
| `P2D_TELEGRAM_BOT_TOKEN` | 是 | 无 | Telegram Bot token |
| `P2D_TELEGRAM_CHAT_ID` | 是 | 无 | 私聊、群组或频道 chat ID |
| `P2D_DB_PATH` | 否 | `events.db` | 相对路径以当前工作目录为基准 |
| `P2D_MARKER` | 否 | `listener-req` | 与 Client 一致 |
| `P2D_POLL_INTERVAL` | 否 | `10` | 轮询间隔秒数，正数 |
| `P2D_REQUEST_TIMEOUT` | 否 | `10` | HTTP 超时秒数，正数 |
| `P2D_PENDING_BATCH` | 否 | `100` | 每轮最大发送数量 |
| `P2D_DNSLOG_URL` | 否 | `https://dnslog.org/{token}` | 必须保留 `{token}` |
| `P2D_TELEGRAM_API_BASE` | 否 | `https://api.telegram.org` | Telegram API 根地址 |
| `P2D_TELEGRAM_DISABLE_NOTIFICATION` | 否 | `false` | 是否静音通知 |
| `P2D_RUN_ONCE` | 否 | `false` | 只执行一轮后退出 |
| `P2D_CHECK_CONFIG` | 否 | `false` | 只验证配置，不创建数据库或访问网络 |
| `P2D_VERBOSE` | 否 | `false` | 输出被忽略记录的原因等调试信息 |

Server 退出码：

| 退出码 | 含义 |
|---:|---|
| `0` | 正常停止、单轮成功或配置验证成功 |
| `1` | `P2D_RUN_ONCE` 单轮失败，或不可恢复的启动/SQLite 运行错误 |
| `2` | 环境变量配置无效 |

持续运行模式下，临时 DNSLog/Telegram 网络错误会记录日志并等待下一轮，不会让服务
立即退出。

## 8. Windows 使用

### 8.1 环境文件加载

在 PowerShell 会话中定义：

```powershell
function Import-EnvFile([string]$Path) {
    Get-Content -LiteralPath $Path | ForEach-Object {
        $line = $_.Trim()
        if ($line -and -not $line.StartsWith('#')) {
            $name, $value = $line -split '=', 2
            [Environment]::SetEnvironmentVariable($name, $value, 'Process')
        }
    }
}
```

环境变量只写入当前 PowerShell 进程及随后启动的子进程，不会永久修改用户或系统环境。

### 8.2 构建 Client

```powershell
Set-Location .\client
go install mvdan.cc/garble@v0.17.0
.\scripts\build.ps1
```

构建脚本会：

1. 执行 `go test ./...`；
2. 查找 PATH 或 `GOPATH/bin` 中的 Garble；
3. 分别构建 Linux amd64、Linux arm64 和 Windows amd64；
4. 每个产物使用独立随机 Garble seed；
5. 显示最终文件大小和 Garble 版本。

### 8.3 启动 Server

```powershell
Set-Location .\server
Copy-Item .\config\server.env.example .\server.env
# 编辑 server.env

Import-EnvFile .\server.env
$env:P2D_CHECK_CONFIG = 'true'
python -m portlistener2dns_server
```

配置检查成功后，重新导入环境文件并正式启动：

```powershell
Import-EnvFile .\server.env
python -m portlistener2dns_server
```

也可以创建虚拟环境并安装命令：

```powershell
python -m venv .venv
.\.venv\Scripts\pip.exe install .
Import-EnvFile .\server.env
.\.venv\Scripts\portlistener2dns-server.exe
```

### 8.4 启动 Client

```powershell
Set-Location .\client
Copy-Item .\config\client.env.example .\client.env
# 编辑 client.env

Import-EnvFile .\client.env
.\bin\portlistener2dns-windows-amd64.exe
```

默认终端没有任何输出。排错：

```powershell
Import-EnvFile .\client.env
$env:P2D_VERBOSE = 'true'
.\bin\portlistener2dns-windows-amd64.exe
```

## 9. Linux 与 systemd

### 9.1 Client

构建并安装：

```sh
sudo install -m 0755 \
  client/bin/portlistener2dns-linux-amd64 \
  /usr/local/bin/portlistener2dns

sudo install -d -m 0700 /etc/portlistener2dns
sudo install -m 0600 client/client.env /etc/portlistener2dns/client.env
sudo install -m 0644 \
  client/deploy/systemd/portlistener2dns-client.service \
  /etc/systemd/system/portlistener2dns-client.service

sudo systemctl daemon-reload
sudo systemctl enable --now portlistener2dns-client
```

该单元使用动态非特权用户，只授予 `CAP_NET_BIND_SERVICE` 以监听 1024 以下端口，并将
stdout/stderr 指向空设备。

### 9.2 Server

将 `server/` 内容部署到 `/opt/portlistener2dns-server`，然后：

```sh
cd /opt/portlistener2dns-server
sudo python3 -m venv .venv
sudo .venv/bin/pip install .

sudo install -d -m 0700 /etc/portlistener2dns
sudo install -m 0600 server.env /etc/portlistener2dns/server.env
sudo install -m 0644 \
  deploy/systemd/portlistener2dns-server.service \
  /etc/systemd/system/portlistener2dns-server.service

sudo systemctl daemon-reload
sudo systemctl enable --now portlistener2dns-server
```

`StateDirectory=portlistener2dns` 会创建 `/var/lib/portlistener2dns`。示例服务将其设为
工作目录，因此默认 `P2D_DB_PATH=events.db` 实际对应
`/var/lib/portlistener2dns/events.db`。

查看 Server 日志：

```sh
journalctl -u portlistener2dns-server -f
```

Client 默认没有应用日志。需要排错时临时将环境文件中的 `P2D_VERBOSE` 设为 `true`，
并暂时调整 systemd 单元的输出设置。

## 10. 联调验证

建议按以下顺序：

1. Server 设置 `P2D_CHECK_CONFIG=true` 验证全部必需变量；
2. Server 正式启动并确认能读取 dnslog.org；
3. Client 临时设置 `P2D_VERBOSE=true`，确认全部端口绑定成功；
4. 从另一台内网主机连接一个监听端口；
5. 在 dnslog.org API 中确认出现带 UUID 和 marker 的查询；
6. 查看 Server 是否记录新 UUID；
7. 查看 Telegram 是否收到源、目标和时间；
8. 恢复 Client 的 `P2D_VERBOSE=false`。

PowerShell 连接测试：

```powershell
Test-NetConnection <Client-IP> -Port 445
```

Linux 连接测试：

```sh
nc -vz <Client-IP> 445
```

不要在生产探针本机反复测试低端口，以免把验证流量与真实横向扫描告警混在一起。

## 11. 常见问题

### Client 启动后没有任何输出

这是默认行为。检查进程是否存在及退出码；需要排错时将 `P2D_VERBOSE=true`。如果通过
systemd 运行，还要临时取消 `StandardOutput=null` 和 `StandardError=null`。

### Client 立即以退出码 2 结束

环境变量配置无效。常见原因：

- `P2D_DOMAIN`、`P2D_DNS_SERVER` 或密钥为空；
- `P2D_DNS_SERVER` 使用了主机名而不是 IP；
- `P2D_AUTH_KEY` 少于 32 字节；
- `P2D_PORTS` 包含空值、重复端口或超出 1–65535；
- `P2D_BIND_ADDRESS` 不是合法 IP；
- `P2D_QUERY_TIMEOUT` 不是 Go duration。

开启 `P2D_VERBOSE` 不会显示配置阶段错误，因为无效配置会在日志器创建前退出。建议使用
环境变量模板逐项核对。

### 端口只监听了一部分

正常实现不会进入部分监听状态。任意端口绑定失败都会关闭全部监听器并退出。检查端口
是否已被其他服务占用，以及 Linux 是否具有低端口 capability。

### dnslog.org 有记录但 Server 不处理

检查：

- 查询名的 marker 和 domain 是否与 Server 完全相同；
- Client/Server 是否都为协议版本 2；
- `P2D_XOR_KEY` 和 `P2D_AUTH_KEY` 是否逐字一致；
- 完整 DNS 名称是否被上游系统截断；
- Server 设置 `P2D_VERBOSE=true` 后是否报告 HMAC 或格式错误。

### Telegram 暂时不可用

事件已进入 SQLite 时会保留为 `pending`，下一轮继续重试。为避免同一种网络/机器人错误
对整个队列快速重复请求，一次发送失败后当前批次会停止，等待下一轮。

### DNS 查询超时

Client 不会回退到其他 DNS 服务器。检查 `P2D_DNS_SERVER` 是否为隔离网中实际允许的
递归 DNS IP、UDP 53 是否可达，以及域名是否能由该服务器递归解析。

## 12. 安全说明

- Garble 混淆、删除符号和随机 seed 只能提高分析成本；
- 环境变量读取后删除可以减少 `/proc` 和子进程意外泄露，但密钥仍会存在于进程内存；
- 环境文件必须限制为管理员/root 可读，推荐权限 `0600`；
- HMAC 可以阻止不知道认证密钥的一方伪造消息，但客户端主机完全失陷后无法保证密钥；
- XOR 不是加密，不要在载荷中加入密码、token、用户名或其他秘密；
- 定期轮换两把密钥时必须同步更新 Client 和 Server；
- SQLite 数据库包含内网地址和扫描时间，应纳入权限控制与备份策略；
- Client 只观察成功完成 TCP 握手的连接，不检测纯 SYN scan。

## 13. 测试

Client：

```powershell
Set-Location .\client
go vet ./...
go test ./...
garble -literals -tiny -seed=random test ./...
```

Server：

```powershell
Set-Location .\server
python -m unittest discover -s tests -v
python -m compileall -q .\portlistener2dns_server .\tests
```

测试覆盖：

- Go/Python 固定协议互操作向量；
- IPv4、IPv6 和 DNS 标签切分；
- HMAC 篡改拒绝；
- 环境变量读取后删除；
- 多端口监听及真实 UDP DNS 请求；
- SQLite UUID 去重和 pending 重试；
- DNSLog/Telegram HTTP 请求；
- 配置边界和异常输入。
