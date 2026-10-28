#!/bin/bash
#
# build-spk.sh — Build Synology SPK package for OpenList
#
# Usage: ./build-spk.sh <version> <arch> <binary_path>
#   version:      OpenList version (e.g. 4.2.9)
#   arch:         Target architecture: x64, arm64
#   binary_path:  Path to the extracted openlist binary
#
set -euo pipefail

VERSION="${1:?Usage: build-spk.sh <version> <arch> <binary_path>}"
ARCH="${2:?arch must be x64 or arm64}"
BINARY_PATH="${3:?binary_path is required}"

PKG_NAME="openlist"
SPK_FILE="${PKG_NAME}_${ARCH}_${VERSION}.spk"

# ── DSM arch platform codes per architecture ─────────────────────────
case "$ARCH" in
  x64)
    DSM_ARCH="apollolake avoton braswell broadwell broadwellnk broadwellnkv2 broadwellntbap bromolow cedarview denverton dockerx64 epyc7002 geminilake grantley kvmx64 purley r1000 v1000 x86 x86_64"
    ;;
  arm64)
    DSM_ARCH="armada37xx armada7040 armada8080 rtd1296 rtd1619 rtd1619b"
    ;;
  *)
    echo "ERROR: Unsupported arch: $ARCH (use x64 or arm64)"
    exit 1
    ;;
esac

# ── Version comparison: check if version < 4.2.7 ────────────────────
# Before v4.2.7, admin password must be set manually.
# From v4.2.7, initialization page lets user set account/password on first visit.
version_lt() {
  # Returns 0 (true) if $1 < $2
  echo -e "$1\n$2" | sort -V -C && [ "$1" != "$2" ]
}

if version_lt "$VERSION" "4.2.7"; then
  NEED_ADMIN_PASSWORD=true
  echo "  Version $VERSION < 4.2.7 → 预置 admin/admin 密码"
else
  NEED_ADMIN_PASSWORD=false
  echo "  Version $VERSION >= 4.2.7 → 无需预置密码（初始化页面设置）"
fi

WORK_DIR="$(mktemp -d)"
STAGING_DIR="$(mktemp -d)"
trap "rm -rf $WORK_DIR $STAGING_DIR" EXIT

echo "=== Building OpenList SPK ==="
echo "  Version: $VERSION"
echo "  Arch:    $ARCH"
echo "  Binary:  $BINARY_PATH"

# ── 1. Prepare staging (package.tgz contents) ───────────────────────
mkdir -p "$STAGING_DIR/bin" "$STAGING_DIR/etc" "$STAGING_DIR/ui/images"

# Binary
cp "$BINARY_PATH" "$STAGING_DIR/bin/openlist"
chmod 755 "$STAGING_DIR/bin/openlist"

# Config
cp etc/config.json "$STAGING_DIR/etc/config.json"
chmod 644 "$STAGING_DIR/etc/config.json"

