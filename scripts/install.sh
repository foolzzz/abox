#!/bin/sh
set -eu
umask 077

usage() {
  cat >&2 <<EOF
usage: $0 [--enable-services]

Installs Agent Box for the current user. Services are installed but are not
enabled or started unless --enable-services is supplied.
EOF
}

ENABLE_SERVICES=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --enable-services) ENABLE_SERVICES=true ;;
    -h|--help) usage; exit 0 ;;
    *) echo "error: unknown option: $1" >&2; usage; exit 2 ;;
  esac
  shift
done

if [ -z "${HOME:-}" ] || [ ! -d "$HOME" ]; then
  echo "error: HOME must name an existing directory" >&2
  exit 1
fi

normalize_target() {
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
}

xml_escape() {
  printf '%s' "$1" | sed \
    -e 's/&/\&amp;/g' \
    -e 's/</\&lt;/g' \
    -e 's/>/\&gt;/g' \
    -e 's/"/\&quot;/g' \
    -e "s/'/\\\&apos;/g"
}

sed_replacement_escape() {
  printf '%s' "$1" | sed 's/[&|\\]/\\&/g'
}

json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

normalize_target
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

for required in \
  "$SCRIPT_DIR/TARGET_OS" \
  "$SCRIPT_DIR/TARGET_ARCH" \
  "$SCRIPT_DIR/bin/agentbox-server" \
  "$SCRIPT_DIR/bin/agentboxd" \
  "$SCRIPT_DIR/configs/server.example.json" \
  "$SCRIPT_DIR/configs/agentboxd.example.json" \
  "$SCRIPT_DIR/share/agentbox/web/index.html" \
  "$SCRIPT_DIR/packaging/launchd/io.agentbox.server.plist" \
  "$SCRIPT_DIR/packaging/launchd/io.agentbox.daemon.plist" \
  "$SCRIPT_DIR/packaging/systemd/agentbox-server.service" \
  "$SCRIPT_DIR/packaging/systemd/agentboxd.service"; do
  if [ ! -f "$required" ]; then
    echo "error: release payload is incomplete; missing $required" >&2
    exit 1
  fi
done

PACKAGE_OS=$(sed -n '1p' "$SCRIPT_DIR/TARGET_OS")
PACKAGE_ARCH=$(sed -n '1p' "$SCRIPT_DIR/TARGET_ARCH")
if [ "$PACKAGE_OS" != "$OS" ] || [ "$PACKAGE_ARCH" != "$ARCH" ]; then
  echo "error: this package targets $PACKAGE_OS/$PACKAGE_ARCH, but this host is $OS/$ARCH" >&2
  exit 1
fi

BIN_DIR="$HOME/.local/bin"
SERVER_HOME="$HOME/.agentbox"
DAEMON_HOME="$HOME/.agentboxd"
WEB_DIR="$SERVER_HOME/web"
SERVER_CONFIG="$SERVER_HOME/server.json"
DAEMON_CONFIG="$DAEMON_HOME/config.json"

install -d -m 0700 "$HOME/.local" "$BIN_DIR" "$SERVER_HOME" "$DAEMON_HOME" "$DAEMON_HOME/state"

install -m 0755 "$SCRIPT_DIR/bin/agentbox-server" "$BIN_DIR/.agentbox-server.new"
mv -f "$BIN_DIR/.agentbox-server.new" "$BIN_DIR/agentbox-server"
install -m 0755 "$SCRIPT_DIR/bin/agentboxd" "$BIN_DIR/.agentboxd.new"
mv -f "$BIN_DIR/.agentboxd.new" "$BIN_DIR/agentboxd"

rm -rf "$SERVER_HOME/.web.new"
install -d -m 0700 "$SERVER_HOME/.web.new"
cp -R "$SCRIPT_DIR/share/agentbox/web/." "$SERVER_HOME/.web.new/"
rm -rf "$WEB_DIR"
mv "$SERVER_HOME/.web.new" "$WEB_DIR"

