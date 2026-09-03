.PHONY: build run test vet fmt up down logs clean

BINARY := bin/gitcrackind

build: ## Compile the server binary to bin/gitcrackind.
	go build -o $(BINARY) ./cmd/gitcrackind

run: ## Run the server directly (no build step, no Docker).
	go run ./cmd/gitcrackind

test: ## Run the full test suite.
	go test ./...

vet: ## Run go vet across the module.
	go vet ./...

fmt: ## Check formatting; fails if gofmt would change anything.
	@test -z "$$(gofmt -l .)" || (gofmt -l . && echo 'gofmt: files need formatting' && exit 1)

up: ## Build and start the containerized server (requires a running Docker daemon).
	docker compose up --build -d

down: ## Stop and remove the containerized server.
	docker compose down

logs: ## Follow the containerized server's logs.
	docker compose logs -f

clean: ## Remove build artifacts.
	rm -rf bin
