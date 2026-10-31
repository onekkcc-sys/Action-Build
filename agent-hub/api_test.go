package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPort = 17321

func newTestApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	st, err := openStore(filepath.Join(t.TempDir(), "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{store: st, dataPath: st.path, port: testPort, running: map[string]*exec.Cmd{}}
	return app, app.routes(http.NotFoundHandler())
}

type resp struct {
	code int
	body map[string]any
	raw  string
}

func do(t *testing.T, h http.Handler, method, path, body string, hdr map[string]string) resp {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = fmt.Sprintf("127.0.0.1:%d", testPort)
	req.Header.Set("Content-Type", "application/json")
	if method != http.MethodGet {
		req.Header.Set("X-Agent-Hub", "1")
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	r := resp{code: rec.Code, raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &r.body)
	return r
}

func accounts(r resp) []map[string]any {
	var out []map[string]any
	for _, a := range r.body["accounts"].([]any) {
		out = append(out, a.(map[string]any))
	}
	return out
}

func byName(r resp, name string) map[string]any {
	for _, a := range accounts(r) {
		if a["name"] == name {
			return a
		}
	}
	return nil
}

func TestGuard(t *testing.T) {
	_, h := newTestApp(t)
	cases := []struct {
		name   string
		method string
		hdr    map[string]string
		host   string
		want   int
	}{
		{"own page GET", "GET", nil, "", 200},
		{"foreign host (DNS rebinding)", "GET", nil, "evil.com:17321", 403},
		{"wrong port", "GET", nil, "127.0.0.1:9999", 403},
		{"cross-site origin", "POST", map[string]string{"Origin": "http://evil.com"}, "", 403},
		{"missing custom header", "POST", map[string]string{"X-Agent-Hub": ""}, "", 403},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "/api/state", nil)
		path := "/api/state"
		if c.method == "POST" {
			path = "/api/accounts"
			req = httptest.NewRequest(c.method, path, strings.NewReader(`{"name":"x"}`))
			req.Header.Set("X-Agent-Hub", "1")
		}
		req.Host = fmt.Sprintf("127.0.0.1:%d", testPort)
		if c.host != "" {
			req.Host = c.host
		}
		for k, v := range c.hdr {
			if v == "" {
				req.Header.Del(k)
			} else {
				req.Header.Set(k, v)
			}
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: got %d want %d (%s)", c.name, rec.Code, c.want, rec.Body.String())
		}
	}
}

func TestAccountLifecycle(t *testing.T) {
	_, h := newTestApp(t)
	r := do(t, h, "GET", "/api/state", "", nil)
	if n := len(accounts(r)); n != 2 {
		t.Fatalf("seed accounts = %d, want 2", n)
	}

	r = do(t, h, "POST", "/api/accounts", `{"name":"Claude 9","agent":"阿测","provider":"claude","plan":"Max 20x","color":"#123456"}`, nil)
	if r.code != 200 {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	a := byName(r, "Claude 9")
	if a == nil || a["status"] != "unlinked" {
		t.Fatalf("created account wrong: %v", a)
	}
	id := a["id"].(string)

	r = do(t, h, "PUT", "/api/accounts/"+id, `{"name":"Claude 9","agent":"阿测","provider":"claude","plan":"Pro","color":"#123456","status":"online","configDir":"C:\\x"}`, nil)
	if a = byName(r, "Claude 9"); a["plan"] != "Pro" || a["status"] != "online" || a["configDir"] != `C:\x` {
		t.Fatalf("update failed: %v", a)
	}

	// manual usage
	reset := time.Now().Add(2 * time.Hour).UnixMilli()
	r = do(t, h, "POST", "/api/accounts/"+id+"/usage", fmt.Sprintf(`{"fiveHour":{"left":42,"reset":%d},"weekly":{"left":80,"reset":%d,"approx":true}}`, reset, reset+86400000), nil)
	u := byName(r, "Claude 9")["usage"].(map[string]any)
	if u["source"] != "manual" || u["fiveHour"].(map[string]any)["left"].(float64) != 42 || u["readAt"].(float64) == 0 {
		t.Fatalf("usage wrong: %v", u)
	}

	// move to top
	for i := 0; i < 3; i++ {
		do(t, h, "POST", "/api/accounts/"+id+"/move", `{"dir":-1}`, nil)
	}
	r = do(t, h, "GET", "/api/state", "", nil)
	if accounts(r)[0]["id"] != id {
		t.Fatalf("move: first account is %v", accounts(r)[0]["name"])
	}

	// messages
	r = do(t, h, "POST", "/api/accounts/"+id+"/messages", `{"text":"  你好  "}`, nil)
	msgs := byName(r, "Claude 9")["messages"].([]any)
	if len(msgs) != 1 || msgs[0].(map[string]any)["text"] != "你好" {
		t.Fatalf("message: %v", msgs)
	}
	if r := do(t, h, "POST", "/api/accounts/"+id+"/messages", `{"text":"  "}`, nil); r.code != 400 {
		t.Fatalf("empty message accepted: %d", r.code)
	}
	mid := msgs[0].(map[string]any)["id"].(string)
	r = do(t, h, "DELETE", "/api/accounts/"+id+"/messages/"+mid, "", nil)
	if len(byName(r, "Claude 9")["messages"].([]any)) != 0 {
		t.Fatal("message not deleted")
	}

	// clear usage, delete
	r = do(t, h, "DELETE", "/api/accounts/"+id+"/usage", "", nil)
	if byName(r, "Claude 9")["usage"] != nil {
		t.Fatal("usage not cleared")
	}
	r = do(t, h, "DELETE", "/api/accounts/"+id, "", nil)
	if byName(r, "Claude 9") != nil {
		t.Fatal("account not deleted")
	}
	if r := do(t, h, "DELETE", "/api/accounts/"+id, "", nil); r.code != 404 {
		t.Fatalf("delete missing = %d", r.code)
	}
}

func TestDecisions(t *testing.T) {
	_, h := newTestApp(t)
	r := do(t, h, "POST", "/api/decisions", `{"text":"要不要换号"}`, nil)
	ds := r.body["decisions"].([]any)
	if len(ds) != 1 {
		t.Fatalf("decisions: %v", ds)
	}
	id := ds[0].(map[string]any)["id"].(string)
	r = do(t, h, "PUT", "/api/decisions/"+id, `{"text":"要不要换号","done":true}`, nil)
	d := r.body["decisions"].([]any)[0].(map[string]any)
	if d["done"] != true || d["doneAt"].(float64) == 0 {
		t.Fatalf("toggle: %v", d)
	}
	r = do(t, h, "DELETE", "/api/decisions/"+id, "", nil)
	if len(r.body["decisions"].([]any)) != 0 {
		t.Fatal("not deleted")
	}
}

func TestReportAPI(t *testing.T) {
	app, h := newTestApp(t)
	tok := app.store.Snapshot().Settings.Token

	// no token / bad token
	if r := do(t, h, "POST", "/api/report", `{"account":"Claude 1","five_hour":{"left":1}}`, nil); r.code != 401 {
		t.Fatalf("no token = %d", r.code)
	}
	if r := do(t, h, "POST", "/api/report", `{"account":"Claude 1","five_hour":{"left":1}}`, map[string]string{"X-Token": "nope"}); r.code != 401 {
		t.Fatalf("bad token = %d", r.code)
	}
	// unknown account
	if r := do(t, h, "POST", "/api/report", `{"account":"Nobody","five_hour":{"left":1}}`, map[string]string{"X-Token": tok}); r.code != 404 {
		t.Fatalf("unknown account = %d", r.code)
	}
	// no usage data
	if r := do(t, h, "POST", "/api/report", `{"account":"Claude 1"}`, map[string]string{"X-Token": tok}); r.code != 400 {
		t.Fatalf("empty report = %d", r.code)
	}
	// scripts do not send Origin/X-Agent-Hub; token alone is enough
	r := do(t, h, "POST", "/api/report?token="+tok, `{"account":"claude 1","five_hour":{"utilization":25,"resets_in_seconds":600},"seven_day":{"utilization":70},"source":"hook","plan":"Max 5x"}`, map[string]string{"X-Agent-Hub": ""})
	if r.code != 200 {
		t.Fatalf("report: %d %s", r.code, r.raw)
	}
	// partial report keeps the other window
	do(t, h, "POST", "/api/report", `{"account":"Claude 1","weekly":{"left":50}}`, map[string]string{"X-Token": tok})
	a := byName(do(t, h, "GET", "/api/state", "", nil), "Claude 1")
	u := a["usage"].(map[string]any)
	if u["fiveHour"].(map[string]any)["left"].(float64) != 75 || u["weekly"].(map[string]any)["left"].(float64) != 50 {
		t.Fatalf("merged usage wrong: %v", u)
	}
	if u["source"] != "report" || a["plan"] != "Max 5x" {
		t.Fatalf("source/plan wrong: %v %v", u["source"], a["plan"])
	}
}

func TestCodexScan(t *testing.T) {
	app, h := newTestApp(t)
	home := t.TempDir()
	day := filepath.Join(home, "sessions", "2026", "10", "31")
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().UTC().Add(-time.Minute)
	resetAt := ts.Add(5 * 24 * time.Hour).Unix()
	lines := []string{
		`{"timestamp":"` + ts.Add(-time.Hour).Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","rate_limits":{"primary":{"used_percent":50,"window_minutes":300},"secondary":{"used_percent":10,"window_minutes":10080}}}}`,
		`{"timestamp":"` + ts.Format(time.RFC3339) + `","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":6.0,"window_minutes":10080,"resets_at":` + fmt.Sprint(resetAt) + `},"secondary":null,"plan_type":"pro"}}}`,
		`{"timestamp":"` + ts.Format(time.RFC3339) + `","type":"response_item","payload":{"type":"message"}}`,
		`not json`,
	}
	if err := os.WriteFile(filepath.Join(day, "rollout-a.jsonl"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	r := do(t, h, "GET", "/api/state", "", nil)
	codex := byName(r, "Codex 1")
	id := codex["id"].(string)
	body, _ := json.Marshal(map[string]any{"name": "Codex 1", "provider": "codex", "plan": "", "autoScan": true, "configDir": home, "status": "offline"})
	do(t, h, "PUT", "/api/accounts/"+id, string(body), nil)

	r = do(t, h, "POST", "/api/scan", "", nil)
	if r.body["updated"].(float64) != 1 {
		t.Fatalf("scan updated = %v, errs %v", r.body["updated"], r.body["scanErrs"])
	}
	a := byName(r, "Codex 1")
	u := a["usage"].(map[string]any)
	if u["noFiveHour"] != true || u["weekly"].(map[string]any)["left"].(float64) != 94 || u["source"] != "file" {
		t.Fatalf("codex usage wrong: %v", u)
	}
	if int64(u["weekly"].(map[string]any)["reset"].(float64)) != resetAt*1000 {
		t.Fatalf("reset wrong: %v", u["weekly"])
	}
	if a["plan"] != "Pro" {
		t.Fatalf("plan = %v", a["plan"])
	}
	// second scan: nothing newer
	if r := do(t, h, "POST", "/api/scan", "", nil); r.body["updated"].(float64) != 0 {
		t.Fatal("rescan should not update")
	}
	// a newer manual reading must not be overwritten by the older file
	do(t, h, "POST", "/api/accounts/"+id+"/usage", `{"weekly":{"left":11}}`, nil)
	scanAll(app.store)
	a = byName(do(t, h, "GET", "/api/state", "", nil), "Codex 1")
	if a["usage"].(map[string]any)["weekly"].(map[string]any)["left"].(float64) != 11 {
		t.Fatal("manual reading was overwritten by older file data")
	}
	// missing dir reports an error instead of crashing
	body, _ = json.Marshal(map[string]any{"name": "Codex 1", "provider": "codex", "autoScan": true, "configDir": filepath.Join(home, "nope")})
	do(t, h, "PUT", "/api/accounts/"+id, string(body), nil)
	if r := do(t, h, "POST", "/api/scan", "", nil); r.body["scanErrs"].(map[string]any)[id] == nil {
		t.Fatal("expected scan error for missing dir")
	}
}

func TestSettingsExportImportPersistence(t *testing.T) {
	app, h := newTestApp(t)
	old := app.store.Snapshot().Settings.Token
	r := do(t, h, "PUT", "/api/settings", `{"staleHours":3,"lowPercent":20,"scanSeconds":5,"regenToken":true}`, nil)
	s := r.body["settings"].(map[string]any)
	if s["staleHours"].(float64) != 3 || s["lowPercent"].(float64) != 20 || s["scanSeconds"].(float64) != 60 || s["token"] == old {
		t.Fatalf("settings: %v", s)
	}

	exp := do(t, h, "GET", "/api/export", "", nil)
	if exp.code != 200 || len(exp.body["accounts"].([]any)) != 2 {
		t.Fatalf("export: %d", exp.code)
	}
	do(t, h, "POST", "/api/accounts", `{"name":"Temp"}`, nil)
	if r := do(t, h, "POST", "/api/import", `{"foo":1}`, nil); r.code != 400 {
		t.Fatalf("bad import = %d", r.code)
	}
	r = do(t, h, "POST", "/api/import", exp.raw, nil)
	if r.code != 200 || byName(r, "Temp") != nil || len(accounts(r)) != 2 {
		t.Fatalf("import did not restore: %d", r.code)
	}

	// data survives a restart
	st2, err := openStore(app.dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if d := st2.Snapshot(); len(d.Accounts) != 2 || d.Settings.StaleHours != 3 {
		t.Fatalf("reloaded: %d accounts, stale %v", len(d.Accounts), d.Settings.StaleHours)
	}
}

func TestCorruptDataIsBackedUp(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.json")
	_ = os.WriteFile(p, []byte("{broken"), 0o600)
	if _, err := openStore(p); err == nil {
		t.Fatal("expected error for corrupt data")
	}
	m, _ := filepath.Glob(p + ".broken-*")
	if len(m) != 1 {
		t.Fatal("corrupt file was not backed up")
	}
	if raw, _ := os.ReadFile(p); string(raw) != "{broken" {
		t.Fatal("corrupt file was overwritten")
	}
}
