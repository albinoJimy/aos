package plandispatch

import (
	"context"

	"github.com/aos-ref/control-plane/orchestrator/plan"
)

// TerminalOutcome é o ESTADO TERMINAL registado de um nó — o observável
// `terminal_state` de ADR-022 §2.1. Os símbolos são EXACTAMENTE os do enum fechado
// do schema ([plan.EnumComplete]/[plan.EnumFailed]): a avaliação compara símbolos
// do mesmo alfabeto, sem tabela de tradução que pudesse divergir.
type TerminalOutcome string

const (
	// TerminalUnset — sentinela fail-closed: sem estado terminal registado, nenhum
	// predicado sobre `terminal_state` é satisfeito.
	TerminalUnset TerminalOutcome = ""
	// TerminalComplete — o nó terminou com sucesso.
	TerminalComplete TerminalOutcome = "complete"
	// TerminalFailed — o nó terminou em falha.
	TerminalFailed TerminalOutcome = "failed"
)

// VerdictValue é o veredicto ESTRUTURADO registado por um nó — o observável
// `verdict` de ADR-022 §2.1. O emissor é o papel verificador de §2.2 (AOS-271): o
// facto `plan.verdict_recorded`, projectado para aqui por [ResultFromVerdict].
//
// Os símbolos DERIVAM da partição de [plan.SubjectVerdict], como os do emissor
// ([plannerevents.VerdictOutcome]). As três pontas — schema da condição, evento
// emitido, observável consumido — são o MESMO alfabeto por construção; nenhuma
// tabela de tradução no meio, logo nenhuma que possa divergir.
type VerdictValue string

const (
	// VerdictAbsent — sentinela fail-closed: sem veredicto registado, NENHUM
	// predicado sobre `verdict` é satisfeito — nem sequer um `ne`. A ausência de
	// observável não é um valor que se possa comparar: é razão para não ramificar.
	VerdictAbsent VerdictValue = ""
	VerdictPass   VerdictValue = VerdictValue(plan.EnumPass)
	VerdictFail   VerdictValue = VerdictValue(plan.EnumFail)
)

// NodeResultRecord é o RESULTADO REGISTADO de um nó terminal — a ÚNICA superfície
// sobre a qual uma condição de ADR-022 §2.1 pode ser avaliada. É um retrato
// IMUTÁVEL de um facto já apenso (não uma vista viva): é essa imutabilidade que
// torna a avaliação uma função pura e a decisão de ramo estável entre passagens.
//
// Métricas são INTEIRAS por desenho (sem vírgula flutuante): uma comparação sobre
// float não é reproduzível byte-a-byte entre plataformas e partiria ADR-010.
type NodeResultRecord struct {
	// Terminal é o estado terminal registado ([TerminalUnset] se não houver).
	Terminal TerminalOutcome
	// Verdict é o veredicto estruturado registado ([VerdictAbsent] se não houver).
	Verdict VerdictValue
	// Subjects são os nós que o veredicto declara ter EXAMINADO, pela ordem do facto.
	// Vazio quando não há veredicto.
	//
	// PORQUE ATRAVESSA (correcção da auditoria da wave). O `subjects[]` era construído
	// na admissão, validado na emissão e DESCARTADO aqui: `ResultFromVerdict` só
	// projectava outcome+métricas. Consequência: um verificador legítimo podia emitir
	// `pass` sobre o nó X enquanto o ramo que consome esse veredicto libertava o
	// trabalho do nó Y, e nada no caminho quente verificava a correspondência — a
	// atribuição era decorativa em runtime. O despachante exige agora que os sujeitos
	// COBRAM os produtores que a aresta condicional guarda ([evalConditional]).
	Subjects []string
	// Metrics são as métricas DECLARADAS do resultado, por nome. Uma métrica ausente
	// deixa o predicado INDECIDO (o nó espera) — nunca falso por omissão: ver
	// [evalPredicate].
	Metrics map[string]int64
}

// ResultView é a PORTA de LEITURA do resultado registado de um nó. Simétrica de
// [LifecycleView] na assimetria que interessa: LÊ factos, nunca os escreve. É a
// fronteira que garante que o despachante avalia o RESULTADO REGISTADO (ADR-022
// §2.1) e não um estado vivo, opinião de um agente ou saída de um LLM.
type ResultView interface {
	// Result devolve o resultado registado de um nó. ok=false significa «ainda não
	// registado» — a condição fica INDECIDA (o nó espera), nunca falsa por omissão de
	// leitura. Um erro é fail-closed e SURFACED pela passagem de despacho.
	Result(ctx context.Context, planID, nodeID string) (NodeResultRecord, bool, error)
}

