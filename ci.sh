#!/usr/bin/env bash
# CI gate, run from the repo root. It checks and never rewrites files.
set -euo pipefail

GO="${GO:-go}"

# gofmt, not go fmt: go fmt rewrites in place.
echo "== gofmt =="
unformatted="$(gofmt -l .)"
if [ -n "$unformatted" ]; then
	echo "gofmt would reformat (run gofmt -w on them):"
	echo "$unformatted"
	exit 1
fi

echo "== go vet =="
$GO vet ./...

echo "== staticcheck =="
$GO tool staticcheck ./...

echo "== go build =="
$GO build ./...

echo "== go test -race =="
$GO test -race ./...

echo "OK"
