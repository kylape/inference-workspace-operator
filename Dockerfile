FROM docker.io/library/golang:1.25 AS builder

WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOMAXPROCS=2 GOFLAGS=-p=1 CGO_ENABLED=0 go build -o /manager ./cmd/manager

FROM docker.io/alpine/helm:3.19.0 AS helm
RUN mkdir -p /charts && \
    helm pull oci://quay.io/klape/charts/vcluster --version 0.0.1-combined.4 --destination /charts && \
    echo "c4c5f1fc5fd4b6f8e7bad290fcf756825cf5fbc040a18077ba86e689ed771c59  /charts/vcluster-0.0.1-combined.4.tgz" | sha256sum -c -

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /manager /manager
COPY --from=helm /usr/bin/helm /helm
COPY --from=helm /charts/vcluster-0.0.1-combined.4.tgz /charts/vcluster-0.0.1-combined.4.tgz
USER 65532:65532
ENTRYPOINT ["/manager"]
