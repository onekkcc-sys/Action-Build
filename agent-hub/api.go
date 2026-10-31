package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type App struct {
	store    *Store
	dataPath string
	port     int

	mu       sync.Mutex
	running  map[string]*exec.Cmd
	scanErrs map[string]string
	lastPing time.Time
}

func defaultLaunch(provider string) string {
	switch provider {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	}
	return ""
}

func accountEnv(a *Account) []string {
	if a.ConfigDir == "" {
		return nil
	}
	switch a.Provider {
	case "claude":
		return []string{"CLAUDE_CONFIG_DIR=" + a.ConfigDir}
	case "codex":
		return []string{"CODEX_HOME=" + a.ConfigDir}
	}
	return []string{"AGENT_CONFIG_DIR=" + a.ConfigDir}
}

func workDir(a *Account) string {
	if a.WorkDir != "" {
		if st, err := os.Stat(a.WorkDir); err == nil && st.IsDir() {
			return a.WorkDir
		}
	}
	home, _ := os.UserHomeDir()
	return home
}

var errNotFound = errors.New("not found")

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func readBody(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

// guard rejects requests that do not come from our own page: wrong Host
// (DNS rebinding) or a foreign Origin (cross-site requests from websites).
func (app *App) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, port, err := net.SplitHostPort(r.Host)
		if err != nil || (host != "127.0.0.1" && host != "localhost") || port != itoa(app.port) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/report" {
			next.ServeHTTP(w, r) // token-authenticated, used by scripts
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != "http://"+r.Host {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/") && r.Header.Get("X-Agent-Hub") != "1" {
			http.Error(w, "missing header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func (app *App) routes(static http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/", static)

	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) {
		app.mu.Lock()
		app.lastPing = time.Now()
		app.mu.Unlock()
		writeJSON(w, 200, map[string]any{"app": "agent-hub", "version": version})
	})
	mux.HandleFunc("GET /api/state", app.state)

	mux.HandleFunc("POST /api/accounts", func(w http.ResponseWriter, r *http.Request) {
		var in Account
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		in.ID = newID()
		in.CreatedAt = nowMs()
		in.Messages = []Message{}
		in.Usage = nil
		if in.Status == "" {
			in.Status = "unlinked"
		}
		app.mutate(w, func(d *Data) error {
			if err := syncAutoRead(d, nil, &in); err != nil {
				return err
			}
			in.Order = len(d.Accounts)
			d.Accounts = append(d.Accounts, &in)
			return nil
		})
	})
	mux.HandleFunc("PUT /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in Account
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		app.withAccount(w, r, func(d *Data, a *Account) error {
			before := *a
			next := *a
			next.Name, next.Agent, next.Provider, next.Plan, next.Color = in.Name, in.Agent, in.Provider, in.Plan, in.Color
			next.Status, next.Note, next.ConfigDir, next.LaunchCmd, next.WorkDir, next.AutoScan = in.Status, in.Note, in.ConfigDir, in.LaunchCmd, in.WorkDir, in.AutoScan
			if err := syncAutoRead(d, &before, &next); err != nil {
				return err
			}
			*a = next
			return nil
		})
	})
	mux.HandleFunc("DELETE /api/accounts/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		app.mutate(w, func(d *Data) error {
			for i, a := range d.Accounts {
				if a.ID == id {
					if err := syncAutoRead(d, a, nil); err != nil {
						return err
					}
					d.Accounts = append(d.Accounts[:i], d.Accounts[i+1:]...)
					return nil
				}
			}
			return errNotFound
		})
	})
	mux.HandleFunc("POST /api/accounts/{id}/autoread", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enable bool `json:"enable"`
		}
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		app.withAccount(w, r, func(d *Data, a *Account) error {
			before := *a
			next := *a
			next.AutoScan = in.Enable
			if err := syncAutoRead(d, &before, &next); err != nil {
				return err
			}
			*a = next
			return nil
		})
		if in.Enable {
			go scanAll(app.store)
		}
	})
	mux.HandleFunc("POST /api/accounts/{id}/move", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Dir int `json:"dir"`
		}
		_ = readBody(r, &in)
		id := r.PathValue("id")
		app.mutate(w, func(d *Data) error {
			for i, a := range d.Accounts {
				if a.ID == id {
					j := i + in.Dir
					if j >= 0 && j < len(d.Accounts) {
						d.Accounts[i], d.Accounts[j] = d.Accounts[j], d.Accounts[i]
						for k, acc := range d.Accounts {
							acc.Order = k
						}
					}
					return nil
				}
			}
			return errNotFound
		})
	})
	mux.HandleFunc("POST /api/accounts/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		var in Usage
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		if in.ReadAt == 0 {
			in.ReadAt = nowMs()
		}
		in.Source = "manual"
		in.Detail = ""
		app.withAccount(w, r, func(d *Data, a *Account) error {
			u := in
			a.Usage = &u
			return nil
		})
	})
	mux.HandleFunc("DELETE /api/accounts/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		app.withAccount(w, r, func(d *Data, a *Account) error {
			a.Usage = nil
			return nil
		})
	})
	mux.HandleFunc("POST /api/accounts/{id}/messages", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Text string `json:"text"`
		}
		if err := readBody(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
			fail(w, 400, "内容不能为空")
			return
		}
		app.withAccount(w, r, func(d *Data, a *Account) error {
			a.Messages = append(a.Messages, Message{ID: newID(), Text: strings.TrimSpace(in.Text), At: nowMs()})
			return nil
		})
	})
	mux.HandleFunc("DELETE /api/accounts/{id}/messages/{mid}", func(w http.ResponseWriter, r *http.Request) {
		mid := r.PathValue("mid")
		app.withAccount(w, r, func(d *Data, a *Account) error {
			for i, m := range a.Messages {
				if m.ID == mid {
					a.Messages = append(a.Messages[:i], a.Messages[i+1:]...)
					return nil
				}
			}
			return errNotFound
		})
	})
	mux.HandleFunc("POST /api/accounts/{id}/launch", app.launch)
	mux.HandleFunc("POST /api/accounts/{id}/opendir", func(w http.ResponseWriter, r *http.Request) {
		snap := app.store.Snapshot()
		a := snap.account(r.PathValue("id"))
		if a == nil {
			fail(w, 404, "账号不存在")
			return
		}
		dir := a.ConfigDir
		if dir == "" && a.Provider == "codex" {
			dir = defaultCodexHome()
		}
		if dir == "" {
			fail(w, 400, "这个账号没有设置配置目录")
			return
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fail(w, 500, err.Error())
			return
		}
		if err := openPath(dir); err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	})

	mux.HandleFunc("POST /api/decisions", func(w http.ResponseWriter, r *http.Request) {
		var in Decision
		if err := readBody(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
			fail(w, 400, "内容不能为空")
			return
		}
		in.ID, in.CreatedAt, in.Done, in.Text = newID(), nowMs(), false, strings.TrimSpace(in.Text)
		app.mutate(w, func(d *Data) error {
			d.Decisions = append(d.Decisions, &in)
			return nil
		})
	})
	mux.HandleFunc("PUT /api/decisions/{id}", func(w http.ResponseWriter, r *http.Request) {
		var in Decision
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		id := r.PathValue("id")
		app.mutate(w, func(d *Data) error {
			for _, x := range d.Decisions {
				if x.ID == id {
					if t := strings.TrimSpace(in.Text); t != "" {
						x.Text = t
					}
					x.Account = in.Account
					if in.Done && !x.Done {
						x.DoneAt = nowMs()
					}
					x.Done = in.Done
					return nil
				}
			}
			return errNotFound
		})
	})
	mux.HandleFunc("DELETE /api/decisions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		app.mutate(w, func(d *Data) error {
			for i, x := range d.Decisions {
				if x.ID == id {
					d.Decisions = append(d.Decisions[:i], d.Decisions[i+1:]...)
					return nil
				}
			}
			return errNotFound
		})
	})

	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Settings
			RegenToken bool `json:"regenToken"`
		}
		if err := readBody(r, &in); err != nil {
			fail(w, 400, err.Error())
			return
		}
		app.mutate(w, func(d *Data) error {
			if in.StaleHours > 0 {
				d.Settings.StaleHours = in.StaleHours
			}
			if in.LowPercent > 0 {
				d.Settings.LowPercent = in.LowPercent
			}
			if in.ScanSeconds >= 10 {
				d.Settings.ScanSeconds = in.ScanSeconds
			}
			if in.RegenToken {
				d.Settings.Token = newToken()
			}
			return nil
		})
	})
	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		n, errs := scanAll(app.store)
		app.mu.Lock()
		app.scanErrs = errs
		app.mu.Unlock()
		app.stateWith(w, map[string]any{"updated": n})
	})
	mux.HandleFunc("POST /api/report", app.report)
	mux.HandleFunc("GET /api/export", func(w http.ResponseWriter, r *http.Request) {
		snap := app.store.Snapshot()
		w.Header().Set("Content-Disposition", `attachment; filename="agent-hub-backup-`+time.Now().Format("20060102-1504")+`.json"`)
		writeJSON(w, 200, snap)
	})
	mux.HandleFunc("POST /api/import", func(w http.ResponseWriter, r *http.Request) {
		var in Data
		if err := readBody(r, &in); err != nil {
			fail(w, 400, "不是有效的备份文件: "+err.Error())
			return
		}
		if in.Accounts == nil {
			fail(w, 400, "备份文件里没有账号数据")
			return
		}
		app.mutate(w, func(d *Data) error {
			token := d.Settings.Token
			*d = in
			if d.Settings.Token == "" {
				d.Settings.Token = token
			}
			app.store.normalize()
			return nil
		})
	})
	return app.guard(mux)
}

