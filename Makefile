.PHONY: build test clean

build:
	go build -o erg .

test:
	go test -p=1 -count=1 ./...

clean:
	go clean -cache
	rm -f erg 
