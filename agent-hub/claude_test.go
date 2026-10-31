package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return home
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s: %v\n%s", path, err, raw)
	}
	return m
}

func TestInstallStatusLineFresh(t *testing.T) {
	home := fakeHome(t)
	dir := filepath.Join(t.TempDir(), "claude2")
	if err := installStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	s := readJSON(t, filepath.Join(dir, "settings.json"))
	sl := s["statusLine"].(map[string]any)
	if sl["type"] != "command" || sl["command"] != helperCommand() {
		t.Fatalf("statusLine = %v", sl)
	}
	if _, err := os.Stat(filepath.Join(home, ".agent-hub", helperName())); err != nil {
		t.Fatal("helper not copied:", err)
	}
	if st := statusLineState(dir); st != "on" {
		t.Fatalf("state = %s", st)
	}
	// installing twice is harmless
	if err := installStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	if err := uninstallStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	if _, ok := readJSON(t, filepath.Join(dir, "settings.json"))["statusLine"]; ok {
		t.Fatal("statusLine not removed")
	}
	if st := statusLineState(dir); st != "off" {
		t.Fatalf("state after uninstall = %s", st)
	}
}

func TestInstallKeepsUserSettingsAndStatusLine(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	orig := `{
  "model": "opus",
  "statusLine": {"type": "command", "command": "echo mine"},
  "env": {"FOO": "1"},
  "permissions": {"allow": ["Bash(ls:*)"]}
}`
	p := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(p, []byte(orig), 0o600)
	if st := statusLineState(dir); st != "other" {
		t.Fatalf("state = %s", st)
	}
	if err := installStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	got := string(raw)
	if !(strings.Index(got, `"model"`) < strings.Index(got, `"statusLine"`) &&
		strings.Index(got, `"statusLine"`) < strings.Index(got, `"env"`) &&
		strings.Index(got, `"env"`) < strings.Index(got, `"permissions"`)) {
		t.Fatalf("key order not kept:\n%s", got)
	}
	s := readJSON(t, p)
	if s["model"] != "opus" || s["env"].(map[string]any)["FOO"] != "1" {
		t.Fatalf("other settings lost: %v", s)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, settingsBackup)); string(b) != orig {
		t.Fatal("backup missing")
	}
	if prev := readJSON(t, filepath.Join(dir, prevStatusFile)); prev["command"] != "echo mine" {
		t.Fatalf("previous status line not saved: %v", prev)
	}

	// the helper runs the user's previous status line
	if runtime.GOOS != "windows" {
		t.Setenv("CLAUDE_CONFIG_DIR", dir)
		var out bytes.Buffer
		runStatusLine(strings.NewReader(`{"model":{"display_name":"Opus"}}`), &out)
		if strings.TrimSpace(out.String()) != "mine" {
			t.Fatalf("chained output = %q", out.String())
		}
	}

	if err := uninstallStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	s = readJSON(t, p)
	if s["statusLine"].(map[string]any)["command"] != "echo mine" {
		t.Fatalf("previous status line not restored: %v", s["statusLine"])
	}
	if _, err := os.Stat(filepath.Join(dir, prevStatusFile)); err == nil {
		t.Fatal("prev file should be removed")
	}
}

func TestUninstallLeavesForeignStatusLine(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(p, []byte(`{"statusLine":{"type":"command","command":"echo other"}}`), 0o600)
	if err := uninstallStatusLine(dir); err != nil {
		t.Fatal(err)
	}
	if readJSON(t, p)["statusLine"].(map[string]any)["command"] != "echo other" {
		t.Fatal("foreign status line was touched")
	}
}

func TestInvalidSettingsIsNotOverwritten(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.json")
	_ = os.WriteFile(p, []byte(`{"model": "opus",`), 0o600)
	if err := installStatusLine(dir); err == nil {
		t.Fatal("expected error for broken settings.json")
	}
	if raw, _ := os.ReadFile(p); string(raw) != `{"model": "opus",` {
		t.Fatal("broken settings.json was modified")
	}
}

