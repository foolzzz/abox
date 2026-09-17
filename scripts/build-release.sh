#!/bin/sh
set -eu

usage() {
  echo "usage: $0 VERSION [OUTPUT_DIRECTORY]" >&2
  exit 2
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || usage
VERSION=$1
OUTPUT_DIR=${2:-dist/release}

case "$VERSION" in
  ""|*[!A-Za-z0-9._+-]*|[-.+_]*)
    echo "error: VERSION must start with an alphanumeric character and contain only letters, digits, '.', '_', '+', or '-'" >&2
    exit 2
    ;;
esac

for command_name in go npm tar; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    echo "error: required command not found: $command_name" >&2
    exit 1
  fi
done

if command -v sha256sum >/dev/null 2>&1; then
  checksum_files() {
    sha256sum "$@"
  }
elif command -v shasum >/dev/null 2>&1; then
  checksum_files() {
    shasum -a 256 "$@"
  }
else
  echo "error: sha256sum or shasum is required" >&2
  exit 1
fi

json_escape() {
  printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'
}

REPO_ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
case "$OUTPUT_DIR" in
  /*) ;;
  *) OUTPUT_DIR="$REPO_ROOT/$OUTPUT_DIR" ;;
esac

TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/agentbox-release.XXXXXX")
cleanup() {
  rm -rf "$TMP_DIR"
}
trap cleanup EXIT HUP INT TERM

mkdir -p "$OUTPUT_DIR"
rm -f "$OUTPUT_DIR"/agentbox-*.tar.gz "$OUTPUT_DIR/checksums.txt"

(
  cd "$REPO_ROOT"
  npm --workspace apps/web run build
)

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  os=${target%/*}
  arch=${target#*/}
  package_name="agentbox-${VERSION}-${os}-${arch}"
  package_root="$TMP_DIR/$package_name"

  mkdir -p \
    "$package_root/bin" \
    "$package_root/configs" \
    "$package_root/share/agentbox/web" \
    "$package_root/packaging/launchd" \
    "$package_root/packaging/systemd"

  (
    cd "$REPO_ROOT"
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
      -trimpath -ldflags "-s -w" \
      -o "$package_root/bin/agentbox-server" ./cmd/agentbox-server
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build \
      -trimpath -ldflags "-s -w -X main.version=$VERSION" \
      -o "$package_root/bin/agentboxd" ./cmd/agentboxd
  )

  cp -R "$REPO_ROOT/apps/web/dist/." "$package_root/share/agentbox/web/"
  cp "$REPO_ROOT/configs/server.example.json" "$package_root/configs/server.example.json"
  cp "$REPO_ROOT/configs/agentboxd.example.json" "$package_root/configs/agentboxd.example.json"
  sed "s/\"version\": \"dev\"/\"version\": \"$VERSION\"/" \
    "$package_root/configs/server.example.json" > "$package_root/configs/server.example.json.tmp"
  mv "$package_root/configs/server.example.json.tmp" "$package_root/configs/server.example.json"

  cp "$REPO_ROOT/packaging/launchd/io.agentbox.server.plist" "$package_root/packaging/launchd/"
  cp "$REPO_ROOT/packaging/launchd/io.agentbox.daemon.plist" "$package_root/packaging/launchd/"
  cp "$REPO_ROOT/packaging/systemd/agentbox-server.service" "$package_root/packaging/systemd/"
  cp "$REPO_ROOT/packaging/systemd/agentboxd.service" "$package_root/packaging/systemd/"
  cp "$REPO_ROOT/scripts/install.sh" "$package_root/install.sh"
  cp "$REPO_ROOT/scripts/uninstall.sh" "$package_root/uninstall.sh"
  chmod 0755 "$package_root/install.sh" "$package_root/uninstall.sh" \
    "$package_root/bin/agentbox-server" "$package_root/bin/agentboxd"

  printf '%s\n' "$os" > "$package_root/TARGET_OS"
  printf '%s\n' "$arch" > "$package_root/TARGET_ARCH"
  printf '%s\n' "$VERSION" > "$package_root/VERSION"
  cat > "$package_root/manifest.json" <<EOF
{
  "schemaVersion": 1,
  "name": "agentbox",
  "version": "$VERSION",
  "os": "$os",
  "arch": "$arch",
  "binaries": ["agentbox-server", "agentboxd"],
  "webAssets": "share/agentbox/web"
}
EOF

  COPYFILE_DISABLE=1 tar -C "$TMP_DIR" -czf "$OUTPUT_DIR/$package_name.tar.gz" "$package_name"
  rm -rf "$package_root"
done

(
  cd "$OUTPUT_DIR"
  checksum_files agentbox-*.tar.gz > checksums.txt
)

json_output_dir=$(json_escape "$OUTPUT_DIR")
printf '{"schemaVersion":1,"version":"%s","outputDirectory":"%s","targets":["darwin/arm64","darwin/amd64","linux/arm64","linux/amd64"],"checksums":"checksums.txt"}\n' \
  "$VERSION" "$json_output_dir"
