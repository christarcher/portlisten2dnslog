# PortListener2DNS

PortListener2DNS 是面向隔离内网的轻量 TCP 扫描告警探针。客户端部署在需要监测的内网
主机上，监听正常业务不会访问的端口；连接一旦完成 TCP 三次握手，客户端就把源 IP、
源端口、目标 IP、目标端口和时间通过 DNS 查询送出。公网服务端轮询 dnslog.org，
验证消息、持久化去重，并通过 Telegram Bot 发送告警。

## 适用场景

- 内网主机只能进行 DNS 出网，无法直接访问普通 TCP/UDP 公网服务；
- 主机之间的合法通信端口固定，希望利用未使用端口发现 connect scan；
- 需要将告警集中到公网服务端，并避免 DNS 重试造成重复 Telegram 消息。

本项目是早期告警探针，不是端口扫描阻断、防火墙或完整的入侵检测系统。

## 主要能力

- Go 客户端可一次监听多个 TCP 端口，例如 `139,445,1080,1099`；
- 客户端可排除内网漏扫等固定来源 IP，不为白名单连接发送告警；
- 非敏感默认值编译在客户端中，环境变量读取后立即从自身进程环境删除；
- 客户端默认不写 stdout/stderr，配置错误通过退出码表示；
- 使用 Garble `-literals -tiny -seed=random` 构建客户端；
- 协议版本 2 支持 IPv4、IPv6、56 字符 DNS 标签切分；
- 使用独立 `P2D_AUTH_KEY` 计算 HMAC-SHA256 认证标签，拒绝篡改和未知密钥伪造；
- Python 服务端使用 SQLite 保存 `pending`/`sent` 状态并按 UUID 去重；
- Telegram 失败时保留待发事件，下次轮询继续发送；
- 提供 Windows/Linux 构建产物、自动化测试和 systemd 加固单元。

## 工作流程

```text
扫描者
  |
  | TCP connect
  v
client/ Go 监听器
  |
  | 带 UUID、五项连接信息和 HMAC 的 DNS A 查询
  v
内网递归 DNS -----> dnslog.org
                       |
                       | HTTPS 轮询
                       v
                 server/ Python
                       |
                       +----> SQLite 去重/待发队列
                       |
                       +----> Telegram Bot API
```

客户端收到包括 NXDOMAIN 在内的有效 DNS 响应即认为本次查询已经到达递归 DNS。无响应
重试沿用同一个 UUID，因此 dnslog.org 中即使出现重复查询，服务端也只会正常通知一次。

## 仓库结构

```text
portlistener2dns/
├── README.md
├── docs/
│   └── CLIENT_SERVER_GUIDE.md
├── client/
│   ├── cmd/                    # 客户端程序入口
│   ├── internal/client/        # 配置、监听和 DNS 发送
│   ├── internal/protocol/      # Go 协议编码
│   ├── config/                 # 客户端环境变量模板
│   ├── scripts/                # Garble 构建脚本
│   ├── deploy/systemd/         # 客户端 systemd 单元
│   └── README.md
└── server/
    ├── portlistener2dns_server/ # 服务端包、协议解码和通知逻辑
    ├── tests/                   # Python 测试
    ├── config/                  # 服务端环境变量模板
    ├── deploy/systemd/          # 服务端 systemd 单元
    └── README.md
```

更详细的模块职责、协议格式、配置对应关系及部署步骤见
[Client/Server 结构与使用指南](docs/CLIENT_SERVER_GUIDE.md)。

## 运行要求

| 组件 | 要求 |
|---|---|
| 客户端源码构建 | Go 1.26+、Garble v0.17.0 |
| 客户端运行 | 构建好的单文件二进制 |
| 服务端 | Python 3.11+ |
| 外部服务 | dnslog.org token/根域名、Telegram bot token/chat ID |

Python 服务端没有第三方运行时依赖，Go 客户端也没有第三方运行时依赖。

## 快速开始

### 1. 生成两把不同的密钥

分别执行两次，得到不同的 64 字符十六进制字符串：

```powershell
[Convert]::ToHexString(
    [Security.Cryptography.RandomNumberGenerator]::GetBytes(32)
).ToLowerInvariant()
```

- 第一把填写到两端的 `P2D_XOR_KEY`；
- 第二把填写到两端的 `P2D_AUTH_KEY`；
- `P2D_DOMAIN` 和 `P2D_MARKER` 也必须在两端保持一致。

不要把真实 token 或密钥提交到 Git。

### 2. 配置客户端和服务端

```powershell
Copy-Item .\client\config\client.env.example .\client\client.env
Copy-Item .\server\config\server.env.example .\server\server.env
```

客户端至少需要填写：

```text
P2D_DOMAIN=<dnslog 根域名>
P2D_XOR_KEY=<共享混淆密钥>
P2D_AUTH_KEY=<共享认证密钥>
```

`P2D_DNS_SERVER` 编译默认值为 `223.5.5.5`；`P2D_IP_WHITELIST` 默认排除
`192.168.100.254,192.168.100.253`。需要时可通过同名环境变量覆盖。

服务端还需要填写：

