FROM golang:1.24.12-bookworm@sha256:322a794c4289ad86e3043e5d9f16ead790463658a1bd2c3983427f02983ab978 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /out/stockroom ./cmd/stockroom && \
    CGO_ENABLED=0 go build -trimpath -o /out/reconcile ./cmd/reconcile

FROM scratch
COPY --from=build /out/ /
USER 65532:65532
ENTRYPOINT ["/stockroom"]
CMD ["-addr=:8080"]
