FROM golang:1.23-alpine AS builder

WORKDIR /app

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ENV GOARCH=amd64
RUN go build -o forgetai-backend

FROM alpine:latest

WORKDIR /root/

COPY --from=builder /app/forgetai-backend .

EXPOSE 8080

CMD ["./forgetai-backend"]