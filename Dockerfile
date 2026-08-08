FROM golang:1.26.2-trixie AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

ARG TARGETOS=linux
ARG TARGETARCH

RUN arch="${TARGETARCH:-$(go env GOARCH)}" \
	&& CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$arch" \
	go build -mod=readonly -trimpath -buildvcs=false -ldflags="-s -w" -o /out/tessa ./cmd/tessa \
	&& mkdir -p /out/data /out/etc/tessa

FROM scratch

WORKDIR /

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /out/tessa /usr/local/bin/tessa
COPY --from=build --chown=65532:65532 /out/data /data
COPY --from=build --chown=65532:65532 /out/etc /etc

USER 65532:65532

ENV HEALTHCHECK_ADDR=127.0.0.1:9090

STOPSIGNAL SIGTERM
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
	CMD ["/usr/local/bin/tessa", "check-health"]

ENTRYPOINT ["/usr/local/bin/tessa"]
CMD ["run"]
