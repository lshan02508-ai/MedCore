FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY cmd/mock-backend ./cmd/mock-backend
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mock-backend ./cmd/mock-backend

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/mock-backend /mock-backend
EXPOSE 8000
ENTRYPOINT ["/mock-backend"]
