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
	"runtime"
	"syscall"
	"time"

	"monitor/internal/collector"
	"monitor/internal/server"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	listen   string
	interval time.Duration
	token    string
	open     bool
}

func main() {
	cfg, showVersion := loadConfig()
	if showVersion {
		fmt.Printf("monitor %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	historySize := min(max(300, int(time.Hour/cfg.interval)), 20000)
	c := collector.New(collector.Options{
		Interval:     cfg.interval,
		TopProcesses: 12,
		HistorySize:  historySize,
	})
	go c.Start(ctx)

	srv := &http.Server{
		Handler:           server.New(c, server.Options{Token: cfg.token, Version: version}),
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
}

func loadConfig() (config, bool) {
	cfg := config{
		listen:   envString("MONITOR_LISTEN", "127.0.0.1:9090"),
		interval: envDuration("MONITOR_INTERVAL", time.Second),
		token:    os.Getenv("MONITOR_TOKEN"),
	}
	var showVersion bool

	flag.StringVar(&cfg.listen, "listen", cfg.listen, "监听地址，例如 127.0.0.1:9090 或 0.0.0.0:9090")
	flag.DurationVar(&cfg.interval, "interval", cfg.interval, "采样间隔，例如 1s、2s、500ms")
	flag.StringVar(&cfg.token, "token", cfg.token, "访问令牌（可选），设置后 API 需携带 token")
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
