#!/bin/bash
set -e

echo "Building Expeditus Web for Linux..."
cd "$(dirname "$0")/../.."

GOOS=linux GOARCH=amd64 go build -o dist/expeditus-web-linux-amd64 ./cmd/expeditus-web/

echo "Build complete: dist/expeditus-web-linux-amd64"
