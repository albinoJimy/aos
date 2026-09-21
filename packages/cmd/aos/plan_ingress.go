package main

// plan_ingress.go — POR ONDE ENTRA UM OBJECTIVO NO CAMINHO DO PLANO (AOS-417, ADR-028).
//
// # O QUE ISTO RESOLVE
//
// O caminho do plano não tinha superfície de rede nenhuma: o `aos-orq` é só CLI, e o compose
// exclui-o do arranque de propósito («um `serve` possui um run e termina, não é um daemon»).
// Alguém tinha de estar no terminal do servidor. Tudo o resto que faltava para o produto ser
// usável — não haver UI, cunhar o NHI à mão, a cerimónia de aprovação — podia ser resolvido e o
// problema permanecia, porque este passo permanecia.
//
// # O QUE ISTO NÃO É
//
// Não é o orquestrador. O nó GRAVA UM FACTO e devolve; quem corre o plano é o `aos-orq`, que
// consome o facto e reclama o lease como sempre fez. O nó não importa `control-plane/orchestrator`
// — nem aqui nem transitivamente —, e o guard-test de fronteira que o impõe não muda. É a
// diferença entre o nó CONHECER a existência do caminho do plano e o nó EXECUTAR o caminho do
// plano: a primeira é o que o ADR-028 aceita, a segunda é o que o ADR-018 proíbe.
//
// O ADR-023 já abria esta porta: o SCH «escreve os seus próprios factos de decisão, que vivem no
// stream do plano, não no stream do run». Um pedido de plano é dessa família — não é uma
// transição de ciclo de vida, e por isso não precisa da posse para ser escrito.
//
// # O QUE UMA REVISÃO ADVERSARIAL CORRIGIU AQUI, E QUE NÃO SE PODE PERDER
//
// A primeira versão desta rota tinha dois defeitos CRÍTICOS, ambos com a mesma raiz: seguiu o
// molde do `POST /runs` sem verificar se as razões do molde valiam aqui.
//
//  1. **A fila estava no espaço de nomes dos runs.** O stream chamava-se `plan.requests` e o
//     read-path de trajectória endereça streams POR `run_id` — logo `GET /runs/plan.requests/
//     trajectory` servia a fila INTEIRA, ao vivo, a um leitor de QUALQUER região (uma fila não
//     tem residência selada, pelo que a verificação cross-region caía no ramo «run legado, sem
//     check»). O `run_id` também não era validado, pelo que `POST /runs {run_id:"plan.requests"}`
//     injectava eventos de run dentro da fila. Ver [runIDReservado].
//
//  2. **Selava a residência de um run que não criava.** O `POST /runs` sela porque VAI HOSPEDAR
//     o run; esta rota selava sem criar nada e deixava o `run_id` LIVRE. Como a residência é
//     fixada pelo PRIMEIRO registo e não é re-negociável, quem pedisse um plano primeiro fixava
//     a fronteira de soberania de um run que OUTRA pessoa viria a criar: a vítima corria o run e
//     recebia 404 no seu próprio resultado, e a região do atacante lia-o. É pior do que o squat
//     que o `POST /runs` já permitia — ali o id fica ocupado e a vítima não corre (negação de
//     serviço); aqui a vítima corre e o conteúdo sai (exfiltração).
//
// A lição, que vale para a próxima rota que espelhe outra: copiar a FORMA de um handler é
// barato, e copiar a JUSTIFICAÇÃO é o que tem de ser feito à mão. Um passo cuja razão de ser
// não se verifica no destino não é defesa em profundidade — é um efeito colateral por escrever.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/aos-ref/substrate/eventstore"
)

// EventTypePlanRequestSubmitted — um objectivo entrou no caminho do plano e espera consumo.
//
// FAMÍLIA PRÓPRIA, e não `plan.*`. A família `plan` existe e tem dono declarado em
// `plannerevents`, que vive no módulo que o nó está PROIBIDO de importar. Escrever nela a partir
// daqui faria a coluna «dono» da taxonomia mentir, e o ADR-028 §2.1 diz explicitamente para não
// reutilizar aquelas constantes. Uma família própria custa uma linha em `tecnica/13` e mantém a
// propriedade legível: o PEDIDO é do nó, o PLANO é do orquestrador.
const EventTypePlanRequestSubmitted = "planrequest.submitted"

