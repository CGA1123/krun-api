.PHONY: build clean agent vmm

build: agent vmm
	go build -o krun-api ./cmd/krun-api
	go build -o krun-run ./cmd/krun-run

agent:
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o vminit-agent ./cmd/vminit-agent

vmm:
	go build -o krun-vmm ./cmd/krun-vmm
	codesign --sign - --entitlements entitlements.plist --force krun-vmm

clean:
	rm -f krun-api krun-run krun-vmm vminit-agent
