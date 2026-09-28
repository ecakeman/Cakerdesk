FROM docker.m.daocloud.io/library/golang:1.25-bookworm
WORKDIR /src
COPY go/go.mod go/go.sum ./
RUN go mod download
COPY go/ ./
COPY schemas/ /schemas/
RUN CGO_ENABLED=0 go build -o /usr/local/bin/cakerdesk ./cmd/cakerdesk
ENTRYPOINT ["cakerdesk"]
