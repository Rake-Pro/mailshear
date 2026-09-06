# mailshear - developer tasks. `make help` lists targets.
APP     := mailshear
PKG     := ./cmd/mailshear
BIN     := bin/$(APP)
LDFLAGS := -s -w

.PHONY: help
help: ## Show this help.
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-10s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build bin/mailshear.
	./build.sh

.PHONY: install
install: ## go install so `mailshear` works from any shell; adds GOPATH/bin to your shell rc (NO_ADD_TO_PATH=1 to skip).
	./build.sh --install $(if $(NO_ADD_TO_PATH),--no-add-to-path,)

.PHONY: test
test: ## Run tests.
	go test ./...

.PHONY: vet
vet: ## gofmt check and go vet.
	@test -z "$$(gofmt -l cmd internal tools)" || { gofmt -l cmd internal tools; exit 1; }
	go vet ./...

.PHONY: demos
demos: ## Re-record the docs/demo GIFs (needs a Python with pyte and Pillow).
	./tools/demo/record-all.sh

.PHONY: tidy
tidy: ## go mod tidy + go mod vendor (dependencies are vendored).
	go mod tidy
	go mod vendor