SERVER_CONFIG_CREATED=false
DAEMON_CONFIG_CREATED=false
if [ ! -e "$SERVER_CONFIG" ]; then
  install -m 0600 "$SCRIPT_DIR/configs/server.example.json" "$SERVER_CONFIG"
  SERVER_CONFIG_CREATED=true
fi
if [ ! -e "$DAEMON_CONFIG" ]; then
  install -m 0600 "$SCRIPT_DIR/configs/agentboxd.example.json" "$DAEMON_CONFIG"
  DAEMON_CONFIG_CREATED=true
fi
chmod 0700 "$SERVER_HOME" "$DAEMON_HOME" "$DAEMON_HOME/state" "$WEB_DIR"
chmod 0600 "$SERVER_CONFIG" "$DAEMON_CONFIG"

case "$OS" in
  darwin)
    SERVICE_DIR="$HOME/Library/LaunchAgents"
    install -d -m 0700 "$SERVICE_DIR"
    escaped_home=$(xml_escape "$HOME")
    escaped_home=$(sed_replacement_escape "$escaped_home")
    for service in io.agentbox.server io.agentbox.daemon; do
      sed "s|@HOME@|$escaped_home|g" \
        "$SCRIPT_DIR/packaging/launchd/$service.plist" > "$SERVICE_DIR/$service.plist.new"
      chmod 0600 "$SERVICE_DIR/$service.plist.new"
      mv -f "$SERVICE_DIR/$service.plist.new" "$SERVICE_DIR/$service.plist"
    done
    if [ "$ENABLE_SERVICES" = true ]; then
      if ! command -v launchctl >/dev/null 2>&1; then
        echo "error: launchctl is required to enable services on macOS" >&2
        exit 1
      fi
      domain="gui/$(id -u)"
      launchctl bootout "$domain/io.agentbox.server" >/dev/null 2>&1 || true
      launchctl bootout "$domain/io.agentbox.daemon" >/dev/null 2>&1 || true
      if ! launchctl bootstrap "$domain" "$SERVICE_DIR/io.agentbox.server.plist"; then
        echo "error: failed to enable io.agentbox.server in launchd domain $domain" >&2
        exit 1
      fi
      if ! launchctl bootstrap "$domain" "$SERVICE_DIR/io.agentbox.daemon.plist"; then
        echo "error: failed to enable io.agentbox.daemon in launchd domain $domain" >&2
        exit 1
      fi
    fi
    ;;
  linux)
    SERVICE_DIR="$HOME/.config/systemd/user"
    install -d -m 0700 "$HOME/.config" "$HOME/.config/systemd" "$SERVICE_DIR"
    install -m 0600 "$SCRIPT_DIR/packaging/systemd/agentbox-server.service" "$SERVICE_DIR/agentbox-server.service"
    install -m 0600 "$SCRIPT_DIR/packaging/systemd/agentboxd.service" "$SERVICE_DIR/agentboxd.service"
    if [ "$ENABLE_SERVICES" = true ]; then
      if ! command -v systemctl >/dev/null 2>&1; then
        echo "error: systemctl is required to enable services on Linux" >&2
        exit 1
      fi
      if ! systemctl --user daemon-reload; then
        echo "error: failed to reload the systemd user manager" >&2
        exit 1
      fi
      if ! systemctl --user enable --now agentbox-server.service agentboxd.service; then
        echo "error: failed to enable and start Agent Box systemd user services" >&2
        exit 1
      fi
    fi
    ;;
esac

json_home=$(json_escape "$HOME")
printf '{"schemaVersion":1,"installed":true,"os":"%s","arch":"%s","binDirectory":"%s/.local/bin","serverConfig":"%s/.agentbox/server.json","daemonConfig":"%s/.agentboxd/config.json","serverConfigCreated":%s,"daemonConfigCreated":%s,"servicesStarted":%s}\n' \
  "$OS" "$ARCH" "$json_home" "$json_home" "$json_home" \
  "$SERVER_CONFIG_CREATED" "$DAEMON_CONFIG_CREATED" "$ENABLE_SERVICES"
