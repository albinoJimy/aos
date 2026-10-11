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
// SEM CANDIDATO A SERVIR NADA DISTO CORRE PARA UM RUN NOVO. Um run novo recebe o modelo do nó como
// sempre, sem evento, sem marca e SEM UMA LEITURA A MAIS do Event Store. A única coisa que um nó com
// a pasta de perfis e sem candidato a servir faz a mais é RECUSAR o run novo de um plano que o livro
// do canary dizia atribuído ao candidato no arranque (um mapa em memória): o plano falha, não
// continua no titular (decisão do dono).
//
// DOIS CAMINHOS, SEPARADOS POR UM FACTO DO NÓ. O serviço lê o estado durável de um `run_id` antes
// de o hospedar ([NodeService.submitObservando]) — já o lia, para recusar um run terminado:
//
//	(i)  o `run_id` não tem estado durável — é um run NOVO. Não se lê história nenhuma: não há.
//	     É aqui que a escolha corre, e só aqui uma recusa fecha o run `failed`.
//	(ii) o `run_id` já existe (`running`) — um `POST /runs` de um run em curso. É uma RETOMA por
//	     outro nome, e tem o contrato da retoma: lê-se o que está selado para o run
//	     ([Node.goalDoRunExistente]) e, se este nó não o pode continuar com o que está selado, a
//	     submissão é RECUSADA ANTES DE HOSPEDAR — o run fica como estava, recuperável. Nunca é
//	     selado `failed` por aqui, e nunca passa para o titular em silêncio.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
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

// marcaDeEscolhaConhecida diz se a marca é uma versão da função de escolha que este binário conhece.
// Conjunto FECHADO: hoje, uma. Um run fixado por uma versão que o binário não conhece não é
// reinterpretado — nem na retoma, nem numa re-submissão.
func marcaDeEscolhaConhecida(marca string) bool {
	return marca == versaoDaEscolha
}

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
	// causaLivroDeVersaoMaisRecente — o plano está atribuído ao candidato e o livro tem eventos de
	// uma versão mais recente do nó: esta versão não sabe se o pode servir.
	causaLivroDeVersaoMaisRecente = "livro_de_versao_mais_recente"
	// causaMarcaSemFixacaoGravada — o Goal traz a marca de modelo fixado por escolha e o log do nó
	// não tem, para esse run, uma fixação com essa marca, esse alias e esse perfil; ou a marca não
	// é uma versão da função de escolha que este binário conheça.
	causaMarcaSemFixacaoGravada = "marca_sem_fixacao_gravada"
	// causaHistoriaDoRunIlegivel — o nó não conseguiu ler do seu log se o run já existia (o stream
	// do run, ou o seu registo de retoma). Sem saber se o run é novo, não se escolhe.
	causaHistoriaDoRunIlegivel = "historia_do_run_ilegivel"
)

// fixado devolve o modelo em que o log diz que o run está fixado por escolha: o do registo de
// retoma com a marca, ou — se o run ainda não tem registo — o do `run.model_pinned`.
//
// `contradiz` diz que o log se contradiz, e então não se escolhe um dos lados: um registo de retoma
// SEM a marca num run que tem um `run.model_pinned`; ou o registo e o evento a dizer aliases ou
// digests de perfil diferentes.
func (h historiaDoRun) fixado() (alias, digest, marca string, ok, contradiz bool) {
	switch {
	case h.registo != nil && h.registo.ModelPinnedBy != "":
		if h.fixacao != nil && (h.fixacao.Alias != h.registo.Model.ModelID || h.fixacao.ProfileDigest != h.registo.RouteProfileDigest) {
			return "", "", "", false, true
		}
		return h.registo.Model.ModelID, h.registo.RouteProfileDigest, h.registo.ModelPinnedBy, true, false
	case h.registo != nil && h.fixacao != nil:
		return "", "", "", false, true
	case h.fixacao != nil:
		return h.fixacao.Alias, h.fixacao.ProfileDigest, h.fixacao.Function, true, false
	}
	return "", "", "", false, false
}

