.PHONY: up down build clean

up:
	docker compose --profile=full up -d

down:
	docker compose --profile=full down

build:
	docker compose build

clean:
	docker compose --profile=full down -v
