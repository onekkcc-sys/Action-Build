package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Window is one quota window (5-hour or weekly) as last read.
type Window struct {
	Left   *float64 `json:"left"`             // remaining percent 0-100, nil = unknown
	Reset  int64    `json:"reset,omitempty"`  // unix ms, 0 = unknown
	Approx bool     `json:"approx,omitempty"` // reset time is an estimate
}

// Usage is the latest quota reading for an account.
type Usage struct {
	FiveHour   *Window `json:"fiveHour,omitempty"`
	Weekly     *Window `json:"weekly,omitempty"`
	NoFiveHour bool    `json:"noFiveHour,omitempty"` // plan currently has no 5h window
	ReadAt     int64   `json:"readAt"`               // unix ms
	Source     string  `json:"source"`               // manual | report | file
	Detail     string  `json:"detail,omitempty"`     // e.g. file path or reporter name
}

type Message struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	At   int64  `json:"at"`
}

type Account struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`     // e.g. "Claude 1"
	Agent     string    `json:"agent"`    // persona name, e.g. "阿璃（管家）"
	Provider  string    `json:"provider"` // claude | codex | other
	Plan      string    `json:"plan"`     // e.g. "Max 20x"
	Color     string    `json:"color"`
	Status    string    `json:"status"` // online | offline | unlinked
	Note      string    `json:"note"`
	ConfigDir string    `json:"configDir"` // CLAUDE_CONFIG_DIR / CODEX_HOME
	LaunchCmd string    `json:"launchCmd"`
	WorkDir   string    `json:"workDir"`
	AutoScan  bool      `json:"autoScan"` // read Codex session files automatically
	Usage     *Usage    `json:"usage,omitempty"`
	Messages  []Message `json:"messages"`
	Order     int       `json:"order"`
	CreatedAt int64     `json:"createdAt"`
}

type Decision struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Account   string `json:"account,omitempty"`
	Done      bool   `json:"done"`
	CreatedAt int64  `json:"createdAt"`
	DoneAt    int64  `json:"doneAt,omitempty"`
}

type Settings struct {
	StaleHours  float64 `json:"staleHours"`  // reading older than this is marked 旧
	LowPercent  float64 `json:"lowPercent"`  // highlight bars below this
	ScanSeconds int     `json:"scanSeconds"` // Codex auto-scan interval
	Token       string  `json:"token"`       // required by /api/report
}

type Data struct {
	Version   int         `json:"version"`
	Accounts  []*Account  `json:"accounts"`
	Decisions []*Decision `json:"decisions"`
	Settings  Settings    `json:"settings"`
}

type Store struct {
	mu   sync.Mutex
	path string
	data Data
}

func nowMs() int64 { return time.Now().UnixMilli() }

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func openStore(path string) (*Store, error) {
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.data); err != nil {
			// keep a copy of the unreadable file rather than silently overwriting it
			_ = os.WriteFile(path+".broken-"+time.Now().Format("20060102-150405"), raw, 0o600)
			return nil, errors.New("数据文件损坏，已备份: " + err.Error())
		}
	case errors.Is(err, os.ErrNotExist):
		s.data = seedData()
	default:
		return nil, err
	}
	s.normalize()
	return s, s.saveLocked()
}

func seedData() Data {
	t := nowMs()
	return Data{
		Version: 1,
		Accounts: []*Account{
			{ID: newID(), Name: "Claude 1", Agent: "管家", Provider: "claude", Plan: "Max 20x", Color: "#e2623b", Status: "online", LaunchCmd: "claude", Order: 0, CreatedAt: t},
			{ID: newID(), Name: "Codex 1", Agent: "管家", Provider: "codex", Plan: "Pro", Color: "#2f78d6", Status: "offline", LaunchCmd: "codex", AutoScan: true, Order: 1, CreatedAt: t},
		},
	}
}

func (s *Store) normalize() {
	d := &s.data
	if d.Version == 0 {
		d.Version = 1
	}
	if d.Settings.StaleHours <= 0 {
		d.Settings.StaleHours = 6
	}
	if d.Settings.LowPercent <= 0 {
		d.Settings.LowPercent = 15
	}
	if d.Settings.ScanSeconds <= 0 {
		d.Settings.ScanSeconds = 60
	}
	if d.Settings.Token == "" {
		d.Settings.Token = newToken()
	}
	if d.Accounts == nil {
		d.Accounts = []*Account{}
	}
	if d.Decisions == nil {
		d.Decisions = []*Decision{}
	}
	for _, a := range d.Accounts {
		if a.ID == "" {
			a.ID = newID()
		}
		if a.Messages == nil {
			a.Messages = []Message{}
		}
		if a.Status == "" {
			a.Status = "unlinked"
		}
	}
	s.sortLocked()
}

func (s *Store) sortLocked() {
	sort.SliceStable(s.data.Accounts, func(i, j int) bool { return s.data.Accounts[i].Order < s.data.Accounts[j].Order })
	for i, a := range s.data.Accounts {
		a.Order = i
	}
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Update runs fn under the lock and persists the result if fn succeeds.
func (s *Store) Update(fn func(d *Data) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := fn(&s.data); err != nil {
		return err
	}
	s.sortLocked()
	return s.saveLocked()
}

// Snapshot returns a deep copy of the data.
func (s *Store) Snapshot() Data {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(&s.data)
	var d Data
	_ = json.Unmarshal(raw, &d)
	return d
}

func (d *Data) account(id string) *Account {
	for _, a := range d.Accounts {
		if a.ID == id {
			return a
		}
	}
	return nil
}
