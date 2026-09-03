FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /out/gitcrackind ./cmd/gitcrackind

FROM gcr.io/distroless/static-debian12
COPY --from=builder /out/gitcrackind /gitcrackind
EXPOSE 8080
ENTRYPOINT ["/gitcrackind"]
