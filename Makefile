.PHONY: run build test vet clean

run:
	go run ./cmd/simple-magpie -config config/config.json

build:
	go build -o bin/simple-magpie ./cmd/simple-magpie

test:
	go test ./...

vet:
	go vet ./...

clean:
	rm -rf bin