func (app *App) state(w http.ResponseWriter, r *http.Request) { app.stateWith(w, nil) }

func (app *App) stateWith(w http.ResponseWriter, extra map[string]any) {
	snap := app.store.Snapshot()
	app.mu.Lock()
	running := map[string]bool{}
	for id := range app.running {
		running[id] = true
	}
	scanErrs := app.scanErrs
	app.mu.Unlock()
	statusLines := map[string]string{}
	for _, a := range snap.Accounts {
		if a.Provider == "claude" {
			statusLines[a.ID] = statusLineState(claudeDir(a))
		}
	}
	out := map[string]any{
		"statusLines": statusLines,
		"claudeHome":  defaultClaudeDir(),
		"accounts":    snap.Accounts,
		"decisions":   snap.Decisions,
		"settings":    snap.Settings,
		"running":     running,
		"scanErrs":    scanErrs,
		"dataPath":    app.dataPath,
		"port":        app.port,
		"now":         nowMs(),
		"version":     version,
		"codexHome":   defaultCodexHome(),
	}
	for k, v := range extra {
		out[k] = v
	}
	writeJSON(w, 200, out)
}

func (app *App) mutate(w http.ResponseWriter, fn func(d *Data) error) {
	if err := app.store.Update(fn); err != nil {
		if errors.Is(err, errNotFound) {
			fail(w, 404, "没有找到")
			return
		}
		fail(w, 500, err.Error())
		return
	}
	app.state(w, nil)
}

