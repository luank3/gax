#!/bin/sh
set -eu

module=$(go mod edit -json)
if ! printf '%s\n' "$module" | jq -e '((.Require // []) | length == 0) and ((.Tool // []) | length == 0)' > /dev/null; then
    echo 'go.mod must not contain require or tool directives' >&2
    exit 1
fi
