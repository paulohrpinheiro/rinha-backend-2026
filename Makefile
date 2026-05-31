.PHONY: build test bench bench-go bench-load docker-build docker-push docker-tag-latest docker-up docker-down docker-logs docker-up-submission submission-file clean all

BINARY_API    := bin/api
LDFLAGS       := -s -w
GOBUILD       := CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath

# Docker Hub settings
IMAGE_NAME_API   ?= paulohrpinheiro/rinha-api
IMAGE_NAME_NGINX ?= paulohrpinheiro/rinha-nginx
VERSION          ?= latest

build:
	@mkdir -p bin
	$(GOBUILD) -o $(BINARY_API) ./cmd/api

test:
	go test ./... -v

bench: bench-go bench-load

bench-go:
	go test ./... -bench=. -benchmem

bench-load:
	@echo "Running load benchmark against http://localhost:9999"
	@echo "Make sure 'docker compose up -d' is running first."
	@echo ""
	scripts/bench.sh http://localhost:9999 5000 20

docker-build:
	docker build -t $(IMAGE_NAME_API):$(VERSION) -f Dockerfile.api .
	docker build -t $(IMAGE_NAME_NGINX):$(VERSION) -f Dockerfile.nginx .

docker-push:
	docker push $(IMAGE_NAME_API):$(VERSION)
	docker push $(IMAGE_NAME_NGINX):$(VERSION)

docker-tag-latest:
	docker tag $(IMAGE_NAME_API):$(VERSION) $(IMAGE_NAME_API):latest
	docker tag $(IMAGE_NAME_NGINX):$(VERSION) $(IMAGE_NAME_NGINX):latest
	docker push $(IMAGE_NAME_API):latest
	docker push $(IMAGE_NAME_NGINX):latest

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
	@sed 's|image: $(IMAGE_NAME_API):.*|image: $(IMAGE_NAME_API):$(VERSION)|g; s|image: $(IMAGE_NAME_NGINX):.*|image: $(IMAGE_NAME_NGINX):$(VERSION)|g' docker-compose.submission.yml

clean:
	rm -rf bin/

all: build test
