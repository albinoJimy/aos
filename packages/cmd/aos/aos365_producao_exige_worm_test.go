package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// AOS-365 — a única configuração de produção do WORM que arrancava era a VOLÁTIL, porque a guarda
// que existia era a da chave (KEK) e não a do trilho. O nó caía em `audit.NewMemStore()` sem
// consultar o modo, e o banner honesto («in-memory de referencia (nao-duravel)») desarmava a
// suspeita. Estes testes fixam a guarda simétrica à do Event Store (AOS-300): em produção o WORM
// tem de ser durável, INCONDICIONALMENTE.

// aos365ProducaoComSubstratoSemWORM monta uma produção que passa TODAS as colunas anteriores
// (identidade, soberania, Event Store durável) e deixa SÓ o WORM por definir — para que o que
// falha seja a guarda que este ficheiro mede, e não outra. Reusa a fixture de AOS-300, que já zera
// o WORM e as colunas condicionais; só acrescenta o Event Store durável que a guarda de substrato
// exige antes de o fluxo chegar à do WORM.
func aos365ProducaoComSubstratoSemWORM(t *testing.T) {
	t.Helper()
	aos300ProducaoQuaseCompleta(t)                                            // AOS_WORM_PATH="" incluído
	t.Setenv("AOS_EVENTSTORE_PATH", filepath.Join(t.TempDir(), "events.wal")) // passa a guarda de substrato
}

// TestAOS365_ProducaoSemWORMDuravelRecusa é a AC central: em produção, com Event Store durável e
// AOS_WORM_PATH AUSENTE, o nó recusa compor-se — o mesmo estado que a auditoria mediu a ARRANCAR
// (§3.6, O-12). É a fronteira REAL de leitura do ambiente ([nodeConfigFromEnv]).
func TestAOS365_ProducaoSemWORMDuravelRecusa(t *testing.T) {
	aos365ProducaoComSubstratoSemWORM(t)

	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableWORM) {
		t.Fatalf("producao sem WORM duravel devia abortar com ErrProductionNeedsDurableWORM, veio: %v", err)
	}
}

// TestAOS365_ForaDeProducaoOMemStoreArranca é o controlo negativo (a): a MESMA configuração FORA de
// produção continua a arrancar com o MemStore, sem alteração de comportamento. Prova-se pelo
// arranque completo E pelo banner — que é onde o comportamento «in-memory de referencia» é
// declarado e onde uma regressão apareceria.
func TestAOS365_ForaDeProducaoOMemStoreArranca(t *testing.T) {
	aos365ProducaoComSubstratoSemWORM(t)
	t.Setenv("AOS_MODE", "") // modo de referência — a guarda tem `production &&`, não deve entrar.

	var sb strings.Builder
	if err := run(&sb); err != nil {
		t.Fatalf("fora de producao o WORM em memoria tem de arrancar (inalterado), veio: %v", err)
	}
	// Específico do WORM (`worm=…`), não o substring genérico «in-memory de referencia» — que o
	// vault de KEK também usa e faria esta asserção passar pela razão errada. O Event Store daqui é
	// durável (a fixture monta-o), logo o banner distingue os dois com o prefixo `worm=`.
	if !strings.Contains(sb.String(), "worm=in-memory de referencia") {
		t.Fatalf("o banner de substrato devia declarar worm=in-memory de referencia, veio:\n%s", sb.String())
	}
}

// TestAOS365_ProducaoComWORMeVaultNaoColideComKEK é o controlo negativo (b): produção com
// AOS_WORM_PATH E AOS_DSAR_VAULT_ADDR compõe — a guarda nova não colide com
// ErrProductionNeedsDurableKEK, e a KEK durável satisfaz a sua própria. Config-only para não abrir
// ligação ao cluster; o que se mede é a config passar as DUAS guardas.
func TestAOS365_ProducaoComWORMeVaultNaoColideComKEK(t *testing.T) {
	aos365ProducaoComSubstratoSemWORM(t)
	fixarSubstratoDuravelDeProducao(t) // AOS_WORM_PATH durável + AOS_DSAR_VAULT_ADDR + token

	_, err := nodeConfigFromEnv()
	if errors.Is(err, ErrProductionNeedsDurableWORM) {
		t.Fatalf("com AOS_WORM_PATH definido a guarda do WORM NAO devia disparar, veio: %v", err)
	}
	if errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("com AOS_DSAR_VAULT_ADDR definido a guarda da KEK NAO devia disparar, veio: %v", err)
	}
}

// TestAOS365_OErroNomeiaAsDuasVariaveis é a AC do CA-5: um WORM durável arrasta uma KEK durável, e
// um operador que ouvisse uma variável de cada vez trocaria um erro por outro. As DUAS têm de
// aparecer na mensagem.
func TestAOS365_OErroNomeiaAsDuasVariaveis(t *testing.T) {
	msg := ErrProductionNeedsDurableWORM.Error()
	for _, v := range []string{"AOS_WORM_PATH", "AOS_DSAR_VAULT_ADDR"} {
		if !strings.Contains(msg, v) {
			t.Errorf("o erro tem de nomear %s para o operador nao trocar um erro por outro; veio: %s", v, msg)
		}
	}
}

// TestAOS365_OSubstratoVemANTESDoWORM fixa a ordem entre as duas guardas de durabilidade
// incondicionais: quando faltam OS DOIS (Event Store e WORM), o operador tem de ouvir primeiro
// «falta o Event Store». Pôr a guarda do WORM antes da de substrato roubar-lhe-ia o diagnóstico —
// é o mesmo contrato de ordem que TestAOS300_AGuardaVEMDEPOISDasOutrasColunas protege por cima.
func TestAOS365_OSubstratoVemANTESDoWORM(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)      // AOS_WORM_PATH="" e SEM Event Store
	t.Setenv("AOS_EVENTSTORE_PATH", "") // os dois em falta

	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableSubstrate) {
		t.Fatalf("com Event Store E WORM em falta o diagnostico tem de ser o do substrato, veio: %v", err)
	}
}

// TestAOS365_AKEKVemANTESDoWORM fixa o outro lado da ordem: a guarda do WORM tem de ficar DEPOIS da
// KEK. Com execução durável ligada, sem WORM e sem vault, a KEK dispara (o seu ramo
// `durableExecution` é verdadeiro) e é esse o diagnóstico certo. Se alguém arrumasse a guarda do
// WORM para ANTES da KEK, este combo passaria a ouvir «falta o WORM» — e, pior, tornaria o ramo
// `cfg.WORMPath != ""` da KEK sempre-verdadeiro em produção, mudando-lhe o significado em silêncio.
func TestAOS365_AKEKVemANTESDoWORM(t *testing.T) {
	aos365ProducaoComSubstratoSemWORM(t) // Event Store durável, WORM ausente, vault ausente
	t.Setenv("AOS_DURABLE_EXECUTION", "1")

	if _, err := nodeConfigFromEnv(); !errors.Is(err, ErrProductionNeedsDurableKEK) {
		t.Fatalf("com execucao duravel e sem vault o diagnostico tem de ser o da KEK, nao o do WORM, veio: %v", err)
	}
}
