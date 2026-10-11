package main

// modelo_fixado.go — O MODELO DE UM RUN É ESCOLHIDO UMA VEZ E FICA FIXADO (AOS-520, ADR-041 §2.4).
//
// Com um candidato a servir, quem decide que modelo serve um run novo é uma função do identificador
// do PLANO de que o run é trabalho e da fatia assinada — não quem submete. A decisão toma-se uma
// vez, em [NodeService.hostRun], depois do lease e do claim e antes do registo de retoma e do
// primeiro turno, e fica gravada em três sítios, com o MESMO valor:
//
//   - no Goal: `Model.ModelID` (o alias), `RouteProfileDigest` (o digest do perfil desse alias) e
//     `ModelPinnedBy` (a marca, com a versão da função) — e por ele no manifesto de cada turno;
//   - no stream do run, o evento `run.model_pinned`, em FAIL-CLOSED: se não ficar gravado, o run não
//     arranca;
//   - no registo de retoma (`ModelPinnedBy`, ao lado do modelo e do digest).
//
// TODAS AS LEITURAS SEGUINTES USAM O VALOR GRAVADO. Um Goal com a marca veio do registo de retoma:
// aqui não se volta a escolher nem se sobrepõe o modelo do nó ([Node.fixarModelo]). Quem confere
// que o modelo fixado ainda é servido é a retoma, antes de re-hospedar
// ([Node.conferirPerfilFixado]), o despachante ([clientePorModelo]) e o adaptador do gateway, em
// cada turno.
//
// SEM CANDIDATO A SERVIR NADA DISTO CORRE. Um run novo recebe o modelo do nó como sempre, sem
// evento e sem marca. A única coisa que um nó com a pasta de perfis e sem candidato a servir faz a
// mais é RECUSAR o run novo de um plano que o livro do canary diz atribuído ao candidato: o plano
// falha, não continua no titular (decisão do dono).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// EventTypeRunModelPinned — o run declara que modelo o nó lhe fixou, e porquê.
const EventTypeRunModelPinned = "run.model_pinned"

// passoDoModeloFixado é o StepID do evento: um por run. Uma segunda escrita devolve a primeira.
const passoDoModeloFixado = "model-pinned"

// modeloFixadoNHI é a identidade emissora: o facto é do NÓ.
const modeloFixadoNHI = "nhi:aos-node/model-pin"

// versaoDoModeloFixado é a versão do payload do `run.model_pinned`.
const versaoDoModeloFixado = "1.0"

// versaoDaEscolha é a marca que fica no Goal e no registo de retoma de um run fixado por escolha:
// a versão da função que repartiu o plano.
const versaoDaEscolha = canary.Version

// reasonModelPinRefused é a razão da transição terminal (`failed`) de um run recusado pela
// fixação do modelo. A causa — de vocabulário fechado — fica no log do nó.
const reasonModelPinRefused = "model_pin_refused"

// Causas de recusa de um run NOVO pela fixação do modelo. Vocabulário FECHADO
// ([causasDaFixacaoDoModelo]). As da retoma de um run já fixado são as do perfil fixado
// ([modelgateway.PinnedRouteProfileCauses]).
const (
	// causaPlanoDoCandidatoSemCandidato — o plano de que o run é trabalho foi atribuído ao
	// candidato, e este nó não o está a servir (interruptor desligado, fatia a zero, registo
	// retirado, ou outro alias ou outro perfil).
	causaPlanoDoCandidatoSemCandidato = "plano_do_candidato_sem_candidato"
	// causaLivroIlegivel — o livro do canary não se leu, ou tem o que não se sabe ler.
	causaLivroIlegivel = "livro_do_canary_ilegivel"
	// causaLivroEmContencao — o ciclo de concorrência optimista do livro esgotou as tentativas.
	causaLivroEmContencao = "livro_do_canary_em_contencao"
	// causaFixacaoNaoGravada — o `run.model_pinned` não ficou gravado.
	causaFixacaoNaoGravada = "fixacao_nao_gravada"
	// causaFixacaoDiferenteDaGravada — o run já tinha um `run.model_pinned`, e o que lá está não é
	// o que este nó escolheria agora. O valor gravado não é reescrito nem ignorado.
	causaFixacaoDiferenteDaGravada = "fixacao_diferente_da_gravada"
	// causaCandidatoSemPerfil — o perfil em vigor para o alias do candidato não é o do registo com
	// que o canary abriu.
	causaCandidatoSemPerfil = "candidato_sem_o_perfil_do_registo"
)

