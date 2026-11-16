#!/bin/sh

set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v selinuxenabled >/dev/null 2>&1 || ! selinuxenabled; then
    echo "SELinux is not enabled; nothing to install."
    exit 0
fi

for tool in make semodule restorecon; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "Missing ${tool}; install selinux-policy-devel and policycoreutils." >&2
        exit 1
    fi
done

work=$(mktemp -d)
trap 'rm -rf -- "${work}"' EXIT HUP INT TERM

cp "${here}/directslave.te" "${here}/directslave.fc" "${work}/"
make -C "${work}" -f /usr/share/selinux/devel/Makefile directslave.pp
semodule -i "${work}/directslave.pp"
restorecon -RFv /usr/local/directslave

echo "DirectSlave SELinux policy installed."
