# Build stage: a static binary, so the runtime image needs no libc.
FROM cgr.dev/chainguard/go:latest AS build
WORKDIR /src
# Download modules before the source is copied, so the layer is cached.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gobb ./cmd/gobb \
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
