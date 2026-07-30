# Client

客户端是无第三方运行时依赖的 Go TCP 监听器。连接完成三次握手后，它立即关闭连接；
来源 IP 不在白名单时，生成随机 UUID，并通过指定的递归 DNS 服务器发送认证过的五元组。

完整的两端目录、协议、部署和联调说明见
[Client/Server 结构与使用指南](../docs/CLIENT_SERVER_GUIDE.md)。

## 运行输出

默认 `P2D_VERBOSE=false`：

- 不输出启动、监听、连接、DNS 成功或失败日志；
- 配置错误只通过非零退出码表示，不打印配置内容；
- Garble `-tiny` 构建还会移除 panic、fatal 和运行时堆栈输出；
- systemd 示例将 `StandardOutput` 和 `StandardError` 都设为 `null`。

只有排错时显式设置 `P2D_VERBOSE=true` 才会向标准错误输出日志。操作系统自身、服务
管理器或终端对进程退出的提示不受应用控制。

## 环境变量

客户端的非敏感默认值集中编译在 `internal/client/defaults.go` 中；环境变量只用于必需配置
或覆盖默认值，读取后会立即从自身进程环境中删除所有 `P2D_*` 配置。

| 变量 | 必需 | 默认值 | 说明 |
|---|---|---|---|
| `P2D_DOMAIN` | 是 | 无 | dnslog.org 根域名 |
| `P2D_DNS_SERVER` | 否 | `223.5.5.5` | DNS 服务器 IP 或 `IP:端口` |
| `P2D_IP_WHITELIST` | 否 | `192.168.100.254,192.168.100.253` | 不生成告警的来源 IP，逗号分隔 |
| `P2D_XOR_KEY` | 是 | 无 | 混淆密钥，至少 8 字节 |
| `P2D_AUTH_KEY` | 是 | 无 | HMAC 密钥，至少 32 字节，必须与服务端一致 |
| `P2D_BIND_ADDRESS` | 否 | `0.0.0.0` | 监听的 IPv4 或 IPv6 地址 |
| `P2D_PORTS` | 否 | `139,445,1080,1099` | 逗号分隔的多个 TCP 端口 |
| `P2D_MARKER` | 否 | `listener-req` | DNS 请求标识 |
| `P2D_QUEUE_SIZE` | 否 | `256` | 有界内存告警队列 |
| `P2D_WORKERS` | 否 | `2` | DNS 发送协程数 |
| `P2D_RETRIES` | 否 | `3` | DNS 无响应时的总发送次数 |
| `P2D_QUERY_TIMEOUT` | 否 | `3s` | 每次 DNS 请求超时 |
| `P2D_VERBOSE` | 否 | `false` | 是否输出排错日志 |

例如同时监听 139、445、1080 和 1099：

```text
P2D_BIND_ADDRESS=0.0.0.0
P2D_PORTS=139,445,1080,1099
```

任意一个端口绑定失败时，客户端会关闭已经打开的监听器并整体退出，避免静默进入部分
监听状态。完整模板见 [config/client.env.example](config/client.env.example)。

白名单只匹配完整 IP，不接受 CIDR。命中的连接仍会立即关闭，但不会生成 UUID、进入告警
队列或发送 DNS 查询。若要禁用编译后的默认白名单，可显式设置空值：

```text
P2D_IP_WHITELIST=
```

## Garble 构建

要求 Go 1.26+。先安装项目验证过的 Garble 版本：

```powershell
go install mvdan.cc/garble@v0.17.0
.\scripts\build.ps1
```

脚本先运行全部 Go 测试，再分别产生：

- `bin/portlistener2dns-linux-amd64`
- `bin/portlistener2dns-linux-arm64`
- `bin/portlistener2dns-windows-amd64.exe`

实际构建参数是：

```text
garble -literals -tiny -seed=random build -trimpath -ldflags="-s -w -buildid="
```

`-literals` 混淆字符串，`-tiny` 删除更多符号、位置和运行时错误输出，
`-seed=random` 使每次构建使用不同的混淆映射。Garble 官方也明确说明混淆可以在投入
足够精力后被逆向，因此不能把它当作密钥保护机制。

## 启动

PowerShell 示例：

```powershell
Get-Content .\config\client.env.example | ForEach-Object {
    if ($_ -and -not $_.StartsWith('#')) {
        $name, $value = $_ -split '=', 2
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}
.\bin\portlistener2dns-windows-amd64.exe
```

Linux 监听 1024 以下端口需要 root 或 `CAP_NET_BIND_SERVICE`。生产部署可以使用
[deploy/systemd/portlistener2dns-client.service](deploy/systemd/portlistener2dns-client.service)，
并把权限为 `0600` 的环境文件放在 `/etc/portlistener2dns/client.env`。

## 安全边界

- `defaults.go` 只能保存非敏感默认值。编译到二进制并经过 Garble 的字符串仍可能被提取，
  不要把 `P2D_XOR_KEY`、`P2D_AUTH_KEY` 或其他凭据写入其中。
- HMAC-SHA256 的前 16 字节用于消息认证；不知道 `P2D_AUTH_KEY` 的第三方无法构造能被
  服务端接受的新消息。
- Garble 只提高静态逆向成本。若攻击者已经获得读取进程内存、服务管理器环境或配置
  文件的权限，仍可能取得认证密钥并伪造消息，应同时依赖主机加固和密钥轮换。
- 循环异或只是载荷混淆，不提供现代加密意义上的机密性。
- 标准 TCP `Accept` 只检测完成三次握手的连接；只发 SYN 后复位的半开放扫描不会进入
  应用层监听队列。
