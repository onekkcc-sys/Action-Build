package main

import "testing"

func TestParseUsageShapes(t *testing.T) {
	const readAt = int64(1_700_000_000_000)
	cases := []struct {
		name      string
		in        map[string]any
		five      float64 // -1 = expect nil
		week      float64
		noFive    bool
		weekReset int64
	}{
		{"simple", map[string]any{"five_hour": map[string]any{"left": 77.0}, "weekly": map[string]any{"used": 32.0, "resets_in_seconds": 60.0}}, 77, 68, false, readAt + 60_000},
		{"claude", map[string]any{"five_hour": map[string]any{"utilization": 23.0}, "seven_day": map[string]any{"utilization": 40.0, "resets_at": "2023-11-20T00:00:00Z"}}, 77, 60, false, 1700438400000},
		{"codex weekly only", map[string]any{"rate_limits": map[string]any{"primary": map[string]any{"used_percent": 6.0, "window_minutes": 10080.0, "resets_at": 1700000100.0}, "secondary": nil}}, -1, 94, true, 1700000100000},
		{"codex both", map[string]any{"primary": map[string]any{"used_percent": 10.0, "window_minutes": 300.0}, "secondary": map[string]any{"used_percent": 50.0, "window_minutes": 10080.0}}, 90, 50, false, 0},
	}
	for _, c := range cases {
		u, ok := parseUsage(c.in, readAt)
		if !ok {
			t.Fatalf("%s: not parsed", c.name)
		}
		if c.five < 0 {
			if u.FiveHour != nil {
				t.Errorf("%s: five hour should be nil", c.name)
			}
		} else if u.FiveHour == nil || *u.FiveHour.Left != c.five {
			t.Errorf("%s: five hour = %+v, want %v", c.name, u.FiveHour, c.five)
		}
		if u.Weekly == nil || *u.Weekly.Left != c.week {
			t.Errorf("%s: weekly = %+v, want %v", c.name, u.Weekly, c.week)
		} else if c.weekReset != 0 && u.Weekly.Reset != c.weekReset {
			t.Errorf("%s: weekly reset = %d, want %d", c.name, u.Weekly.Reset, c.weekReset)
		}
		if u.NoFiveHour != c.noFive {
			t.Errorf("%s: noFiveHour = %v", c.name, u.NoFiveHour)
		}
	}
	if _, ok := parseUsage(map[string]any{"foo": 1.0}, readAt); ok {
		t.Error("garbage should not parse")
	}
}
