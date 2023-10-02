bin/canon: *.go go.mod go.sum canon_setup.sh
	go build -tags osusergo,netgo -ldflags "-s -w" -o bin/canon .

bin/golangci-lint:
	GOBIN=`pwd`/bin go install github.com/golangci/golangci-lint/cmd/golangci-lint@v1.54.2

lint: bin/golangci-lint
	go mod tidy
	bin/golangci-lint run -v --fix

clean:
	git clean -fxd