// causasDaFixacaoDoModelo devolve o vocabulário fechado, pela ordem em que está escrito acima.
func causasDaFixacaoDoModelo() []string {
	return []string{causaPlanoDoCandidatoSemCandidato, causaLivroIlegivel, causaLivroEmContencao,
		causaFixacaoNaoGravada, causaFixacaoDiferenteDaGravada, causaCandidatoSemPerfil}
}

// ErrModelPinRefused — o run novo não pode ser hospedado com um modelo fixado. Fail-closed: não
// arranca, e fecha `failed`. Nunca é servido pelo titular «na dúvida».
var ErrModelPinRefused = errors.New("aos: o run foi recusado pela fixacao do modelo (AOS-520)")

// modelPinRefusedError é a recusa, com a causa.
type modelPinRefusedError struct {
	causa string
	err   error
}

func (e *modelPinRefusedError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%v: causa=%s: %v", ErrModelPinRefused, e.causa, e.err)
	}
	return fmt.Sprintf("%v: causa=%s", ErrModelPinRefused, e.causa)
}

func (e *modelPinRefusedError) Unwrap() []error {
	if e.err == nil {
		return []error{ErrModelPinRefused}
	}
	return []error{ErrModelPinRefused, e.err}
}

// causaDaFixacaoDoModelo devolve a causa de uma recusa pela fixação do modelo, ou vazio.
func causaDaFixacaoDoModelo(err error) string {
	var e *modelPinRefusedError
	if errors.As(err, &e) {
		return e.causa
	}
	return ""
}

func recusaDaFixacao(causa string, err error) error {
	return &modelPinRefusedError{causa: causa, err: err}
}

// origemDoPlano é o que o `POST /runs` diz à hospedagem sobre o plano de que o run é trabalho,
// DEPOIS de o nó o ter verificado (AOS-439): o identificador do pedido de plano e o seu `seq` na
// fila. O valor-zero é «o run não é trabalho de um pedido de plano verificado».
//
// Vem do que o nó PROVOU — o `planrequest.submitted` lido do Event Store, sob a reclamação viva de
// quem chama —, e nunca de um campo do corpo aceite por estar declarado: o `plan_id` não entra aqui.
type origemDoPlano struct {
	// pedido é o `plan_request.run_id` verificado.
	pedido string
	// seq é o `seq` do `planrequest.submitted` desse pedido na fila de pedidos de plano.
	seq uint64
}

// chaveDoCanary devolve a chave do canary de um run: o identificador do pedido de plano verificado,
// ou — num run que não é trabalho de um pedido verificado — o `run_id` INTEIRO. Um run sem plano é
// um plano de um nó. O id nunca é partido pelo `~`: um identificador de pedido pode contê-lo.
func chaveDoCanary(runID string, o origemDoPlano) string {
	if o.pedido != "" {
		return o.pedido
	}
	return runID
}

// canaryDoNo é o que o nó sabe do candidato e do canary, composto no arranque. nil ⇒ o nó não tem a
// pasta de perfis assinados configurada: nada disto existe.
type canaryDoNo struct {
	livro *livroDoCanary
	// servir diz se há um candidato a servir, e candidato é ele.
	servir    bool
	candidato candidatoEmVigor
	// atribuidosNoArranque são os planos que o livro dizia atribuídos ao candidato quando este nó
	// arrancou. Só se usa SEM candidato a servir, para recusar os runs novos desses planos; com
	// candidato a servir o livro lê-se em cada run novo.
	atribuidosNoArranque map[string]bool
}

