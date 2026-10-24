package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"monitor/internal/collector"
	"monitor/internal/historydb"
	"monitor/internal/server"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	listen          string
	interval        time.Duration
	token           string
	open            bool
	dbPath          string
	rawRetention    time.Duration
	minuteRetention time.Duration
	hourRetention   time.Duration
}

func main() {
	cfg, showVersion := loadConfig()
	if showVersion {
		fmt.Printf("monitor %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		db      *historydb.DB
		sink    collector.Sink
		history server.HistoryStore
	)
	if strings.EqualFold(cfg.dbPath, "off") {
		log.Print("历史持久化已关闭，仅保留内存数据")
	} else {
		path := cfg.dbPath
		if path == "" {
			path = defaultDBPath()
		}
		opened, err := historydb.Open(path, historydb.Options{
			RawRetention:    cfg.rawRetention,
			MinuteRetention: cfg.minuteRetention,
			HourRetention:   cfg.hourRetention,
		})
		if err != nil {
			log.Printf("历史持久化不可用，回退到内存: %v", err)
		} else {
			db = opened
			sink = db
			history = db
			log.Printf("历史数据库: %s（1s %d 天 / 1m %d 天 / 1h %d 天）",
				path, days(cfg.rawRetention), days(cfg.minuteRetention), days(cfg.hourRetention))
		}
	}

	historySize := min(max(300, int(time.Hour/cfg.interval)), 20000)
	c := collector.New(collector.Options{
		Interval:     cfg.interval,
		TopProcesses: 12,
		HistorySize:  historySize,
		Sink:         sink,
	})
	go c.Start(ctx)

	srv := &http.Server{
		Handler: server.New(c, server.Options{
			Token:   cfg.token,
			Version: version,
			History: history,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ln, err := net.Listen("tcp", cfg.listen)
	if err != nil {
		log.Fatalf("无法监听 %s: %v", cfg.listen, err)
	}
	url := displayURL(ln.Addr())
	log.Printf("monitor %s 已启动，访问 %s", version, url)
	if cfg.open {
		go openBrowser(url)
	}
	log.Printf("按 Ctrl+C 停止服务")

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP 服务异常退出: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("正在关闭服务…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("关闭超时: %v", err)
	}
	if db != nil {
		if err := db.Close(); err != nil {
			log.Printf("关闭历史数据库失败: %v", err)
		}
	}
}

func loadConfig() (config, bool) {
	cfg := config{
		listen:          envString("MONITOR_LISTEN", "127.0.0.1:9090"),
		interval:        envDuration("MONITOR_INTERVAL", time.Second),
		token:           os.Getenv("MONITOR_TOKEN"),
		dbPath:          os.Getenv("MONITOR_DB"),
		rawRetention:    envDuration("MONITOR_RAW_RETENTION", historydb.DefaultRawRetention),
		minuteRetention: envDuration("MONITOR_MINUTE_RETENTION", historydb.DefaultMinuteRetention),
		hourRetention:   envDuration("MONITOR_HOUR_RETENTION", historydb.DefaultHourRetention),
	}
	var showVersion bool

	flag.StringVar(&cfg.listen, "listen", cfg.listen, "监听地址，例如 127.0.0.1:9090 或 0.0.0.0:9090")
	flag.DurationVar(&cfg.interval, "interval", cfg.interval, "采样间隔，例如 1s、2s、500ms")
	flag.StringVar(&cfg.token, "token", cfg.token, "访问令牌（可选），设置后 API 需携带 token")
	flag.StringVar(&cfg.dbPath, "db", cfg.dbPath, "历史数据库路径，设为 off 关闭持久化（默认 ./data/monitor.db）")
	flag.DurationVar(&cfg.rawRetention, "raw-retention", cfg.rawRetention, "1 秒原始数据保留时长")
	flag.DurationVar(&cfg.minuteRetention, "minute-retention", cfg.minuteRetention, "1 分钟聚合数据保留时长")
	flag.DurationVar(&cfg.hourRetention, "hour-retention", cfg.hourRetention, "1 小时聚合数据保留时长")
	flag.BoolVar(&cfg.open, "open", false, "启动后自动打开浏览器")
	flag.BoolVar(&showVersion, "version", false, "打印版本号后退出")
	flag.Parse()

	if cfg.interval < 200*time.Millisecond {
		cfg.interval = 200 * time.Millisecond
	}
	if cfg.interval > time.Minute {
		cfg.interval = time.Minute
	}
	return cfg, showVersion
}

func days(d time.Duration) int {
	if d < 24*time.Hour {
		return 1
	}
	return int(d.Hours() / 24)
}

func defaultDBPath() string {
	return filepath.Join("data", "monitor.db")
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

// displayURL turns a listen address into a URL a human can open.
func displayURL(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "http://" + addr.String()
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("无法自动打开浏览器，请手动访问 %s: %v", url, err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
