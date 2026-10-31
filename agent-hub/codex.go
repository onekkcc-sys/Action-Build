package main

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func defaultCodexHome() string {
	if v := os.Getenv("CODEX_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

type codexReading struct {
	usage *Usage
	plan  string
}

// scanCodexHome finds the newest rate-limit event in a Codex home directory
// (sessions/**/rollout-*.jsonl).
func scanCodexHome(dir string) (*codexReading, error) {
	if dir == "" {
		dir = defaultCodexHome()
	}
	type f struct {
		path string
		mod  time.Time
	}
	var files []f
	root := filepath.Join(dir, "sessions")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			if info, err := d.Info(); err == nil {
				files = append(files, f{p, info.ModTime()})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, os.ErrNotExist
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) > 8 {
		files = files[:8]
	}
	var best *codexReading
	for i, fl := range files {
		r := lastRateLimit(fl.path, fl.mod)
		if r != nil && (best == nil || r.usage.ReadAt > best.usage.ReadAt) {
			best = r
		}
		// files are newest-first; once we have a reading newer than the
		// next file's mtime nothing older can beat it
		if best != nil && i+1 < len(files) && best.usage.ReadAt >= files[i+1].mod.UnixMilli() {
			break
		}
	}
	if best == nil {
		return nil, os.ErrNotExist
	}
	return best, nil
}

func lastRateLimit(path string, mod time.Time) *codexReading {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	const tail = 4 << 20
	info, err := fh.Stat()
	if err != nil {
		return nil
	}
	start := int64(0)
	if info.Size() > tail {
		start = info.Size() - tail
	}
	if _, err := fh.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	buf, err := io.ReadAll(fh)
	if err != nil {
		return nil
	}
	lines := bytes.Split(buf, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		ln := lines[i]
		if !bytes.Contains(ln, []byte("rate_limits")) {
			continue
		}
		var ev map[string]any
		if json.Unmarshal(ln, &ev) != nil {
			continue
		}
		readAt := mod.UnixMilli()
		if ts, ok := ev["timestamp"].(string); ok {
			if t := parseTimeValue(ts); t > 0 {
				readAt = t
			}
		}
		rl := digRateLimits(ev, 0)
		if rl == nil {
			continue
		}
		u, ok := parseUsage(rl, readAt)
		if !ok {
			continue
		}
		u.Source = "file"
		u.Detail = path
		r := &codexReading{usage: u}
		if p, ok := rl["plan_type"].(string); ok {
			r.plan = p
		}
		return r
	}
	return nil
}

func digRateLimits(m map[string]any, depth int) map[string]any {
	if depth > 5 {
		return nil
	}
	if v, ok := m["rate_limits"].(map[string]any); ok {
		return v
	}
	for _, v := range m {
		if mm, ok := v.(map[string]any); ok {
			if r := digRateLimits(mm, depth+1); r != nil {
				return r
			}
		}
	}
	return nil
}

// scanAll refreshes every auto-scan Codex account. Returns how many updated.
func scanAll(st *Store) (int, map[string]string) {
	snap := st.Snapshot()
	errs := map[string]string{}
	results := map[string]*codexReading{}
	for _, a := range snap.Accounts {
		if !a.AutoScan || a.Provider != "codex" {
			continue
		}
		r, err := scanCodexHome(a.ConfigDir)
		if err != nil {
			errs[a.ID] = "没找到带额度信息的 Codex 会话文件"
			continue
		}
		results[a.ID] = r
	}
	n := 0
	if len(results) == 0 {
		return 0, errs
	}
	_ = st.Update(func(d *Data) error {
		for id, r := range results {
			a := d.account(id)
			if a == nil {
				continue
			}
			if a.Usage != nil && a.Usage.ReadAt >= r.usage.ReadAt {
				continue // existing reading is newer (e.g. manual entry)
			}
			a.Usage = mergeUsage(a.Usage, r.usage)
			if a.Plan == "" && r.plan != "" {
				a.Plan = strings.ToUpper(r.plan[:1]) + r.plan[1:]
			}
			n++
		}
		return nil
	})
	return n, errs
}