// comporCanaryDoNo compõe o [canaryDoNo] no arranque. Sem a pasta de perfis configurada devolve nil
// sem ler nada.
//
// Com candidato a servir, garante a ABERTURA do canary no livro (idempotente; um reinício não repõe
// a contagem). Sem candidato a servir, lê o livro UMA vez. Nos dois casos uma falha aborta o
// arranque: um nó que não sabe que planos são do candidato não pode decidir nem recusar.
func comporCanaryDoNo(ctx context.Context, es eventstore.EventStore, assinados *perfisAssinados, titular string, log func(string, ...any)) (*canaryDoNo, error) {
	if assinados == nil {
		return nil, nil
	}
	candidato, servir := assinados.candidatoAServir(titular)
	if es == nil {
		if servir {
			return nil, fmt.Errorf("%w: nao ha Event Store onde ter o livro do canary", ErrCanaryUnreadable)
		}
		return nil, nil
	}
	c := &canaryDoNo{livro: &livroDoCanary{es: es, selar: assinados.selarFimDoCanary, log: log}, servir: servir, candidato: candidato}
	if servir {
		if err := c.livro.abrir(ctx, candidato, marcaDaFilaDePedidos(es)); err != nil {
			return nil, fmt.Errorf("aos: abrir o canary do modelo candidato (AOS-520): %w", err)
		}
		estado := "ABERTO"
		if c.livro.fechadoVisto.Load() == 1 {
			estado = "TERMINADO (o candidato ja nao recebe planos novos; os planos que lhe foram atribuidos acabam com ele)"
		}
		log("canary do modelo candidato (AOS-520): %s — candidato %q, digest do registo %s, %d de %d planos servidos pelo candidato (lido do livro duravel %s; um reinicio nao repoe a contagem)",
			estado, candidato.alias, candidato.entryDigest, c.livro.atribuidosVistos.Load(), canaryLimiteDePlanos, canaryStream)
		return c, nil
	}
	atribuidos, err := c.livro.planosAtribuidos(ctx)
	if err != nil {
		return nil, fmt.Errorf("aos: ler o livro do canary do modelo candidato (AOS-520): %w", err)
	}
	c.atribuidosNoArranque = atribuidos
	if len(atribuidos) > 0 {
		log("canary do modelo candidato (AOS-520): este no NAO serve candidato e o livro duravel (%s) tem %d plano(s) atribuido(s) ao candidato — um run novo de um desses planos e RECUSADO (%s): o plano falha, nao continua no titular. Lido uma vez, no arranque",
			canaryStream, len(atribuidos), causaPlanoDoCandidatoSemCandidato)
	}
	return c, nil
}

// modeloFixadoPayload é o corpo do `run.model_pinned`. Só identificadores e digests.
type modeloFixadoPayload struct {
	V string `json:"v"`
	// Role é o papel do modelo: `titular` ou `candidato`.
	Role string `json:"role"`
	// Alias é o nome pedido em que o run fica fixado.
	Alias string `json:"alias"`
	// ProfileDigest é o digest do perfil de rota em vigor para o alias; ausente num alias sem perfil.
	ProfileDigest string `json:"profile_digest,omitempty"`
	// EntryDigest é o digest do registo de entrada assinado do candidato em vigor na escolha, e
	// SharePercent a fatia que ele assina — o «porquê» da escolha, seja ela qual for.
	EntryDigest  string `json:"entry_digest"`
	SharePercent int    `json:"share_percent"`
	// Function é a versão da função de escolha; Reason a razão, de vocabulário fechado.
	Function string `json:"function"`
	Reason   string `json:"reason"`
	// PlanKey é a chave do plano que entrou na função.
	PlanKey string `json:"plan_key"`
}

