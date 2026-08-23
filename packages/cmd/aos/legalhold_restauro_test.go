package main

import (
	"context"
	"net/http"
	"testing"

	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// A GUARDA DE DESTRUIÇÃO QUE NÃO TINHA PROVA NENHUMA.
//
// Achado da verificação de completude de 2026-08-23, e VERIFIQUEI-O antes de escrever isto: com o
// corpo de `restoreLegalHolds` substituído por `return 0, nil` — o nó lê a cadeia e repõe ZERO
// holds, em silêncio — a bateria INTEIRA de `cmd/aos` continuava verde.
//
//	restoreLegalHolds   15.2%   (só os returns de store nil / head 0)
//	legalHoldTargetOf    0.0%   (nenhum teste alcança uma iteração do laço)
//
// PORQUE IMPORTA: `holdsRestored` só passa a `false` num ERRO DE LEITURA. Um replay que devolva
// `(0, nil)` deixa a flag a `true`, [retentionSchedulerArmed] arma o varredor AUTOMÁTICO (perna
// 4-bis: «a barreira EXISTE» e «o CONTEÚDO dela sobreviveu ao restart»), e o nó destrói KEKs de
// titulares que a cadeia WORM continua a AFIRMAR sob preservação.
//
// É a lacuna no teste da própria remediação da W6: a barreira foi construída, e nunca se provou
// que ela se enche.
//
// SEMEADO PELA VIA REAL — `POST /dsar/hold` — e não por um `AuditRecord` escrito à mão. Um teste
// que inventasse a forma do selo passaria a divergir do handler sem ninguém notar, e é
// precisamente a divergência de forma que este restauro existe para atravessar.
// ---------------------------------------------------------------------------------------------

// colocaHold sela um legal hold pela rota de produção e devolve o corpo da resposta.
func colocaHold(t *testing.T, h http.Handler, rota, requestID, subject, particao string) {
	t.Helper()
	corpo := map[string]any{"request_id": requestID}
	if subject != "" {
		corpo["subject_id"] = subject
	}
	if particao != "" {
		corpo["partition"] = particao
	}
	rec := postJSONComLeitor(t, h, "POST", rota, corpo, govReader)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s devolveu %d: %s", rota, rec.Code, rec.Body.String())
	}
}

// apósRestart simula o arranque: um [audit.LegalHold] NOVO, reconstruído da cadeia.
func aposRestart(t *testing.T, node *Node) (*audit.LegalHold, int) {
	t.Helper()
	fresco := audit.NewLegalHold()
	n, err := restoreLegalHolds(context.Background(), node.WORM, fresco)
	if err != nil {
		t.Fatalf("restoreLegalHolds: %v", err)
	}
	return fresco, n
}

func TestUmHoldPorTITULARSobreviveAoRestart(t *testing.T) {
	node, h, _ := noComGovernacaoDeLeitura(t)
	const titular = "nhi-titular-preservado"
	colocaHold(t, h, "/dsar/hold", "req-hold-1", titular, "")

	fresco, n := aposRestart(t, node)
	if n != 1 {
		t.Fatalf("o restauro repos %d alvo(s) e a cadeia afirma 1 — um nó que arranca sem os holds "+
			"arma o varredor AUTOMATICO e destroi material sob preservacao", n)
	}
	if !fresco.HeldSubject(titular) {
		t.Errorf("o titular %q NAO ficou sob preservacao depois do restauro", titular)
	}
}

// TestUmHoldLEVANTADONaoVoltaComORestart é o controlo da direcção inversa.
//
// Sem ele, um restauro que repusesse TUDO o que alguma vez foi selado passaria no teste acima — e
// reabriria preservações que um humano levantou, tornando material inapagável para sempre.
func TestUmHoldLEVANTADONaoVoltaComORestart(t *testing.T) {
	node, h, _ := noComGovernacaoDeLeitura(t)
	const titular = "nhi-titular-libertado"
	colocaHold(t, h, "/dsar/hold", "req-hold-2", titular, "")
	colocaHold(t, h, "/dsar/release", "req-rel-2", titular, "")

	fresco, n := aposRestart(t, node)
	if n != 0 {
		t.Errorf("o restauro repos %d alvo(s) sobre um hold JA LEVANTADO — a preservacao volta "+
			"sozinha e o material fica inapagavel", n)
	}
	if fresco.HeldSubject(titular) {
		t.Errorf("o titular %q voltou a ficar sob preservacao depois de o hold ter sido levantado", titular)
	}
}

