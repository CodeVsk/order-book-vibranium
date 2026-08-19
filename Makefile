.PHONY: build test test-integration test-concurrency lint migrate-up migrate-down run k6 k6-full validate

build:
	go build ./cmd/...

test:
	go test ./... -short -count=1

test-integration:
	go test ./... -tags=integration -count=1

test-concurrency:
	go test ./test/concurrency/... -tags=integration -race -count=1 -v

lint:
	golangci-lint run

migrate-up:
	migrate -path migrations -database "$$DATABASE_URL" up

migrate-down:
	migrate -path migrations -database "$$DATABASE_URL" down 1

run:
	docker compose -f build/docker-compose.yml up --build

k6:
	k6 run scripts/k6/load-test.js

k6-moderate:
	PROFILE=moderate k6 run scripts/k6/load-test.js

k6-full:
	PROFILE=full k6 run scripts/k6/load-test.js

validate:
	./scripts/validate-post-test.sh