// escolherModeloDoRun fixa o modelo do run que está a ser hospedado. É o ponto ÚNICO por onde
// passam a submissão, a retoma e a varredura de crash-resume.
//
//   - Um Goal com a marca [agentruntime.Goal.ModelPinnedBy] veio do registo de retoma: não se toca
//     em nada.
//   - Um run RETOMADO sem a marca, e qualquer run num nó sem candidato a servir: [Node.fixarModelo],
//     como sempre — salvo o run novo de um plano já atribuído ao candidato, que é recusado.
//   - Um run NOVO com candidato a servir: o livro do canary decide, e o resultado fica gravado no
//     Goal e no `run.model_pinned` antes de esta função devolver.
//
// Um erro é sempre uma recusa ([ErrModelPinRefused]) com a causa: o run não arranca.
func (s *NodeService) escolherModeloDoRun(ctx context.Context, rs *runState, goal agentruntime.Goal) (agentruntime.Goal, error) {
	n := s.node
	if goal.ModelPinnedBy != "" {
		return goal, nil
	}
	if n == nil || n.canary == nil || rs.retomado {
		return n.fixarModelo(goal), nil
	}
	c := n.canary
	chave := chaveDoCanary(rs.runID, rs.plano)
	if !c.servir {
		if c.atribuidosNoArranque[chave] {
			return goal, recusaDaFixacao(causaPlanoDoCandidatoSemCandidato, nil)
		}
		return n.fixarModelo(goal), nil
	}

	d, err := c.livro.decidir(ctx, c.candidato, chave, rs.plano.seq)
	switch {
	case errors.Is(err, ErrPlanAssignedToAbsentCandidate):
		return goal, recusaDaFixacao(causaPlanoDoCandidatoSemCandidato, nil)
	case errors.Is(err, ErrCanaryContention):
		return goal, recusaDaFixacao(causaLivroEmContencao, err)
	case err != nil:
		return goal, recusaDaFixacao(causaLivroIlegivel, err)
	}

	// O alias: o do candidato, ou o que o nó pede por omissão.
	goal = n.fixarModelo(goal)
	if d.papel == canary.RoleCandidate {
		goal.Model.ModelID = c.candidato.alias
	}
	// O digest do perfil em vigor para esse alias. Para o candidato tem de ser o do registo com que
	// o canary abriu — é o que o livro gravou em cada atribuição.
	digest := ""
	if perfil, tem := n.perfisDaRota.perfis().For(goal.Model.ModelID); tem {
		digest = perfil.Digest()
	}
	if d.papel == canary.RoleCandidate && digest != c.candidato.profileDigest {
		return goal, recusaDaFixacao(causaCandidatoSemPerfil, nil)
	}
	gravado := modeloFixadoPayload{
		V: versaoDoModeloFixado, Role: string(d.papel), Alias: goal.Model.ModelID, ProfileDigest: digest,
		EntryDigest: c.candidato.entryDigest, SharePercent: c.candidato.fatia,
		Function: versaoDaEscolha, Reason: d.razao, PlanKey: chave,
	}
	if err := gravarModeloFixado(ctx, n.EventStore, rs.runID, gravado); err != nil {
		return goal, err
	}
	// Só DEPOIS de gravado é que o Goal leva a escolha: o registo de retoma e o primeiro turno vêm
	// a seguir, com estes valores.
	goal.ModelPinnedBy = versaoDaEscolha
	if digest != "" {
		goal.RouteProfileDigest = digest
	}
	return goal, nil
}

// gravarModeloFixado escreve o `run.model_pinned` no stream do run. FAIL-CLOSED: qualquer falha é
// uma recusa. A chave de idempotência é a do run: se já lá estava um, é o que lá estava que vale —
// e, se não for o que este nó acabou de escolher, o run é recusado em vez de seguir com um modelo
// que o log não diz.
func gravarModeloFixado(ctx context.Context, es eventstore.EventStore, runID string, p modeloFixadoPayload) error {
	if es == nil {
		return recusaDaFixacao(causaFixacaoNaoGravada, errors.New("sem Event Store"))
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return recusaDaFixacao(causaFixacaoNaoGravada, err)
	}
	res, err := es.Append(ctx, runID, eventstore.EventInput{
		Type:     EventTypeRunModelPinned,
		Payload:  raw,
		RunID:    runID,
		StepID:   passoDoModeloFixado,
		Producer: eventstore.Producer{NHIID: modeloFixadoNHI},
	})
	if err != nil {
		return recusaDaFixacao(causaFixacaoNaoGravada, err)
	}
	if res.Status != eventstore.StatusDuplicate {
		return nil
	}
	var antes modeloFixadoPayload
	if err := json.Unmarshal(res.Event.Payload, &antes); err != nil {
		return recusaDaFixacao(causaFixacaoDiferenteDaGravada, err)
	}
	if antes.Alias != p.Alias || antes.ProfileDigest != p.ProfileDigest || antes.Role != p.Role {
		return recusaDaFixacao(causaFixacaoDiferenteDaGravada, nil)
	}
	return nil
}
