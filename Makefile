bin/canon: *.go go.mod go.sum canon_setup.sh
	go build -o bin/canon .

bin/golangci-lint:
	GOBIN=`pwd`/bin go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest

lint: bin/golangci-lint
	bin/golangci-lint run -v --fix

clean:
	git clean -fxd
