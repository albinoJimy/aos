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
        ci ci-secrets ci-build ci-lint ci-test ci-replay ci-memory ci-supplychain ci-routing ci-security ci-evalgate ci-sast ci-sca ci-policy ci-selftest ci-all \
        cover test-unit

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

ci-test: ## Gate: test -race + cobertura (gate generalizado >= COVERAGE_MIN, default 80%)
	$(CI)/test.sh

# --- Comando ÚNICO da suite unit + cobertura máquina-legível (AOS-109 AC1) ----
# Distinto de `make ci` (pipeline completo): corre SÓ o gate 3 (suite unit -race
# por módulo) e emite o relatório LCOV em coverage/lcov.info. Tune do limiar por
# env var: `COVERAGE_MIN=90 make cover`.
cover: ## Suite unit (gate 3) + relatório de cobertura máquina-legível (coverage/lcov.info)
	$(CI)/test.sh

test-unit: cover ## Alias de `cover`: corre a suite unit e emite a cobertura LCOV

ci-replay: ## Gate 8: replay determinístico + idempotência por passo (harness AOS-024)
	$(CI)/replay.sh

ci-memory: ## Gate: integridade/migração/proveniência de memória (suite AOS-044, fail-closed)
	$(CI)/memory.sh

ci-supplychain: ## Gate: 7 vectores adversariais de supply-chain + audit WORM (suite AOS-054, fail-closed)
	$(CI)/supplychain.sh

ci-routing: ## Gate: 5 cenários adversariais de roteamento/failover do GW (suite AOS-063, fail-closed)
	$(CI)/routing.sh

ci-security: ## Gate: 4 cenários adversariais de segurança (prompt injection/exfiltração/segredos/isolamento) (suite AOS-075, fail-closed)
	$(CI)/security.sh

ci-evalgate: ## Gate 9: eval harness + golden-sets curados / admission control comportamental (AOS-114, fail-closed)
	$(CI)/evalgate.sh

ci-sast: ## Gate: SAST (gosec, HIGH/CRITICAL)
	$(CI)/sast.sh

ci-sca: ## Gate: SCA (govulncheck, vulns afetantes)
	$(CI)/sca.sh

ci-policy: ## Gate: teste de política do PDP (golden allow/deny + assinatura)
	$(CI)/policy-test.sh

ci-selftest: ## Auto-testes dos gates: prova que falhas de lint/test/política/CVE são bloqueadas
	$(CI)/selftest.sh

ci-all: ci ci-selftest ## Corre os gates E os self-tests (prova completa do pipeline)
