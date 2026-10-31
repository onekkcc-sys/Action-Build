package main

import (
	"strconv"
	"strings"
	"time"
)

// parseUsage understands several shapes of quota data and returns a Usage.
// readAt (unix ms) is the moment the data was observed; relative reset
// times ("resets_in_seconds") are counted from it.
//
// Accepted shapes (keys are matched case-insensitively, any nesting under
// "rate_limits" / "usage" is also searched):
//
//	{"five_hour": {"left": 77, "reset": "2026-10-04T09:00:00+08:00"},
//	 "weekly":    {"used": 32, "resets_in_seconds": 3600}}
//	{"five_hour": {"utilization": 23, "resets_at": "..."}, "seven_day": {...}}   // claude style
//	{"primary": {"used_percent": 12, "window_minutes": 300, "resets_at": 1759...},
//	 "secondary": {...}}                                                        // codex style
func parseUsage(m map[string]any, readAt int64) (*Usage, bool) {
	if inner := findMap(m, "rate_limits", "ratelimits", "usage", "limits"); inner != nil {
		if u, ok := parseUsage(inner, readAt); ok {
			return u, true
		}
	}
	u := &Usage{ReadAt: readAt}
	found := false
	viaMinutes := false

	assign := func(w *Window, minutes float64, fallbackFive bool) {
		if w == nil {
			return
		}
		found = true
		five := fallbackFive
		if minutes > 0 {
			five = minutes <= 12*60
			viaMinutes = true
		}
		if five {
			u.FiveHour = w
		} else {
			u.Weekly = w
		}
	}

	for _, k := range []string{"five_hour", "fivehour", "5h", "session", "five_hour_limit"} {
		if wm := findMap(m, k); wm != nil {
			w, mins := parseWindow(wm, readAt)
			assign(w, mins, true)
			break
		}
	}
	for _, k := range []string{"weekly", "seven_day", "sevenday", "week", "7d", "weekly_limit"} {
		if wm := findMap(m, k); wm != nil {
			w, mins := parseWindow(wm, readAt)
			assign(w, mins, false)
			break
		}
	}
	if wm := findMap(m, "primary"); wm != nil {
		w, mins := parseWindow(wm, readAt)
		assign(w, mins, true)
	}
	if wm := findMap(m, "secondary"); wm != nil {
		w, mins := parseWindow(wm, readAt)
		assign(w, mins, false)
	}
	if b, ok := getAny(m, "no_five_hour", "nofivehour"); ok {
		if bv, ok := b.(bool); ok && bv {
			u.NoFiveHour = true
			found = true
		}
	}
	// A codex-style report whose only window is weekly means the plan has no
	// 5-hour window right now.
	if viaMinutes && u.FiveHour == nil && u.Weekly != nil {
		u.NoFiveHour = true
	}
	return u, found
}

func parseWindow(m map[string]any, readAt int64) (*Window, float64) {
	w := &Window{}
	have := false
	if v, ok := getNum(m, "left", "remaining", "remaining_percent", "left_percent"); ok {
		w.Left = pct(v)
		have = true
	} else if v, ok := getNum(m, "used", "used_percent", "used_percentage", "utilization", "percent_used"); ok {
		w.Left = pct(100 - v)
		have = true
	}
	if v, ok := getAny(m, "reset", "resets_at", "reset_at", "resetsat", "resetat", "reset_time"); ok && v != nil {
		if t := parseTimeValue(v); t > 0 {
			w.Reset = t
			have = true
		}
	}
	if w.Reset == 0 {
		if v, ok := getNum(m, "resets_in_seconds", "reset_in_seconds", "reset_after_seconds", "resets_in"); ok {
			w.Reset = readAt + int64(v*1000)
			have = true
		}
	}
	if v, ok := getAny(m, "approx"); ok {
		if b, ok := v.(bool); ok {
			w.Approx = b
		}
	}
	mins, _ := getNum(m, "window_minutes", "windowminutes")
	if !have {
		return nil, mins
	}
	return w, mins
}

func pct(v float64) *float64 {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	v = float64(int(v*10+0.5)) / 10
	return &v
}

// parseTimeValue accepts unix seconds, unix ms, or a date string; returns unix ms.
func parseTimeValue(v any) int64 {
	switch t := v.(type) {
	case float64:
		if t <= 0 {
			return 0
		}
		if t < 1e12 {
			return int64(t * 1000)
		}
		return int64(t)
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return 0
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return parseTimeValue(f)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02T15:04"} {
			if tt, err := time.ParseInLocation(layout, s, time.Local); err == nil {
				return tt.UnixMilli()
			}
		}
	}
	return 0
}

func norm(k string) string {
	return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(k, "-", "_"), " ", "_"))
}

func getAny(m map[string]any, keys ...string) (any, bool) {
	for _, want := range keys {
		for k, v := range m {
			if norm(k) == want || strings.ReplaceAll(norm(k), "_", "") == strings.ReplaceAll(want, "_", "") {
				return v, true
			}
		}
	}
	return nil, false
}

func getNum(m map[string]any, keys ...string) (float64, bool) {
	v, ok := getAny(m, keys...)
	if !ok || v == nil {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(n), "%"), 64)
		return f, err == nil
	}
	return 0, false
}

func findMap(m map[string]any, keys ...string) map[string]any {
	v, ok := getAny(m, keys...)
	if !ok {
		return nil
	}
	mm, _ := v.(map[string]any)
	return mm
}

// mergeUsage overlays a fresh reading on the previous one: windows present in
// the new reading replace the old, missing ones are kept.
func mergeUsage(old, fresh *Usage) *Usage {
	if old == nil {
		return fresh
	}
	out := *fresh
	if out.FiveHour == nil && !out.NoFiveHour {
		out.FiveHour = old.FiveHour
		out.NoFiveHour = old.NoFiveHour
	}
	if out.Weekly == nil {
		out.Weekly = old.Weekly
	}
	return &out
}
