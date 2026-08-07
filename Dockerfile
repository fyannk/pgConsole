# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build
ARG TARGETOS
ARG TARGETARCH
# The build stamps the release identity into the binary. It defaults to
# dev so a plain `docker build` is honest about being unreleased rather
# than claiming a version it was not given.
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" -o /out/pgconsole ./cmd/pgconsole

FROM gcr.io/distroless/static-debian13:nonroot@sha256:f7f8f729987ad0fdf6b05eeeae94b26e6a0f613bdf46feea7fc40f7bd72953e6
ARG VERSION=dev
# Annotations for images built from this file directly. The release
# workflow overrides these through the registry metadata, which can also
# supply the revision; a local build has no commit to claim.
LABEL org.opencontainers.image.title="pgConsole" \
      org.opencontainers.image.description="Operational console for one CloudNativePG cluster" \
      org.opencontainers.image.source="https://github.com/fyannk/pgConsole" \
      org.opencontainers.image.documentation="https://fyannk.github.io/pgConsole/" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"
COPY --from=build /out/pgconsole /pgconsole
COPY LICENSE /licenses/pgconsole/LICENSE
USER 65532:65532
EXPOSE 3000
ENTRYPOINT ["/pgconsole"]
