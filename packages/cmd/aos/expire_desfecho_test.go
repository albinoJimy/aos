package main

import (
	"net/http"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------------------------
// A VIA HUMANA TAMBÉM SELA O DESFECHO.
//
// Achado da verificação de completude de 2026-08-23. A correcção de 2026-08-21 fechou a
// assimetria de ATRIBUIÇÃO entre a via automática e a humana — ambas passaram a selar QUEM.
// Deixou intacta a de DESFECHO, que é a metade que diz se o apagamento chegou a acontecer.
//
//	varredor automático   sweep.started  +  sweep.completed (contagens, shred_unconfirmed)
//	POST /dsar/expire     sweep.started
//
// Um auditor que lesse a partição anos depois obtinha, para a via automática, «quem, quantos, e
// quantas ficaram por confirmar»; para um apagamento em massa ordenado por uma PESSOA obtinha
// «quem começou». E um `started` órfão é indistinguível de uma passagem que morreu a meio.
//
// As contagens iam só no corpo HTTP — que não é durável e desaparece com a sessão.
// ---------------------------------------------------------------------------------------------

// desfechosDaRota separa, na partição de retenção, os selos de INÍCIO e de CONCLUSÃO atribuídos
// ao principal dado.
func desfechosDaRota(t *testing.T, node *Node, principal string) (inicios, conclusoes int) {
	t.Helper()
	for _, s := range selosDaParticao(t, node, retentionPartition) {
		if s.Principal.NHIID != principal {
			continue
		}
		switch s.Resource.Type {
		case retentionSweepStartedEvent:
			inicios++
		case retentionSweepCompletedEvent:
			conclusoes++
		}
	}
	return inicios, conclusoes
}

func TestARotaSelaINICIOeCONCLUSAO(t *testing.T) {
	node, h, leitor := noComGovernacaoDeLeitura(t)

	rec := postJSONComLeitor(t, h, "POST", "/dsar/expire", map[string]any{}, leitor)
	if rec.Code != http.StatusOK {
		t.Fatalf("/dsar/expire devolveu %d: %s", rec.Code, rec.Body.String())
	}

	inicios, conclusoes := desfechosDaRota(t, node, leitor)
	if inicios != 1 {
		t.Fatalf("esperava 1 selo de INICIO atribuido a %q, veio %d", leitor, inicios)
	}
	if conclusoes != 1 {
		t.Fatalf("a rota selou o inicio e NAO o desfecho — um apagamento em massa ordenado por uma "+
			"pessoa fica sem registo duravel do que fez, e um `started` orfao e indistinguivel de "+
			"uma passagem que morreu a meio; conclusoes=%d", conclusoes)
	}
}

// TestOSeloDeCONCLUSAODaRotaCARREGAAsContagens — o desfecho sem contagens seria um carimbo.
//
// É a informação que ia SÓ no corpo HTTP: quantos foram varridos, quantos expiraram, e quantos
// ficaram RETIDOS por legal hold. Sem ela, o registo diz «acabou» e não diz o quê.
func TestOSeloDeCONCLUSAODaRotaCARREGAAsContagens(t *testing.T) {
	node, h, leitor := noComGovernacaoDeLeitura(t)

	if rec := postJSONComLeitor(t, h, "POST", "/dsar/expire", map[string]any{}, leitor); rec.Code != http.StatusOK {
		t.Fatalf("/dsar/expire devolveu %d", rec.Code)
	}

	var achou bool
	for _, s := range selosDaParticao(t, node, retentionPartition) {
		if s.Resource.Type != retentionSweepCompletedEvent || s.Principal.NHIID != leitor {
			continue
		}
		for _, ob := range s.Obligations {
			if _, tem := ob.Params["scanned"]; tem {
				achou = true
				for _, campo := range []string{"scanned", "expired", "held", "skipped", "not_expired"} {
					if _, ok := ob.Params[campo]; !ok {
						t.Errorf("o selo de desfecho nao carrega %q", campo)
					}
				}
			}
		}
	}
	if !achou {
		t.Error("o selo de desfecho da rota NAO carrega contagens — diz «acabou» e nao diz o que fez")
	}
}

// TestOVarredorAutomaticoCONTINUAASelarOsDoisSobASuaNHI é o controlo da simetria.
//
// Sem ele, uma alteração que passasse a atribuir também o desfecho automático ao humano da rota
// passaria no teste acima — e a cadeia deixaria de distinguir as duas vias, que é exactamente o
// que a correcção de 2026-08-21 construiu.
func TestOVarredorAutomaticoCONTINUAASelarOsDoisSobASuaNHI(t *testing.T) {
	node := noRealComRetencao(t)
	svc := newScheduledRetentionService(t, node, timeHora())
	if !retentionSchedulerArmed(node, timeHora()) {
		t.Fatal("o escalonador ficou DESARMADO — este controlo mediria o vazio")
	}
	if !svc.SweepRetentionNow(t.Context()) {
		t.Fatal("a passagem devia concluir")
	}

	inicios, conclusoes := desfechosDaRota(t, node, retentionSchedulerNHI)
	if inicios != 1 || conclusoes != 1 {
		t.Errorf("o varredor devia continuar a selar INICIO e CONCLUSAO sob a sua NHI propria; "+
			"inicios=%d conclusoes=%d", inicios, conclusoes)
	}
}

// timeHora devolve uma cadência positiva — com 0 o escalonador fica DESARMADO e o controlo
// mediria o vazio (defeito que já apareceu neste repositório).
func timeHora() time.Duration { return time.Hour }
