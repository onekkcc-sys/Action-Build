#!/usr/bin/env sh
# Build AgentHub.exe (Windows x64). Works from Linux/macOS/Windows (Git Bash) with Go 1.22+.
set -e
cd "$(dirname "$0")"
mkdir -p dist
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/AgentHub.exe .
echo "built dist/AgentHub.exe"
