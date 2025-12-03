package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v3"
)

// 配置来源优先级：
// 1. config.yaml
// 2. 环境变量
//
// MONITOR_URLS: 以逗号分隔的 URL 列表（仅在未提供 config.yaml 时使用）
// FEISHU_WEBHOOK: 飞书群机器人的 webhook 地址
// INTERVAL_SECONDS: 轮询间隔（秒），默认 10
// LOG_FILE: 日志文件路径（默认为 monitor.log）

type Config struct {
	Monitor struct {
		URLs          []string `yaml:"urls"`
		Interval      int      `yaml:"interval_seconds"`
		TimeoutSecond int      `yaml:"timeout_seconds"`
	} `yaml:"monitor"`
	Feishu struct {
		Webhook string `yaml:"webhook"`
	} `yaml:"feishu"`
	Log struct {
		File string `yaml:"file"`
	} `yaml:"log"`
	Alert struct {
		CooldownSeconds    int `yaml:"cooldown_seconds"`
		LatencyThresholdMS int `yaml:"latency_threshold_ms"`
	} `yaml:"alert"`
}

type FeishuCard struct {
	MsgType string      `json:"msg_type"`
	Card    interface{} `json:"card"`
}

var (
	reqTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_monitor_requests_total",
			Help: "Total HTTP requests made by monitor, labeled by url and status",
		},
		[]string{"url", "status"},
	)
	reqDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_monitor_request_duration_seconds",
			Help:    "HTTP request duration in seconds",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"url"},
	)
)

type AlertPolicy struct {
	Cooldown         time.Duration
	LatencyThreshold time.Duration
}

type MonitorRuntime struct {
	URLs     []string
	Webhook  string
	Interval time.Duration
	Timeout  time.Duration
	Policy   AlertPolicy
}

var (
	lastAlertMu sync.Mutex
	lastAlert   = make(map[string]time.Time)
)

func init() {
	prometheus.MustRegister(reqTotal, reqDuration)
}

func loadConfig() (*Config, error) {
	data, err := os.ReadFile("config.yaml")
	if err != nil {
		// 没有配置文件则返回 nil，由上层走环境变量逻辑
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Monitor.Interval <= 0 {
		cfg.Monitor.Interval = 10
	}
	if cfg.Monitor.TimeoutSecond <= 0 {
		cfg.Monitor.TimeoutSecond = 5
	}
	if cfg.Log.File == "" {
		cfg.Log.File = "monitor.log"
	}
	if cfg.Alert.CooldownSeconds < 0 {
		cfg.Alert.CooldownSeconds = 0
	}
	if cfg.Alert.LatencyThresholdMS < 0 {
		cfg.Alert.LatencyThresholdMS = 0
	}
	return &cfg, nil
}

func setupLogger(logFile string) {
	if logFile == "" {
		logFile = "monitor.log"
	}
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("打开日志文件失败: %v，回退到标准输出\n", err)
		return
	}
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func monitorOnce(urls []string, timeout time.Duration, webhook string, policy AlertPolicy) {
	client := &http.Client{
		Timeout: timeout,
	}

	for _, u := range urls {
		start := time.Now()
		resp, err := client.Get(u)
		latency := time.Since(start)

		status := "OK"
		var detail string

		if err != nil {
			status = "ERROR"
			detail = err.Error()
		} else {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				status = "ERROR"
				detail = fmt.Sprintf("HTTP 状态码: %d", resp.StatusCode)
			} else {
				detail = fmt.Sprintf("HTTP %d, 耗时 %v", resp.StatusCode, latency)
			}
		}

		alertNeeded := false
		alertReason := detail

		if policy.LatencyThreshold > 0 && status == "OK" && latency > policy.LatencyThreshold {
			status = "SLOW"
			alertNeeded = true
			alertReason = fmt.Sprintf("响应耗时 %v 超过阈值 %v", latency, policy.LatencyThreshold)
		}

		reqTotal.WithLabelValues(u, status).Inc()
		reqDuration.WithLabelValues(u).Observe(latency.Seconds())

		if status == "ERROR" {
			alertNeeded = true
		}

		if alertNeeded {
			log.Printf("[ALERT] %s - %s (reason: %s)\n", u, detail, alertReason)
			fmt.Printf("[ALERT] %s - %s (reason: %s)\n", u, detail, alertReason)

			if webhook != "" && canSendAlert(u, policy.Cooldown) {
				if err := sendFeishuCard(webhook, u, status, alertReason, latency); err != nil {
					log.Printf("发送飞书告警失败: %v\n", err)
				} else {
					recordAlert(u)
				}
			}
		} else {
			log.Printf("[OK] %s - %s\n", u, detail)
			fmt.Printf("[OK] %s - %s\n", u, detail)
			resetAlert(u)
		}
	}
}

func canSendAlert(url string, cooldown time.Duration) bool {
	if cooldown <= 0 {
		return true
	}
	lastAlertMu.Lock()
	defer lastAlertMu.Unlock()

	last, ok := lastAlert[url]
	if !ok || last.IsZero() {
		return true
	}
	return time.Since(last) >= cooldown
}

func recordAlert(url string) {
	lastAlertMu.Lock()
	defer lastAlertMu.Unlock()
	lastAlert[url] = time.Now()
}

func resetAlert(url string) {
	lastAlertMu.Lock()
	defer lastAlertMu.Unlock()
	delete(lastAlert, url)
}

