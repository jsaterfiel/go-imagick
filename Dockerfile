FROM golang:1.8-alpine

WORKDIR /go/src/app
COPY . .

RUN apk add --no-cache --virtual build-stuff pkgconfig \
    build-base \
    libc6-compat \
    imagemagick-dev \
    git \
    && apk add --no-cache imagemagick

RUN go-wrapper download   # "go get -d -v ./..."
RUN go-wrapper install    # "go install -v ./..."

# RUN apk del build-stuff
RUN go build main.go

CMD ["/go/src/app/main"]
