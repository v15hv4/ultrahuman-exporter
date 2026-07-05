.PHONY: up down

up:
	docker compose --profile=full up --build -d

down:
	docker compose --profile=full down
