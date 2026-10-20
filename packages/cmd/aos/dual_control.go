package main

import (
	"context"
	"crypto/subtle"
	"time"

	integration "github.com/aos-ref/integration"
	rm "github.com/aos-ref/kernel/reference-monitor"
	risk "github.com/aos-ref/kernel/reference-monitor/risk"
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
	ttl time.Duration,
) (exigir bool, rec integration.PendingRecord, encontrado bool, expirado bool, err error) {
	if pendentes == nil || len(preview) == 0 {
		return false, integration.PendingRecord{}, false, false, nil
	}
	// O TTL DO PENDENTE VALE AQUI, e não só no varredor — achado da verificação de completude
	// de 2026-08-23.
	//
	// `ListForRun` filtra por grant emitido e por retirada; a IDADE só é avaliada no
	// `ListExpirable`, que é o varredor. Entre o instante em que um pendente cruza o TTL e o tick
	// seguinte havia uma janela — até `AOS_APPROVAL_SWEEP_INTERVAL`, 1 min por omissão — em que
	// ele continuava aprovável e produzia um grant VÁLIDO, com TTL fresco.
	//
	// COM O VARREDOR DESLIGADO (`interval=0`, suportado) a janela é INFINITA: o pendente nunca
	// expira e fica aprovável para sempre.
	//
	// O QUE A EXPIRAÇÃO SIGNIFICA, e é o que estava a ser contornado: o varredor regista que «a
	// accao ficara NEGADA» e o run volta a ser retomável. Aprovar depois do prazo converte um
	// «vai ser negado» num «autorizado» — a decisão que o TTL existe para tomar por omissão.
	//
	// Passa a ser uma INVARIANTE DA ROTA em vez de uma corrida com um trabalho de fundo.
	recs, err := pendentes.ListForRun(ctx, runID)
	if err != nil {
		// FAIL-CLOSED na leitura: não se decide o número de aprovadores sobre um registo que não
		// se conseguiu ler. Degradar para «o cliente que diga» seria repor exactamente o defeito.
		//
		// NÃO ESTÁ PROVADO POR TESTE, e fica dito: a mutação que torna este ramo fail-open não
		// cai, porque o [integration.PendingApprovals] é um struct concreto sobre o Event Store
		// e não há forma de lhe induzir um erro de leitura sem remodelar código de produção para
		// caber num teste. O ramo mantém-se por ser a única postura defensável, não por prova.
		return false, integration.PendingRecord{}, false, false, err
	}
	for _, r := range recs {
		// Tempo constante: a preview é o segredo da amarra WYSIWYS, e uma comparação que sai
		// cedo transforma esta rota num oráculo de adivinhação de previews.
		if len(r.Preview) == len(preview) && subtle.ConstantTimeCompare(r.Preview, preview) == 1 {
			// EXPIRADO? A idade vem do carimbo que o proprio no selou na escalada.
			if ttl > 0 && r.CreatedAt != "" {
				if nascido, perr := time.Parse(time.RFC3339Nano, r.CreatedAt); perr == nil &&
					time.Since(nascido) > ttl {
					return false, r, true, true, nil
				}
			}
			return rm.CapabilityIrreversible(r.Capability), r, true, false, nil
		}
	}
	return false, integration.PendingRecord{}, false, false, nil
}

// ---------------------------------------------------------------------------------------------
// A CLASSE DE RISCO TAMBÉM NÃO É ESCOLHA DE QUEM PEDE.
//
// Achado da verificação de completude de 2026-08-23, e é o campo VIZINHO do que se fechou na
// véspera — na MESMA struct do wire, a três linhas de distância. Fechou-se o
// `dual_control_required` e deixou-se o `risk_class` aberto.
//
// O QUE ELE DECIDE: `hitl.RequiredAuthority(class)` devolve `approve:<classe>`, e cada perna só
// é aceite se a autoridade do aprovador CONTIVER essa string. A comparação é de igualdade exacta,
// sem hierarquia — `approve:danger` NÃO implica `approve:gray`. Logo a classe declarada escolhe
// que escalão de autoridade a cerimónia exige.
//
// A EXPLORAÇÃO, e não é forja: a classe entra na mensagem assinada de cada perna
// (`fourEyesMessage` → `policy := []byte{byte(riskClass), dualByte}`), portanto ninguém a rebaixa
// sozinho. O que acontece é que QUEM PEDE ESCOLHE O QUADRO EM QUE OS HUMANOS ASSINAM: dois
// aprovadores que só têm `approve:gray` assinam «gray» e, no quadro deles, fazem exactamente
// aquilo para que têm autoridade. O nó nunca deriva a classe verdadeira nem a confronta — e
// nada exige que o roster contenha sequer um detentor de `approve:danger`.
//
// NÃO HÁ SEGUNDA LINHA DE DEFESA. Na retoma o Reference Monitor RE-CLASSIFICA e obtém `danger`,
// e depois deita-a fora: as três verificações a jusante perguntam `humanApproved != nil`, um
// booleano. O `ApprovalProof` que lá chega é `{Approvers, DualControl}` — sem classe.
//
// ---------------------------------------------------------------------------------------------
// PORQUE É UM LIMITE INFERIOR, e não a classe exacta.
//
// A classificação completa precisa de três eixos — sensibilidade, egress e reversibilidade — e o
// registo pendente só carrega a capability. Do que ele tem deriva-se UMA implicação, e é a que
// interessa: capability irreversível ⇒ [risk.ClassDanger] (`classifier.go:327`).
//
// Por isso a regra é BINÁRIA e não precisa de comparador de severidade — que, aliás, não existe
// no pacote `risk`. Uma acção irreversível TEM de ser aprovada como `danger`; para as outras o
// nó não sabe mais do que quem pede, e não finge que sabe.
// ---------------------------------------------------------------------------------------------

// classeExigida devolve a classe de risco que o NÓ exige para o efeito que este pendente
// representa, e se essa exigência é derivável de todo.
//
// `derivavel=false` significa «o nó não consegue dizer», e o chamador deixa passar a declaração —
// recusar aí seria transformar «não sei» em «não», e travava toda a aprovação de acções que o
// pendente não permite classificar.
func classeExigida(rec integration.PendingRecord) (risk.Class, bool) {
	if rm.CapabilityIrreversible(rec.Capability) {
		return risk.ClassDanger, true
	}
	return risk.ClassSafe, false
}
