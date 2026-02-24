.PHONY: build run test fuzz cover clean release lint bench vet check tidy

build:
	go build -o vigil ./cmd/vigil

run: build
	./vigil

test:
	go test -race -count=1 ./...

vet:
	go vet ./...

fuzz:

lint:
	golangci-lint cache clean
	golangci-lint run ./...

cover:
	go test -race -count=1 -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out
	@go tool cover -func=coverage.out | awk '/^total:/ { gsub(/%/, "", $$NF); if ($$NF+0 < 70) { printf "FAIL: total coverage %s%% is below threshold 70%%\n", $$NF; exit 1 } else { printf "OK: total coverage %s%% meets threshold 70%%\n", $$NF } }'
	@rm coverage.out

clean:
	rm -rf vigil coverage.out

tidy:
	go mod tidy
	git diff --exit-code go.mod go.sum

check: tidy vet lint cover

release:
	/build-system/build.sh --repo . --ref HEAD --track stable