GOFILES := $(shell find . -name '*.go' -not -path './vendor/*')
COVERAGE_MIN ?= 80

.PHONY: build ci fmt fmt-check test test-cover tidy-check vet

build:
	go build ./...

ci: fmt-check tidy-check vet test-cover build

fmt:
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofmt -l $(GOFILES))"

test:
	go test ./...

test-cover:
	go test ./... -race -covermode=atomic -coverprofile=coverage.out
	go tool cover -func=coverage.out
	@coverage="$$(go tool cover -func=coverage.out | awk '/^total:/ {gsub("%", "", $$3); print $$3}')"; \
		awk -v coverage="$$coverage" -v minimum="$(COVERAGE_MIN)" 'BEGIN { if (coverage + 0 < minimum + 0) { printf "coverage %.1f%% is below %.1f%%\n", coverage, minimum; exit 1 } }'

tidy-check:
	go mod tidy
	git diff --exit-code -- go.mod go.sum

vet:
	go vet ./...
