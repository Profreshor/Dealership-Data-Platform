SHELL := /bin/sh
.DEFAULT_GOAL := setup
export TEST_DATABASE_URL ?= postgres://postgres:ddp-local-only@127.0.0.1:55432/ddp_development?sslmode=disable
export DATABASE_URL ?= $(TEST_DATABASE_URL)

include tests/project.mk

.PHONY: setup db build assets dev check check-go check-python check-frontend check-system check-image check-smoke check-client-tests schema schema-check secrets

setup:
	go mod download
	uv sync --locked
	npm ci --prefix frontend

db:
	docker compose -p ddp-dev -f deploy/compose.dev.yaml up -d --wait postgres

assets:
	npm run build --prefix frontend

build: assets
	mkdir -p build
	go build -o build/ddp ./cmd/ddp

dev:
	go run ./cmd/ddp dev

# Start local Postgres with `make db` (the devcontainer and CI provide their own).
check: check-go check-python check-frontend check-system

check-go: assets check-client-tests
	test -z "$$(gofmt -l cmd internal migrations frontend/embed.go)"
	go vet ./...
	go test -race -count=1 ./internal/app/...
	go test -race -count=1 ./internal/ddp/migrate -run '^TestMigrationChainAgainstPostgres$$'
	$(MAKE) build

check-python:
	uv run --locked ruff check ddp client jobs tests/python tests/platform tests/check-smoke.py tests/check-client-tests.py internal/ddp/testdata/reporting $(PROJECT_PYTHON)
	uv run --locked pyright
	@for project in $(PROJECT_PYRIGHT) internal/ddp/testdata/reporting/pyrightconfig.json; do \
		uv run --locked pyright --project "$$project" || exit $$?; \
	done
	uv run --locked lint-imports
	uv run --locked pytest -q

check-frontend:
	npm run check --prefix frontend

check-system: assets schema-check secrets check-client-tests
	bash tests/check-install.sh
	go run ./cmd/ddp validate

check-client-tests:
	python3 tests/check-client-tests.py

# Requires a Docker-capable host; the development container has no Docker socket.
check-image:
	sh tests/check-image.sh
check-smoke: assets
	python3 tests/check-client-tests.py --smoke
	python3 tests/check-smoke.py

schema:
	go run ./cmd/ddp config schema > schema/ddp.schema.json

schema-check:
	go test ./internal/ddp/config -run TestGeneratedSchema -count=1

secrets:
	go run github.com/zricethezav/gitleaks/v8@v8.24.3 dir . --redact --no-banner
