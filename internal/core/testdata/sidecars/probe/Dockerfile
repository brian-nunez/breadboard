FROM golang:1.27.4-alpine3.24 AS dev
WORKDIR /src
COPY . .
RUN go build -o /usr/local/bin/probe .
CMD ["probe", "serve"]
