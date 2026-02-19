.PHONY: build test clean

build:
	go build -o plural-agent .

test:
	go test -count=1 ./...

clean:
	go clean -cache
	rm -f plural-agent