// BranchDecision é a decisão de ramo de UM nó, tal como fica REGISTADA. É o
// artefacto que torna o replay uma LEITURA: o despachante consulta-a antes de
// avaliar seja o que for, e uma decisão presente vence sempre a avaliação.
type BranchDecision struct {
	// NodeID é o nó cujas arestas condicionais foram avaliadas.
	NodeID string
	// Taken indica se o ramo foi tomado (conjunção satisfeita).
	Taken bool
	// ConditionDigest é [plan.ConditionDigest] das arestas avaliadas. AMARRA a
	// decisão à expressão exacta: um digest divergente no replay significa documento
	// alterado, e um plano alterado não é um replay ([ErrBranchDigestMismatch]).
	ConditionDigest string
	// Sources são os node_ids das origens avaliadas, pela ordem declarada.
	Sources []string
}

// BranchJournal é a PORTA do registo APPEND-ONLY das decisões de ramo. É o eixo do
// determinismo de ADR-022 §2.4(3): a decisão é um FACTO, não um cálculo repetido.
//
// A leitura é do PLANO INTEIRO de uma vez ([BranchJournal.Decisions]) e não por nó:
// uma passagem de despacho consulta o registo UMA vez, o que mantém o custo linear
// no stream e — mais importante — dá à passagem uma vista COERENTE (todas as
// decisões do mesmo instante), em vez de um mosaico de leituras intercaladas.
type BranchJournal interface {
	// Decisions devolve as decisões JÁ REGISTADAS do plano, indexadas por node_id.
	Decisions(ctx context.Context, planID string) (map[string]BranchDecision, error)
	// Record apensa UMA decisão. Tem de ser IDEMPOTENTE por (plan_id, node_id): a
	// decisão de ramo de um nó é um facto único e imutável do stream.
	Record(ctx context.Context, planID string, d BranchDecision) error
}

// BranchBudget é a PORTA de DÉBITO do orçamento da árvore pela avaliação de
// condições (ADR-022 §2.4(4) / ADR-008): avaliar CUSTA, e o custo entra na mesma
// hierarquia CAS de tokens/$ que qualquer outro trabalho — não há trabalho grátis
// escondido no despachante.
//
// O débito acompanha a DECISÃO, não a tentativa: só se debita quando a condição
// fica DECIDIDA (e, por isso, registada uma única vez). Uma condição ainda indecisa
// — origem por terminar — não debita nada, o que impede que re-invocações do
// escalonador enquanto se espera drenem a árvore.
//
// # PORQUE DUAS FASES (e não um único Debit)
//
// «Por decisão, não por tentativa» só se sustenta se o pagamento estiver amarrado
// ao FACTO, e o facto é a escrita no journal — que pode falhar. Com um débito
// único confirmado ANTES do registo, uma indisponibilidade do Event Store dava N
// débitos pelo mesmo nó em N re-invocações do escalonador (o dreno). A porta é,
// por isso, a mesma disciplina Reserve→Commit do ADR-008 que o [budget.Reserver]
// já impõe internamente, apenas exposta: RESERVA (verifica headroom em toda a
// ancestralidade, atomicamente) → REGISTA o facto → CONFIRMA (ou LIBERTA, se o
// registo falhar).
//
// A reserva é identificada por (plan_id, node_id) — não há handle opaco a
// transportar — porque a decisão de ramo de um nó é, por construção, ÚNICA: a
// idempotency_key do facto `plan.branch_decided` é a mesma chave.
type BranchBudget interface {
	// ReserveConditionEval reserva o custo de UMA avaliação decidida do nó, sem o
	// confirmar. Um erro (tipicamente falta de headroom) é fail-closed: a decisão
	// NÃO é tomada nem registada, e o nó continua em espera.
	ReserveConditionEval(ctx context.Context, planID, nodeID string) error
	// CommitConditionEval confirma a reserva depois de o facto estar apenso.
	CommitConditionEval(ctx context.Context, planID, nodeID string) error
	// ReleaseConditionEval devolve a reserva quando o facto NÃO chegou a ser apenso
	// — sem decisão registada não há nada a pagar.
	ReleaseConditionEval(ctx context.Context, planID, nodeID string) error
}

// NodeState é o estado do ciclo de vida de um nó, LIDO da autoridade (a máquina de
// estados durável do run, AOS-017) — nunca escrito por este pacote. A ordem de
// declaração começa no sentinela fail-closed: um estado desconhecido ou um erro da
// porta trata-se como NÃO-despachável e NÃO-satisfeito (nem despacha, nem satisfaz
// dependentes).
type NodeState int

