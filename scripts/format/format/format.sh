#!/usr/bin/env bash
set -e

go mod tidy
tag_name="$(yq '.jobs.super-linter.steps[-1].uses' .github/workflows/super-linter.yml | sed -e 's;/slim@.*;:slim;g')"
tag_version="$(yq '.jobs.super-linter.steps[-1].uses | line_comment' .github/workflows/super-linter.yml)"
version="$(docker run --rm --entrypoint '' "ghcr.io/${tag_name}-${tag_version}" /bin/sh -c 'goreleaser -v' | grep GitVersion | sed -e 's/GitVersion: *//g')"
yq -i ".jobs.goreleaser.steps[-1].with.version|=\"v$version\"" .github/workflows/release.yml
