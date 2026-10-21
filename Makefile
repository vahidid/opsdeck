VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build web test demo clean release

all: build

web:
	cd web && npm ci && npm run build

build: web
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/opsdeck ./cmd/opsdeck

test:
	go test ./...
	cd web && npm run typecheck

# Local SSH servers (bastion + app) and a scripted model, for trying the UI.
demo:
	go build -o bin/opsdeck-demo ./tools/demo
	@echo "run: bin/opsdeck-demo -pubkey \"<public key of a vault ssh-key>\""

release: web
	@for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=; [ $$os = windows ] && ext=.exe; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/opsdeck-$$os-$$arch$$ext ./cmd/opsdeck || exit 1; \
	done

clean:
	rm -rf bin dist internal/server/dist/assets internal/server/dist/index.html
