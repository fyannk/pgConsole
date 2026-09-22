# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:4cb7ac979db5fcc41cae44b2227ba5ab8a51e8807f40d9ba4dee20a0ad960b5b AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
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
# The vendored front-end assets are embedded in the binary above, so their
# licences have to travel with the image that carries them.
COPY third_party/ /licenses/pgconsole/third_party/
USER 65532:65532
EXPOSE 3000
ENTRYPOINT ["/pgconsole"]
