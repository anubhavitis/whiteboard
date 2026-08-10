.PHONY: dev dev-server dev-web build lint test install

install:
	cd web && npm install
	cd server && go mod download

# Both processes, one terminal. Ctrl-C stops the pair.
dev:
	@trap 'kill 0' INT TERM EXIT; \
	$(MAKE) dev-server & \
	$(MAKE) dev-web & \
	wait

dev-server:
	cd server && go run ./cmd/whiteboard

dev-web:
	cd web && npm run dev

build:
	cd server && go build -o ../bin/whiteboard ./cmd/whiteboard
	cd web && npm run build

lint:
	cd server && go vet ./...
	cd web && npm run lint

test:
	cd server && go test ./...
	cd web && npm test