# UI config + images
cp ui/config "$STAGING_DIR/ui/config"
chmod 755 "$STAGING_DIR/ui/config"
cp ui/images/*.png "$STAGING_DIR/ui/images/"
chmod 755 "$STAGING_DIR/ui/images/"*.png

# ── 2. Generate service-setup (with or without admin password) ──────
mkdir -p "$WORK_DIR/scripts" "$WORK_DIR/conf" "$WORK_DIR/WIZARD_UIFILES"

if [ "$NEED_ADMIN_PASSWORD" = true ]; then
  # Create wrapper script that sets admin password on first run
  cat > "$STAGING_DIR/bin/start-openlist.sh" << 'WRAPEOF'
#!/bin/sh
# OpenList startup wrapper — sets admin password on first run
DATA_DIR="${OPENLIST_DATA_DIR:-/var/packages/openlist/var}"
CONFIG_FILE="${OPENLIST_CONFIG_FILE:-/var/packages/openlist/target/etc/config.json}"
BIN_DIR="$(dirname "$0")"

FLAG_FILE="$DATA_DIR/.admin_initialized"
if [ ! -f "$FLAG_FILE" ]; then
  echo "First run: setting admin password to 'admin'..."
  "$BIN_DIR/openlist" admin set admin --data "$DATA_DIR" --config "$CONFIG_FILE" 2>&1
  touch "$FLAG_FILE"
  echo "Admin password set."
fi

exec "$BIN_DIR/openlist" server --data "$DATA_DIR" --config "$CONFIG_FILE"
WRAPEOF
  chmod 755 "$STAGING_DIR/bin/start-openlist.sh"

  # service-setup uses wrapper script
  cat > "$WORK_DIR/scripts/service-setup" << 'SVCSETUP'
### Generic variables and functions
### -------------------------------

if [ -z "${SYNOPKG_PKGNAME}" ] || [ -z "${SYNOPKG_DSM_VERSION_MAJOR}" ]; then
  echo "Error: Environment variables are not set." 1>&2;
  echo "Please run me using synopkg instead. Example: \"synopkg start [packagename]\"" 1>&2;
  exit 1
fi

PACKAGE_BASE="/var/packages/${SYNOPKG_PKGNAME}/target"
OPENLIST_WRAPPER="${PACKAGE_BASE}/bin/start-openlist.sh"
SVC_CWD="${SYNOPKG_PKGVAR}"

# start-stop-status script redirect stdout/stderr to LOG_FILE
LOG_FILE="${SYNOPKG_PKGVAR}/openlist.log"

# Service command has to deliver its pid into PID_FILE
PID_FILE="${SYNOPKG_PKGVAR}/openlist.pid"

### Package specific variables and functions
### ----------------------------------------

export OPENLIST_DATA_DIR="${SYNOPKG_PKGVAR}"
export OPENLIST_CONFIG_FILE="${PACKAGE_BASE}/etc/config.json"

SERVICE_COMMAND[0]="${OPENLIST_WRAPPER}"

SVC_BACKGROUND=y
SVC_WRITE_PID=y
SVCSETUP
else
  # v4.2.7+: no wrapper needed, init page handles account setup
  cat > "$WORK_DIR/scripts/service-setup" << 'SVCSETUP'
### Generic variables and functions
### -------------------------------

if [ -z "${SYNOPKG_PKGNAME}" ] || [ -z "${SYNOPKG_DSM_VERSION_MAJOR}" ]; then
  echo "Error: Environment variables are not set." 1>&2;
  echo "Please run me using synopkg instead. Example: \"synopkg start [packagename]\"" 1>&2;
  exit 1
fi

PACKAGE_BASE="/var/packages/${SYNOPKG_PKGNAME}/target"
OPENLIST_BIN="${PACKAGE_BASE}/bin/openlist"
SVC_CWD="${SYNOPKG_PKGVAR}"

# start-stop-status script redirect stdout/stderr to LOG_FILE
LOG_FILE="${SYNOPKG_PKGVAR}/openlist.log"

# Service command has to deliver its pid into PID_FILE
PID_FILE="${SYNOPKG_PKGVAR}/openlist.pid"

### Package specific variables and functions
### ----------------------------------------

SERVICE_COMMAND[0]="${OPENLIST_BIN} server --data ${SYNOPKG_PKGVAR} --config ${PACKAGE_BASE}/etc/config.json"

SVC_BACKGROUND=y
SVC_WRITE_PID=y
SVCSETUP
fi
chmod 755 "$WORK_DIR/scripts/service-setup"

# ── 3. Create package.tgz ───────────────────────────────────────────
# package.tgz (files at root level, no ./ prefix)
cd "$STAGING_DIR"
if [ "$NEED_ADMIN_PASSWORD" = true ]; then
  tar cpf - --sort=name --owner=root --group=root bin etc ui | gzip -n > "$WORK_DIR/package.tgz"
else
  tar cpf - --sort=name --owner=root --group=root bin etc ui | gzip -n > "$WORK_DIR/package.tgz"
fi
cd - > /dev/null

CHECKSUM=$(md5sum "$WORK_DIR/package.tgz" | cut -d' ' -f1)
echo "  package.tgz MD5: $CHECKSUM"

# ── 3.5. Set description based on version ────────────────────────────
if [ "$NEED_ADMIN_PASSWORD" = true ]; then
  PKG_DESCRIPTION="OpenList — 多存储文件列表程序（AList 社区分支）。默认账号 admin / 密码 admin，访问 http://<NAS-IP>:5244"
else
  PKG_DESCRIPTION="OpenList — 多存储文件列表程序（AList 社区分支）。首次访问通过初始化向导设置管理员账号，访问 http://<NAS-IP>:5244"
fi

# ── 4. Create INFO ──────────────────────────────────────────────────
cat > "$WORK_DIR/INFO" << EOF
package="${PKG_NAME}"
version="${VERSION}"
description="${PKG_DESCRIPTION}"
displayname="OpenList"
maintainer="OpenList Team"
maintainer_url="https://github.com/openlistteam/openlist"
distributor="OpenList Team"
distributor_url="https://github.com/openlistteam/openlist"
helpurl="https://github.com/openlistteam/openlist"
arch="${DSM_ARCH}"
os_min_ver="7.2-64570"
dsmappname="SYNO.SDS.OpenList.Application"
dsmuidir="ui"
checksum="${CHECKSUM}"
EOF
chmod 755 "$WORK_DIR/INFO"

# ── 5. Copy scripts, conf, wizard, icons ────────────────────────────
# Copy standard scripts (except service-setup which was generated above)
for f in functions installer start-stop-status preinst postinst preuninst postuninst preupgrade postupgrade; do
  cp "scripts/$f" "$WORK_DIR/scripts/$f"
done
chmod 755 "$WORK_DIR/scripts/"*

cp conf/privilege "$WORK_DIR/conf/privilege"
cp conf/resource "$WORK_DIR/conf/resource"
chmod 755 "$WORK_DIR/conf/"*

cp WIZARD_UIFILES/uninstall_uifile "$WORK_DIR/WIZARD_UIFILES/uninstall_uifile"
chmod 755 "$WORK_DIR/WIZARD_UIFILES/uninstall_uifile"

cp icons/PACKAGE_ICON.PNG "$WORK_DIR/PACKAGE_ICON.PNG"
cp icons/PACKAGE_ICON_256.PNG "$WORK_DIR/PACKAGE_ICON_256.PNG"
chmod 755 "$WORK_DIR/PACKAGE_ICON.PNG" "$WORK_DIR/PACKAGE_ICON_256.PNG"

# ── 6. Assemble SPK ─────────────────────────────────────────────────
cd "$WORK_DIR"
tar cpf "$OLDPWD/$SPK_FILE" --sort=name --owner=root --group=root \
  package.tgz INFO scripts WIZARD_UIFILES conf PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG
cd - > /dev/null

echo ""
echo "=== Done ==="
echo "  Output: $SPK_FILE ($(du -h "$SPK_FILE" | cut -f1))"
echo "  Admin password: $([ "$NEED_ADMIN_PASSWORD" = true ] && echo '预置 admin/admin' || echo '初始化页面设置')"

# Verify checksum
INFO_CS=$(grep '^checksum=' "$WORK_DIR/INFO" | cut -d'"' -f2)
ACT_CS=$(md5sum "$WORK_DIR/package.tgz" | cut -d' ' -f1)
if [ "$INFO_CS" = "$ACT_CS" ]; then
  echo "  Checksum: VALID ✓"
else
  echo "  Checksum: MISMATCH ✗"
  exit 1
fi