func sendFeishuCard(webhook, url, status, detail string, latency time.Duration) error {
	card := map[string]interface{}{
		"config": map[string]interface{}{
			"wide_screen_mode": true,
		},
		"header": map[string]interface{}{
			"title": map[string]string{
				"tag":     "plain_text",
				"content": "HTTP 监控告警",
			},
			"template": "red",
		},
		"elements": []interface{}{
			map[string]interface{}{
				"tag": "div",
				"text": map[string]string{
					"tag": "lark_md",
					"content": fmt.Sprintf(
						"**URL**: %s\n**状态**: %s\n**详情**: %s\n**耗时**: %v",
						url, status, detail, latency,
					),
				},
			},
		},
	}

	body := FeishuCard{
		MsgType: "interactive",
		Card:    card,
	}

	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	resp, err := http.Post(webhook, "application/json", bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu 返回非 200 状态码: %d", resp.StatusCode)
	}

	return nil
}

func startMetricsServer(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("metrics server 关闭失败: %v\n", err)
		}
	}()

	go func() {
		log.Printf("metrics server listen on %s\n", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("metrics server 启动失败: %v\n", err)
		}
	}()
}

func runMonitorLoop(ctx context.Context, runtime MonitorRuntime) {
	ticker := time.NewTicker(runtime.Interval)
	defer ticker.Stop()

	log.Printf("monitor started with %d urls, interval=%s, timeout=%s, cooldown=%s, latency_threshold=%s\n",
		len(runtime.URLs), runtime.Interval, runtime.Timeout, runtime.Policy.Cooldown, runtime.Policy.LatencyThreshold)
	fmt.Printf("开始监控 %d 个 URL，每 %s 检查一次\n", len(runtime.URLs), runtime.Interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("monitor loop exiting")
			return
		default:
			monitorOnce(runtime.URLs, runtime.Timeout, runtime.Webhook, runtime.Policy)
		}

		select {
		case <-ctx.Done():
			log.Println("monitor loop exiting")
			return
		case <-ticker.C:
		}
	}
}

func main() {
	flag.Parse()

	// 优先尝试从 config.yaml 加载
	cfg, err := loadConfig()
	var urls []string
	var webhook string
	var intervalSec int
	var timeout time.Duration
	var policy AlertPolicy

	if err == nil && cfg != nil {
		urls = cfg.Monitor.URLs
		webhook = cfg.Feishu.Webhook
		intervalSec = cfg.Monitor.Interval
		timeout = time.Duration(cfg.Monitor.TimeoutSecond) * time.Second
		setupLogger(cfg.Log.File)
		policy = AlertPolicy{
			Cooldown:         time.Duration(cfg.Alert.CooldownSeconds) * time.Second,
			LatencyThreshold: time.Duration(cfg.Alert.LatencyThresholdMS) * time.Millisecond,
		}
		fmt.Println("已从 config.yaml 加载配置")
	} else {
		// 回退到环境变量
		rawURLs := getEnv("MONITOR_URLS", "")
		webhook = getEnv("FEISHU_WEBHOOK", "")
		intervalStr := getEnv("INTERVAL_SECONDS", "10")
		logFile := getEnv("LOG_FILE", "monitor.log")
		setupLogger(logFile)

		if rawURLs == "" {
			fmt.Println("请通过 config.yaml 或环境变量 MONITOR_URLS 设置要监控的 URL，多个用逗号分隔")
			os.Exit(1)
		}

		fmt.Sscanf(intervalStr, "%d", &intervalSec)
		if intervalSec <= 0 {
			intervalSec = 10
		}

		for _, u := range bytes.Split([]byte(rawURLs), []byte(",")) {
			trimmed := string(bytes.TrimSpace(u))
			if trimmed != "" {
				urls = append(urls, trimmed)
			}
		}
		timeout = 5 * time.Second
		cooldownEnv := getEnv("ALERT_COOLDOWN_SECONDS", "60")
		latencyEnv := getEnv("ALERT_LATENCY_THRESHOLD_MS", "0")
		var cooldownSec int
		var latencyMs int
		fmt.Sscanf(cooldownEnv, "%d", &cooldownSec)
		fmt.Sscanf(latencyEnv, "%d", &latencyMs)
		if cooldownSec < 0 {
			cooldownSec = 0
		}
		if latencyMs < 0 {
			latencyMs = 0
		}
		policy = AlertPolicy{
			Cooldown:         time.Duration(cooldownSec) * time.Second,
			LatencyThreshold: time.Duration(latencyMs) * time.Millisecond,
		}
	}

	if len(urls) == 0 {
		fmt.Println("配置中的 URL 列表为空")
		os.Exit(1)
	}

	if webhook == "" {
		fmt.Println("警告：未配置 FEISHU_WEBHOOK，将不会发送飞书告警，只会在控制台/日志中打印")
	}

	runtime := MonitorRuntime{
		URLs:     urls,
		Webhook:  webhook,
		Interval: time.Duration(intervalSec) * time.Second,
		Timeout:  timeout,
		Policy:   policy,
	}

	run := func(ctx context.Context) {
		startMetricsServer(ctx, ":2112")
		runMonitorLoop(ctx, runtime)
	}

	if handleWindowsService(run) {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sigCh
		log.Printf("收到信号 %s，准备退出\n", s.String())
		cancel()
	}()

	run(ctx)
}
