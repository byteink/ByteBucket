.PHONY: ui dev build test vet clean

WEB_DIR := web
DIST_SRC := $(WEB_DIR)/dist
DIST_DST := internal/webui/dist

ui:
	cd $(WEB_DIR) && npm ci --no-audit --no-fund
	cd $(WEB_DIR) && npm run build
	find $(DIST_DST) -mindepth 1 ! -name .keep -delete
	cp -R $(DIST_SRC)/. $(DIST_DST)/

dev: ui
	go run ./cmd/ByteBucket

build: ui
	CGO_ENABLED=0 go build -o ./build/ByteBucket ./cmd/ByteBucket

vet:
	go vet ./...

test:
	go test -count=1 ./...

clean:
	rm -rf $(DIST_SRC) $(DIST_DST) $(WEB_DIR)/node_modules ./build
