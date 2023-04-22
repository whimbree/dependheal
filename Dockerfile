FROM docker.io/golang:alpine as builder
WORKDIR /build
# download go modules
COPY go.mod go.sum ./
RUN go mod download
# copy the source code
COPY *.go ./
# build
RUN CGO_ENABLED=0 GOOS=linux go build -o dependheal .

FROM docker.io/alpine:latest
COPY --from=builder /build/dependheal /dependheal
CMD ["/dependheal"]