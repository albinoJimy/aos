package hitl

import (
	"context"
	"testing"
)

// ---------------------------------------------------------------------------------------------
// O CANÁRIO ENTRA NO QUE O HUMANO ASSINA.
//
// Achado da verificação de completude de 2026-08-23. O `CanaryPassed` estava FORA do canónico —
// e o nó fazia DUAS coisas com ele: recusava a promoção quando era falso (pré-condição do gate) e
// SELAVA-O na cadeia como facto do registo de ratificação.
//
// Flipá-lo não mudava o [SelfModArtifact.RatificationID] e não invalidava assinatura nenhuma.
//
// O QUE NÃO ERA: um bypass de autorização. O nó DECLARA no arranque que o pipeline
// staging→eval-gate→canary→produção corre a montante e FORA DO NÓ; nunca prometeu verificar o
// canário. O que promete é que nada chega a produção sem ratificação humana assinada, fresca e de
// uso-único.
//
// O QUE ERA: a cadeia registava `canary_passed=true` como parte de uma promoção RATIFICADA, e o
// humano não tinha atestado esse facto. É o mesmo eixo dos outros achados desta varredura.
// ---------------------------------------------------------------------------------------------

func TestOCanarioMUDAOratificationID(t *testing.T) {
	comCanario := passingArtifact()
	semCanario := passingArtifact()
	semCanario.CanaryPassed = false

	if comCanario.RatificationID() == semCanario.RatificationID() {
		t.Fatal("flipar `canary_passed` NAO mudou o RatificationID — o campo fica fora do que o " +
			"humano assina, e a cadeia sela-o na mesma como facto ratificado")
	}
}

// TestUmaRatificacaoAssinadaComCanarioFALSONaoPromoveComEleVERDADEIRO é a propriedade que
// interessa — e a DIRECÇÃO importa.
//
// A minha primeira versão testava o sentido inverso (assinar com canário passado, apresentar sem
// ele) e era VACUOSA: a pré-condição do gate recusa `CanaryPassed=false` ANTES de olhar para a
// assinatura, pelo que o teste passava mesmo com o campo fora do canónico. A mutação denunciou-a.
//
// O sentido do ATAQUE é este: o humano assina um canónico onde o canário está a FALSO, e quem
// apresenta o artefacto flipa-o para VERDADEIRO. A pré-condição fica satisfeita, a assinatura —
// antes desta correcção — continuava a validar, e a cadeia selava `canary_passed=true` como facto
// de uma promoção ratificada que ninguém atestou.
func TestUmaRatificacaoAssinadaComCanarioFALSONaoPromoveComEleVERDADEIRO(t *testing.T) {
	h := newRatHarness(t)

	// O humano assina um canónico com o canário a FALSO.
	falhado := passingArtifact()
	falhado.CanaryPassed = false
	assinada := signRatificationFor(t, h.vault, "ratifier", true, falhado)

	// E quem apresenta flipa-o para VERDADEIRO — a pré-condição passa a estar satisfeita.
	flipado := falhado
	flipado.CanaryPassed = true

	admit, err := h.gate.Ratify(context.Background(), flipado, assinada)
	if err == nil && admit {
		t.Fatal("uma ratificacao assinada com `canary_passed=false` promoveu o artefacto com ele a " +
			"TRUE — o campo nao esta amarrado a assinatura, e a cadeia sela-o como facto ratificado")
	}
}

// TestOArtefactoHONESTOContinuaAPromover é a âncora anti-vacuidade.
//
// Sem ela, um `canonical()` que devolvesse bytes aleatórios passaria nos dois testes acima e
// tornaria TODA a promoção impossível — o defeito simétrico.
func TestOArtefactoHONESTOContinuaAPromover(t *testing.T) {
	h := newRatHarness(t)
	art := passingArtifact()
	assinada := signRatificationFor(t, h.vault, "ratifier", true, art)

	admit, err := h.gate.Ratify(context.Background(), art, assinada)
	if err != nil {
		t.Fatalf("uma ratificacao HONESTA devia promover: %v", err)
	}
	if !admit {
		t.Fatal("o artefacto honesto NAO foi promovido")
	}
}
