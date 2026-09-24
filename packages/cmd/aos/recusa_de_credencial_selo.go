package main

// recusa_de_credencial_selo.go — A RECUSA DE CREDENCIAL VOLTA A DEIXAR RASTO DURÁVEL (AOS-435).
//
// # A REGRESSÃO QUE ISTO FECHA, E QUE FOI INTRODUZIDA PELA CORRECÇÃO DE OUTRA
//
// Antes do AOS-428, uma credencial do run que não verificava era negada pelo hook `identity` do
// Reference Monitor, na primeira chamada mediada — tarde, mas AUDITADA: produzia um
// `MediationRecord` selado no WORM, com métrica.
//
// O AOS-428 passou a verificação para a porta do `POST /runs`, que era o que estava certo — e,
// ao fazê-lo, trocou uma negação tardia-mas-auditada por uma negação precoce-e-não-auditada. A
// recusa passou a ser uma linha de log, sujeita à rotação do Docker, sem cadeia de hash. Uma
// campanha de submissões com tokens roubados ficou invisível ao trilho de auditoria.
//
// O AOS-433 acrescentou a métrica, que fecha a DETECÇÃO. Isto fecha a PROVA.
//
// # A QUEM SE ATRIBUI — E PORQUE NÃO AO PRINCIPAL DA CREDENCIAL
//
// A tentação era atribuir o facto ao principal do token recusado. **Não se pode**: foi
// exactamente esse token que não verificou, logo o seu principal é uma afirmação por provar.
// Selá-lo seria gravar numa cadeia tamper-evidente uma identidade que ninguém confirmou.
//
// Atribui-se ao **submissor**, que o gate soberano JÁ verificou antes de esta guarda correr. É
// outra pessoa — quem chamou a rota, não quem o token diz ser — e é precisamente a pessoa que um
// auditor quer encontrar numa campanha: quem está a tentar usar credenciais que não verificam.
//
// # UMA PARTIÇÃO ÚNICA, E NÃO UMA POR RUN — E ISTO É UMA DEFESA, NÃO UMA PREFERÊNCIA
//
// As leituras sensíveis selam em `gov.read/<run>`, uma partição por run. Copiar esse molde aqui
// seria um defeito: o `run_id` de uma submissão recusada vem do PEDIDO, e o run nunca chega a
// existir. Um chamador autenticado que submetesse com `run_id`s aleatórios criaria **partições
// WORM sem limite**, uma por tentativa — poluição do espaço de nomes do trilho de auditoria,
// paga por quem o opera.
//
// Vai numa partição única de governação, no molde de `governance.dsar` e
// `governance.retention`. O `run_id` fica no registo, onde é dado, e não no nome, onde seria
// estrutura.
//
// # A RETOMA NÃO É SELADA, E A RAZÃO É ESTRUTURAL
//
// O `POST /runs/{id}/resume` é rota de `planoControlo`: passa por `admitControl` e
// `admitControlMTLS`, mas nenhum deles entrega ao handler uma identidade VERIFICADA do chamador.
// Sem ela não há a quem atribuir a recusa — e um registo sem principal numa cadeia cujo valor é
// a atribuição seria pior do que nenhum, porque pareceria prova e não o seria.
//
// A métrica do AOS-433 continua a contar as duas rotas. Fica declarado como resíduo do AOS-435.

import (
	"context"

	"github.com/aos-ref/platform/audit"
)

const (
	// credencialRecusadaPartition é a partição ÚNICA dos selos de recusa. Ver o cabeçalho:
	// uma partição por `run_id` deixaria um chamador criar partições sem limite.
	credencialRecusadaPartition = "governance.credential"
	// credencialRecusadaToolID identifica o produtor do selo, no molde de `gov.legalhold` e
	// `gov.control`.
	credencialRecusadaToolID = "gov.credential"
	// credencialRecusadaMotivoObl leva a CAUSA da recusa (expirada, revogada, emissor
	// desconhecido…). Não é PII: é a sentinela traduzida por `nomeDaRecusaDeCredencial`.
	credencialRecusadaMotivoObl = "gov.credential.motivo"
	// capRunSubmit é a capability da acção recusada.
	capRunSubmit = "run:submit"
)

// selarRecusaDeCredencial encadeia no WORM o facto de uma credencial do run ter sido recusada.
//
// # BEST-EFFORT, E PORQUÊ ISSO NÃO É O MESMO QUE A LEITURA
//
// O selo de uma LEITURA sensível é pré-condição: se falha, a leitura é negada (503). Aqui não
// há nada a negar — a recusa já aconteceu e mantém-se. Tornar o selo obrigatório só podia
// transformar um 403 num 503, que diria ao chamador algo sobre o estado do WORM sem nenhum
// ganho de segurança.
//
// Por isso: tenta-se, e uma falha é registada na saúde da selagem e devolvida para o log. A
// métrica do AOS-433 conta a recusa na mesma, pelo que mesmo com o WORM em baixo a campanha não
// fica invisível — fica só sem prova tamper-evidente, que é a degradação declarada.
func (g *readGovernance) selarRecusaDeCredencial(ctx context.Context, submissor readerIdentity, runID, motivo string) error {
	if g == nil || g.worm == nil {
		return nil
	}
	rec := audit.AuditRecord{
		Partition:  credencialRecusadaPartition,
		Timestamp:  g.now().UTC(),
		Decision:   audit.DecisionDeny,
		Principal:  audit.Principal{NHIID: submissor.principal},
		Capability: capRunSubmit,
		RunID:      runID,
		ToolID:     credencialRecusadaToolID,
		// A região do SUBMISSOR, como no selo de leitura — é a fronteira de soberania de quem
		// chamou, não a de um run que nunca existiu.
		Resource: audit.Resource{Type: "run", Value: runID, Region: submissor.region},
		Obligations: []audit.Obligation{
			{Type: readBoardObligation, Fields: []string{submissor.board}},
			{Type: credencialRecusadaMotivoObl, Fields: []string{motivo}},
		},
	}
	_, err := g.worm.Append(ctx, rec)
	if g.saude != nil {
		err = g.saude.registar(err)
	}
	return err
}
