//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const createNewConsole = 0x00000010

// launchCommand opens a new console window running the account's command
// with its config directory exported.
func launchCommand(a *Account) (*exec.Cmd, error) {
	cmdline := a.LaunchCmd
	if cmdline == "" {
		cmdline = defaultLaunch(a.Provider)
	}
	title := a.Name
	if a.Agent != "" {
		title = a.Agent + " · " + a.Name
	}
	title = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`&|<>^"%()`, r) {
			return ' '
		}
		return r
	}, title)
	cmd := exec.Command("cmd.exe")
	// raw command line so quoting inside LaunchCmd is passed through untouched
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CmdLine:       `cmd.exe /k "title ` + title + ` && ` + cmdline + `"`,
		CreationFlags: createNewConsole,
	}
	cmd.Env = append(os.Environ(), accountEnv(a)...)
	cmd.Dir = workDir(a)
	return cmd, cmd.Start()
}

func openPath(p string) error {
	return exec.Command("explorer.exe", p).Start()
}

func openURL(u string) error {
	return exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", u).Start()
}

func appBrowsers() []string {
	var out []string
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LocalAppData"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		out = append(out,
			filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
			filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
		)
	}
	return out
}
