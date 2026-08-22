package main

import (
	"context"
	"crypto/subtle"

	integration "github.com/aos-ref/integration"
	rm "github.com/aos-ref/kernel/reference-monitor"
)

// ---------------------------------------------------------------------------------------------
// O NÚMERO DE APROVADORES NÃO É ESCOLHA DE QUEM PEDE.
//
// Achado 1.10 da varredura adversarial de 2026-08-21:
//
//	`dual_control_required` é um campo do corpo do pedido, passado directo ao gate. O
//	classificador de reversibilidade que o nó tem nunca é consultado para essa decisão. Está
//	provada a CERIMÓNIA de duas pernas; não a OBRIGATORIEDADE.
//
// O gate honra o campo à letra: `false` ⇒ EXACTAMENTE uma perna. Um chamador que pusesse
// `dual_control_required: false` obtinha, com UM humano, um grant para um efeito que a política
// manda ter dois — e a cerimónia, que funciona, nunca era obrigada a acontecer.
//
// A DECISÃO PASSA A SER DO NÓ, e a fonte é o registo pendente que ELE PRÓPRIO selou quando
// escalou a call. Não é o corpo do pedido, que quem pede controla; é o que o Reference Monitor
// escreveu antes de haver pedido nenhum.
//
// E NÃO SE CORRIGE A DECLARAÇÃO — RECUSA-SE, e a razão é criptográfica e não de gosto: o
// `dual_control_required` entra na MENSAGEM ASSINADA de cada perna (ver `fourEyesMessage`).
// Um nó que o sobrepusesse em silêncio estaria a verificar assinaturas contra uma afirmação
// DIFERENTE daquela que os humanos assinaram — e, na prática, nem verificariam: a primeira
// versão desta correcção fazia isso e as pernas legítimas passaram a dar 403.
//
// O humano que assina «basta um aprovador» está a atestar OUTRA COISA. A cerimónia repete-se com
// a exigência certa declarada, e as assinaturas passam a cobrir a afirmação verdadeira.
// ---------------------------------------------------------------------------------------------

// exigenciaDeDuploControlo diz se o NÓ exige dois aprovadores distintos para o efeito que esta
// preview representa. Quem chama RECUSA a cerimónia quando o corpo declarou menos.
//
// Devolve também o registo pendente que a fundamenta — quem chama recusa fail-closed quando ele
// não aparece, porque aprovar um efeito que o nó nunca escalou é aprovar uma preview inventada.
//
// A classificação é recalculada a partir da capability do pendente, e não transportada desde a
// mediação: [rm.CapabilityIrreversible] é puro e determinístico, e a alternativa — arrastar o
// veredicto por Decision → PendingApproval → PendingRecord → rota — atravessaria cinco camadas
// para chegar ao mesmo valor.
func exigenciaDeDuploControlo(
	ctx context.Context,
	pendentes *integration.PendingApprovals,
	runID string,
	preview []byte,
	_ bool,
) (exigir bool, rec integration.PendingRecord, encontrado bool, err error) {
	if pendentes == nil || len(preview) == 0 {
		return false, integration.PendingRecord{}, false, nil
	}
	recs, err := pendentes.ListForRun(ctx, runID)
	if err != nil {
		// FAIL-CLOSED na leitura: não se decide o número de aprovadores sobre um registo que não
		// se conseguiu ler. Degradar para «o cliente que diga» seria repor exactamente o defeito.
		//
		// NÃO ESTÁ PROVADO POR TESTE, e fica dito: a mutação que torna este ramo fail-open não
		// cai, porque o [integration.PendingApprovals] é um struct concreto sobre o Event Store
		// e não há forma de lhe induzir um erro de leitura sem remodelar código de produção para
		// caber num teste. O ramo mantém-se por ser a única postura defensável, não por prova.
		return false, integration.PendingRecord{}, false, err
	}
	for _, r := range recs {
		// Tempo constante: a preview é o segredo da amarra WYSIWYS, e uma comparação que sai
		// cedo transforma esta rota num oráculo de adivinhação de previews.
		if len(r.Preview) == len(preview) && subtle.ConstantTimeCompare(r.Preview, preview) == 1 {
			return rm.CapabilityIrreversible(r.Capability), r, true, nil
		}
	}
	return false, integration.PendingRecord{}, false, nil
}
