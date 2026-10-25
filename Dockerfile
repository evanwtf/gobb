# Build stage: a static binary, so the runtime image needs no libc. It runs on
# the platform of the builder and cross-compiles for the target, so the build of
# an arm64 image needs no emulation. The runtime stage below has no RUN step for
# the same reason.
FROM --platform=$BUILDPLATFORM cgr.dev/chainguard/go:latest AS build
# BuildKit sets both. An empty value gives the Go default of the builder.
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
# Download modules before the source is copied, so the layer is cached.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/gobb ./cmd/gobb \
 && mkdir -p /out/data

# Runtime stage: no shell, no package manager. The binary checks its own health.
FROM cgr.dev/chainguard/static:latest
COPY --from=build /out/gobb /usr/bin/gobb
# Create the data directory with the right owner before the switch to nonroot,
# so a new volume mounted here is writable.
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/gobb
VOLUME ["/var/lib/gobb"]
USER nonroot
ENV GOBB_LISTEN=0.0.0.0:8080 GOBB_DATA_DIR=/var/lib/gobb
EXPOSE 8080
ENTRYPOINT ["/usr/bin/gobb"]
CMD ["serve"]
