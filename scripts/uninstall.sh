#!/bin/sh
set -eu

usage() {
  cat >&2 <<EOF
usage: $0 [--purge]

Removes Agent Box binaries, web assets, and user service definitions.
Configuration and daemon state are preserved unless --purge is supplied.
EOF
}

PURGE=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --purge) PURGE=true ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

if [ -z "${HOME:-}" ] || [ ! -d "$HOME" ]; then
  echo "error: HOME must name an existing directory" >&2
  exit 1
fi

host_os=$(uname -s)
host_arch=$(uname -m)
case "$host_os" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) echo "error: unsupported operating system: $host_os (supported: Darwin, Linux)" >&2; exit 1 ;;
esac
case "$host_arch" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *) echo "error: unsupported architecture: $host_arch (supported: arm64, amd64)" >&2; exit 1 ;;
esac

json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

case "$OS" in
  darwin)
    SERVICE_DIR="$HOME/Library/LaunchAgents"
    if command -v launchctl >/dev/null 2>&1; then
      domain="gui/$(id -u)"
      launchctl bootout "$domain/io.agentbox.server" >/dev/null 2>&1 || true
      launchctl bootout "$domain/io.agentbox.daemon" >/dev/null 2>&1 || true
    fi
    rm -f "$SERVICE_DIR/io.agentbox.server.plist" "$SERVICE_DIR/io.agentbox.daemon.plist"
    ;;
  linux)
    SERVICE_DIR="$HOME/.config/systemd/user"
    if command -v systemctl >/dev/null 2>&1; then
      systemctl --user disable --now agentbox-server.service agentboxd.service >/dev/null 2>&1 || true
    fi
    rm -f "$SERVICE_DIR/agentbox-server.service" "$SERVICE_DIR/agentboxd.service"
    if command -v systemctl >/dev/null 2>&1; then
      systemctl --user daemon-reload >/dev/null 2>&1 || true
    fi
    ;;
esac

rm -f "$HOME/.local/bin/agentbox-server" "$HOME/.local/bin/agentboxd"
rm -rf "$HOME/.agentbox/web"

if [ "$PURGE" = true ]; then
  rm -rf "$HOME/.agentbox" "$HOME/.agentboxd"
fi

json_home=$(json_escape "$HOME")
printf '{"schemaVersion":1,"uninstalled":true,"os":"%s","arch":"%s","binDirectory":"%s/.local/bin","configurationPurged":%s}\n' \
  "$OS" "$ARCH" "$json_home" "$PURGE"
