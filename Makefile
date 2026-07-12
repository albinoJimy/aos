# Makefile — wrappers da IaC AOS. Encapsula o ciclo de vida por ambiente.
# Uso: make <alvo> ENV=dev|staging   (ENV por omissão: dev)
# Requer: OpenTofu (tofu) ou Terraform, Docker. Ver README.md (arranque <= 30 min).

ENV ?= dev
TF  ?= tofu
INFRA_DIR := infra

# Escolhe o backend/var-file do ambiente.
BACKEND := backend-$(ENV).hcl
VARFILE := env/$(ENV).tfvars

.DEFAULT_GOAL := help
.PHONY: help bootstrap bootstrap-down init plan apply destroy fmt validate output check-env \
        ci ci-secrets ci-build ci-lint ci-test ci-replay ci-sast ci-sca ci-policy ci-selftest ci-all

help: ## Lista os alvos disponíveis
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

check-env: ## Valida que ENV é dev ou staging
	@case "$(ENV)" in dev|staging) ;; *) echo "ENV inválido: '$(ENV)' (usa dev|staging)"; exit 1;; esac

bootstrap: ## Sobe o estado remoto local (MinIO) e cria o bucket
	docker compose -f $(INFRA_DIR)/bootstrap/docker-compose.yml up -d

bootstrap-down: ## Desliga o estado remoto local (mantém o estado)
	docker compose -f $(INFRA_DIR)/bootstrap/docker-compose.yml down

init: check-env ## tofu init com o backend do ambiente
	cd $(INFRA_DIR) && $(TF) init -reconfigure -backend-config=$(BACKEND)

plan: check-env ## Mostra o plano do ambiente
	cd $(INFRA_DIR) && $(TF) plan -var-file=$(VARFILE)

apply: check-env ## Aplica (idempotente) o ambiente
	cd $(INFRA_DIR) && $(TF) apply -var-file=$(VARFILE)

destroy: check-env ## Destrói (limpo) o ambiente
	cd $(INFRA_DIR) && $(TF) destroy -var-file=$(VARFILE)

output: check-env ## Mostra os endpoints do ambiente
	cd $(INFRA_DIR) && $(TF) output

fmt: ## Formata o HCL
	cd $(INFRA_DIR) && $(TF) fmt -recursive

validate: ## Valida a configuração
	cd $(INFRA_DIR) && $(TF) validate

# --- CI / Gates de qualidade (AOS-010) ---------------------------------------
# Fonte de verdade dos gates: scripts/ci/*.sh (fail-closed). O YAML da CI só os
# invoca. Reprodução local por UM comando: 'make ci'. Requer Go, gcc/mingw (para
# -race) e bash; staticcheck/gosec/govulncheck são auto-instalados (go install
# pinado, idempotente). Ver CONTRIBUTING.md.
CI := bash scripts/ci

ci: ## Corre TODOS os gates locais (build→lint→test→sast→sca→policy-test), fail-closed
	$(CI)/run.sh

ci-secrets: ## Gate: scan de segredos (chaves privadas / ficheiros de segredo rastreados)
	$(CI)/secrets.sh

ci-build: ## Gate: build (go build ./... por módulo)
	$(CI)/build.sh

ci-lint: ## Gate: lint/format + arch-lint AOS-003 (gofmt, vet, staticcheck)
	$(CI)/lint.sh

ci-test: ## Gate: test -race + cobertura (gate kernel >= 80%)
	$(CI)/test.sh

ci-replay: ## Gate 8: replay determinístico + idempotência por passo (harness AOS-024)
	$(CI)/replay.sh

ci-sast: ## Gate: SAST (gosec, HIGH/CRITICAL)
	$(CI)/sast.sh

ci-sca: ## Gate: SCA (govulncheck, vulns afetantes)
	$(CI)/sca.sh

ci-policy: ## Gate: teste de política do PDP (golden allow/deny + assinatura)
	$(CI)/policy-test.sh

ci-selftest: ## Auto-testes dos gates: prova que falhas de lint/test/política/CVE são bloqueadas
	$(CI)/selftest.sh

ci-all: ci ci-selftest ## Corre os gates E os self-tests (prova completa do pipeline)
