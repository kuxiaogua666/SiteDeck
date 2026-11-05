.PHONY: build test cross deploy
build:
	mkdir -p bin
	go build -trimpath -o bin/site-hub ./cmd/site-hub
	go build -trimpath -o bin/site-agent ./cmd/site-agent
	go build -trimpath -o bin/site-cli ./cmd/site-cli

test:
	go test -race ./...

cross:
	python3 scripts/build.py

deploy: build
	python3 scripts/deploy.py

.PHONY: privacy source-archive install-hooks
privacy:
	python3 scripts/check_public_repo.py

source-archive:
	python3 scripts/export_source.py

install-hooks:
	git config core.hooksPath .githooks