// TestUmHoldPorPARTICAOSobreviveAoRestart cobre o segundo alvo — e é o que exercita o ramo do
// `Resource` em [legalHoldTargetOf], que estava a 0,0%.
func TestUmHoldPorPARTICAOSobreviveAoRestart(t *testing.T) {
	node, h, _ := noComGovernacaoDeLeitura(t)
	const particao = "gov-particao-preservada"
	colocaHold(t, h, "/dsar/hold", "req-hold-3", "", particao)

	fresco, n := aposRestart(t, node)
	if n != 1 {
		t.Fatalf("o restauro repos %d alvo(s) para um hold POR-PARTICAO", n)
	}
	if !fresco.HeldPartition(particao) {
		t.Errorf("a particao %q NAO ficou sob preservacao depois do restauro", particao)
	}
}

// TestAOrdemDOSSelosDECIDE — o último facto sobre cada alvo ganha.
//
// Sem este teste, um restauro que ignorasse a ordem (ou que parasse no primeiro selo) passaria
// nos anteriores, e um hold RE-COLOCADO depois de levantado não voltaria a vigorar.
func TestAOrdemDOSSelosDECIDE(t *testing.T) {
	node, h, _ := noComGovernacaoDeLeitura(t)
	const titular = "nhi-titular-recolocado"
	colocaHold(t, h, "/dsar/hold", "req-h-a", titular, "")
	colocaHold(t, h, "/dsar/release", "req-r-a", titular, "")
	colocaHold(t, h, "/dsar/hold", "req-h-b", titular, "")

	fresco, n := aposRestart(t, node)
	if n != 1 || !fresco.HeldSubject(titular) {
		t.Errorf("place→release→place devia terminar SOB preservacao; n=%d held=%t",
			n, fresco.HeldSubject(titular))
	}
}

// TestUmHoldSOBRE_OS_DOIS_ALVOS é o caso em que a OBRIGAÇÃO é a única fonte que serve — e
// apareceu porque uma mutação não caía.
//
// `legalHoldTargetOf` lê duas fontes: a obrigação `legalHoldTargetObl` (que carrega subject E
// partition) e, em recurso, o `Resource` — que nomeia só o alvo PRIMÁRIO. Com um hold sobre um só
// alvo, as duas fontes dão o mesmo, e apagar a leitura da obrigação não fazia teste nenhum cair.
//
// Com os DOIS alvos no mesmo selo, o `Resource` nomeia um e perde o outro: sem a obrigação, o
// segundo alvo desaparece silenciosamente no restart — e um hold POR-PARTIÇÃO que não sobrevive
// é material destruído por um varredor que se julga autorizado.
func TestUmHoldSOBRE_OS_DOIS_ALVOS_SobreviveInteiro(t *testing.T) {
	node, h, _ := noComGovernacaoDeLeitura(t)
	const titular, particao = "nhi-titular-duplo", "gov-particao-dupla"
	colocaHold(t, h, "/dsar/hold", "req-hold-duplo", titular, particao)

	fresco, n := aposRestart(t, node)
	if n != 2 {
		t.Fatalf("um hold sobre DOIS alvos devia repor 2; veio %d — o `Resource` nomeia so o alvo "+
			"primario e o outro perde-se em silencio", n)
	}
	if !fresco.HeldSubject(titular) {
		t.Errorf("o titular %q perdeu-se no restauro", titular)
	}
	if !fresco.HeldPartition(particao) {
		t.Errorf("a particao %q perdeu-se no restauro — um hold por-particao que nao sobrevive e "+
			"material destruido por um varredor que se julga autorizado", particao)
	}
}