const (
	// NodeUnknown — sentinela fail-closed (default do zero-value). O nó não é
	// despachável e não satisfaz dependentes.
	NodeUnknown NodeState = iota
	// NodePending — ainda não despachado. O ÚNICO estado a partir do qual este
	// pacote entrega um nó ao sink.
	NodePending
	// NodeRunning — já despachado, em voo. Não re-despacha (evita duplo-despacho sem
	// este pacote deter o "running": a autoridade é a vista do ciclo de vida).
	NodeRunning
	// NodeComplete — sucesso terminal. É o ÚNICO estado que SATISFAZ uma `depends_on`.
	NodeComplete
	// NodeFailed — falha terminal. Não satisfaz dependentes; não re-despacha.
	NodeFailed
)

// Gate é a PORTA do gate de aprovação/materialização do plano (AOS-121/AOS-237). O
// despacho é a jusante desta: fail-closed, nenhum nó despacha até o plano estar
// materializado (`plan.materialized` apenso). Este pacote OBSERVA o gate — nunca o
// aprova nem materializa.
type Gate interface {
	// Materialized reporta se o plano passou o gate e foi materializado. Um false (ou
	// erro) mantém TODO o plano em espera de gate, sem tocar no headroom.
	Materialized(ctx context.Context, planID string) (bool, error)
}

// LifecycleView é a PORTA de LEITURA do estado do ciclo de vida dos nós. A
// autoridade é externa (a máquina de estados durável do run, AOS-017); este pacote
// apenas a consulta — NUNCA escreve transições. É esta assimetria (ler, não
// escrever) que preserva a fronteira ADR-018: o SCH despacha, não é autoridade
// concorrente do ciclo de vida.
type LifecycleView interface {
	// State devolve o estado corrente de um nó. Fail-closed: erro ⇒ [NodeUnknown]
	// tratado como não-despachável e não-satisfeito.
	State(ctx context.Context, planID, nodeID string) (NodeState, error)
}

// Headroom é a PORTA do escalonador de CONCORRÊNCIA run-time (AOS-028): o tecto
// max_spawn = f(headroom). É DISTINTO dos tectos de TAMANHO do plano (AOS-231, a
// montante). O escalonador pode viver noutro módulo, pelo que é modelado como porta
// que o wiring liga.
//
// A disciplina TOCTOU é o coração desta porta: [Available] é um snapshot ADVISORY
// (pode estar obsoleto à hora do spawn), enquanto [Acquire] é a re-verificação
// ATÓMICA no instante do spawn — a única autoridade. Confiar no snapshot em vez de
// Acquire seria oversubscrição (a falha-antes do teste de headroom).
type Headroom interface {
	// Available reporta o tecto corrente max_spawn = f(headroom). ADVISORY: serve de
	// dica de planeamento, não de autorização. NÃO reservar com base nisto.
	Available(ctx context.Context) (int, error)
	// Acquire tenta reservar ATOMICAMENTE UM slot de concorrência contra o headroom
	// VIVO, re-verificando no instante do spawn. Devolve false sob pressão (adiar o
	// spawn): nunca oversubscreve. Um slot reservado é devolvido por [Release] em
	// falha de despacho, ou libertado pelo escalonador (AOS-028) na conclusão do nó —
	// NUNCA por este pacote (que não detém autoridade de ciclo de vida).
	Acquire(ctx context.Context) (bool, error)
	// Release devolve um slot reservado por [Acquire] que NÃO pôde ser usado (falha do
	// sink). Evita o leak sem tornar este pacote dono do slot: a libertação por
	// conclusão é do escalonador.
	Release(ctx context.Context) error
}

// CardOracle é a PORTA que reporta se o CARTÃO de um nó está resolvido. Um nó em
// `waiting_on_capability` (gap de capacidade aberto, capabilitygap) ou `danger` (que
// exige aprovação item-a-item no gate) só é despachável quando o seu cartão está
// RESOLVIDO. Fail-closed: erro ou false ⇒ o nó permanece em espera — e, por
// construção, essa espera NÃO consome headroom (é avaliada antes de [Headroom.Acquire]).
type CardOracle interface {
	// Cleared reporta se o cartão pendente do nó (aprovação de danger / gap de
	// capacidade resolvido) está autorizado. Só consultada para nós marcados
	// [Node.RequiresCard].
	Cleared(ctx context.Context, planID, nodeID string) (bool, error)
}

// DispatchSink é a PORTA de DESPACHO efectivo — o acto do SCH a jusante de toda a
// elegibilidade. Recebe um nó já autorizado (gate + deps + cartão + slot de headroom
// reservado) e entrega-o à execução (spawn do sub-agente / task.node despachado). É
// o único efeito "para fora" deste pacote; não decide nada, apenas executa a entrega.
type DispatchSink interface {
	// Dispatch entrega o nó à execução. Um erro é SURFACED (nunca silencioso): o slot
	// reservado é devolvido e [Dispatcher.Dispatch] propaga o erro — sem spawn parcial
	// silencioso.
	Dispatch(ctx context.Context, node Node) error
}
