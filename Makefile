.PHONY: build controller worker clean up down

build: controller worker

controller:
	go build -o bin/controller ./cmd/controller

worker:
	go build -o bin/worker ./cmd/worker

clean:
	rm -rf bin/

run-controller:
	go run ./cmd/controller

run-worker:
	go run ./cmd/worker

# Docker

up-controller:
	docker compose -f docker-compose.controller.yml up --build -d

down-controller:
	docker compose -f docker-compose.controller.yml down

up-worker:
	docker compose -f docker-compose.worker.yml up --build -d

down-worker:
	docker compose -f docker-compose.worker.yml down
