FROM golang:alpine AS builder
RUN apk --update add ca-certificates
WORKDIR /src
COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /go/bin/loadtest

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /go/bin/loadtest /go/bin/loadtest
ENTRYPOINT ["/go/bin/loadtest"]