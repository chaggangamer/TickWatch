FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tickwatch ./cmd/tickwatch \
	&& mkdir -p /out/data && chown 65532:65532 /out/data

FROM scratch
LABEL org.opencontainers.image.title="Tickwatch"
LABEL org.opencontainers.image.description="Self-hosted uptime and cron monitoring"
LABEL org.opencontainers.image.source="https://github.com/chaggangamer/tickwatch"
LABEL org.opencontainers.image.licenses="MIT"
COPY --from=build /out/tickwatch /tickwatch
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=alpine:3.21 /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
USER 65532:65532
WORKDIR /data
ENV TICKWATCH_DB_PATH=/data/tickwatch.db
EXPOSE 8080
VOLUME /data
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/tickwatch", "healthcheck", "http://localhost:8080/healthz"]
ENTRYPOINT ["/tickwatch"]
CMD ["serve"]
