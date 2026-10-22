#!/bin/sh
# Compile the declared target matrix. Compilation is not runtime validation.
set -eu

version="${VERSION:-dev}"
commit="${COMMIT:-none}"
build_date="${BUILD_DATE:-unknown}"
module="$(go list -m)"
output_dir="${OUTPUT_DIR:-dist}"

mkdir -p "$output_dir"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
	goos="${target%/*}"
	goarch="${target#*/}"
	extension=""
	if [ "$goos" = "windows" ]; then
		extension=".exe"
	fi

	output="$output_dir/abouei-checker_${version}_${goos}_${goarch}${extension}"
	printf 'Building %s\n' "$output"

	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build \
		-trimpath \
		-ldflags "-s -w \
			-X ${module}/internal/buildinfo.Version=${version} \
			-X ${module}/internal/buildinfo.Commit=${commit} \
			-X ${module}/internal/buildinfo.BuildDate=${build_date}" \
		-o "$output" \
		./cmd/abouei-checker
done