// causasDaFixacaoDoModelo devolve o vocabulário fechado, pela ordem em que está escrito acima.
func causasDaFixacaoDoModelo() []string {
	return []string{causaPlanoDoCandidatoSemCandidato, causaLivroIlegivel, causaLivroEmContencao,
		causaFixacaoNaoGravada, causaFixacaoDiferenteDaGravada, causaLivroDeVersaoMaisRecente, causaMarcaSemFixacaoGravada, causaHistoriaDoRunIlegivel}
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
//
// A chave leva o TIPO ([chaveCanary]): um pedido de plano e um run avulso com o mesmo identificador
// são dois planos, e a atribuição de um não vale para o outro.
func chaveDoCanary(runID string, o origemDoPlano) chaveCanary {
	if o.pedido != "" {
		return chaveDePedido(o.pedido)
	}
	return chaveDeRun(runID)
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
//
// `sobreJetStream` diz se o Event Store do nó é o backend JetStream. Com um candidato a servir sobre
// esse backend, o arranque AVISA: o backend tem um defeito conhecido no CAS com a vista local
// atrasada (reproduzido em `substrate/eventstore/jetstream/aos520_cas_vista_atrasada_test.go`) que
// prende o livro numa réplica quando outra escreve — o candidato só é suportado com UMA réplica do
// nó. É um aviso e não uma recusa porque o nó não tem como saber quantas réplicas partilham o
// Event Store: o factor de replicação do stream não é o número de réplicas do nó.
func comporCanaryDoNo(ctx context.Context, es eventstore.EventStore, assinados *perfisAssinados, titular string, sobreJetStream bool, log func(string, ...any)) (*canaryDoNo, error) {
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
		if sobreJetStream {
			log("AVISO (AOS-520): candidato a servir com o Event Store sobre JetStream — SO E SUPORTADO COM UMA REPLICA DO NO. O backend JetStream tem um defeito conhecido na escrita com concorrencia optimista (a vista local do stream nao e refrescada quando outra replica escreve): com duas replicas, a que ficar atras passa a recusar TODOS os planos novos do candidato com a causa %s, ate reiniciar. Os runs do titular decididos pela funcao nao sao afectados; nenhum plano e servido pelo modelo errado. Este no nao sabe quantas replicas partilham o Event Store: confirme que e uma so, ou ponha AOS_MODEL_CANDIDATE=off", causaLivroEmContencao)
		}
		if f := c.livro.futurosVistos.Load(); f > 0 {
			log("AVISO (AOS-520): o livro do canary (%s) tem %d evento(s) de uma VERSAO MAIS RECENTE do no, que este binario nao sabe ler — o canary NAO E DECIDIVEL por esta versao: nenhum plano novo e atribuido ao candidato (os runs novos sao do titular, com a razao %s), e um run novo de um plano ja atribuido ao candidato e recusado. Os runs ja fixados acabam como estavam. Actualize este no para a versao que escreveu esses eventos",
				canaryStream, f, razaoLivroMaisRecente)
		}
		estado := "ABERTO"
		if c.livro.fechadoVisto.Load() == 1 {
			estado = "TERMINADO (o candidato ja nao recebe planos novos; os planos que lhe foram atribuidos acabam com ele)"
		}
		log("canary do modelo candidato (AOS-520): %s — candidato %q, digest do registo %s, %d de %d planos ATRIBUIDOS ao candidato (lido do livro duravel %s; um reinicio nao repoe a contagem; «atribuido» nao e «servido»: um plano conta quando lhe e atribuido, mesmo que o run seja depois recusado)",
			estado, candidato.alias, candidato.entryDigest, c.livro.atribuidosVistos.Load(), canaryLimiteDePlanos, canaryStream)
		return c, nil
	}
	atribuidos, err := c.livro.planosAtribuidos(ctx)
	if err != nil {
		return nil, fmt.Errorf("aos: ler o livro do canary do modelo candidato (AOS-520): %w", err)
	}
	c.atribuidosNoArranque = atribuidos
	if f := c.livro.futurosVistos.Load(); f > 0 {
		log("AVISO (AOS-520): o livro do canary (%s) tem %d evento(s) de uma VERSAO MAIS RECENTE do no, que este binario nao sabe ler. Este no nao serve candidato: arranca e serve o titular como sempre; os planos que o livro diz atribuidos ao candidato continuam a ser recusados", canaryStream, f)
	}
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
	// PlanKey é a chave do plano que entrou na função, e PlanKeyKind o seu tipo: `pedido` (o
	// identificador de um pedido de plano verificado) ou `run` (o id de um run sem pedido).
	PlanKey     string `json:"plan_key"`
	PlanKeyKind string `json:"plan_key_kind"`
}

// historiaDoRun é o que o LOG do nó diz de um run que JÁ EXISTE, antes de ele ser re-hospedado: se
// já deu um turno, se tem registo de retoma, e se tem um `run.model_pinned`. É o que está selado
// para o run.
type historiaDoRun struct {
	temTurno bool
	registo  *integration.ResumeRecord
	fixacao  *modeloFixadoPayload
}

// temHistoria diz se o run já existia: um turno gravado, um registo de retoma ou uma fixação.
func (h historiaDoRun) temHistoria() bool {
	return h.temTurno || h.registo != nil || h.fixacao != nil
}

// lerHistoriaDoRun lê do Event Store a história de um run. Um stream que não existe é um run sem
// história. Qualquer outra falha de leitura é devolvida: quem decide não decide sobre o que não leu.
//
// SÓ CORRE NO CAMINHO DE UM RUN QUE JÁ EXISTE ([Node.goalDoRunExistente]). São duas leituras, e a
// segunda é cara: [integration.ResumeRecords.Get] varre o stream das aprovações INTEIRO, que cresce
// com a vida do nó. Um run novo não as paga.
func (n *Node) lerHistoriaDoRun(ctx context.Context, runID string) (historiaDoRun, error) {
	var h historiaDoRun
	if n == nil || n.EventStore == nil {
		return h, errors.New("sem Event Store onde ler a historia do run")
	}
	evs, err := n.EventStore.Read(ctx, runID, 1)
	if err != nil && !errors.Is(err, eventstore.ErrStreamNotFound) {
		return h, err
	}
	for _, ev := range evs {
		switch ev.Type {
		case agentruntime.EventTypeTurnRecorded:
			h.temTurno = true
		case EventTypeRunModelPinned:
			var p modeloFixadoPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil || p.Alias == "" {
				return h, fmt.Errorf("run.model_pinned ilegivel no evento %d", ev.Seq)
			}
			if h.fixacao == nil {
				h.fixacao = &p
			}
		}
	}
	if n.ResumeRecords != nil {
		rec, tem, err := n.ResumeRecords.Get(ctx, runID)
		if err != nil {
			return h, err
		}
		if tem {
			h.registo = &rec
		}
	}
	return h, nil
}

// ErrRunExistenteRecusado — o `run_id` submetido é o de um run que JÁ EXISTE no estado durável, e
// este nó não o pode continuar com o que está selado para ele. A submissão é recusada ANTES de o
// run ser hospedado: nada é escrito, e o run fica como estava (`running`, recuperável) — repõe-se o
// perfil ou o registo do candidato, ou retoma-se num nó que o sirva. A causa, de vocabulário
// fechado, vem a seguir.
var ErrRunExistenteRecusado = errors.New("aos: o run ja existe e este no nao o pode continuar com o que esta selado para ele (AOS-520)")

// runExistenteRecusadoError é a recusa de [ErrRunExistenteRecusado], com a causa.
type runExistenteRecusadoError struct {
	causa string
	err   error
}

func (e *runExistenteRecusadoError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%v: causa=%s: %v", ErrRunExistenteRecusado, e.causa, e.err)
	}
	return fmt.Sprintf("%v: causa=%s", ErrRunExistenteRecusado, e.causa)
}

