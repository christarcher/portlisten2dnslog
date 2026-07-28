# Server

服务端轮询 dnslog.org，验证和解码协议版本 2 的 DNS 查询，将新 UUID 先写入 SQLite
待发队列，再通过 Telegram Bot API 告警。

完整的两端目录、协议、部署和联调说明见
[Client/Server 结构与使用指南](../docs/CLIENT_SERVER_GUIDE.md)。

## 安装与测试

要求 Python 3.11+，无第三方运行时依赖。

```powershell
python -m unittest discover -s tests -v
python -m pip wheel . --no-deps --no-build-isolation --wheel-dir .\dist
```

安装为命令：

```powershell
python -m venv .venv
.\.venv\Scripts\pip.exe install .
```

## 环境变量

服务端也只使用环境变量，并在读取后从自身进程环境中删除。模板见
[config/server.env.example](config/server.env.example)。

| 变量 | 必需 | 默认值 |
|---|---|---|
| `P2D_DNSLOG_TOKEN` | 是 | 无 |
| `P2D_DOMAIN` | 是 | 无 |
| `P2D_XOR_KEY` | 是 | 无 |
| `P2D_AUTH_KEY` | 是 | 无 |
| `P2D_TELEGRAM_BOT_TOKEN` | 是 | 无 |
| `P2D_TELEGRAM_CHAT_ID` | 是 | 无 |
| `P2D_DB_PATH` | 否 | `events.db` |
| `P2D_MARKER` | 否 | `listener-req` |
| `P2D_POLL_INTERVAL` | 否 | `10` |
| `P2D_REQUEST_TIMEOUT` | 否 | `10` |
| `P2D_PENDING_BATCH` | 否 | `100` |
| `P2D_DNSLOG_URL` | 否 | `https://dnslog.org/{token}` |
| `P2D_TELEGRAM_API_BASE` | 否 | `https://api.telegram.org` |
| `P2D_TELEGRAM_DISABLE_NOTIFICATION` | 否 | `false` |
| `P2D_RUN_ONCE` | 否 | `false` |
| `P2D_CHECK_CONFIG` | 否 | `false` |
| `P2D_VERBOSE` | 否 | `false` |

`P2D_XOR_KEY`、`P2D_AUTH_KEY`、`P2D_DOMAIN` 和 `P2D_MARKER` 必须与客户端一致。
认证密钥至少 32 个 UTF-8 字节，并应与 XOR 密钥不同。

PowerShell 启动示例：

```powershell
Get-Content .\config\server.env.example | ForEach-Object {
    if ($_ -and -not $_.StartsWith('#')) {
        $name, $value = $_ -split '=', 2
        [Environment]::SetEnvironmentVariable($name, $value, 'Process')
    }
}
python -m portlistener2dns_server
```

若只校验配置，在启动前设置 `P2D_CHECK_CONFIG=true`；若只执行一轮轮询和发送，设置
`P2D_RUN_ONCE=true`。

## 去重和重试

新事件先以 `pending` 状态进入 SQLite，再调用 Telegram。Telegram 失败时记录仍保留，
下一轮继续发送；成功后变为 `sent`，相同 UUID 不再发送。

Telegram API 不支持客户端幂等键。若进程恰好在 Telegram 成功后、SQLite 提交前崩溃，
重启后理论上可能重复一次；正常 DNS 重复、客户端重试和轮询均会正确去重。

## systemd

将服务端安装到 `/opt/portlistener2dns-server`，把权限为 `0600` 的环境文件安装到
`/etc/portlistener2dns/server.env`，并使用
[deploy/systemd/portlistener2dns-server.service](deploy/systemd/portlistener2dns-server.service)。
示例单元使用动态非特权用户，数据库写入 `/var/lib/portlistener2dns/events.db`。
