package main

// Automatic quota readings for Claude Code.
//
// Claude Code passes `rate_limits.five_hour` / `rate_limits.seven_day`
// (used_percentage, resets_at) to the status line command on stdin. Agent Hub
// installs itself as the status line of each Claude account's config dir:
//
//	settings.json: {"statusLine": {"type": "command", "command": "~/.agent-hub/agenthub.exe statusline"}}
//
// The helper saves the rate limits to <config dir>/agent-hub-usage.json and
// prints a status line (or runs the status line the user had before). The
// main app reads that file the same way it reads Codex session files.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	usageFileName   = "agent-hub-usage.json"
	prevStatusFile  = "agent-hub-prev-statusline.json"
	settingsBackup  = "settings.json.agent-hub-backup"
	helperDirName   = ".agent-hub"
	statusLineToken = "agenthub" // marks a statusLine command as ours
)

func defaultClaudeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func claudeDir(a *Account) string {
	if a.ConfigDir != "" {
		return a.ConfigDir
	}
	return defaultClaudeDir()
}

func helperName() string {
	if runtime.GOOS == "windows" {
		return "agenthub.exe"
	}
	return "agenthub"
}

// helperPath is a stable copy of this program used as the status line command.
// It lives under the home dir so the command can be written as "~/..." which
// works in Git Bash and PowerShell regardless of spaces in the user name.
func helperPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, helperDirName, helperName())
}

func helperCommand() string {
	return "~/" + helperDirName + "/" + helperName() + " statusline"
}

// ensureHelper copies the running executable to helperPath. On Windows the
// copy is switched to the console subsystem so shells wait for it and pipe its
// output normally.
func ensureHelper() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		raw = setPESubsystem(raw, 3) // IMAGE_SUBSYSTEM_WINDOWS_CUI
	}
	dst := helperPath()
	if old, err := os.ReadFile(dst); err == nil && bytes.Equal(old, raw) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, raw, 0o755); err != nil {
		return err
	}
	// a running exe cannot be overwritten on Windows, but it can be renamed
	_ = os.Remove(dst + ".old")
	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, dst+".old"); err != nil {
			return err
		}
	}
	return os.Rename(tmp, dst)
}

// setPESubsystem patches the Subsystem field of a PE image (returns a copy).
func setPESubsystem(img []byte, subsystem uint16) []byte {
	out := append([]byte(nil), img...)
	if len(out) < 0x40 {
		return out
	}
	pe := int(uint32(out[0x3c]) | uint32(out[0x3d])<<8 | uint32(out[0x3e])<<16 | uint32(out[0x3f])<<24)
	off := pe + 24 + 68 // PE sig + COFF header, Subsystem offset in optional header
	if pe <= 0 || off+2 > len(out) || string(out[pe:pe+4]) != "PE\x00\x00" {
		return out
	}
	out[off] = byte(subsystem)
	out[off+1] = byte(subsystem >> 8)
	return out
}

// ---------- settings.json editing (keeps key order) ----------

type kv struct {
	key string
	val json.RawMessage
}

func readSettings(path string) ([]kv, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(raw)) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("%s 不是有效的 JSON 对象", path)
	}
	var out []kv
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("%s 解析失败: %v", path, err)
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("%s 解析失败: %v", path, err)
		}
		out = append(out, kv{t.(string), v})
	}
	return out, nil
}

