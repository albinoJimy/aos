package intake

import (
	"context"
	"errors"

	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// A INVARIANTE DE NÃO-BYPASS (tecnica/18 §3.5; ADR-013).
//
// A fronteira de segurança real NÃO é o classificador — é o gate por-spawn. Um run
// classificado `simple` é 1-nó e não traz plano pré-aprovado; se tentar delegar, a
// delegação REENTRA no mesmo gate ao nível L0–L5 do chamador, como um "plano
// just-in-time" do subgrafo. O gate de plano do meta-run é apenas a forma
// antecipada (organigrama completo) deste mesmo gate por-spawn.
//
// Este ficheiro modela o PONTO DE REENTRADA como interface ([SpawnGate]) + hook de
// enforcement ([DelegationGuard]). A propriedade que os testes fixam: NÃO existe
// caminho de código de uma classificação `simple` para um spawn que salte a consulta
// ao gate. Logo, manipular o classificador para `simple` não escapa ao gate.

// SpawnAttempt descreve uma tentativa de delegação (spawn de filho, AOS-026) no
// momento em que ocorre. Carrega o nível L0–L5 do CHAMADOR — o gate avalia ao nível
// do chamador, nunca a um nível auto-declarado mais permissivo — e a classificação
// de ORIGEM do run que delega, para tornar explícito que até um run `simple` passa
// aqui. Não carrega texto do objectivo: a decisão do gate não depende de conteúdo
// untrusted.
type SpawnAttempt struct {
	PlanID string
	// ParentNodeID é o nó que tenta delegar; ChildRole o papel pedido para o filho.
	ParentNodeID string
	ChildRole    string
	// CallerLevel é o nível de autonomia do chamador (ADR-013): a reentrada é
	// avaliada a ESTE nível.
	CallerLevel Level
	// OriginClassification é a rota do run que delega. É informativo/auditável — o
	// guard consulta o gate INDEPENDENTEMENTE deste valor; um run `simple` não é
	// tratado de forma especial que o deixe saltar o gate.
	OriginClassification plannerevents.Classification
}

// GateDecision é o veredicto do gate por-spawn. Approved==false significa que o
// spawn NÃO procede sem aprovação (ex.: L0–L3 exigem revisão humana do plano
// just-in-time; danger/capability_gap forçam-na mesmo a L4/L5). RequiresHuman
// distingue "gated à espera de humano" de outras recusas.
type GateDecision struct {
	Approved      bool
	RequiresHuman bool
	// Reason é um rótulo content-free (nunca conteúdo untrusted) para auditoria.
	Reason string
}

// SpawnGate é o gate por-spawn (ADR-013): o ponto de reentrada onde uma delegação é
// aprovada ou barrada ao nível do chamador. A POLÍTICA concreta (mapa L0–L5 →
// decisão, tratamento de danger/capability_gap) é de AOS-236/AOS-121 — este pacote
// declara apenas a FRONTEIRA e garante que ela é sempre atravessada. Implementações
// devem ser fail-closed: na dúvida, não aprovar.
type SpawnGate interface {
	// GateSpawn avalia a tentativa e devolve a decisão. Um erro é fail-closed: o
	// [DelegationGuard] trata-o como recusa e nenhum spawn procede.
	GateSpawn(ctx context.Context, a SpawnAttempt) (GateDecision, error)
}

// Erros do guard de delegação.
var (
	// ErrNilGate — construir um guard sem gate seria um bypass silencioso: recusado.
	ErrNilGate = errors.New("intake: SpawnGate nil (a invariante de nao-bypass exige um gate)")
	// ErrNilSpawn — não há efeito de spawn a proteger; recusado (não é caminho válido).
	ErrNilSpawn = errors.New("intake: funcao de spawn nil")
	// ErrInvalidCallerLevel — o nível do chamador está fora de L0–L5. Um nível fora
	// do intervalo é lixo e NÃO pode ser avaliado como uma autonomia válida: seria
	// exactamente o "nível auto-declarado mais permissivo" que a invariante recusa
	// (um valor numérico grande escaparia a um gate de envelope `<= L3`). Fail-closed:
	// o spawn nunca chega ao gate.
	ErrInvalidCallerLevel = errors.New("intake: CallerLevel fora de L0-L5 (nivel invalido, fail-closed)")
	// ErrSpawnGated — o gate não aprovou a delegação just-in-time; o spawn NÃO ocorre.
	ErrSpawnGated = errors.New("intake: delegacao barrada pelo gate por-spawn (plano just-in-time nao aprovado)")
)

// ChildHandle é o resultado opaco de um spawn aprovado. O intake não o interpreta —
// pertence ao delegador (AOS-026); aqui é só o valor que atravessa o guard quando,
// e SÓ quando, o gate aprova.
type ChildHandle any

// SpawnFunc é o efeito de delegação real (AOS-026), fornecido pelo chamador. O guard
// só o invoca DEPOIS de o gate aprovar — nunca antes, nunca em paralelo.
type SpawnFunc func(ctx context.Context) (ChildHandle, error)

// DelegationGuard é o hook de enforcement da invariante de não-bypass. É o ÚNICO
// ponto por onde um run — de QUALQUER classificação, incluindo `simple` — delega.
// [DelegationGuard.Delegate] consulta SEMPRE o [SpawnGate] antes de qualquer efeito
// de spawn; não há ramo condicionado à classificação que salte essa consulta. Essa
// AUSÊNCIA de ramo é a invariante, e é exercida por teste.
type DelegationGuard struct {
	gate SpawnGate
}

// NewDelegationGuard constrói o guard sobre gate. gate é obrigatório: sem gate não
// há fronteira, e um guard sem fronteira seria exactamente o bypass que este tipo
// existe para impedir — por isso é fail-closed ([ErrNilGate]).
func NewDelegationGuard(gate SpawnGate) (*DelegationGuard, error) {
	if gate == nil {
		return nil, ErrNilGate
	}
	return &DelegationGuard{gate: gate}, nil
}

// Delegate é a reentrada no gate por-spawn. Fluxo, sem excepções por classificação:
//
//  1. valida o [SpawnAttempt.CallerLevel] em L0–L5; um nível fora do intervalo é
//     fail-closed ([ErrInvalidCallerLevel]) e NEM chega ao gate — avaliar a reentrada
//     a um nível-lixo seria o próprio bypass;
//  2. consulta SEMPRE o gate (o "plano just-in-time" do subgrafo, avaliado ao
//     CallerLevel);
//  3. um erro do gate é fail-closed → nenhum spawn, erro propagado;
//  4. decisão não-aprovada → nenhum spawn, [ErrSpawnGated];
//  5. só com aprovação explícita é que spawn é invocado.
//
// spawn só é chamado no passo 5. Um run `simple` que tente delegar percorre
// exactamente este caminho: a sua delegação está gated tal como a de um meta-run.
func (g *DelegationGuard) Delegate(ctx context.Context, a SpawnAttempt, spawn SpawnFunc) (ChildHandle, error) {
	if spawn == nil {
		return nil, ErrNilSpawn
	}
	// (1) Nível do chamador tem de ser um L0–L5 real. Um valor fora do intervalo é
	// lixo: rejeitado ANTES do gate (fail-closed) para que nenhum nível auto-declarado
	// mais permissivo seja avaliado como autonomia válida.
	if !a.CallerLevel.Valid() {
		return nil, ErrInvalidCallerLevel
	}
	// Reentrada incondicional no gate — NÃO há `if a.OriginClassification == simple`
	// que salte esta linha. Remover/curto-circuitar esta consulta parte o teste de
	// não-bypass (o spawn correria sem aprovação).
	dec, err := g.gate.GateSpawn(ctx, a)
	if err != nil {
		return nil, err // fail-closed: erro do gate barra o spawn
	}
	if !dec.Approved {
		return nil, ErrSpawnGated // gated (ex.: L0 à espera de aprovação humana)
	}
	return spawn(ctx)
}
