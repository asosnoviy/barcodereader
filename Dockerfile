# Сборка и запуск. Образ собирается без cgo: PDFium приезжает в виде
# WebAssembly-модуля, поэтому нужен только Go.
FROM golang:1.27-alpine AS build

WORKDIR /src

# Сначала зависимости — слой кэшируется отдельно от исходников.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 даёт статический бинарник, который запускается в scratch.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/barcodereader ./cmd/barcodereader

FROM scratch

COPY --from=build /out/barcodereader /barcodereader

# 8080/tcp — healthz, api/v1/decode
EXPOSE 8080
USER 65532:65532

ENV ADDR=:8080

ENTRYPOINT ["/barcodereader"]