func writeSettings(path string, items []kv) error {
	var b bytes.Buffer
	b.WriteString("{\n")
	for i, it := range items {
		var val bytes.Buffer
		if err := json.Indent(&val, it.val, "  ", "  "); err != nil {
			return err
		}
		k, _ := json.Marshal(it.key)
		fmt.Fprintf(&b, "  %s: %s", k, val.String())
		if i < len(items)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("}\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func getKey(items []kv, key string) (json.RawMessage, int) {
	for i, it := range items {
		if it.key == key {
			return it.val, i
		}
	}
	return nil, -1
}

func isOurStatusLine(v json.RawMessage) bool {
	var sl struct {
		Command string `json:"command"`
	}
	return json.Unmarshal(v, &sl) == nil && strings.Contains(sl.Command, statusLineToken) && strings.Contains(sl.Command, "statusline")
}

// statusLineState reports "on" (ours), "other" (someone else's) or "off".
func statusLineState(dir string) string {
	items, err := readSettings(filepath.Join(dir, "settings.json"))
	if err != nil {
		return "error"
	}
	v, i := getKey(items, "statusLine")
	switch {
	case i < 0:
		return "off"
	case isOurStatusLine(v):
		return "on"
	default:
		return "other"
	}
}

func installStatusLine(dir string) error {
	if err := ensureHelper(); err != nil {
		return fmt.Errorf("准备状态栏程序失败: %v", err)
	}
	path := filepath.Join(dir, "settings.json")
	items, err := readSettings(path)
	if err != nil {
		return err
	}
	if raw, err := os.ReadFile(path); err == nil {
		if _, err := os.Stat(filepath.Join(dir, settingsBackup)); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile(filepath.Join(dir, settingsBackup), raw, 0o600)
		}
	}
	ours, _ := json.Marshal(map[string]any{"type": "command", "command": helperCommand(), "padding": 0})
	old, i := getKey(items, "statusLine")
	if i >= 0 && !isOurStatusLine(old) {
		// keep the user's own status line running behind ours
		if err := os.WriteFile(filepath.Join(dir, prevStatusFile), old, 0o600); err != nil {
			return err
		}
	}
	if i >= 0 {
		items[i].val = ours
	} else {
		items = append(items, kv{"statusLine", ours})
	}
	return writeSettings(path, items)
}

func uninstallStatusLine(dir string) error {
	path := filepath.Join(dir, "settings.json")
	items, err := readSettings(path)
	if err != nil {
		return err
	}
	v, i := getKey(items, "statusLine")
	if i < 0 || !isOurStatusLine(v) {
		return nil // not ours, leave it alone
	}
	if prev, err := os.ReadFile(filepath.Join(dir, prevStatusFile)); err == nil && json.Valid(prev) {
		items[i].val = prev
		_ = os.Remove(filepath.Join(dir, prevStatusFile))
	} else {
		items = append(items[:i], items[i+1:]...)
	}
	return writeSettings(path, items)
}

// ---------- the status line helper (runs inside Claude Code) ----------

// runStatusLine is the `agenthub statusline` entry point. It must be fast and
// must never fail visibly: any error just results in a plain status line.
func runStatusLine(stdin io.Reader, stdout io.Writer) {
	raw, _ := io.ReadAll(io.LimitReader(stdin, 4<<20))
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		dir = defaultClaudeDir()
	}
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	if rl, ok := in["rate_limits"].(map[string]any); ok && len(rl) > 0 {
		rec := map[string]any{"read_at": nowMs(), "rate_limits": rl}
		if m, ok := in["model"].(map[string]any); ok {
			rec["model"] = m["display_name"]
		}
		if b, err := json.Marshal(rec); err == nil {
			tmp := filepath.Join(dir, usageFileName+".tmp")
			if os.WriteFile(tmp, b, 0o600) == nil {
				_ = os.Rename(tmp, filepath.Join(dir, usageFileName))
			}
		}
	}
	if out, ok := runPrevStatusLine(dir, raw); ok {
		_, _ = stdout.Write(out)
		return
	}
	fmt.Fprintln(stdout, statusText(in))
}

func statusText(in map[string]any) string {
	var parts []string
	if m, ok := in["model"].(map[string]any); ok {
		if n, ok := m["display_name"].(string); ok && n != "" {
			parts = append(parts, "["+n+"]")
		}
	}
	if u, ok := parseUsage(map[string]any{"rate_limits": in["rate_limits"]}, nowMs()); ok {
		if u.FiveHour != nil && u.FiveHour.Left != nil {
			parts = append(parts, fmt.Sprintf("5小时剩 %d%%", int(math.Round(*u.FiveHour.Left))))
		}
		if u.Weekly != nil && u.Weekly.Left != nil {
			parts = append(parts, fmt.Sprintf("每周剩 %d%%", int(math.Round(*u.Weekly.Left))))
		}
	}
	if len(parts) == 0 {
		return "Agent Hub"
	}
	return strings.Join(parts, " · ")
}

func runPrevStatusLine(dir string, input []byte) ([]byte, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, prevStatusFile))
	if err != nil {
		return nil, false
	}
	var sl struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &sl) != nil || strings.TrimSpace(sl.Command) == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := shellCommand(ctx, sl.Command)
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return nil, false
	}
	return out, true
}

// shellCommand runs a status line command the way Claude Code does: Git Bash
// when available, otherwise PowerShell (Windows) or sh.
func shellCommand(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS != "windows" {
		return exec.CommandContext(ctx, "sh", "-c", command)
	}
	for _, b := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), `Git\bin\bash.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Git\bin\bash.exe`),
		filepath.Join(os.Getenv("LocalAppData"), `Programs\Git\bin\bash.exe`),
	} {
		if _, err := os.Stat(b); err == nil {
			return exec.CommandContext(ctx, b, "-c", command)
		}
	}
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-Command", command)
}

// ---------- reading in the main app ----------

func readClaudeUsage(dir string) (*Usage, error) {
	raw, err := os.ReadFile(filepath.Join(dir, usageFileName))
	if err != nil {
		return nil, err
	}
	var rec map[string]any
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, err
	}
	readAt := parseTimeValue(rec["read_at"])
	if readAt == 0 {
		return nil, errors.New("no read_at")
	}
	u, ok := parseUsage(rec, readAt)
	if !ok {
		return nil, errors.New("no rate limits")
	}
	u.Source = "statusline"
	if m, ok := rec["model"].(string); ok {
		u.Detail = m
	}
	return u, nil
}
