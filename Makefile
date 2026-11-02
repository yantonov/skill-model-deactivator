.PHONY: help help-all setup build install test types lint fmt check stop

.DEFAULT_GOAL := help

# The platform executable suffix, so Windows gets smd.exe and no name is
# hardcoded in a target.
BIN_NAME := smd$(shell go env GOEXE)
TARGET_DIR := target

# `stop` finds the daemon by its command line, not by its executable name: `smd log --follow`
# is also smd.exe and is a legitimate long-running process, so the name alone is not enough
# and the state directory is not needed. `serve` is the only command that starts the daemon,
# and the single-instance lock means there is at most one.
ifeq ($(OS),Windows_NT)
# Get-CimInstance and not tasklist: tasklist prints the name but not the arguments.
smd_pids = powershell.exe -NoProfile -Command "Get-CimInstance Win32_Process -Filter \"Name='$(BIN_NAME)'\" | Where-Object { \$$_.CommandLine -match '\bserve\b' } | ForEach-Object { \$$_.ProcessId }" | tr -d '\r'
# Git Bash needs // so MSYS does not rewrite the /F-style flags as paths.
kill_pid = taskkill //F //PID
else
smd_pids = pgrep -f 'smd serve'
kill_pid = kill
endif

help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "  check   - run lint + types + test"
	@echo "  build   - build $(TARGET_DIR)/smd"
	@echo "  install - build and install to ~/.local/bin"
	@echo "  test    - run tests"
	@echo ""
	@echo "Run 'make help-all' for the full list, including internal targets."

help-all:
	@echo "Public targets:"
	@echo "  check   - run lint + types + test"
	@echo "  build   - build $(TARGET_DIR)/smd"
	@echo "  install - build and install to ~/.local/bin"
	@echo "  test    - run tests"
	@echo ""
	@echo "Internal targets (typically called via check):"
	@echo "  lint    - fail if any file is not gofmt-clean"
	@echo "  types   - run go vet"
	@echo ""
	@echo "Maintenance:"
	@echo "  fmt     - rewrite files with gofmt"
	@echo "  setup   - download the module dependencies"
	@echo "  stop    - kill the running smd daemon (asks first)"

setup:
	go mod download

build:
	go build -o $(TARGET_DIR)/$(BIN_NAME) ./cmd/smd

install: build
	@mkdir -p "$(HOME)/.local/bin"
	@cp $(TARGET_DIR)/$(BIN_NAME) "$(HOME)/.local/bin/$(BIN_NAME)"
	@echo "target dir: ~/.local/bin"
	@echo "smd installed successfully"

test:
	go test ./...

types:
	go vet ./...

# -l lists offending files and exits 0, so a non-empty list is turned into a failure.
lint:
	@files=$$(gofmt -l .); if [ -n "$$files" ]; then echo "not gofmt-clean:"; echo "$$files"; exit 1; fi

fmt:
	gofmt -l -w .

# Only the `serve` process is killed: `smd log --follow` shares the name and must survive.
#
# The prompt is unconditional: [ -t 0 ] is false even in an interactive Git Bash, so a test for
# a terminal would silence the question forever. An unreadable stdin reads as an empty answer,
# and only [yY] proceeds, so a script or a closed stdin aborts instead of killing.
stop:
	@pids=$$($(smd_pids)); \
	if [ -z "$$pids" ]; then echo "smd is not running"; exit 0; fi; \
	printf 'kill smd serve (pid %s)? [y/N] ' "$$(echo $$pids)"; \
	read -r ans || ans=; \
	case "$$ans" in [yY]*) ;; *) echo "aborted"; exit 0 ;; esac; \
	for pid in $$pids; do $(kill_pid) $$pid >/dev/null && echo "stopped smd serve (pid $$pid)"; done

check: lint types test
	@echo "ALL CHECKS PASSED"
