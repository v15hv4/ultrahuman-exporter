.PHONY: build up down

build:
	docker compose build

up:
	docker compose --profile=full up -d

down:
	docker compose --profile=full down
