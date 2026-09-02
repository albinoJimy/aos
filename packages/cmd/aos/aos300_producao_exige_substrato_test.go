package main

// AOS-300 — PRODUÇÃO EXIGE EVENT STORE DURÁVEL, PELA REVOGAÇÃO DE NHI.
//
// AOS-288 ligou a revogação e deu-lhe `Revocations.Rebuild`, que repovoa a projecção no arranque a
// partir do stream `identity.nhi.revoked`. Isso fecha o esquecimento **quando o substrato é
// durável**. Sobre o Event Store de referência in-memory o stream morre com o processo, e um token
// revogado volta a ser ACEITE ao primeiro restart — em silêncio, com um `Principal` completo,
// enquanto o banner anuncia «revogacao».
//
// # PORQUE ESTA GUARDA É INCONDICIONAL, AO CONTRÁRIO DAS OUTRAS DUAS
//
// [ErrDurableExecutionNeedsDurableSubstrate] exige o substrato a quem PEDE execução durável;
// [ErrProductionNeedsDurableApproval] exige-o a quem configura four-eyes. As duas são condicionais
// a uma opção que o operador ligou. A revogação não é opcional: desde AOS-288 o registo é composto
// no verifier SEMPRE. Um nó de produção sem event store durável tem a revogação ligada, anunciada,
// e a expirar no restart seguinte.
//
// # A ORDEM É PARTE DO CONTRATO
//
// A guarda vem DEPOIS das outras colunas de postura — identidade, soberania, KEK, four-eyes. Um nó
// mal configurado deve ouvir primeiro o que é mais fundamental, e a ordem já era relied-upon: pô-la
// mais cedo trocava o diagnóstico de cinco guard-tests existentes por um erro sobre o event store.
// O teste abaixo fixa isso, para ninguém a «arrumar» para o início do bloco.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// aos300ProducaoQuaseCompleta monta uma produção que passa TODAS as colunas de postura excepto a
// que estiver por definir — para que o teste meça a guarda que nomeia, e não outra.
func aos300ProducaoQuaseCompleta(t *testing.T) {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	t.Setenv("AOS_MODE", "production")
	t.Setenv("AOS_ISSUER_ID", "iss:aos-300")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	t.Setenv("AOS_ISSUER_KEY_PATH", "")
	t.Setenv("AOS_BOARD_REGIONS", "board:demo=eu")
	t.Setenv("AOS_SOVEREIGN_OIDC_ISSUER", "https://idp-soberania.example")
	t.Setenv("AOS_SOVEREIGN_OIDC_AUDIENCE", "aos-node")
	// SEM WORM nem execução durável ⇒ a guarda da KEK não entra em jogo e não mascara esta.
	t.Setenv("AOS_WORM_PATH", "")
	t.Setenv("AOS_DURABLE_EXECUTION", "")
	t.Setenv("AOS_APPROVERS_FILE", "")
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	t.Setenv("AOS_MODEL_NAME", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "")
}

// TestAOS300_ProducaoSemEventStoreDuravelRecusa é a AC2, no molde de
// TestRunProductionRequiresHardenedIdentity.
func TestAOS300_ProducaoSemEventStoreDuravelRecusa(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", "") // o estado sob teste

	if err := run(io.Discard); !errors.Is(err, ErrProductionNeedsDurableSubstrate) {
		t.Fatalf("producao sem Event Store duravel devia abortar com ErrProductionNeedsDurableSubstrate, veio: %v", err)
	}
}

// TestAOS300_OErroNomeiaAsDuasVias: um fail-closed que não diz o que definir manda o operador
// adivinhar. As duas vias são aceites e as duas têm de aparecer.
func TestAOS300_OErroNomeiaAsDuasVias(t *testing.T) {
	msg := ErrProductionNeedsDurableSubstrate.Error()
	for _, v := range []string{"AOS_EVENTSTORE_PATH", "AOS_EVENTSTORE_NATS"} {
		if !strings.Contains(msg, v) {
			t.Errorf("o erro tem de nomear %s; veio: %s", v, msg)
		}
	}
}

// TestAOS300_OEventStoreREPLICADOSatisfazAGuarda fixa que NATS conta como durável. É replicado por
// quórum, que é mais do que o WAL local dá, não menos — e a guarda dizer o contrário obrigaria um
// deployment replicado a montar um ficheiro local que não usa.
func TestAOS300_OEventStoreREPLICADOSatisfazAGuarda(t *testing.T) {
	aos300ProducaoQuaseCompleta(t)
	t.Setenv("AOS_EVENTSTORE_PATH", "")
	t.Setenv("AOS_EVENTSTORE_NATS", "aos-es-0:4222")

	// Não se exige arranque completo (isso abriria ligação ao cluster): exige-se que a config
	// passe a guarda, que é o que este teste mede.
	if _, err := nodeConfigFromEnv(); errors.Is(err, ErrProductionNeedsDurableSubstrate) {
		t.Fatal("AOS_EVENTSTORE_NATS TEM de satisfazer a guarda de substrato duravel — um deployment replicado nao pode ser obrigado a montar um WAL local que nao usa")
	}
}

// TestAOS300_AGuardaVEMDEPOISDasOutrasColunas fixa a ORDEM, que é parte do contrato de diagnóstico.
//
// Sem isto, alguém que arrume esta guarda para o início do bloco de produção troca em silêncio o
// erro de cinco guard-tests existentes: um nó sem identidade endurecida passaria a ouvir «falta o
// event store», mandando quem depura para o sítio errado.
func TestAOS300_AGuardaVEMDEPOISDasOutrasColunas(t *testing.T) {
	casos := []struct {
		nome  string
		monta func(t *testing.T)
		quero error
	}{
		{
			nome: "identidade antes do substrato",
			monta: func(t *testing.T) {
				aos300ProducaoQuaseCompleta(t)
				t.Setenv("AOS_ISSUER_PUBKEY", "") // sem trust anchor
				t.Setenv("AOS_EVENTSTORE_PATH", "")
			},
			quero: ErrProductionNeedsHardenedIdentity,
		},
		{
			nome: "soberania antes do substrato",
			monta: func(t *testing.T) {
				aos300ProducaoQuaseCompleta(t)
				t.Setenv("AOS_BOARD_REGIONS", "")
				t.Setenv("AOS_EVENTSTORE_PATH", "")
			},
			quero: ErrProductionNeedsSovereignRead,
		},
		{
			// Reusa a fixture do próprio guarda do four-eyes. A primeira versão deste caso
			// escrevia um caminho para um ficheiro INEXISTENTE e passava — mas por a config
			// abortar na leitura do ficheiro, sem nunca chegar ao guarda. Foi a asserção
			// forte (`errors.Is(err, c.quero)`) que o apanhou; a fraca escondia-o.
			nome:  "four-eyes antes do substrato",
			monta: func(t *testing.T) { prodApprovalEnvBase(t) },
			quero: ErrProductionNeedsDurableApproval,
		},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			c.monta(t)
			_, err := nodeConfigFromEnv()
			if errors.Is(err, ErrProductionNeedsDurableSubstrate) {
				t.Fatalf("a guarda de AOS-300 disparou ANTES de %v — a ordem das colunas de postura e parte do diagnostico", c.quero)
			}
			// E afirma-se o erro ESPERADO, não só a ausência do outro: um teste que só dissesse
			// «não foi o de AOS-300» passaria a verde com qualquer falha espúria da config.
			if !errors.Is(err, c.quero) {
				t.Fatalf("erro = %v, quero %v", err, c.quero)
			}
		})
	}
}