func (e *runExistenteRecusadoError) Unwrap() []error {
	if e.err == nil {
		return []error{ErrRunExistenteRecusado}
	}
	return []error{ErrRunExistenteRecusado, e.err}
}

// causaDoRunExistenteRecusado devolve a causa de uma recusa de [ErrRunExistenteRecusado], ou vazio.
func causaDoRunExistenteRecusado(err error) string {
	var e *runExistenteRecusadoError
	if errors.As(err, &e) {
		return e.causa
	}
	return ""
}

// goalDoRunExistente é o caminho (ii): o Goal com que um run que JÁ EXISTE pode ser re-hospedado
// neste nó, ou a recusa. Corre ANTES de o run ser hospedado, com ou sem a pasta de perfis.
//
//   - A história não se lê ⇒ recusa (`historia_do_run_ilegivel`).
//   - O log contradiz-se (registo de retoma sem a marca e um `run.model_pinned`; ou o registo e o
//     evento com aliases ou digests diferentes) ⇒ recusa (`fixacao_diferente_da_gravada`).
//   - O run tem registo de retoma ⇒ a conferência é A DA RETOMA ([Node.conferirPerfilFixado]): o
//     alias fixado tem de ser servido por este nó, com o perfil fixado, e a marca tem de ser de uma
//     versão conhecida. Um nó sem a pasta de perfis recusa o run fixado no candidato.
//   - O run só tem o `run.model_pinned` (caiu antes do registo de retoma) ⇒ a mesma conferência,
//     sobre o evento.
//   - O Goal submetido traz a marca ⇒ tem de ser a selada, com o mesmo alias e o mesmo digest;
//     senão recusa (`marca_sem_fixacao_gravada`).
//   - Fixação gravada que o nó serve ⇒ o Goal sai com o alias, o digest e a marca SELADOS.
//   - História sem fixação ⇒ o Goal sai como entrou: o titular de sempre.
func (n *Node) goalDoRunExistente(ctx context.Context, goal agentruntime.Goal) (agentruntime.Goal, error) {
	recusa := func(causa string, err error) (agentruntime.Goal, error) {
		return goal, &runExistenteRecusadoError{causa: causa, err: err}
	}
	h, err := n.lerHistoriaDoRun(ctx, goal.RunID)
	if err != nil {
		return recusa(causaHistoriaDoRunIlegivel, err)
	}
	alias, digest, marca, fixado, contradiz := h.fixado()
	if contradiz {
		return recusa(causaFixacaoDiferenteDaGravada, nil)
	}
	if h.registo != nil {
		// O MESMO contrato da retoma pela API e do crash-resume, sobre o MESMO registo.
		if perr := n.conferirPerfilFixado(*h.registo); perr != nil {
			return recusa(causaDoPerfilFixado(perr), nil)
		}
	} else if fixado {
		if !marcaDeEscolhaConhecida(marca) || !n.aliasServido(alias) {
			return recusa(modelgateway.PinnedCauseModelNotServed, nil)
		}
		var assinados *perfisAssinados
		if n != nil {
			assinados = n.perfisDaRota
		}
		if perr := assinados.perfis().CheckPinnedProfile(alias, digest); perr != nil {
			return recusa(causaDoPerfilFixado(perr), nil)
		}
	}
	if !fixado {
		if goal.ModelPinnedBy != "" {
			return recusa(causaMarcaSemFixacaoGravada, nil)
		}
		return goal, nil
	}
	if goal.ModelPinnedBy != "" && (goal.ModelPinnedBy != marca || goal.Model.ModelID != alias || (digest != "" && goal.RouteProfileDigest != digest)) {
		return recusa(causaMarcaSemFixacaoGravada, nil)
	}
	goal.Model.ModelID, goal.ModelPinnedBy = alias, marca
	if digest != "" {
		goal.RouteProfileDigest = digest
	}
	return goal, nil
}

