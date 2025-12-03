package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
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

func monitorOnce(urls []string, timeout time.Duration, webhook string) {
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

		reqTotal.WithLabelValues(u, status).Inc()
		reqDuration.WithLabelValues(u).Observe(latency.Seconds())

		if status == "ERROR" {
			log.Printf("[ERROR] %s - %s\n", u, detail)
			fmt.Printf("[ERROR] %s - %s\n", u, detail)
			if webhook != "" {
				if err := sendFeishuCard(webhook, u, status, detail, latency); err != nil {
					log.Printf("发送飞书告警失败: %v\n", err)
				}
			}
		} else {
			log.Printf("[OK] %s - %s\n", u, detail)
			fmt.Printf("[OK] %s - %s\n", u, detail)
		}
	}
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

func main() {
	// 优先尝试从 config.yaml 加载
	cfg, err := loadConfig()
	var urls []string
	var webhook string
	var intervalSec int
	var timeout time.Duration

	if err == nil && cfg != nil {
		urls = cfg.Monitor.URLs
		webhook = cfg.Feishu.Webhook
		intervalSec = cfg.Monitor.Interval
		timeout = time.Duration(cfg.Monitor.TimeoutSecond) * time.Second
		setupLogger(cfg.Log.File)
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
	}

	if len(urls) == 0 {
		fmt.Println("配置中的 URL 列表为空")
		os.Exit(1)
	}

	if webhook == "" {
		fmt.Println("警告：未配置 FEISHU_WEBHOOK，将不会发送飞书告警，只会在控制台/日志中打印")
	}

	// 启动 Prometheus /metrics 端点
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		addr := ":2112"
		fmt.Printf("Prometheus metrics 暴露在 %s/metrics\n", addr)
		log.Printf("metrics server listen on %s\n", addr)
		if err := http.ListenAndServe(addr, nil); err != nil {
			log.Printf("metrics server 启动失败: %v\n", err)
		}
	}()

	ticker := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer ticker.Stop()

	fmt.Printf("开始监控 %d 个 URL，每 %d 秒检查一次\n", len(urls), intervalSec)
	log.Printf("monitor started with %d urls, interval=%ds, timeout=%s\n", len(urls), intervalSec, timeout)

	for {
		monitorOnce(urls, timeout, webhook)
		<-ticker.C
	}
}
