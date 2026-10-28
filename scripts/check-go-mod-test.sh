#!/bin/sh
set -eu

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
export GOWORK=off GOTOOLCHAIN=local

check() {
    name=$1
    expected=$2
    contents=$3
    printf '%s\n' "$contents" > "$tmp/go.mod"
    if (cd "$tmp" && sh "$root/scripts/check-go-mod.sh") > "$tmp/output" 2>&1; then
        actual=pass
    else
        actual=fail
    fi
    if [ "$actual" != "$expected" ]; then
        printf '%s: got %s, want %s\n' "$name" "$actual" "$expected" >&2
        cat "$tmp/output" >&2
        exit 1
    fi
}

check clean pass 'module example.com/test

go 1.25.0'
check comments pass 'module example.com/test

go 1.25.0
// require example.com/not-a-dependency v1.0.0
// tool example.com/not-a-tool'
check single-require fail 'module example.com/test

go 1.25.0
require example.com/dependency v1.0.0'
check grouped-indirect-require fail 'module example.com/test

go 1.25.0
require (
    example.com/dependency v1.0.0 // indirect
)'
check single-tool fail 'module example.com/test

go 1.25.0
tool example.com/tool/cmd'
check grouped-tool fail 'module example.com/test

go 1.25.0
tool (
    example.com/tool/cmd
)'
check malformed fail 'module example.com/test

go 1.25.0
require ('
printf 'go.mod policy tests passed\n'