// streamsReservados é o PREFIXO que separa os streams internos do nó do espaço de nomes dos
// `run_id`. Existe porque o Event Store tem UM espaço de nomes de streams e as rotas de run
// endereçam-no directamente a partir do URL: sem esta separação, todo o stream interno é
// legível por `GET /runs/{id}/trajectory` e escrevível por `POST /runs`.
//
// A barra é deliberada: nenhum `run_id` em uso a contém, e [runIDReservado] recusa-a na
// fronteira das duas rotas de submissão — a reserva só vale enquanto for IMPOSTA.
const streamsReservados = "aos.internal/"

// planRequestStream é o stream ÚNICO onde os pedidos se acumulam — a fila. Não se inventa
// substrato: o Event Store já é append-only, ordenado e durável, e o consumo-uma-só-vez sai da
// idempotency-key, no molde que o `approval_store_durable` já usa para as aprovações.
const planRequestStream = streamsReservados + "plan-requests"

// planRequestRunID é o «run» sintético que, com o StepID, forma a idempotency-key
// (`run_id + ":" + step_id`). O stream não pertence a run nenhum: pertence à fila.
const planRequestRunID = streamsReservados + "plan-requests"

// planIngressNHI é a identidade emissora do facto. O pedido é do NÓ — quem o submeteu está no
// payload, atribuído pela credencial verificada, não auto-declarado.
const planIngressNHI = "nhi:aos-node/plan-ingress"

// maxObjetivoBytes limita o objectivo de um pedido de plano.
//
// O tecto de corpo (`maxBodyBytes`) admite ~1 MiB, e um objectivo é uma frase ou um parágrafo —
// não um ficheiro. Sem tecto próprio, cada pedido escreve até um megabyte de texto livre e
// untrusted num log append-only que vai ao WAL e aos backups, e a fila não tem hoje quem a
// consuma nem retenção que a encolha. 16 KiB é generoso para o uso legítimo e fecha a diferença
// de quatro ordens de grandeza entre o que o produto precisa e o que a fronteira aceitava.
const maxObjetivoBytes = 16 << 10

// runIDReservado indica se um `run_id` invade o espaço de nomes interno do nó.
//
// É chamada pelas DUAS rotas de submissão, e não só por esta, e isso é necessário e não zelo:
// se o `POST /runs` puder nomear o stream da fila, os eventos desse run são apensos À FILA e um
// consumidor que não filtre por `type` lê transições de estado como pedidos. Uma reserva que só
// uma das portas respeita não é uma reserva.
func runIDReservado(runID string) bool {
	return strings.HasPrefix(strings.TrimSpace(runID), streamsReservados)
}

// planRequest é o corpo aceite. Deliberadamente MÍNIMO: o que o orquestrador precisa para
// decompor é o objectivo; tudo o resto — tools, snapshot, aprovadores — é decisão do plano, não
// do pedido.
type planRequest struct {
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
}

// planRequestResponse tem a MESMA forma da resposta do `POST /runs`: um pedido repetido e um
// pedido novo são indistinguíveis de fora, e é isso que se quer.
type planRequestResponse struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

// planRequestVersao é a versão do SCHEMA DESTE PAYLOAD, e não a do envelope.
//
// São coisas distintas e a distinção custou-me uma revisão: o Event Store preenche
// `schema_version` no ENVELOPE (hoje "1.0"), que versiona a forma do envelope — não a do corpo
// que cada tipo de evento define. O `tecnica/13` §3.1 di-lo por escrito: o payload «tem o seu
// próprio schema por tipo de evento».
//
// PORQUE PRECISA DE VERSÃO PRÓPRIA, e com urgência maior do que a dos outros factos do nó: o
// ÚNICO consumidor previsto deste facto vive no `packages/cmd/aos-orq`, que é OUTRO MÓDULO, e
// este tipo está em `package main` — ele NÃO O PODE IMPORTAR. Vai reescrever a struct à mão, e
// nenhum gate liga as duas cópias. Sem um campo que as distinga, acrescentar um campo aqui
// amanhã é invisível do outro lado; com ele, o consumidor recusa o que não sabe ler em vez de
// o interpretar por omissão.
//
// RESÍDUO DECLARADO: publicar o schema em `packages/substrate/eventstore/schemas/` — como o
// envelope já tem — fecharia isto melhor do que uma constante, e fica por fazer. A versão é o
// que se consegue hoje sem inventar maquinaria que ninguém pediu.
const planRequestVersao = "1.0"

