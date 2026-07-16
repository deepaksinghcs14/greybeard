# greybeard — local development targets

.PHONY: build install install-system test check adapters clean

# Build the binary into the repo root (gitignored).
build:
	go build -o greybeard ./cmd/greybeard

# Install to ~/go/bin (atomic rename — safe on macOS; never cp over a
# running binary in place, Apple Silicon kills it on signature mismatch).
install:
	go install ./cmd/greybeard

# Also refresh /usr/local/bin, which non-interactive contexts (Claude Code's
# MCP server and session hook) resolve instead of ~/go/bin. rm-then-cp gets a
# fresh inode, avoiding the macOS code-signature kill.
install-system: install
	sudo rm -f /usr/local/bin/greybeard
	sudo cp $(shell go env GOPATH)/bin/greybeard /usr/local/bin/greybeard
	/usr/local/bin/greybeard version

test:
	go test ./...

# Everything CI runs.
check:
	go build ./...
	go vet ./...
	go test ./...
	node scripts/check-adapter-copies.js

# Regenerate the Codex / instruction-only adapters from the canonical skill.
adapters:
	node scripts/build-adapters.js

clean:
	rm -f greybeard
