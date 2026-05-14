.PHONY: build test bench docker-build docker-up docker-down docker-logs clean all

BINARY_API    := bin/api
BINARY_PROXY  := bin/proxy
LDFLAGS       := -s -w
GOBUILD       := CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath

build:
	@mkdir -p bin
	$(GOBUILD) -o $(BINARY_API) ./cmd/api
	$(GOBUILD) -o $(BINARY_PROXY) ./cmd/proxy

test:
	go test ./... -v

bench:
	go test ./... -bench=. -benchmem

docker-build:
	docker build -t rinha-api -f Dockerfile.api .
	docker build -t rinha-proxy -f Dockerfile.proxy .

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

clean:
	rm -rf bin/

all: build test
