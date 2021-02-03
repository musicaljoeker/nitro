.PHONY: docker docs

VERSION ?= 2.0.0-beta.2

build:
	go build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitro ./cmd/nitro
build-macos:
	GOOS=darwin go build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitro ./cmd/nitro
build-macos-arm:
	GOOS=darwin GOARH=arm64 go1.16rc1 build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitro ./cmd/nitro
build-api:
	go build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitrod ./cmd/nitrod
build-win:
	GOOS="windows" go build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitro.exe ./cmd/nitro
build-linux:
	GOOS=linux go build -trimpath -ldflags="-s -w -X 'github.com/craftcms/nitro/command/version.Version=${VERSION}'" -o nitro ./cmd/nitro
upx: build
	upx --brute nitro

beta: beta-macos beta-macos-arm beta-win beta-linux
beta-macos: build-macos
	zip -X macos_nitro_${VERSION}.zip nitro
	rm nitro
beta-macos-arm: build-macos-arm
	zip -X macos_arm_nitro_${VERSION}.zip nitro
	rm nitro
beta-win: build-win
	zip -X windows_nitro_${VERSION}.zip nitro.exe
	rm nitro.exe
beta-linux: build-linux
	zip -X linux_nitro_${VERSION}.zip nitro
	rm nitro

upx-macos:
	upx --brute nitro
upx-win:
	upx --brute nitro.exe
upx-linux:
	upx --brute nitro

docker:
	docker build --build-arg NITRO_VERSION=${VERSION} -t craftcms/nitro-proxy:${VERSION} .
docs:
	go run cmd/docs/main.go

local: build
	mv nitro /usr/local/bin/nitro
local-win: build-win
	mv nitro.exe "${HOME}"/Nitro/nitro.exe
local-linux: build
	sudo mv nitro /usr/local/bin/nitro
local-prod: build upx
	mv nitro /usr/local/bin/nitro

dev: rm docker init
rm:
	docker container rm -f nitro-v2
init:
	nitro init

test:
	go test ./...
coverage:
	go test -v ./... -coverprofile profile.out
	go tool cover -html=profile.out
vet:
	go vet ./...

releaser:
	goreleaser --skip-publish --rm-dist --skip-validate

win-home:
	mkdir "${HOME}"/Nitro

proto:
	protoc protob/nitrod.proto --go_out=plugins=grpc:.