// escolherModeloDoRun fixa o modelo do run que está a ser hospedado. É o ponto ÚNICO por onde
// passam a submissão, a retoma e a varredura de crash-resume.
//
//   - Um run que JÁ EXISTE (`rs.existente`: a retoma, o crash-resume, ou um `POST /runs` de um run
//     em curso) não é escolhido: o seu Goal já foi conferido com o que está selado, ANTES de chegar
//     aqui. Com a marca, fica como está; sem ela, é o modelo do nó, como sempre.
//   - Um run NOVO não tem história para ler. Um Goal novo que traga a marca é recusado: nada a
//     selou. Sem candidato a servir, [Node.fixarModelo] — salvo o run de um plano que o livro dizia
//     atribuído ao candidato no arranque, que é recusado. Com candidato a servir, o livro do canary
//     decide, e o resultado fica gravado no Goal e no `run.model_pinned` antes de esta função
//     devolver.
//
// Um erro é sempre uma recusa ([ErrModelPinRefused]) com a causa: o run novo não arranca, e fecha
// `failed`.
func (s *NodeService) escolherModeloDoRun(ctx context.Context, rs *runState, goal agentruntime.Goal) (agentruntime.Goal, error) {
	n := s.node
	if n == nil || rs.existente {
		return n.fixarModelo(goal), nil
	}
	if goal.ModelPinnedBy != "" {
		return goal, recusaDaFixacao(causaMarcaSemFixacaoGravada, nil)
	}
	if n.canary == nil {
		return n.fixarModelo(goal), nil
	}
	c := n.canary
	chave := chaveDoCanary(rs.runID, rs.plano)
	if !c.servir {
		if c.atribuidosNoArranque[chave.indice()] {
			return goal, recusaDaFixacao(causaPlanoDoCandidatoSemCandidato, nil)
		}
		return n.fixarModelo(goal), nil
	}

	d, err := c.livro.decidir(ctx, c.candidato, chave, rs.plano.seq)
	switch {
	case errors.Is(err, ErrPlanAssignedToAbsentCandidate):
		return goal, recusaDaFixacao(causaPlanoDoCandidatoSemCandidato, nil)
	case errors.Is(err, ErrCanaryNewerVersion):
		return goal, recusaDaFixacao(causaLivroDeVersaoMaisRecente, nil)
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
	// O digest do perfil em vigor para esse alias. Para o candidato é, por construção, o do registo
	// com que o canary abriu: o candidato em vigor e o conjunto de perfis do nó saem dos MESMOS
	// registos, lidos uma vez no arranque — não há aqui uma conferência a fazer entre os dois.
	digest := ""
	if perfil, tem := n.perfisDaRota.perfis().For(goal.Model.ModelID); tem {
		digest = perfil.Digest()
	}
	gravado := modeloFixadoPayload{
		V: versaoDoModeloFixado, Role: string(d.papel), Alias: goal.Model.ModelID, ProfileDigest: digest,
		EntryDigest: c.candidato.entryDigest, SharePercent: c.candidato.fatia,
		Function: versaoDaEscolha, Reason: d.razao, PlanKey: chave.id, PlanKeyKind: chave.tipo,
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