func (app *App) withAccount(w http.ResponseWriter, r *http.Request, fn func(d *Data, a *Account) error) {
	id := r.PathValue("id")
	app.mutate(w, func(d *Data) error {
		a := d.account(id)
		if a == nil {
			return errNotFound
		}
		return fn(d, a)
	})
}

func (app *App) launch(w http.ResponseWriter, r *http.Request) {
	snap := app.store.Snapshot()
	a := snap.account(r.PathValue("id"))
	if a == nil {
		fail(w, 404, "账号不存在")
		return
	}
	if a.LaunchCmd == "" && defaultLaunch(a.Provider) == "" {
		fail(w, 400, "请先在账号设置里填写启动命令")
		return
	}
	if a.ConfigDir != "" {
		_ = os.MkdirAll(a.ConfigDir, 0o755)
	}
	cmd, err := launchCommand(a)
	if err != nil {
		fail(w, 500, "启动失败: "+err.Error())
		return
	}
	app.mu.Lock()
	app.running[a.ID] = cmd
	app.mu.Unlock()
	go func(id string) {
		_ = cmd.Wait()
		app.mu.Lock()
		if app.running[id] == cmd {
			delete(app.running, id)
		}
		app.mu.Unlock()
	}(a.ID)
	app.state(w, nil)
}

// report lets scripts push a usage reading:
//
//	POST /api/report  (header X-Token: <token>)
//	{"account": "Claude 1", "five_hour": {"left": 77, "reset": "..."}, "weekly": {...}}
func (app *App) report(w http.ResponseWriter, r *http.Request) {
	snap := app.store.Snapshot()
	tok := r.Header.Get("X-Token")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	if tok == "" || tok != snap.Settings.Token {
		fail(w, 401, "token 不正确（在设置里查看）")
		return
	}
	var in map[string]any
	if err := readBody(r, &in); err != nil {
		fail(w, 400, "JSON 格式不正确: "+err.Error())
		return
	}
	key, _ := in["account"].(string)
	if key == "" {
		key = r.URL.Query().Get("account")
	}
	var target *Account
	for _, a := range snap.Accounts {
		if a.ID == key || strings.EqualFold(a.Name, key) || (a.Agent != "" && strings.EqualFold(a.Agent, key)) {
			target = a
			break
		}
	}
	if target == nil {
		fail(w, 404, "找不到账号: "+key)
		return
	}
	readAt := nowMs()
	if v, ok := in["read_at"]; ok {
		if t := parseTimeValue(v); t > 0 {
			readAt = t
		}
	}
	u, ok := parseUsage(in, readAt)
	if !ok {
		fail(w, 400, "没有识别到额度数据（five_hour / weekly）")
		return
	}
	u.Source = "report"
	if s, ok := in["source"].(string); ok {
		u.Detail = s
	}
	err := app.store.Update(func(d *Data) error {
		a := d.account(target.ID)
		if a == nil {
			return errNotFound
		}
		a.Usage = mergeUsage(a.Usage, u)
		if p, ok := in["plan"].(string); ok && p != "" {
			a.Plan = p
		}
		return nil
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "account": target.Name, "usage": u})
}

// syncAutoRead installs or removes the Claude Code status line hook when a
// Claude account's auto-read setting or config dir changes. before/after may
// be nil (create/delete).
func syncAutoRead(d *Data, before, after *Account) error {
	on := func(a *Account) bool { return a != nil && a.Provider == "claude" && a.AutoScan }
	if on(before) && (!on(after) || claudeDir(before) != claudeDir(after)) {
		dir := claudeDir(before)
		shared := false
		for _, o := range d.Accounts {
			if o.ID != before.ID && on(o) && claudeDir(o) == dir {
				shared = true
			}
		}
		if !shared {
			if err := uninstallStatusLine(dir); err != nil {
				return fmt.Errorf("关闭自动读数失败: %v", err)
			}
		}
	}
	if on(after) {
		if err := installStatusLine(claudeDir(after)); err != nil {
			return fmt.Errorf("开启自动读数失败: %v", err)
		}
	}
	return nil
}
