package plandispatch

import "context"

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
