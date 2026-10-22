# syntax=docker/dockerfile:1

# GO_IMAGE can be overridden to use an approved registry mirror.
ARG GO_IMAGE=golang:1.27.3
FROM ${GO_IMAGE} AS build

WORKDIR /src

# The official Go image includes CA certificates; copying them avoids a network
# package installation in the build stage.

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ARG COMMIT=none
ARG BUILD_DATE=unknown

RUN CGO_ENABLED=0 go build -trimpath \
	-ldflags "-s -w \
		-X github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo.Version=${VERSION} \
		-X github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo.Commit=${COMMIT} \
		-X github.com/Erfan-Abouei/Abouei_Checker/internal/buildinfo.BuildDate=${BUILD_DATE}" \
	-o /out/abouei-checker ./cmd/abouei-checker \
	&& mkdir -p /out/data

FROM scratch

ARG VERSION=dev

LABEL org.opencontainers.image.title="AboueiChecker" \
	org.opencontainers.image.description="Backend health monitoring CLI with webhook notifications" \
	org.opencontainers.image.url="https://github.com/Erfan-Abouei/Abouei_Checker" \
	org.opencontainers.image.source="https://github.com/Erfan-Abouei/Abouei_Checker" \
	org.opencontainers.image.licenses="MIT" \
	org.opencontainers.image.version="${VERSION}"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/abouei-checker /usr/local/bin/abouei-checker
COPY --from=build --chown=65532:65532 /out/data /data

ENV HOME=/data
ENV ABOUEI_CONFIG=/etc/abouei/abouei.yaml

WORKDIR /data
USER 65532:65532
STOPSIGNAL SIGTERM

ENTRYPOINT ["/usr/local/bin/abouei-checker"]
CMD ["version"]
