build:
	@go build -o bin/zin ./

run: build
	@./bin/zin

dev:
	@go run ./

.PHONY: load-env
load-env:
	@echo "To load .env into your current shell, run:"
	@echo
	@echo "$$(sed -E -e 's/^[[:space:]]*#.*$$//' -e 's/^[[:space:]]*$$//' .env | \
		grep '=' | \
		sed -E 's/^[[:space:]]*([^=[:space:]]+)[[:space:]]*=[[:space:]]*(.*)/export \1=\2/' | \
		while read line; do \
			echo -n "$$line "; \
		done)"
	@echo

.PHONE: gen-hex
gen-hex:
	@openssl rand -hex 32
