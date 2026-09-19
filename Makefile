.PHONY: check-version fmt-check lint test test-race test-integration build vuln release-check verify

check-version:
	bash scripts/check-version.sh

fmt-check:
	test -z "$$(gofmt -l $$(find . -name '*.go'))"

lint:
	go vet ./...

test:
	go test ./...

test-race:
	go test -race ./...

test-integration:
	go test -count=1 -race -tags=integration ./...

build:
	go build ./...

vuln:
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

release-check:
	bash .github/scripts/release-version-test.sh

verify: check-version fmt-check lint test-race build release-check
