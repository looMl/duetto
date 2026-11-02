// Command duetto hosts a synchronized watch-together session for a local
// video file.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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

	"duetto/internal/app"
	"duetto/internal/media"
	"duetto/internal/power"
	"duetto/internal/procgroup"
	"duetto/internal/room"
	"duetto/internal/server"
	"duetto/internal/tunnel"
	"duetto/web"
)

var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "duetto:", err)
		if runtime.GOOS == "windows" {
			fmt.Fprintln(os.Stderr, "\nPress Enter to close.")
			_, _ = fmt.Scanln()
		}
		os.Exit(1)
	}
}

func run() error {
	exeDir := executableDir()
	cacheRoot, _ := os.UserCacheDir()

	var (
		adminPort  = flag.Int("admin-port", 7790, "host UI port (loopback only)")
		publicPort = flag.Int("public-port", 7791, "guest port (loopback only; exposed by the tunnel)")
		file       = flag.String("file", "", "video to open at startup")
		library    = flag.String("library", "", "semicolon-separated folders to list videos from (default: Videos, Downloads)")
		name       = flag.String("name", "", "your display name (default: computer user name)")
		audioLang  = flag.String("audio", "", "preferred audio language, ISO 639-2 (e.g. eng, ita)")
		cacheDir   = flag.String("cache", filepath.Join(cacheRoot, "duetto"), "folder for prepared videos")
		noTunnel   = flag.Bool("no-tunnel", false, "do not open the internet tunnel (LAN/testing)")
		noBrowser  = flag.Bool("no-browser", false, "do not open the host page in the browser")
		verbose    = flag.Bool("v", false, "verbose logging")
	)
	flag.Parse()

	if err := os.MkdirAll(*cacheDir, 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(*cacheDir, "duetto.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stdout, logFile), &slog.HandlerOptions{Level: level}))
	log.Info("duetto starting", "version", version)
	if err := procgroup.KillChildrenOnExit(); err != nil {
		log.Warn("child processes may outlive duetto", "err", err)
	}
	if n := media.CleanPartials(*cacheDir); n > 0 {
		log.Info("removed unfinished files from an interrupted run", "count", n)
	}

	toolDirs := []string{exeDir, filepath.Join(exeDir, "tools")}
	ffmpeg, err := findTool("ffmpeg", toolDirs)
	if err != nil {
		return err
	}
	ffprobe, err := findTool("ffprobe", toolDirs)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	enc := media.DetectEncoder(ctx, ffmpeg)
	log.Info("tools", "ffmpeg", ffmpeg, "encoder", enc)

	rm := room.New(nil)
	srv := server.New(log, rm, web.FS())
	srv.HostName = displayName(*name)

	libs := app.DefaultLibrary()
	if *library != "" {
		libs = strings.Split(*library, ";")
	}
	a := &app.App{
		Log:    log,
		Server: srv,
		Preparer: &media.Preparer{
			Tools:    media.Tools{FFmpeg: ffmpeg, FFprobe: ffprobe},
			CacheDir: *cacheDir,
			Options:  media.PlanOptions{AudioLang: *audioLang, Encoder: enc},
		},
		Tunnel:   &tunnel.Tunnel{Target: fmt.Sprintf("http://127.0.0.1:%d", *publicPort)},
		Library:  libs,
		ToolsDir: filepath.Join(exeDir, "tools"),
	}
	if b, ok := tunnel.Locate(toolDirs...); ok {
		a.Tunnel.Bin = b
	}
	srv.Hooks = a
	defer a.Close()

	adminLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *adminPort))
	if err != nil {
		return fmt.Errorf("port %d is busy (is Duetto already running?): %w", *adminPort, err)
	}
	publicLn, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *publicPort))
	if err != nil {
		return fmt.Errorf("port %d is busy: %w", *publicPort, err)
	}
	servers := []*http.Server{
		newHTTPServer(srv.Admin(*adminPort)),
		newHTTPServer(srv.Public()),
	}
	errc := make(chan error, 2)
	go func() { errc <- servers[0].Serve(adminLn) }()
	go func() { errc <- servers[1].Serve(publicLn) }()

	release := power.KeepAwake()
	defer release()

	if *file != "" {
		a.Open(*file, false)
	}
	if !*noTunnel {
		go a.StartTunnel()
	}

	hostURL := fmt.Sprintf("http://127.0.0.1:%d/", *adminPort)
	fmt.Printf("\n  Duetto is running. Host page: %s\n  Keep this window open while watching. Press Ctrl+C to stop.\n\n", hostURL)
	if !*noBrowser {
		openBrowser(hostURL)
	}
	if *noTunnel {
		fmt.Printf("  Guest link (local): http://127.0.0.1:%d/#k=%s\n\n", *publicPort, srv.Secret())
	}

	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shutdownCtx)
	}
	return nil
}

func newHTTPServer(h http.Handler) *http.Server {
	// No WriteTimeout: a movie response legitimately streams for hours.
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

func findTool(name string, dirs []string) (string, error) {
	exe := name
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	for _, d := range dirs {
		p := filepath.Join(d, exe)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if p, err := exec.LookPath(exe); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found: put %s in the 'tools' folder next to duetto, or on PATH", name, exe)
}

func executableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return filepath.Dir(exe)
}

func displayName(flagName string) string {
	if n := strings.TrimSpace(flagName); n != "" {
		return n
	}
	for _, k := range []string{"USERNAME", "USER"} {
		if v := os.Getenv(k); v != "" && !strings.EqualFold(v, "admin") && !strings.EqualFold(v, "root") {
			return v
		}
	}
	return "Host"
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