// planRequestPayload é o facto gravado — e é um CONTRATO, porque alguém noutro módulo o vai ler.
//
// O `principal`, o `board` e a `region` vêm da credencial VERIFICADA pelo mesmo gate que
// autoriza o `POST /runs` — nunca do corpo, que não teria como os provar. O `board` viaja porque
// é o identificador de GOVERNAÇÃO do pedido (não é PII) e, desde que esta rota deixou de selar
// residência, não fica gravado em mais lado nenhum: sem ele o consumidor não consegue
// reconstituir sob que autoridade o pedido entrou.
type planRequestPayload struct {
	Versao    string `json:"v"`
	RunID     string `json:"run_id"`
	Objective string `json:"objective"`
	Principal string `json:"principal,omitempty"`
	Board     string `json:"board,omitempty"`
	Region    string `json:"region,omitempty"`
}

// handlePlanRequest recebe um objectivo, grava o facto e devolve.
//
// # PORQUE É QUE A COLISÃO RESPONDE 201 E NUNCA 409
//
// Um pedido para um run que já foi pedido devolve `201 accepted`, exactamente como um pedido
// novo. Não é conveniência: é a garantia de NÃO-ORACULARIDADE do ADR-016. O `POST /runs` já
// responde assim de propósito, e só dá `409` a quem traz credencial forte E residência selada
// coincidente — uma excepção que existe por retro-compatibilidade e que aqui NÃO se repete,
// porque não há comportamento antigo a preservar. Uma superfície nova começa na postura mais
// apertada que consegue.
//
// Quem quiser saber o estado de um run pede-o pela rota de leitura, que passa pela governação.
func (h *apiHandler) handlePlanRequest(w http.ResponseWriter, r *http.Request) {
	// (1) ADMISSION antes de ler o corpo. O balde é o MESMO do `POST /runs`, de propósito: uma
	// superfície nova que não passasse pela admissão seria uma porta lateral para o mesmo nó.
	//
	// O TECTO DE RUNS EM CURSO NÃO SE APLICA AQUI, e dizê-lo é mais honesto do que copiá-lo: ele
	// conta runs hospedados, e esta rota não hospeda nenhum (é o que
	// [TestAOS417IngressoNaoHospedaORun] impõe). Chamá-lo seria escrever uma guarda que nunca
	// dispara e afirmar uma protecção que não existe. O que LIMITA esta rota é o balde, o tecto
	// de corpo e o [maxObjetivoBytes]; o TECTO DE PENDENTES da fila é decisão em aberto —
	// ADR-028 §4, registada no AOS-417 como resíduo por decidir, não como esquecimento.
	if !h.bucket.allow() {
		writeError(w, http.StatusTooManyRequests, "rate limit excedido")
		return
	}

	var req planRequest
	if status, ok := h.decodeJSON(w, r, &req); !ok {
		writeError(w, status, "corpo invalido")
		return
	}
	if req.RunID == "" {
		writeError(w, http.StatusBadRequest, "run_id em falta")
		return
	}
	// O ESPAÇO DE NOMES INTERNO É RESERVADO. Ver [runIDReservado]: sem isto, um pedido pode
	// nomear o stream da própria fila.
	if runIDReservado(req.RunID) {
		writeError(w, http.StatusBadRequest, "run_id reservado")
		return
	}
	if req.Objective == "" {
		// Um pedido sem objectivo não tem o que decompor. Recusa-se aqui em vez de gravar um
		// facto que o consumidor teria de rejeitar mais tarde, longe de quem o submeteu.
		writeError(w, http.StatusBadRequest, "objective em falta")
		return
	}
	if len(req.Objective) > maxObjetivoBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "objective demasiado longo")
		return
	}

	p := planRequestPayload{Versao: planRequestVersao, RunID: req.RunID, Objective: req.Objective}

	// (2) SOBERANIA — a MESMA autoridade que o `POST /runs` usa, e SÓ a autoridade.
	//
	// NÃO SE SELA RESIDÊNCIA AQUI. O selo é pré-condição da HOSPEDAGEM de um run («nenhum run
	// soberano fica legível antes de a sua residência estar durável») e esta rota não hospeda
	// nada: selar fixaria, de forma não-renegociável, a fronteira de soberania de um `run_id`
	// que fica LIVRE para outra pessoa criar. Ver o cabeçalho deste ficheiro e
	// [TestAOS417IngressoNaoReivindicaResidencia]. A residência continua a ser selada por quem
	// cria o run — incluindo os runs que o `aos-orq` vier a criar a partir deste pedido —, que é
	// onde o invariante pertence. A REGIÃO do submissor viaja no facto para o consumidor a
	// honrar; o que não viaja é uma reivindicação sobre um run que ainda não existe.
	if h.readGov != nil {
		submitter, ok := h.readGov.authorize(r)
		if !ok {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		if submitter.principal == "" {
			writeError(w, http.StatusForbidden, "nao autorizado")
			return
		}
		p.Principal = submitter.principal
		p.Board = submitter.board
		p.Region = submitter.region
	}

	bruto, err := json.Marshal(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "erro interno")
		return
	}

	// SEM SUBSTRATO NÃO HÁ FILA. O Event Store do nó é nil quando a execução durável não está
	// composta — o `/readyz` já conta com isso. Aqui o efeito é mais duro do que degradar a
	// prontidão: sem onde gravar o facto, aceitar o pedido seria prometer uma corrida que
	// desaparece com o processo. Recusa-se, e diz-se que é indisponibilidade e não erro do
	// chamador, porque é o nó que não está em condições — não o pedido que está mal.
	//
	// HOJE É INALCANÇÁVEL, e fica escrito para não ser lido como protecção activa: o `Bootstrap`
	// nunca deixa o store nil (cria um de referência no ramo default ou falha o arranque) e o
	// `NewNodeService` recusa um nó sem ele. É uma asserção local do invariante no ponto de uso,
	// no mesmo idioma da guarda de principal vazio do `handleSubmit`.
	if h.node == nil || h.node.EventStore == nil {
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return
	}

	// (3) O FACTO. A idempotency-key é (fila, pedido-deste-run): um segundo pedido para o mesmo
	// run devolve `StatusDuplicate` com erro NIL — não é um erro, e por isso cai no mesmo
	// caminho de sucesso sem ramo próprio. É o que dá a idempotência SEM a distinguir de fora:
	// o pedido já está na fila, não se duplica, e a resposta é a mesma. Mesmo primitivo que o
	// `approval_store_durable` usa para reclamar uma aprovação uma só vez.
	if _, err := h.node.EventStore.Append(r.Context(), planRequestStream, eventstore.EventInput{
		Type:     EventTypePlanRequestSubmitted,
		Payload:  bruto,
		RunID:    planRequestRunID,
		StepID:   "req-" + req.RunID,
		Producer: eventstore.Producer{NHIID: planIngressNHI},
	}); err != nil {
		// FAIL-CLOSED: sem facto durável não há pedido. Responder 201 aqui seria prometer uma
		// corrida que ninguém vai consumir — o modo de falha que este ticket existe para fechar.
		writeError(w, http.StatusServiceUnavailable, "indisponivel")
		return
	}

	// `201`, e não `202`. O `202` seria a leitura literal de «aceite para processamento futuro»,
	// mas o que se quer aqui é o código ser INDISTINGUÍVEL do do `POST /runs`: é dessa
	// uniformidade que sai a não-oracularidade, e um código próprio para esta rota seria mais um
	// canal por onde a existência de um pedido alheio se podia inferir. O ADR-028 §2.3 fixa-o.
	writeJSON(w, http.StatusCreated, planRequestResponse{RunID: req.RunID, Status: "accepted"})
}
