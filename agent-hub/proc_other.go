//go:build !windows

package main

import (
	"os"
	"os/exec"
	"runtime"
)

// launchCommand runs the account's command in a terminal emulator when one is
// available (development/testing on non-Windows systems).
func launchCommand(a *Account) (*exec.Cmd, error) {
	cmdline := a.LaunchCmd
	if cmdline == "" {
		cmdline = defaultLaunch(a.Provider)
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "darwin" {
		cmd = exec.Command("open", "-a", "Terminal", "-n", "--args", "sh", "-c", cmdline)
	} else if t, err := exec.LookPath("x-terminal-emulator"); err == nil {
		cmd = exec.Command(t, "-e", "sh", "-c", cmdline+"; exec $SHELL")
	} else {
		cmd = exec.Command("sh", "-c", cmdline)
	}
	cmd.Env = append(os.Environ(), accountEnv(a)...)
	cmd.Dir = workDir(a)
	return cmd, cmd.Start()
}

func opener() string {
	if runtime.GOOS == "darwin" {
		return "open"
	}
	return "xdg-open"
}

func openPath(p string) error { return exec.Command(opener(), p).Start() }
func openURL(u string) error  { return exec.Command(opener(), u).Start() }

func appBrowsers() []string {
	if runtime.GOOS == "darwin" {
		return []string{
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
	}
	var out []string
	for _, n := range []string{"microsoft-edge", "google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(n); err == nil {
			out = append(out, p)
		}
	}
	return out
}
