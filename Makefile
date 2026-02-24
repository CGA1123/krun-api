.PHONY: build clean generate init vmm

build: init vmm
	go build -o krun-api ./cmd/krun-api
	go build -o krun-run ./cmd/krun-run

generate:
	buf generate

init:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o vminit ./cmd/vminit

vmm:
	go build -o krun-vmm ./cmd/krun-vmm
	codesign --sign - --entitlements entitlements.plist --force krun-vmm

clean:
	rm -f krun-api krun-run krun-vmm vminit
