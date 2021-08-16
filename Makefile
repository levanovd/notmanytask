all: web

protos:
	true

make_build:
	mkdir -p build

get_statik:
	go get -u github.com/rakyll/statik

statik: get_statik protos
	statik -f -Z -src web -dest pkg

web: make_build statik
	go build -o build ./cmd/web

callback: make_build statik
	go build -o build ./cmd/callback

all: web callback

run_web: web
	./build/web