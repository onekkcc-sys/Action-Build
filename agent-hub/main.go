package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const version = "1.0.0"
const defaultPort = 17321

//go:embed web
var webFS embed.FS

func dataDir() string {
	// portable mode: keep data next to the exe if a data.json is already there
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if _, err := os.Stat(filepath.Join(dir, "data.json")); err == nil {
			return dir
		}
	}
	if d, err := os.UserConfigDir(); err == nil {
		return filepath.Join(d, "AgentHub")
	}
	return "."
}

func main() {
	port := flag.Int("port", defaultPort, "本地端口")
	noWindow := flag.Bool("no-window", false, "只启动服务，不打开窗口")
	dirFlag := flag.String("data", "", "数据目录")
	flag.Parse()

	dir := *dirFlag
	if dir == "" {
		dir = dataDir()
	}
	_ = os.MkdirAll(dir, 0o755)
	if lf, err := os.OpenFile(filepath.Join(dir, "agent-hub.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		log.SetOutput(lf)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", *port))
	if err != nil {
		// already running? then just open another window onto it
		if alreadyRunning(*port) {
			if !*noWindow {
				openWindow(fmt.Sprintf("http://127.0.0.1:%d/", *port), dir)
			}
			return
		}
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port

	dataPath := filepath.Join(dir, "data.json")
	st, err := openStore(dataPath)
	if err != nil {
		log.Fatal(err)
	}
	app := &App{store: st, dataPath: dataPath, port: actualPort, running: map[string]*exec.Cmd{}}

	sub, _ := fs.Sub(webFS, "web")
	static := http.FileServer(http.FS(sub))
	srv := &http.Server{Handler: app.routes(noCache(static)), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	go app.scanLoop()

	url := fmt.Sprintf("http://127.0.0.1:%d/", actualPort)
	log.Printf("Agent Hub %s listening on %s (data %s)", version, url, dataPath)
	if *noWindow {
		fmt.Println("Agent Hub 运行于", url)
		select {}
	}

	done := make(chan struct{})
	if waitFn := openWindow(url, dir); waitFn != nil {
		go func() {
			start := time.Now()
			waitFn()
			if time.Since(start) > 5*time.Second {
				close(done)
				return
			}
			// the browser handed the window to an existing process; fall back
			// to heartbeat tracking
			app.heartbeatWatch(done)
		}()
	} else {
		go app.heartbeatWatch(done)
	}
	<-done
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func noCache(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		h.ServeHTTP(w, r)
	})
}

func alreadyRunning(port int) bool {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/api/ping", port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	buf := make([]byte, 256)
	n, _ := resp.Body.Read(buf)
	return strings.Contains(string(buf[:n]), "agent-hub")
}

// openWindow shows the UI in a chromeless Edge/Chrome app window when possible
// and returns a function that blocks until that window's browser exits.
// Returns nil if it fell back to the default browser.
func openWindow(url, dir string) func() {
	for _, b := range appBrowsers() {
		if _, err := os.Stat(b); err != nil {
			continue
		}
		cmd := exec.Command(b,
			"--app="+url,
			"--user-data-dir="+filepath.Join(dir, "webview"),
			"--window-size=1280,860",
			"--no-first-run",
			"--no-default-browser-check",
			"--disable-background-mode",
			"--disable-features=Translate",
		)
		if err := cmd.Start(); err != nil {
			log.Printf("start %s: %v", b, err)
			continue
		}
		return func() { _ = cmd.Wait() }
	}
	if err := openURL(url); err != nil {
		log.Printf("open browser: %v", err)
	}
	return nil
}

// heartbeatWatch exits once the page stops pinging (window/tab closed).
func (app *App) heartbeatWatch(done chan struct{}) {
	start := time.Now()
	for range time.Tick(5 * time.Second) {
		app.mu.Lock()
		last := app.lastPing
		app.mu.Unlock()
		if last.IsZero() {
			if time.Since(start) > 5*time.Minute {
				close(done)
				return
			}
			continue
		}
		// background tabs may be throttled to one timer per minute
		if time.Since(last) > 3*time.Minute {
			close(done)
			return
		}
	}
}

func (app *App) scanLoop() {
	for {
		n, errs := scanAll(app.store)
		if n > 0 {
			log.Printf("codex scan updated %d account(s)", n)
		}
		app.mu.Lock()
		app.scanErrs = errs
		app.mu.Unlock()
		secs := app.store.Snapshot().Settings.ScanSeconds
		if secs < 10 {
			secs = 60
		}
		time.Sleep(time.Duration(secs) * time.Second)
	}
}
