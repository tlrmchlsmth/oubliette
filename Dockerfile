FROM alpine/helm:3.20.2 AS chart
RUN mkdir -p /charts && \
    helm pull vcluster --repo https://charts.loft.sh --version 0.36.1 --destination /charts && \
    echo "84a6aa28ffd2504069ed987202238de85509c50050748fb2da4fd262a6861b35  /charts/vcluster-0.36.1.tgz" | sha256sum -c -

FROM golang:1.25.6 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api api
COPY cmd cmd
COPY internal internal
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/controller ./cmd/controller && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/mcp ./cmd/mcp && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/metrics-gateway ./cmd/metrics-gateway && \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/evidence-export ./cmd/evidence-export

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/controller /controller
COPY --from=build /out/mcp /mcp
COPY --from=build /out/metrics-gateway /metrics-gateway
COPY --from=build /out/evidence-export /evidence-export
COPY --from=chart /charts/vcluster-0.36.1.tgz /charts/vcluster-0.36.1.tgz
USER 65532:65532
ENTRYPOINT ["/controller"]
