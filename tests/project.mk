# Client-owned Python checks. `ddp init` replaces this file in a client tree.
PROJECT_PYTHON := tests/proving-ground
PROJECT_PYRIGHT := tests/proving-ground/pyrightconfig.json

.PHONY: check-init check-proving-ground-frontend
check-frontend: check-proving-ground-frontend
check-proving-ground-frontend:
	frontend/node_modules/.bin/oxlint tests/proving-ground/browser.mjs

check-system: check-init
check-init: assets
	uv run --locked python tests/proving-ground/check-init.py
