#!/bin/sh

set -eu

prefix=/usr/local/directslave

if [ "$(id -u)" -ne 0 ]; then
    echo "Run this installer as root." >&2
    exit 1
fi

if [ ! -x "${prefix}/bin/directslave" ]; then
    echo "Extract the archive into /usr/local first." >&2
    exit 1
fi

# BIND utilities and libraries may coexist with DirectSlave. Only packages
# that install the named daemon conflict with the integrated DNS listener.
#for package in bind9 bind named bind9-server; do
#    if command -v dpkg-query >/dev/null 2>&1 && \
#        dpkg-query -W -f='${db:Status-Status}' "${package}" 2>/dev/null | grep -qx installed; then
#        echo "Conflicting DNS server package installed: ${package}" >&2
#        exit 1
#    fi
#    if command -v rpm >/dev/null 2>&1 && rpm -q "${package}" >/dev/null 2>&1; then
#        echo "Conflicting DNS server package installed: ${package}" >&2
#        exit 1
#    fi
#done

for service in bind9.service named.service; do
    if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet "${service}"; then
        echo "Conflicting DNS service is active: ${service}" >&2
        exit 1
    fi
done

if [ ! -e "${prefix}/etc/directslave.conf" ]; then
    cp "${prefix}/etc/directslave.conf.sample" "${prefix}/etc/directslave.conf"
    key=$(od -An -N48 -tx1 /dev/urandom | tr -d ' \n')
    sed -i "s/CHANGE_THIS_TO_A_LONG_RANDOM_VALUE/${key}/" "${prefix}/etc/directslave.conf"
    chmod 0600 "${prefix}/etc/directslave.conf"
    echo "Created ${prefix}/etc/directslave.conf"
fi

if command -v systemctl >/dev/null 2>&1; then
    ln -sfn "${prefix}/etc/systemd/directslave.service" /etc/systemd/system/directslave.service
    systemctl daemon-reload
fi

if command -v apparmor_parser >/dev/null 2>&1; then
    cp "${prefix}/etc/apparmor/usr.local.directslave.bin.directslave" \
        /etc/apparmor.d/usr.local.directslave.bin.directslave
    apparmor_parser -r /etc/apparmor.d/usr.local.directslave.bin.directslave
    echo "DirectSlave AppArmor profile loaded."
fi

if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then
    "${prefix}/etc/selinux/install.sh"
fi

"${prefix}/bin/directslave-prepare"

if command -v systemctl >/dev/null 2>&1; then
    systemctl enable directslave.service
fi

cat <<'EOF'

Installation prepared, service not started.

Next steps:
  1. Review /usr/local/directslave/etc/directslave.conf
  2. Create API credentials:
       /usr/local/directslave/bin/directslave --password admin
     (asks for the password twice, nothing is echoed)
  3. Verify paths and permissions:
       /usr/local/directslave/bin/directslave --check
  4. Start:
       systemctl start directslave
EOF
