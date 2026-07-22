#!/bin/bash

set -euo pipefail

SOURCE_DIR=$(cd "$(dirname "$0")/../.." && pwd)

assert_file_contains() {
    local file=$1
    local pattern=$2

    if ! grep -Eq -- "$pattern" "$SOURCE_DIR/$file"; then
        echo "$file does not contain expected pattern: $pattern" >&2
        return 1
    fi
}

assert_file_contains package/systemd/chatops.service '^User=chatops$'
assert_file_contains package/systemd/chatops.service '^EnvironmentFile=-/etc/chatops/chatops.env$'
assert_file_contains package/systemd/chatops.service '--chat=\$\{CHAT\}'
assert_file_contains package/systemd/chatops.service '--planner=\$\{PLANNER\}'
assert_file_contains package/systemd/chatops.service '--credentials=\$\{CRED_STORE\}'
assert_file_contains package/systemd/chatops.service '--connection-id=\$\{CONNECTION_ID\}'
assert_file_contains package/systemd/chatops.service '--max-concurrency=\$\{MAX_CONCURRENCY\}'
assert_file_contains package/systemd/chatops.service '--log-level=\$\{LOG_LEVEL\}'
assert_file_contains package/systemd/chatops.service '--log-format=\$\{LOG_FORMAT\}'
assert_file_contains package/systemd/chatops.service '\$EXTRA_ARGS$'
assert_file_contains package/systemd/chatops.env '^CHAT='
assert_file_contains package/systemd/chatops.env '^PLANNER='
assert_file_contains package/systemd/chatops.env '^CRED_STORE='
assert_file_contains package/systemd/chatops.env '^CONNECTION_ID=default$'
assert_file_contains package/systemd/chatops.env '^MAX_CONCURRENCY=8$'
assert_file_contains package/systemd/chatops.env '^LOG_LEVEL=info$'
assert_file_contains package/systemd/chatops.env '^LOG_FORMAT=json$'
assert_file_contains package/systemd/chatops.env '^EXTRA_ARGS='

if grep -Eq 'CHATOPS_ARGS|CREDENTIALS' "$SOURCE_DIR/package/systemd/chatops.env" "$SOURCE_DIR/package/systemd/chatops.service"; then
    echo "systemd configuration contains a retired environment setting" >&2
    exit 1
fi

assert_file_contains package/scripts/build-deb.sh 'package/systemd/chatops.service'
assert_file_contains package/scripts/build-deb.sh 'package/systemd/chatops.env'
assert_file_contains package/deb/DEBIAN/postinst 'systemctl daemon-reload'
assert_file_contains package/deb/DEBIAN/prerm 'systemctl stop chatops.service'
assert_file_contains package/deb/DEBIAN/conffiles '^/etc/chatops/chatops.env$'

assert_file_contains package/rpm/chatops.spec '^BuildRequires:[[:space:]]+systemd-rpm-macros$'
assert_file_contains package/rpm/chatops.spec '%systemd_post chatops.service'
assert_file_contains package/rpm/chatops.spec '%systemd_preun chatops.service'
assert_file_contains package/rpm/chatops.spec '%systemd_postun_with_restart chatops.service'
assert_file_contains package/rpm/chatops.spec '^%\{_unitdir\}/%\{name\}\.service$'
assert_file_contains package/rpm/chatops.spec '^%config\(noreplace\).*%\{_sysconfdir\}/%\{name\}/%\{name\}\.env$'