```text
P2D_DNSLOG_TOKEN=<dnslog API token>
P2D_TELEGRAM_BOT_TOKEN=<Telegram bot token>
P2D_TELEGRAM_CHAT_ID=<Telegram chat ID>
```

`client.env` 和 `server.env` 已加入 `.gitignore`。

### 3. 构建客户端

```powershell
Set-Location .\client
go install mvdan.cc/garble@v0.17.0
.\scripts\build.ps1
```

生成：

```text
client/bin/portlistener2dns-linux-amd64
client/bin/portlistener2dns-linux-arm64
client/bin/portlistener2dns-windows-amd64.exe
```

### 4. 测试服务端

```powershell
Set-Location ..\server
python -m unittest discover -s tests -v
```

服务端可以直接从源码运行，也可以构建 wheel：

```powershell
python -m pip wheel . --no-deps --no-build-isolation --wheel-dir .\dist
```

### 5. 启动

先启动服务端，再启动客户端。PowerShell 环境文件加载函数：

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

服务端：

```powershell
Set-Location .\server
Import-EnvFile .\server.env
python -m portlistener2dns_server
```

客户端：

```powershell
Set-Location .\client
Import-EnvFile .\client.env
.\bin\portlistener2dns-windows-amd64.exe
```

客户端默认完全静默。排错时可以临时设置 `$env:P2D_VERBOSE='true'` 后重新启动。

## Linux systemd 部署客户端

先根据服务器架构选择构建产物：`x86_64` 使用 `linux-amd64`，`aarch64`/`arm64`
使用 `linux-arm64`。以下以 amd64 为例，从仓库根目录执行：

```sh
sudo install -m 0755 \
  client/bin/portlistener2dns-linux-amd64 \
  /usr/local/bin/portlistener2dns

sudo install -d -m 0700 /etc/portlistener2dns
sudo install -m 0600 \
  client/config/client.env.example \
  /etc/portlistener2dns/client.env
sudoedit /etc/portlistener2dns/client.env

sudo install -m 0644 \
  client/deploy/systemd/portlistener2dns-client.service \
  /etc/systemd/system/portlistener2dns-client.service

sudo systemctl daemon-reload
sudo systemctl enable --now portlistener2dns-client
sudo systemctl status portlistener2dns-client
```

环境文件至少填写 `P2D_DOMAIN`、`P2D_XOR_KEY` 和 `P2D_AUTH_KEY`。DNS 服务器和 IP
白名单已有编译默认值，不需要写入环境文件；要覆盖时再取消模板中的对应注释。该单元使用
动态非特权用户，并只授予监听 1024 以下端口所需的 `CAP_NET_BIND_SERVICE`。
如果隔离网不能访问默认的公共 DNS，必须将 `P2D_DNS_SERVER` 覆盖为可达的内网递归
DNS IP。

更新二进制后执行：

```sh
sudo install -m 0755 \
  client/bin/portlistener2dns-linux-amd64 \
  /usr/local/bin/portlistener2dns
sudo systemctl restart portlistener2dns-client
```

客户端默认不写日志。排错时可在环境文件中临时设置 `P2D_VERBOSE=true`，并将 systemd
单元的 `StandardOutput`/`StandardError` 临时改为 `journal` 后执行
`sudo systemctl daemon-reload && sudo systemctl restart portlistener2dns-client`。
服务端的完整 systemd 部署步骤见
[Client/Server 结构与使用指南](docs/CLIENT_SERVER_GUIDE.md#9-linux-与-systemd)。

## 验证

确认测试主机 IP 不在 `P2D_IP_WHITELIST` 中，再从该主机连接一个探针端口：

```powershell
Test-NetConnection <探针IP> -Port 445
```

预期过程：

1. 客户端接受并立即关闭 TCP 连接；
2. 内网 DNS 收到唯一子域查询；
3. 服务端轮询到记录并通过 HMAC 校验；
4. SQLite 新增事件并标记为 `pending`；
5. Telegram 发送成功后事件变为 `sent`。

## 文档

- [Client/Server 结构与使用指南](docs/CLIENT_SERVER_GUIDE.md)
- [客户端说明](client/README.md)
- [服务端说明](server/README.md)
- [客户端环境变量模板](client/config/client.env.example)
- [服务端环境变量模板](server/config/server.env.example)

## 安全边界

- `defaults.go` 只应保存 DNS、白名单等非敏感默认值；编译进二进制的字符串仍可被提取，
  不要把认证密钥、token 或真实凭据写入其中；
- Garble 只能增加静态逆向成本，不能阻止拥有足够时间或主机权限的攻击者分析程序；
- 攻击者若能读取环境文件、服务管理器配置或进程内存，仍可能取得认证密钥；
- HMAC 提供真实性和完整性，循环异或只提供轻量混淆，不提供现代加密意义上的机密性；
- 客户端基于标准 TCP `Accept`，只能检测完成三次握手的连接，纯 SYN 半开放扫描不会
  进入应用层；
- Telegram API 没有客户端幂等键，在 Telegram 成功后、SQLite 提交前的极端崩溃窗口
  内可能多发送一次。
