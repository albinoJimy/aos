package plandispatch

import (
	"context"
	"errors"
	"fmt"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// payload.go — O CONSUMO DE PAYLOAD POR REFERÊNCIA (ADR-022 §2.3, AOS-272).
//
// # O QUE ISTO NÃO É, E PORQUE ISSO É O PONTO
//
// A rejeição (c) do ADR — congelada na ratificação — recusa o *blackboard*: um estado
// partilhado mutável onde os nós escrevem e leem à vontade. O que fica no lugar é
// nomeado com precisão: «referência a registo no Event Store/MEM com proveniência,
// respeitando “contexto ≠ registo” — o consumidor recebe resumo/referência, não o
// histórico bruto».
//
// Um blackboard tem quatro propriedades, e este ficheiro nega as quatro POR
// CONSTRUÇÃO, não por disciplina de quem o usa:
//
//  1. QUALQUER UM LÊ QUALQUER COISA → aqui um nó só obtém o que DECLAROU consumir. O
//     [PayloadResolver] itera [plan.Node.Consumes] e mais nada; não há método que
//     devolva «tudo o que há», nem sequer «tudo o que este produtor publicou». O
//     conjunto legível de um nó é o seu contrato, fixado na admissão e visível no
//     approval-card.
//  2. O VALOR MUDA DEBAIXO DE QUEM O LÊ → a publicação é um facto append-only com
//     step id por contrato ([plannerevents.Recorder.RecordPayloadPublished]): uma
//     referência é imutável, e o `digest` deixa quem a resolve VERIFICAR o que leu.
//  3. O CONTEÚDO VIAJA → o que atravessa é um [PayloadRef]: locator, digest, tipo,
//     taint e proveniência. Não há campo de conteúdo — ir buscá-lo é ir ao registo,
//     sob a governação desse registo (quarentena da MEM, TTL, erasure).
//  4. A PROVENIÊNCIA DILUI-SE → cada referência carrega as origens de que deriva, pela
//     ordem declarada.
//
// # FRONTEIRA (o que fica a jusante, dito em voz alta)
//
// A [PayloadView] é uma PORTA, como a [ResultView] e a [BranchJournal]: quem a
// implementa sobre o Event Store/MEM é o wiring do ciclo-de-vida do run, a jusante de
// AOS-238 — a mesma fronteira em que a EMISSÃO do veredicto de AOS-271 ficou. Este
// pacote não conhece o Event Store (o seu guard de imports admite só `plan` e
// `plannerevents`, fronteira ADR-018) e não vai lá buscar nada. Até esse wiring
// existir, o consumo é fail-closed: sem referência publicada, [PayloadResolver.Inbox]
// devolve erro e o consumidor não lê nada — nunca um valor por omissão.

// PayloadRef é A REFERÊNCIA que o consumidor recebe por cada contrato declarado. É a
// projecção de `plan.payload_published` para a superfície que o consumidor usa —
// mesmo conteúdo (nenhum), mesma proveniência.
type PayloadRef struct {
	// From/Output identificam o CONTRATO: o nó produtor e o nome do output. São
	// exactamente o par que o consumidor declarou em [plan.PayloadEdge].
	From   string
	Output string
	// Type é o schema RESOLVIDO e Taint o rótulo EFECTIVO (piso derivado do tipo,
	// elevado — nunca baixado — pelo advisory do documento).
	Type  plan.PayloadType
	Taint plan.PayloadTaint
	// ContractDigest é [plan.OutputDigest] do contrato que autorizou esta referência.
	ContractDigest string
	// Store/Stream/Seq/Digest são O LOCATOR do registo e o carimbo do conteúdo
	// referido — o que torna a referência resolúvel E verificável. VAZIOS nas formas
	// FECHADAS, que não têm registo a apontar: o conteúdo vem em [PayloadRef.Closed].
	Store  string
	Stream string
	Seq    uint64
	Digest string
	// Closed é o CONTEÚDO das formas fechadas (`metrics`/`verdict`), validado na
	// emissão: símbolos, códigos e inteiros. Nil nas formas abertas.
	Closed *PayloadClosed
	// DerivedFrom é a PROVENIÊNCIA: os contratos de que este payload deriva, pela
	// ordem publicada.
	DerivedFrom []PayloadOrigin
}

// PayloadClosed é o conteúdo INLINE de um payload de forma fechada. Espelha
// [plannerevents.ClosedPayload] sem o acoplar ao consumidor, como [PayloadOrigin].
type PayloadClosed struct {
	Outcome string
	Reasons []string
	Metrics []PayloadMetric
}

// PayloadMetric é um par (nome de charset fechado, INTEIRO) — a forma que torna
// `metrics` fechado por construção. Slice e não mapa: a ordem do facto é a ordem que o
// replay reproduz, e um mapa não a tem.
type PayloadMetric struct {
	Name  string
	Value int64
}

// PayloadOrigin é uma origem de proveniência (nó + contrato). Espelha
// [plannerevents.PayloadOrigin] sem o acoplar ao consumidor.
type PayloadOrigin struct {
	NodeID string
	Output string
}

// IsUntrusted indica se a referência aponta para material que NÃO autoriza elevação
// (ADR-005). A admissão já garantiu que nenhum consumidor PRIVILEGIADO tem um contrato
// untrusted (`planvalidate`, regra P4); este predicado existe para quem resolve a
// referência a jusante poder propagar o rótulo sem o re-derivar.
func (r PayloadRef) IsUntrusted() bool { return r.Taint != plan.TaintTrusted }

// PayloadView é a PORTA de LEITURA das referências publicadas. Simétrica da
// [ResultView] na assimetria que interessa: LÊ factos, nunca os escreve — nenhum
// caminho deste pacote publica um payload.
type PayloadView interface {
	// Payload devolve a referência publicada para (produtor, output). ok=false
	// significa «ainda não publicada» — o consumidor espera, nunca lê um valor por
	// omissão. Um erro é fail-closed e propagado.
	Payload(ctx context.Context, planID, producerNodeID, output string) (PayloadRef, bool, error)
}

// Erros do consumo de payload (comparáveis por errors.Is — fail-closed).
var (
	// ErrPayloadDeps — [NewPayloadResolver] sem porta ligada.
	ErrPayloadDeps = errors.New("plandispatch: PayloadView nil")
	// ErrPayloadNotPublished — um contrato declarado ainda não tem referência
	// publicada. NÃO é uma avaria: é a espera legítima do consumidor (o produtor não
	// terminou). O chamador distingue-o dos restantes por errors.Is.
	ErrPayloadNotPublished = errors.New("plandispatch: contrato de payload ainda sem referencia publicada")
	// ErrPayloadContractMismatch — a referência devolvida pela porta NÃO corresponde
	// ao contrato declarado no documento APROVADO (produtor, output, tipo, taint ou
	// digest do contrato). Fail-closed.
	ErrPayloadContractMismatch = errors.New("plandispatch: referencia de payload nao corresponde ao contrato declarado")
)

// PayloadResolver resolve, POR CONTRATO DECLARADO, as referências que um nó pode ler.
// Construir com [NewPayloadResolver]. Imutável após a construção; a segurança
// concorrente é a da porta ligada.
type PayloadResolver struct {
	view PayloadView
	// byID é o índice do documento APROVADO por node_id. É o que permite a [Inbox]
	// re-verificar a referência contra o CONTRATO — e não só contra o que o consumidor
	// declarou querer.
	byID map[string]plan.Node
}

// NewPayloadResolver constrói o resolvedor sobre a porta de leitura e o documento
// APROVADO. Ambos são OBRIGATÓRIOS — a ausência é fail-closed ([ErrPayloadDeps]),
// nunca um resolvedor que devolve o conjunto vazio em silêncio.
//
// PORQUE O DOCUMENTO (correcção da auditoria da wave). [Inbox] comparava a referência
// devolvida pela porta apenas com o `consumes` do CONSUMIDOR — produtor, output e
// tipo. Com isso, um `taint` desclassificado ou um `contract_digest` inventado
// atravessavam intactos: a propriedade que a documentação prometia («no replay, um
// digest divergente significa que o documento mudou») não era observável por caminho
// nenhum, porque ninguém a comparava com o contrato. O documento aprovado é o único
// sítio onde o contrato existe; sem ele, a re-verificação era metade de si própria.
func NewPayloadResolver(view PayloadView, doc plan.PlanDocument) (*PayloadResolver, error) {
	if view == nil {
		return nil, ErrPayloadDeps
	}
	if len(doc.Nodes) == 0 {
		return nil, fmt.Errorf("%w: documento aprovado sem nós", ErrPayloadDeps)
	}
	byID := make(map[string]plan.Node, len(doc.Nodes))
	for _, n := range doc.Nodes {
		byID[n.NodeID] = n
	}
	return &PayloadResolver{view: view, byID: byID}, nil
}

// Inbox devolve as referências dos contratos que o nó DECLAROU consumir, pela ordem
// declarada. É a única superfície de leitura de payload de um nó — e a sua estreiteza
// é a propriedade, não uma limitação: o que não está em [plan.Node.Consumes] não é
// alcançável a partir daqui, por caminho nenhum.
//
// RE-VERIFICA O CONTRATO em DUAS direcções sobre o que a porta devolveu:
//
//	(a) contra o que o CONSUMIDOR declarou (`consumes`): produtor, output e tipo;
//	(b) contra o CONTRATO do documento APROVADO (o `outputs` do produtor): o tipo, o
//	    TAINT — que não pode vir abaixo do rótulo EFECTIVO derivado do documento — e o
//	    CONTRACT_DIGEST, recomputado aqui e comparado.
//
// É defesa-em-profundidade deliberada: a admissão já validou o contrato, mas quem
// implementa a [PayloadView] está do outro lado de uma fronteira, e uma referência que
// chegue com outro tipo, com um rótulo desclassificado ou amarrada a um contrato que o
// documento já não tem (registo trocado, migração a meio, wiring errado, documento
// editado depois da publicação) tem de morrer aqui — não ser entregue ao consumidor
// como se fosse o que ele pediu. O tipo é comparado por IDENTIDADE, como na admissão.
//
// A metade (b) é a que dá conteúdo à promessa do `contract_digest`: a emissão
// DERIVA-O do documento ([plannerevents.NewPayloadPublished]) e o consumo RE-DERIVA-O
// e compara. Sem as duas pontas a computá-lo da mesma fonte, o campo era decoração.
//
// FAIL-CLOSED E TOTAL: qualquer contrato por publicar ou divergente aborta a resolução
// INTEIRA e devolve erro. Não há entrega parcial — um consumidor que recebesse metade
// dos seus inputs sem o saber é a forma silenciosa de o contrato não valer nada.
func (r *PayloadResolver) Inbox(ctx context.Context, planID string, node plan.Node) ([]PayloadRef, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if planID == "" {
		return nil, fmt.Errorf("plandispatch: plan_id vazio")
	}
	if len(node.Consumes) == 0 {
		return nil, nil
	}
	out := make([]PayloadRef, 0, len(node.Consumes))
	for _, c := range node.Consumes { // ordem DECLARADA — determinística
		// (b) O CONTRATO do documento aprovado. Um `consumes` cuja origem/output não
		// existe no documento é impossível a jusante da admissão (regras P1/P2); aqui é
		// fail-closed em vez de resolver contra nada.
		producer, known := r.byID[c.From]
		if !known {
			return nil, fmt.Errorf("%w: o produtor %q não existe no documento aprovado (no %q)", ErrPayloadContractMismatch, c.From, node.NodeID)
		}
		contract, declared := producer.FindOutput(c.Output)
		if !declared {
			return nil, fmt.Errorf("%w: o produtor %q não declara o output %q (no %q)", ErrPayloadContractMismatch, c.From, c.Output, node.NodeID)
		}

		ref, ok, err := r.view.Payload(ctx, planID, c.From, c.Output)
		if err != nil {
			return nil, fmt.Errorf("plandispatch: referencia de %q/%q: %w", c.From, c.Output, err)
		}
		if !ok {
			return nil, fmt.Errorf("%w: %q/%q (no %q)", ErrPayloadNotPublished, c.From, c.Output, node.NodeID)
		}
		// (a) O que o consumidor pediu.
		if ref.From != c.From || ref.Output != c.Output || ref.Type != c.Type {
			return nil, fmt.Errorf("%w: pedido %q/%q:%q, devolvido %q/%q:%q", ErrPayloadContractMismatch,
				c.From, c.Output, c.Type, ref.From, ref.Output, ref.Type)
		}
		// (b) O que o documento autoriza. O tipo tem de ser o DECLARADO pelo produtor
		// (não só o pedido pelo consumidor), o rótulo NUNCA pode vir abaixo do efectivo,
		// e o digest tem de fechar sobre o mesmo contrato.
		if ref.Type != contract.Type {
			return nil, fmt.Errorf("%w: contrato %q/%q é %q, referência diz %q", ErrPayloadContractMismatch,
				c.From, c.Output, contract.Type, ref.Type)
		}
		if effective := producer.EffectiveOutputTaint(contract); ref.Taint != effective {
			return nil, fmt.Errorf("%w: taint da referência %q/%q é %q, o efectivo do contrato é %q (a referência não desclassifica)",
				ErrPayloadContractMismatch, c.From, c.Output, ref.Taint, effective)
		}
		if want := plan.OutputDigest(producer, contract); ref.ContractDigest != want {
			return nil, fmt.Errorf("%w: contract_digest de %q/%q diverge do documento aprovado (o plano mudou desde a publicação)",
				ErrPayloadContractMismatch, c.From, c.Output)
		}
		out = append(out, ref)
	}
	return out, nil
}

// RefFromPublished projecta o facto `plan.payload_published` no [PayloadRef] que o
// consumidor recebe. É deliberadamente uma PROJECÇÃO e não uma porta nova — a mesma
// forma de [ResultFromVerdict]: o que faltava não era uma superfície, era a FUNÇÃO que
// diz como o facto se lê como referência. O wiring que implementa a [PayloadView] sobre
// o Event Store chama isto e devolve o resultado; este pacote continua sem saber que
// existe um Event Store, que é a fronteira ADR-018 intacta.
//
// Tudo atravessa porque tudo no facto já passou o filtro que interessa, e esse filtro
// é o SCHEMA: nas formas ABERTAS o facto é locator + digests + ids (não existe campo de
// conteúdo); nas FECHADAS o único conteúdo é o inline que [plannerevents.NewPayloadPublished]
// validou — símbolos, códigos e inteiros. Não há nada a filtrar aqui, e filtrar aqui
// seria a segunda fonte de verdade sobre o que é admissível. Pura: sem I/O, sem relógio.
func RefFromPublished(p plannerevents.PayloadPublishedPayload) PayloadRef {
	ref := PayloadRef{
		From:           p.NodeID,
		Output:         p.Output,
		Type:           p.Type,
		Taint:          p.Taint,
		ContractDigest: p.ContractDigest,
		Store:          string(p.Record.Store),
		Stream:         p.Record.Stream,
		Seq:            p.Record.Seq,
		Digest:         p.Record.Digest,
	}
	if p.Closed != nil {
		c := &PayloadClosed{Outcome: string(p.Closed.Outcome)}
		if len(p.Closed.Reasons) > 0 {
			c.Reasons = append([]string(nil), p.Closed.Reasons...)
		}
		if len(p.Closed.Metrics) > 0 {
			c.Metrics = make([]PayloadMetric, 0, len(p.Closed.Metrics))
			for _, m := range p.Closed.Metrics {
				c.Metrics = append(c.Metrics, PayloadMetric{Name: m.Name, Value: m.Value})
			}
		}
		ref.Closed = c
	}
	if len(p.DerivedFrom) > 0 {
		ref.DerivedFrom = make([]PayloadOrigin, 0, len(p.DerivedFrom))
		for _, o := range p.DerivedFrom {
			ref.DerivedFrom = append(ref.DerivedFrom, PayloadOrigin{NodeID: o.NodeID, Output: o.Output})
		}
	}
	return ref
}
