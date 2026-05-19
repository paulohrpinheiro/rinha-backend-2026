.PHONY: build test bench docker-build docker-push docker-tag-latest docker-up docker-down docker-logs docker-up-submission submission-file clean all warmup

BINARY_API    := bin/api
BINARY_PROXY  := bin/proxy
LDFLAGS       := -s -w
GOBUILD       := CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath

# Docker Hub settings
IMAGE_NAME_API   ?= paulohrpinheiro/rinha-api
IMAGE_NAME_PROXY ?= paulohrpinheiro/rinha-proxy
VERSION          ?= latest

build:
	@mkdir -p bin
	$(GOBUILD) -o $(BINARY_API) ./cmd/api
	$(GOBUILD) -o $(BINARY_PROXY) ./cmd/proxy

test:
	go test ./... -v

bench:
	go test ./... -bench=. -benchmem

docker-build:
	docker build -t $(IMAGE_NAME_API):$(VERSION) -f Dockerfile.api .
	docker build -t $(IMAGE_NAME_PROXY):$(VERSION) -f Dockerfile.proxy .

docker-push:
	docker push $(IMAGE_NAME_API):$(VERSION)
	docker push $(IMAGE_NAME_PROXY):$(VERSION)

docker-tag-latest:
	docker tag $(IMAGE_NAME_API):$(VERSION) $(IMAGE_NAME_API):latest
	docker tag $(IMAGE_NAME_PROXY):$(VERSION) $(IMAGE_NAME_PROXY):latest
	docker push $(IMAGE_NAME_API):latest
	docker push $(IMAGE_NAME_PROXY):latest

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

docker-up-submission:
	docker compose -f docker-compose.submission.yml up -d

# Generate the docker-compose.yml content for the submission branch
# with the current VERSION baked into image tags.
# Usage: make submission-file VERSION=v2 > /tmp/dc.yml
submission-file:
	@sed 's|image: $(IMAGE_NAME_API):.*|image: $(IMAGE_NAME_API):$(VERSION)|g; s|image: $(IMAGE_NAME_PROXY):.*|image: $(IMAGE_NAME_PROXY):$(VERSION)|g' docker-compose.submission.yml

clean:
	rm -rf bin/

all: build test
