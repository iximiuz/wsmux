GIT_COMMIT=$(shell git rev-parse --verify HEAD)
UTC_NOW=$(shell date -u +"%Y-%m-%dT%H:%M:%SZ")

build-dev:
	go build \
		-ldflags="-X 'main.version=dev' -X 'main.commit=${GIT_COMMIT}' -X 'main.date=${UTC_NOW}'" \
		-o wsmux \
		cmd/wsmux/main.go

release:
	goreleaser --clean

release-snapshot:
	goreleaser release --snapshot --clean
