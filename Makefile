# remotectl — common tasks. `make up` starts the full local stack incl. the demo device.
COMPOSE := docker compose --profile demo

.PHONY: env up down logs ps build test test-server test-agent test-web test-proto agent-macos agent-windows-cross clean

env:
	@scripts/gen-env.sh

up: env
	$(COMPOSE) up -d --build
	@echo "web app:        http://localhost:5560"
	@echo "device console: http://localhost:5562  (demo device)"

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=100

ps:
	$(COMPOSE) ps

build: env
	$(COMPOSE) build

test: test-proto test-server test-agent test-web

test-proto:
	cd proto && ../scripts/godocker.sh test ./...

test-server:
	scripts/test-server.sh

test-agent:
	cd agent && ../scripts/godocker.sh test ./...

test-web:
	cd web && npm ci && npm run lint && npm test && npm run build

agent-macos:
	packaging/macos/build-app.sh

agent-windows-cross:
	native/windows/xbuild.sh

clean:
	$(COMPOSE) down -v
