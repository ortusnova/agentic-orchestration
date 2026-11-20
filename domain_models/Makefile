.PHONY: help test lint schemas

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-9s %s\n", $$1, $$2}'

test: ## Run the tests, which include the check that schemas/ is current
	uv run pytest -q

lint: ## Fail if schemas/ differs from what the models generate
	uv run domain-models check

schemas: ## Regenerate schemas/ from the models
	uv run domain-models write
