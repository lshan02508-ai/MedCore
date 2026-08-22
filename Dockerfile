FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gollm-gateway ./cmd/gateway

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gollm-gateway /gollm-gateway
COPY configs/gateway.json /etc/gollm/gateway.json
EXPOSE 8080
ENTRYPOINT ["/gollm-gateway"]
CMD ["-config", "/etc/gollm/gateway.json"]