func TestStatusLineHelperWritesUsage(t *testing.T) {
	fakeHome(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	reset5 := time.Now().Add(90 * time.Minute).Unix()
	reset7 := time.Now().Add(3 * 24 * time.Hour).Unix()
	in := `{"model":{"display_name":"Opus"},"rate_limits":{"five_hour":{"used_percentage":23.5,"resets_at":` +
		itoa(int(reset5)) + `},"seven_day":{"used_percentage":41.2,"resets_at":` + itoa(int(reset7)) + `}}}`
	var out bytes.Buffer
	runStatusLine(strings.NewReader(in), &out)
	if got := strings.TrimSpace(out.String()); got != "[Opus] · 5小时剩 77% · 每周剩 59%" {
		t.Fatalf("status text = %q", got)
	}
	u, err := readClaudeUsage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *u.FiveHour.Left != 76.5 || u.FiveHour.Reset != reset5*1000 || *u.Weekly.Left != 58.8 || u.Weekly.Reset != reset7*1000 {
		t.Fatalf("usage = %+v %+v", u.FiveHour, u.Weekly)
	}
	if u.Source != "statusline" || u.Detail != "Opus" || u.NoFiveHour {
		t.Fatalf("usage meta = %+v", u)
	}

	// no rate_limits (API key users, first prompt): file is left alone, line still printed
	out.Reset()
	runStatusLine(strings.NewReader(`{"model":{"display_name":"Sonnet"}}`), &out)
	if strings.TrimSpace(out.String()) != "[Sonnet]" {
		t.Fatalf("status text = %q", out.String())
	}
	if u2, _ := readClaudeUsage(dir); u2 == nil || *u2.FiveHour.Left != 76.5 {
		t.Fatal("usage file should be unchanged")
	}
	// garbage input never panics
	runStatusLine(strings.NewReader("not json"), &out)
}

func TestClaudeAutoReadViaAPI(t *testing.T) {
	fakeHome(t)
	_, h := newTestApp(t)
	dir := filepath.Join(t.TempDir(), "claude-acc")
	body, _ := json.Marshal(map[string]any{"name": "Claude X", "provider": "claude", "configDir": dir, "autoScan": true})
	r := do(t, h, "POST", "/api/accounts", string(body), nil)
	if r.code != 200 {
		t.Fatalf("create: %d %s", r.code, r.raw)
	}
	id := byName(r, "Claude X")["id"].(string)
	if statusLineState(dir) != "on" {
		t.Fatal("status line not installed on create")
	}
	if r.body["statusLines"].(map[string]any)[id] != "on" {
		t.Fatalf("state not reported: %v", r.body["statusLines"])
	}

	// nothing reported yet -> friendly scan error
	r = do(t, h, "POST", "/api/scan", "", nil)
	if r.body["scanErrs"].(map[string]any)[id] == nil {
		t.Fatal("expected 'no reading yet' message")
	}

	// Claude Code runs the helper -> reading appears after a scan
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	runStatusLine(strings.NewReader(`{"rate_limits":{"five_hour":{"used_percentage":10,"resets_at":`+itoa(int(time.Now().Add(time.Hour).Unix()))+`}}}`), &bytes.Buffer{})
	r = do(t, h, "POST", "/api/scan", "", nil)
	u := byName(r, "Claude X")["usage"].(map[string]any)
	if u["source"] != "statusline" || u["fiveHour"].(map[string]any)["left"].(float64) != 90 {
		t.Fatalf("usage = %v", u)
	}

	// turning it off removes the hook; moving the config dir moves it
	r = do(t, h, "POST", "/api/accounts/"+id+"/autoread", `{"enable":false}`, nil)
	if statusLineState(dir) != "off" || byName(r, "Claude X")["autoScan"] != false {
		t.Fatal("autoread off failed")
	}
	do(t, h, "POST", "/api/accounts/"+id+"/autoread", `{"enable":true}`, nil)
	dir2 := filepath.Join(t.TempDir(), "claude-acc2")
	body, _ = json.Marshal(map[string]any{"name": "Claude X", "provider": "claude", "configDir": dir2, "autoScan": true})
	do(t, h, "PUT", "/api/accounts/"+id, string(body), nil)
	if statusLineState(dir) != "off" || statusLineState(dir2) != "on" {
		t.Fatalf("config dir move: old=%s new=%s", statusLineState(dir), statusLineState(dir2))
	}
	do(t, h, "DELETE", "/api/accounts/"+id, "", nil)
	if statusLineState(dir2) != "off" {
		t.Fatal("delete should remove the hook")
	}
}

func TestSetPESubsystem(t *testing.T) {
	img := make([]byte, 0x200)
	img[0], img[1] = 'M', 'Z'
	img[0x3c] = 0x80
	copy(img[0x80:], "PE\x00\x00")
	off := 0x80 + 24 + 68
	img[off] = 2
	out := setPESubsystem(img, 3)
	if out[off] != 3 || img[off] != 2 {
		t.Fatal("subsystem not patched on a copy")
	}
	if got := setPESubsystem([]byte("not a pe"), 3); string(got) != "not a pe" {
		t.Fatal("non-PE input must be returned unchanged")
	}
}
