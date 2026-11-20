.PHONY: help test lint fmt schemas

SOURCES := src tests tools

help: ## Show the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-9s %s\n", $$1, $$2}'

test: ## Run the tests, which include the check that schemas/ is current
	uv run pytest -q

lint: ## Check formatting, the lint rules, attribute-docstring spacing, and that schemas/ is current
	uv run ruff format --check $(SOURCES)
	uv run ruff check $(SOURCES)
	uv run python tools/attribute_docstrings.py $(SOURCES)
	uv run domain-models check

fmt: ## Space out documented attributes, then format with ruff
	uv run python tools/attribute_docstrings.py --fix $(SOURCES)
	uv run ruff format $(SOURCES)
	uv run ruff check --fix $(SOURCES)

schemas: ## Regenerate schemas/ from the models
	uv run domain-models write
