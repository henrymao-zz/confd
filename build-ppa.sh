#!/bin/bash
#
# Build and upload confd source package to PPA:henrymao/ubuntu-nos.
#
# Usage:
#   ./build-ppa.sh                    # auto-bump version, build, sign, upload
#   ./build-ppa.sh --no-upload         # build and sign only, don't upload
#   ./build-ppa.sh --version 0.1.0-7  # specify version explicitly
#
# Prerequisites:
#   - GPG key: A30250A69E5B4C27139CD7898AFC7E4A6437DFA0 (henrymao)
#   - dput config: ~/.dput.cf with [ppa-henrymao-ubuntu-nos]
#   - Build deps installed (see debian/control Build-Depends)
#

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PKG_NAME="confd"
PPA="ppa:henrymao/ubuntu-nos"
GPG_KEY="A30250A69E5B4C27139CD7898AFC7E4A6437DFA0"
DISTRIBUTION="resolute"
MAINTAINER="Henry Mao <henry.mao@canonical.com>"
OUTPUT_DIR="${SCRIPT_DIR}/.."

DO_UPLOAD=true
VERSION_OVERRIDE=""

# --- Parse args ---
while [[ $# -gt 0 ]]; do
    case "$1" in
        --no-upload)   DO_UPLOAD=false; shift ;;
        --version)     VERSION_OVERRIDE="$2"; shift 2 ;;
        --help|-h)
            echo "Usage: $0 [--no-upload] [--version <ver>]"
            exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

# --- Determine current version ---
CURRENT_VERSION=$(dpkg-parsechangelog -S Version -l "${SCRIPT_DIR}/debian/changelog")
echo "=== Current version: ${CURRENT_VERSION} ==="

# --- Auto-bump version if not overridden ---
if [[ -n "${VERSION_OVERRIDE}" ]]; then
    NEW_VERSION="${VERSION_OVERRIDE}"
else
    # Split version into base and revision (e.g. 0.1.0-6 -> base=0.1.0, rev=6)
    BASE="${CURRENT_VERSION%-*}"
    REV="${CURRENT_VERSION##*-}"
    NEW_REV=$((REV + 1))
    NEW_VERSION="${BASE}-${NEW_REV}"
fi
echo "=== New version: ${NEW_VERSION} ==="

# --- Get previous version for changelog reference ---
PREV_VERSION="${CURRENT_VERSION}"

# --- Generate changelog entry ---
TODAY=$(date -R)
cat > /tmp/confd-changelog-entry <<EOF
${PKG_NAME} (${NEW_VERSION}) ${DISTRIBUTION}; urgency=medium

  * Auto-generated changelog entry. Edit debian/changelog for details.

 -- ${MAINTAINER}  ${TODAY}

EOF

# Prepend the new entry to the changelog
CHANGELOG="${SCRIPT_DIR}/debian/changelog"
cat /tmp/confd-changelog-entry "${CHANGELOG}" > /tmp/confd-changelog-new
mv /tmp/confd-changelog-new "${CHANGELOG}"
rm -f /tmp/confd-changelog-entry
echo "=== Changelog updated ==="

# --- Update debian/files ---
echo "${PKG_NAME}_${NEW_VERSION}_source.buildinfo net optional" > "${SCRIPT_DIR}/debian/files"

# --- Clean build artifacts ---
echo "=== Cleaning build artifacts ==="
cd "${SCRIPT_DIR}"
rm -rf confd debian/staging debian/gocache debian/gotmp debian/.debhelper debian/confd
find src -name build -type d -exec rm -rf {} + 2>/dev/null || true
rm -f coverage.out nldump repro*.sh

# --- Verify submodules and vendor are present ---
echo "=== Verifying source completeness ==="
for f in src/libyang/CMakeLists.txt src/sysrepo/CMakeLists.txt \
         src/sysrepo-plugins/CMakeLists.txt src/libyang-cpp/CMakeLists.txt \
         src/sysrepo-cpp/CMakeLists.txt src/umgmt/CMakeLists.txt \
         src/procps/autogen.sh vendor/modules.txt go.mod go.sum; do
    if [[ ! -f "${f}" ]]; then
        echo "ERROR: Missing required file: ${f}"
        exit 1
    fi
