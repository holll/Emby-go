#!/bin/sh
# Static audit for the root flat deployment. Never starts the application,
# opens SQLite or changes files; ownership/mode and hash checks are read-only.
set -eu

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || fail 'usage: sh verify-native-install.sh EXPECTED_BINARY_SHA256 [ABSOLUTE_STAGING_ROOT]'
root=${2:-/}
case "$root" in /*) ;; *) fail 'staging root must be absolute';; esac
[ -d "$root" ] || fail 'staging root is not a directory'
root=${root%/}
# A staging root prefixes file paths only; it is not a chroot.
expected=$1
[ "${#expected}" -eq 64 ] || fail 'expected SHA256 must contain 64 hexadecimal characters'
case "$expected" in *[!0123456789abcdefABCDEF]*) fail 'expected SHA256 must contain 64 hexadecimal characters';; esac
expected=$(printf '%s' "$expected" | tr 'A-F' 'a-f')

check() {
    path=$1 want=$2
    actual=$(stat -L -c '%u:%g:%a' "$path") || fail "cannot stat $path"
    [ "$actual" = "$want" ] || fail "$path ownership/mode is $actual; expected $want"
    printf 'OK: %s ownership/mode\n' "$path"
}

binary=$root/opt/emby-go/emby-go
prefix=$root/opt/emby-go
config_dir=$root/etc/emby-go
config=$config_dir/config.yaml
[ -f "$binary" ] || fail 'application binary is not a regular file'
[ -x "$binary" ] || fail 'application binary is not executable'
check "$binary" '0:0:755'
check "$prefix" '0:0:755'
[ -d "$config_dir" ] || fail 'configuration directory is missing'
[ -f "$config" ] || fail 'configuration is not a regular file'
check "$config_dir" '0:0:755'
check "$config" '0:0:600'

actual=$(sha256sum "$binary") || fail 'cannot hash application binary'
actual=${actual%% *}
[ "$actual" = "$expected" ] || fail 'application SHA256 does not match the supplied candidate'
printf 'OK: candidate SHA256\n'
printf '%s\n' \
    'PASS: static installation checks only' \
    'NOT CHECKED: configuration content, database/schema, systemd Unit/Install and effective policy, media ACL/mounts, running process, startup/shutdown and upgrade/restore' \
    'A staging root changes file paths only; ownership/mode and hash checks are read-only.' \
    'No files, accounts or services were changed. This result is not M1 release approval.'
