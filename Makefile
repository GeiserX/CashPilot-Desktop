CASHPILOT_BACKEND ?= ../CashPilot
DEV_PORT := 8765

.PHONY: dev dev-backend dev-tauri build test

dev:
	@echo "Run 'make dev-backend' in one terminal, 'make dev-tauri' in another."

dev-backend:
	cd $(CASHPILOT_BACKEND) && \
	UVICORN_PORT=$(DEV_PORT) DATA_DIR=/tmp/cashpilot-dev CASHPILOT_HOST=127.0.0.1 \
	python3 -m uvicorn app.main:app --host 127.0.0.1 --port $(DEV_PORT) --reload

dev-tauri:
	cd src-tauri && cargo tauri dev

build:
	npx tauri build

test:
	export PATH="$$HOME/.cargo/bin:$$PATH" && cargo test --manifest-path src-tauri/Cargo.toml
