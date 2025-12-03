## HTTP Monitor with Feishu Alert

使用 Go 编写的简单 HTTP 监控工具，定时检查多个 URL，当返回错误或超时时，通过飞书机器人 Webhook 发送消息卡片进行告警，并暴露 Prometheus `/metrics` 指标。

### 配置优先级

- **优先使用配置文件**：`config.yaml`（可参考 `config.yaml.example`）
- 如果同目录下没有 `config.yaml`，则回退到 **环境变量** 配置

### 配置文件方式（推荐）

在项目根目录复制一份示例文件：

```bash
cp config.yaml.example config.yaml
```

根据实际情况修改：

- **monitor.urls**: 需要监控的 URL 列表
- **monitor.interval_seconds**: 轮询间隔秒数，默认 `10`
- **monitor.timeout_seconds**: 单次请求超时时间秒数，默认 `5`
- **feishu.webhook**: 飞书群机器人的 Webhook 地址
- **log.file**: 日志文件路径（默认 `monitor.log`）
- **alert.cooldown_seconds**: 告警最小间隔，防止频繁推送，默认 `60`
- **alert.latency_threshold_ms**: 响应耗时超过该阈值视为慢请求并触发告警，默认 `0`（关闭）

### 环境变量方式（无 config.yaml 时）

- **MONITOR_URLS**: 需要监控的 URL，多个用逗号分隔，例如：`https://example.com,https://api.example.com/health`
- **FEISHU_WEBHOOK**: 飞书群机器人的 Webhook 地址
- **INTERVAL_SECONDS**: 轮询间隔秒数，默认 `10`
- **LOG_FILE**: 日志文件路径，默认 `monitor.log`
- **ALERT_COOLDOWN_SECONDS**: 告警最小间隔，默认 `60`
- **ALERT_LATENCY_THRESHOLD_MS**: 慢请求耗时阈值（毫秒），默认 `0`

### 运行

```bash
cd /home/maolin/workspace/http-monitor

# 如果使用 config.yaml，直接运行即可
go run .

# 如果使用环境变量方式：
# export MONITOR_URLS="https://example.com,https://api.example.com/health"
# export FEISHU_WEBHOOK="https://open.feishu.cn/open-apis/bot/v2/hook/xxxxxx"
# export INTERVAL_SECONDS=10
# export LOG_FILE="monitor.log"
# go run .
```

程序会每 INTERVAL_SECONDS 秒检查一次所有 URL：

- 当检测到错误、非 2xx 状态码，或耗时超过 `alert.latency_threshold_ms` 时，会尝试发送「HTTP 监控告警」交互卡片，并记录到日志；同一 URL 会遵循 `cooldown` 限制，恢复到正常（包含耗时恢复）后自动重置，可再次即时告警。
- 在本地 `:2112/metrics` 暴露 Prometheus 指标（如 `http_monitor_requests_total`、`http_monitor_request_duration_seconds`）。

### Windows 服务部署

已集成 [kardianos/service](https://github.com/kardianos/service)，可将程序打包并注册为 Windows 服务：

```powershell
cd C:\path\to\http-monitor
set GOOS=windows
set GOARCH=amd64
go build -o http-monitor.exe

# 安装服务（需管理员权限）
http-monitor.exe -service install

# 启动服务
http-monitor.exe -service start

# 停止/卸载
http-monitor.exe -service stop
http-monitor.exe -service uninstall
```

默认服务名称为 `HttpMonitor`。配置仍通过 `config.yaml`（放置在可执行文件同目录）或环境变量读取。若需以服务方式调试，可直接运行 `http-monitor.exe -service run`。



