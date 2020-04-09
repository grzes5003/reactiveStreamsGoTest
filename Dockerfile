FROM golang:1.14.0-alpine

LABEL maintainer="Noone"

ENV GO111MODULE=on
# ENV PORT=21370

WORKDIR /app

COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .

RUN go build -o main .

EXPOSE 80
EXPOSE 7878

# Command to run the executable
CMD ["./main"]