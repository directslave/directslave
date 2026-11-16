#!/bin/sh
#
# Assemble a release tarball that extracts into /usr/local as directslave/.
#
#   make-release.sh <freebsd|linux> [arch] [binary] [outdir]
#
#   arch    default amd64
#   binary  default builds/directslave-<os>-<arch>, what build/Build-*.sh produce
#   outdir  default dist/
#
# Paths are resolved from the location of this script, so it works from any
# directory, including build/:
#
#   cd build && ./Build-free-amd64.sh && ../packaging/make-release.sh freebsd
#   packaging/make-release.sh linux amd64
#   packaging/make-release.sh linux arm builds/directslave-linux-arm /tmp/out

set -eu

os=${1:?usage: make-release.sh <freebsd|linux> [arch] [binary] [outdir]}
arch=${2:-amd64}
root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
binary=${3:-${root}/builds/directslave-${os}-${arch}}
outdir=${4:-${root}/dist}

case "${os}" in
    freebsd|linux) ;;
    *) echo "Unknown os: ${os} (freebsd or linux)" >&2; exit 1 ;;
esac

need()
{
    [ -e "$1" ] || { echo "Missing: $1" >&2; exit 1; }
}

need "${binary}"
need "${root}/directslave.go"
need "${root}/etc/directslave.conf.sample"
need "${root}/install/directslave-prepare"
need "${root}/www"
need "${root}/scripts"
need "${root}/docs/INSTALL-FREEBSD-4.txt"
need "${root}/docs/INSTALL-LINUX-4.txt"

version=$(sed -n 's/^[[:space:]]*CURRENT[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' "${root}/directslave.go")
[ -n "${version}" ] || { echo "Cannot read CURRENT from directslave.go" >&2; exit 1; }

# The binary must be the version being packaged. "strings" is not everywhere,
# grep -a is.
if ! grep -a -q "DirectSlave GO/${version} " "${binary}"; then
    echo "${binary} is not version ${version}; rebuild it first." >&2
    exit 1
fi

name="directslave-${version}-${os}-${arch}"
stage=$(mktemp -d)
trap 'rm -rf -- "${stage}"' EXIT HUP INT TERM
dst="${stage}/directslave"

mkdir -p "${dst}/bin" "${dst}/etc/rc.d" "${dst}/docs" "${dst}/log" "${dst}/run" \
         "${dst}/ssl" "${dst}/var/db" "${dst}/var/zones" "${outdir}"

install -m 0755 "${binary}" "${dst}/bin/directslave"
install -m 0755 "${root}/install/directslave-prepare" "${dst}/bin/directslave-prepare"

cp -R "${root}/www" "${dst}/www"
cp -R "${root}/scripts" "${dst}/scripts"
chmod 0755 "${dst}/scripts"/*
cp "${root}/etc/directslave.conf.sample" "${dst}/etc/"

# User documentation: the manual pages and the 3.x history.
cp "${root}"/docs/*.md "${dst}/docs/"
if [ -d "${root}/docs/legacy" ]; then
    cp -R "${root}/docs/legacy" "${dst}/docs/legacy"
fi

# Top-level files, when the tree has them.
for file in LICENSE README.md CHANGELOG.md; do
    if [ -f "${root}/${file}" ]; then
        cp "${root}/${file}" "${dst}/"
    fi
done

case "${os}" in
    freebsd)
        need "${root}/etc/rc.d/freebsd.directslave"
        install -m 0555 "${root}/etc/rc.d/freebsd.directslave" "${dst}/etc/rc.d/freebsd.directslave"
        cp "${root}/docs/INSTALL-FREEBSD-4.txt" "${dst}/"
        ;;
    linux)
        need "${root}/install/install-linux.sh"
        need "${root}/etc/systemd/directslave.service"
        need "${root}/etc/apparmor"
        need "${root}/etc/selinux/install.sh"
        if [ -f "${root}/etc/rc.d/linux.directslave" ]; then
            install -m 0755 "${root}/etc/rc.d/linux.directslave" "${dst}/etc/rc.d/linux.directslave"
        fi
        cp -R "${root}/etc/systemd" "${root}/etc/apparmor" "${root}/etc/selinux" "${dst}/etc/"
        chmod 0755 "${dst}/etc/selinux/install.sh"
        install -m 0755 "${root}/install/install-linux.sh" "${dst}/install-linux.sh"
        cp "${root}/docs/INSTALL-LINUX-4.txt" "${dst}/"
        ;;
esac

# Never ship credentials, state or desktop junk.
rm -f "${dst}/var/db/passwd" "${dst}/etc/directslave.conf"
find "${dst}" \( -name Thumbs.db -o -name .DS_Store \) -exec rm -f {} +

# A sample that still carries somebody's test addresses would make a fresh
# install fail to bind.
if grep -E '^dns_listen_ip[46][[:space:]]' "${dst}/etc/directslave.conf.sample" | grep -v -E '[[:space:]](\*|none)[[:space:]]*$' >/dev/null; then
    echo "etc/directslave.conf.sample lists specific dns_listen addresses; set them to * before a release." >&2
    exit 1
fi

# Files in the archive belong to root whoever builds it: GNU tar spelling
# first, then bsdtar (FreeBSD), then whatever tar there is.
tar -C "${stage}" --owner=0 --group=0 --numeric-owner -czf "${outdir}/${name}.tar.gz" directslave 2>/dev/null \
    || tar -C "${stage}" --uid 0 --gid 0 --numeric-owner -czf "${outdir}/${name}.tar.gz" directslave 2>/dev/null \
    || tar -C "${stage}" -czf "${outdir}/${name}.tar.gz" directslave

( cd "${outdir}" && { sha256sum "${name}.tar.gz" 2>/dev/null || sha256 -r "${name}.tar.gz"; } > "${name}.tar.gz.sha256" )

echo "${outdir}/${name}.tar.gz"
