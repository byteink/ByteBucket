.PHONY: ui dev build test vet pentest e2e-web image-scan clean

WEB_DIR := web
E2E_DIR := e2e
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

# CGO_ENABLED=0 matches the shipped binary (see build target + Dockerfile) and
# sidesteps a cgo init crash in go-m1cpu — a transitive testcontainers dep we
# never call — under Go 1.26+ on darwin/arm64. Its non-cgo stub is selected
# instead, so the E2E suite tests the exact build mode we deploy.
test:
	CGO_ENABLED=0 go test -count=1 ./...

# pentest brings up bytebucket + an isolated attacker container, runs the
# probe suite over the docker network, propagates the attacker's exit code,
# and tears everything down regardless of outcome. CI and the release
# preflight gate on this target.
pentest:
	@trap 'docker compose -f scripts/pentest/docker-compose.yml down -v --remove-orphans >/dev/null 2>&1' EXIT; \
	docker compose -f scripts/pentest/docker-compose.yml up --build --abort-on-container-exit --exit-code-from pentest

# e2e-web drives the admin UI in a real browser (Playwright/Chromium) against a
# freshly-built container. The browser toolchain lives in e2e/ — isolated from
# the web build so the production image never pulls Playwright or its browsers.
# The container is always torn down with its volume, so every run starts from a
# clean store (the fsync/retention specs depend on the default startup state).
e2e-web:
	cd $(E2E_DIR) && npm ci --no-audit --no-fund && npx playwright install chromium
	docker compose -f docker/compose.yml up --build -d
	@for i in $$(seq 1 60); do curl -sf http://localhost:9001/health >/dev/null 2>&1 && break; sleep 1; done; \
	( cd $(E2E_DIR) && npx playwright test ); status=$$?; \
	docker compose -f docker/compose.yml down -v --remove-orphans >/dev/null 2>&1; \
	exit $$status

# image-scan builds the production image, then runs trivy against it inside
# a throwaway container so no local trivy install is required. We gate on
# HIGH and CRITICAL findings only — Go std lib advisories often land as
# MEDIUM and would force a release-block on every weekly DB refresh.
# The image-config check also flags running as root or missing HEALTHCHECK
# so any future regression in Dockerfile hardening trips this target.
IMAGE_TAG ?= bytebucket-scan:local
image-scan:
	docker build -f docker/Dockerfile -t $(IMAGE_TAG) .
	docker run --rm \
		-v /var/run/docker.sock:/var/run/docker.sock \
		-v $(HOME)/.cache/trivy:/root/.cache/trivy \
		aquasec/trivy:latest image \
		--severity HIGH,CRITICAL \
		--exit-code 1 \
		--ignore-unfixed \
		--scanners vuln,misconfig,secret \
		$(IMAGE_TAG)

clean:
	rm -rf $(DIST_SRC) $(DIST_DST) $(WEB_DIR)/node_modules ./build