done
echo "All required source files present."

# --- Verify uthash symlink ---
if [[ ! -e src/umgmt/deps/uthash/include/utlist.h ]]; then
    echo "ERROR: uthash include symlink is broken"
    exit 1
fi
echo "uthash symlink OK."

# --- Build source package ---
echo "=== Building source package ==="
cd "${SCRIPT_DIR}"
dpkg-buildpackage -S --sign-key="${GPG_KEY}" 2>&1 | tail -15

# --- Verify output files ---
echo "=== Verifying output ==="
cd "${OUTPUT_DIR}"
DSC_FILE="${PKG_NAME}_${NEW_VERSION}.dsc"
TARBALL="${PKG_NAME}_${NEW_VERSION}.tar.xz"
CHANGES_FILE="${PKG_NAME}_${NEW_VERSION}_source.changes"
BUILDINFO_FILE="${PKG_NAME}_${NEW_VERSION}_source.buildinfo"

for f in "${DSC_FILE}" "${TARBALL}" "${CHANGES_FILE}" "${BUILDINFO_FILE}"; do
    if [[ ! -f "${f}" ]]; then
        echo "ERROR: Missing output file: ${f}"
        exit 1
    fi
done
echo "All output files present."

# --- Verify GPG signatures ---
echo "=== Verifying signatures ==="
gpg --verify "${DSC_FILE}" 2>&1 | grep -q "Good signature" && echo "DSC: signed OK" || { echo "DSC: signature FAILED"; exit 1; }
gpg --verify "${CHANGES_FILE}" 2>&1 | grep -q "Good signature" && echo "CHANGES: signed OK" || { echo "CHANGES: signature FAILED"; exit 1; }

# --- Verify the fix is in the tarball ---
echo "=== Verifying tarball contents ==="
TARBALL_FIX=$(tar xJf "${TARBALL}" -O "${PKG_NAME}/src/sysrepo-plugins/core/src/srpcpp/netlink/neighbor.cpp" 2>/dev/null | grep -c 'str == "none"' || true)
if [[ "${TARBALL_FIX}" -eq 0 ]]; then
    echo "WARNING: The 'none' fix is NOT in the tarball neighbor.cpp"
else
    echo "Fix check: neighbor.cpp contains 'none' guard (${TARBALL_FIX} match(es))"
fi

VENDOR_CHECK=$(tar tJf "${TARBALL}" 2>&1 | grep -c 'vendor/modules.txt')
if [[ "${VENDOR_CHECK}" -eq 0 ]]; then
    echo "ERROR: vendor/ directory not in tarball"
    exit 1
fi
echo "vendor/ present in tarball."

GIT_CHECK=$(tar tJf "${TARBALL}" 2>&1 | grep -c '\.git/')
if [[ "${GIT_CHECK}" -gt 0 ]]; then
    echo "WARNING: .git/ directories found in tarball"
else
    echo ".git/ excluded from tarball."
fi

BUILD_CHECK=$(tar tJf "${TARBALL}" 2>&1 | grep -c '/build/')
if [[ "${BUILD_CHECK}" -gt 0 ]]; then
    echo "WARNING: build/ directories found in tarball"
else
    echo "build/ excluded from tarball."
fi

FILE_COUNT=$(tar tJf "${TARBALL}" 2>&1 | wc -l)
echo "Tarball file count: ${FILE_COUNT}"

# --- Upload to PPA ---
if [[ "${DO_UPLOAD}" == "true" ]]; then
    echo "=== Uploading to ${PPA} ==="
    dput "${PPA}" "${CHANGES_FILE}"
    echo "=== Upload complete ==="
    echo "PPA: https://launchpad.net/~henrymao/+archive/ubuntu/ubuntu-nos"
else
    echo "=== Skipping upload (--no-upload) ==="
    echo "Files ready in: ${OUTPUT_DIR}"
    echo "Upload with: dput ${PPA} ${CHANGES_FILE}"
fi