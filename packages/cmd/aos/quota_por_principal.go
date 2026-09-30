package main

// quota_por_principal.go — QUOTA DE DESPESA POR PRINCIPAL, MENSAL UTC (AOS-457).
//
// O tecto por-run (AOS-257/260) limita o que CADA run gasta; não limita quantos runs um chamador
// submete. N runs × tecto = despesa ilimitada por um só principal, e tokens gastos num modelo
// pago não se devolvem. Esta quota limita o AGREGADO por principal num mês.
//
// DECISÃO DO DONO (registada no ticket antes de implementar):
//   - janela MENSAL UTC, que repõe às 00:00 UTC do dia 1;
//   - tokens obrigatórios e micro-USD opcional, como o tecto por-run; nega se QUALQUER esgotou;
//   - DURA: na admissão reserva-se o tecto POR-RUN inteiro; no fim do run liquida-se pelo consumo
//     real e liberta-se o resto — N runs admitidos ao mesmo tempo não ultrapassam a quota pelo que
//     RESERVAM. Podem ultrapassá-la pelo que GASTAM a mais: o último turno de um run pode passar o
//     tecto por-run, porque a resposta só se mede depois de chegar (`integration/model_admission.go`,
//     o caso R > E), e a liquidação conta o consumo real. O excesso possível é, no máximo, esse
//     transbordo do último turno por run em curso — declarado, e medido pela revisão;
//   - o `/dsar/erase` apaga o agregado, e isso repõe a quota (residual declarado).
//
// DURÁVEL NO EVENT STORE. Um stream por (principal, mês), sob o espaço reservado `aos-internal/`
// (AOS-417): uma reserva por run admitido, uma liquidação por run terminado. O gasto do mês é a
// soma, por run, da liquidação quando existe e da reserva quando não. Um restart relê o stream —
// não há estado em memória a perder, que é o critério «um restart não repõe a quota».
//
// A RESERVA PERTENCE AO MÊS DA ADMISSÃO, e isso resolve os runs que nunca terminam. Um run
// suspenso à espera de um humano que nunca vem, pausado, ou órfão não chega a liquidar, e a sua
// reserva ficaria presa para sempre se a janela fosse deslizante. Com a janela mensal, sai do
// cálculo quando o mês acaba. O custo inverso também se declara: o que um run gasta depois de
// mudar o mês conta no mês em que foi admitido.
//
// PORQUE NÃO SE CIFRA SOB A KEK DO TITULAR, ao contrário do molde do AOS-429. O `audit.OpenContent`
// devolve o mesmo erro para «a KEK foi destruída» e para «o vault falhou», e o `EnsureKey` cria uma
// KEK nova depois de uma destruição. Ilegível teria de querer dizer uma de duas coisas, e as duas
// são más: «conta zero» faria uma falha do vault ABRIR a quota; «nega» deixaria bloqueado até ao
// fim do mês um titular cuja KEK o varredor de retenção destruiu. E a cifra não protegeria nada:
// o que aqui se guarda — pseudónimo do principal, `run_id`, tokens, micro-USD — é exactamente o
// que o `turn.recorded` de cada run já guarda em claro no WAL. O apagamento faz-se por MARCA: o
// store DSAR grava `budget.quota.erased`, e a leitura ignora tudo o que vem antes dela. Uma
// leitura que falha continua fail-closed. Esta posição sobre os metadados de uso é do DPO validar.
//
// SÓ SOBRE PRINCIPAL VERIFICADO (precedente AOS-464). Sobre um principal que o chamador escreve,
// uma quota de despesa seria negação dirigida: o atacante escreveria o nome da vítima e gastaria
// a quota DELA. O `Bootstrap` recusa arrancar com a quota configurada sem credencial forte
// ([ErrPrincipalQuotaUnverified]) — ao contrário do AOS-464, que não compõe e declara, porque lá
// ficava um tecto global e aqui não fica nada: uma quota anunciada e não composta deixaria o
// operador a julgar que a despesa tem tecto.
//
// O PLANEAMENTO TAMBÉM CONTA (AOS-466). O `aos-orq` decompõe cada pedido de `POST /plans` com o
// modelo antes de submeter os runs-filho. O pedido reserva `AOS_BUDGET_PRINCIPAL_PLAN_TOKENS` (e
// `..._COST_MICRO_USD`) sob a chave `aos-internal/plan/<run_id>`, no mesmo stream e com os mesmos
// tipos de evento — um tipo novo faria o `ler()` de uma réplica anterior negar tudo num deploy
// rolante. Cada reclamação grava a ENTREGA da geração (`entregue`) antes de a entregar; cada
// desfecho grava uma PARCELA (`geracao`) com o consumo que o drenador mediu; o desfecho terminal,
// DEPOIS de gravado, grava o FECHO (`final`). Ver [cobrancaDoPlano] para o que cada geração custa e
// quando a reserva se liberta.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	budget "github.com/aos-ref/control-plane/budget"
	dsar "github.com/aos-ref/control-plane/governance/dsar"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/durable"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento da quota (família `budget.*`, tecnica/13 §3.3). Constantes junto do emissor —
// nunca um literal no caminho de emissão (gate event-catalog).
const (
	// EventTypeQuotaReserved — o tecto por-run foi reservado contra a quota na admissão de um run.
	EventTypeQuotaReserved = "budget.quota.reserved"
	// EventTypeQuotaSettled — o run terminou e a reserva passou a valer pelo consumo real.
	EventTypeQuotaSettled = "budget.quota.settled"
	// EventTypeQuotaErased — o `/dsar/erase` apagou o agregado: a leitura ignora tudo o que vem
	// antes desta marca, e a quota repõe-se.
	EventTypeQuotaErased = "budget.quota.erased"
)

// quotaStreamPrefix é o prefixo dos streams da quota: `aos-internal/quota-<pseudonimo>-<AAAAMM>`.
// Sob o espaço reservado (AOS-417), que o `POST /runs` e o `POST /plans` recusam como `run_id`.
const quotaStreamPrefix = streamsReservados + "quota-"

// quotaRunID é o «run» sintético que, com o StepID, forma a idempotency-key. O stream não
// pertence a run nenhum — o `run_id` do run está no payload (molde de [planRequestRunID]).
const quotaRunID = streamsReservados + "quota"

// quotaNHI é a identidade emissora: o facto é do NÓ.
const quotaNHI = "nhi:aos-node/quota"

// quotaMaxTentativas limita o ciclo de concorrência optimista. Cada conflito significa que outra
// admissão do MESMO principal escreveu entretanto; mais do que isto é contenção anormal.
const quotaMaxTentativas = 8

// Erros da quota.
var (
	// ErrPrincipalQuotaExhausted — o principal não tem quota para reservar o tecto por-run deste
	// mês. Recusa atribuível: o `POST /runs` responde 429.
	ErrPrincipalQuotaExhausted = errors.New("aos: quota mensal de despesa do principal esgotada (AOS-457)")
	// ErrPrincipalQuotaNoPrincipal — um run a admitir sem principal a quem imputar. Com a quota
	// composta só há principal verificado, pelo que é defesa em profundidade: sem ela, um run sem
	// principal passaria sem quota.
	ErrPrincipalQuotaNoPrincipal = errors.New("aos: run sem principal a quem imputar a quota (AOS-457)")
	// ErrPrincipalQuotaUnreadable — o stream da quota tem um evento que não se sabe ler. Fail-closed:
	// nega a admissão em vez de admitir sobre um gasto que não se conseguiu somar.
	ErrPrincipalQuotaUnreadable = errors.New("aos: registo da quota do principal ilegivel (AOS-457)")
	// ErrPrincipalQuotaContention — o ciclo de concorrência optimista esgotou as tentativas.
	ErrPrincipalQuotaContention = errors.New("aos: contencao ao reservar a quota do principal (AOS-457)")
)

// quotaPayload é o corpo dos três eventos. Sem segredos nem conteúdo: o run e as quantias.
type quotaPayload struct {
	RunID        string `json:"run_id,omitempty"`
	Tokens       int64  `json:"tokens,omitempty"`
	CostMicroUSD int64  `json:"cost_micro_usd,omitempty"`
	// Reserva é o `seq` da reserva que uma liquidação liquida. Uma liquidação de uma reserva
	// anterior a um apagamento não pode valer para a reserva nova do mesmo run.
	Reserva uint64 `json:"reserva,omitempty"`

	// PARCELA DE PLANEAMENTO (AOS-466). Uma liquidação com Geracao > 0 é o consumo de UMA geração
	// de planeamento de um pedido de `POST /plans`, e soma-se às outras em vez de as substituir.
	//
	// O VÍNCULO À RESERVA VIVE NOUTRO CAMPO, de propósito: com `Reserva` a zero, uma réplica
	// anterior a este ticket não reconhece a parcela como liquidação da reserva (compara `Reserva`
	// com o seq) e continua a contar a reserva inteira — o lado seguro num deploy rolante. Se a
	// parcela usasse `Reserva`, a réplica antiga lê-la-ia como a liquidação final e contaria só a
	// ÚLTIMA geração.
	Geracao        int    `json:"geracao,omitempty"`
	ReservaDoPlano uint64 `json:"reserva_do_plano,omitempty"`
	// TokensNaoMedidos / CustoNaoMedido: o consumo desta geração não se conhece por inteiro nessa
	// dimensão (o drenador não o mediu, ou uma chamada falhou). Custa o que se mediu MAIS a reserva.
	TokensNaoMedidos bool `json:"tokens_nao_medidos,omitempty"`
	CustoNaoMedido   bool `json:"custo_nao_medido,omitempty"`

	// Entregue marca a ENTREGA da geração `Geracao` a um drenador, escrita pelo nó na reclamação,
	// ANTES de o drenador a receber. É por ela que a quota conhece a geração em curso — e a que se
	// perdeu — sem depender de uma parcela que pode nunca chegar (achado MÉDIO da re-revisão).
	Entregue bool `json:"entregue,omitempty"`
	// Final marca o FECHO do pedido: um evento próprio, escrito só DEPOIS de o desfecho terminal
	// ficar no log (revisão do AOS-466: escrito antes, uma falha do desfecho deixava a reserva
	// libertada com o pedido ainda vivo).
	Final bool `json:"final,omitempty"`
}

// quotaPorPrincipal é a quota composta. nil ⇒ desligada.
type quotaPorPrincipal struct {
	es      eventstore.EventStore
	limite  budget.Amount // CostMicroUSD = integration.UnlimitedCostMicroUSD sem tecto em $
	reserva budget.Amount // o tecto por-run, reservado por admissão
	// reservaDoPlano é a quantia reservada por pedido de `POST /plans` (AOS-466).
	reservaDoPlano budget.Amount
	consumo        integration.ConsumoDuravel
	agora          func() time.Time
	log            func(string, ...any)
	// porPrincipal serializa as operações de CADA principal neste processo (entre réplicas, o
	// `WithExpectedSeq` no stream dele). Um mutex único para o nó serializava a admissão de todos
	// os principais atrás do I/O de um — achado BAIXO da revisão.
	mu           sync.Mutex
	porPrincipal map[string]*mutexDoPrincipal
}

// mutexDoPrincipal conta quem o tem ou espera por ele, para sair do mapa quando ninguém o usa: sem
// isso o mapa guardava um mutex por principal visto enquanto o processo vivesse.
type mutexDoPrincipal struct {
	sync.Mutex
	uso int
}

// bloquear toma o mutex do principal e devolve a função que o larga.
func (q *quotaPorPrincipal) bloquear(principal string) func() {
	q.mu.Lock()
	if q.porPrincipal == nil {
		q.porPrincipal = map[string]*mutexDoPrincipal{}
	}
	m, ok := q.porPrincipal[principal]
	if !ok {
		m = &mutexDoPrincipal{}
		q.porPrincipal[principal] = m
	}
	m.uso++
	q.mu.Unlock()
	m.Lock()
	return func() {
		m.Unlock()
		q.mu.Lock()
		m.uso--
		if m.uso == 0 {
			delete(q.porPrincipal, principal)
		}
		q.mu.Unlock()
	}
}

// pseudonimoDoPrincipal devolve o identificador do principal no nome do stream. Um hash e não o
// principal cru, porque um principal OIDC pode trazer caracteres que um stream não representa (o
// ponto de um email parte o JetStream). NÃO é anonimização: é um pseudónimo, e declara-se como tal.
func pseudonimoDoPrincipal(principal string) string {
	h := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(h[:16])
}

// mesUTC é a janela: AAAAMM em UTC.
func mesUTC(t time.Time) string { return t.UTC().Format("200601") }

// mesAnteriorUTC é o mês UTC anterior ao de t — para liquidar um run admitido antes da viragem.
func mesAnteriorUTC(t time.Time) string {
	u := t.UTC()
	return mesUTC(time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -1, 0))
}

// proximoMesUTC é o instante em que a janela de t repõe.
func proximoMesUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
}

func quotaStreamDe(principal, mes string) string {
	return quotaStreamPrefix + pseudonimoDoPrincipal(principal) + "-" + mes
}

// estadoDaQuota é o que um stream diz, depois da última marca de apagamento.
type estadoDaQuota struct {
	reservas   map[string]quotaPayload // por run; Reserva = o seq do evento de reserva
	liquidadas map[string]quotaPayload // por run; Reserva = o seq da reserva liquidada
	// parcelas são as liquidações de planeamento (AOS-466), por chave e por geração; entregas, as
	// gerações que o nó entregou; fechos, a marca de pedido terminado, por chave.
	parcelas  map[string]map[int]quotaPayload
	entregas  map[string]map[int]quotaPayload
	fechos    map[string]quotaPayload
	ultimoSeq uint64
}

// gasto soma, por chave, o que cada reserva custa: a liquidação quando existe, as parcelas de
// planeamento quando as há, e a reserva quando não.
func (e estadoDaQuota) gasto() budget.Amount {
	var g budget.Amount
	for chave, r := range e.reservas {
		if ps, entregue, fechado := e.planoDe(chave, r.Reserva); len(ps) > 0 || entregue > 0 || fechado {
			r = cobrancaDoPlano(r, ps, entregue, fechado)
		} else if l, ok := e.liquidadas[chave]; ok && l.Reserva == r.Reserva {
			r = l
		}
		g.Tokens = somaSaturada(g.Tokens, r.Tokens)
		g.CostMicroUSD = somaSaturada(g.CostMicroUSD, r.CostMicroUSD)
	}
	return g
}

// planoDe devolve, da chave de um pedido e para a reserva com esse seq: as parcelas por geração, a
// maior geração entregue, e se o pedido tem fecho. O que pertence a uma reserva anterior a um
// apagamento não vale para a reserva nova.
func (e estadoDaQuota) planoDe(chave string, reserva uint64) (ps map[int]quotaPayload, entregue int, fechado bool) {
	ps = map[int]quotaPayload{}
	for g, p := range e.parcelas[chave] {
		if p.ReservaDoPlano == reserva {
			ps[g] = p
		}
	}
	for g, p := range e.entregas[chave] {
		if p.ReservaDoPlano == reserva {
			entregue = max(entregue, g)
		}
	}
	f, ok := e.fechos[chave]
	return ps, entregue, ok && f.ReservaDoPlano == reserva
}

// cobrancaDoPlano é o que um pedido de plano custa à quota, dadas a reserva, as parcelas, a maior
// geração entregue e o fecho.
//
// A SOMA percorre as gerações 1..G, onde G é a maior que se conhece — entregue pelo nó, ou com
// parcela. Cada geração custa o que se mediu; uma geração NÃO MEDIDA custa o que se mediu MAIS a
// reserva; uma geração SEM PARCELA — a que está a correr, ou uma cujo desfecho se perdeu — custa a
// reserva. A reserva é a única estimativa por geração que o nó tem.
//
// DUAS REVISÕES ADVERSARIAIS mediram versões anteriores a contar a MENOS: `max(reserva, Σ)` para uma
// geração não medida somava zero com Σ acima da reserva; e, sem as entregas, a geração em curso só
// entrava em G quando a sua parcela chegasse — com Σ acima da reserva, corria sem reserva nenhuma.
//
// Antes da primeira entrega o pedido custa a reserva. Sem fecho custa o MAIOR entre a reserva e a
// soma; com fecho custa a SOMA — liberta o que sobra.
func cobrancaDoPlano(r quotaPayload, ps map[int]quotaPayload, entregue int, fechado bool) quotaPayload {
	G := entregue
	for g := range ps {
		G = max(G, g)
	}
	// Todas as parcelas estão em 1..G: G é pelo menos o máximo delas, e o `ler` só guarda gerações >= 1.
	var soma quotaPayload
	for _, p := range ps {
		soma.Tokens = somaSaturada(soma.Tokens, p.Tokens)
		soma.CostMicroUSD = somaSaturada(soma.CostMicroUSD, p.CostMicroUSD)
		if p.TokensNaoMedidos {
			soma.Tokens = somaSaturada(soma.Tokens, r.Tokens)
		}
		if p.CustoNaoMedido {
			soma.CostMicroUSD = somaSaturada(soma.CostMicroUSD, r.CostMicroUSD)
		}
	}
	faltam := int64(G) - int64(len(ps))
	soma.Tokens = somaSaturada(soma.Tokens, produtoSaturado(r.Tokens, faltam))
	soma.CostMicroUSD = somaSaturada(soma.CostMicroUSD, produtoSaturado(r.CostMicroUSD, faltam))
	if fechado {
		return soma
	}
	return quotaPayload{Tokens: max(r.Tokens, soma.Tokens), CostMicroUSD: max(r.CostMicroUSD, soma.CostMicroUSD)}
}

// produtoSaturado multiplica sem dar a volta: a reserva de n gerações em falta.
func produtoSaturado(a, n int64) int64 {
	if a <= 0 || n <= 0 {
		return 0
	}
	if a > math.MaxInt64/n {
		return math.MaxInt64
	}
	return a * n
}

// somaSaturada evita que um gasto enorme dê a volta para negativo e passe a caber na quota.
func somaSaturada(a, b int64) int64 {
	if b > 0 && a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func (q *quotaPorPrincipal) ler(ctx context.Context, stream string) (estadoDaQuota, error) {
	st := estadoDaQuota{reservas: map[string]quotaPayload{}, liquidadas: map[string]quotaPayload{}, parcelas: map[string]map[int]quotaPayload{}, entregas: map[string]map[int]quotaPayload{}, fechos: map[string]quotaPayload{}}
	evs, err := q.es.Read(ctx, stream, 1)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	for _, ev := range evs {
		st.ultimoSeq = ev.Seq
		switch ev.Type {
		case EventTypeQuotaErased:
			st.reservas = map[string]quotaPayload{}
			st.liquidadas = map[string]quotaPayload{}
			st.parcelas = map[string]map[int]quotaPayload{}
			st.entregas = map[string]map[int]quotaPayload{}
			st.fechos = map[string]quotaPayload{}
			continue
		case EventTypeQuotaReserved, EventTypeQuotaSettled:
		default:
			return st, fmt.Errorf("%w: tipo %q no stream %q", ErrPrincipalQuotaUnreadable, ev.Type, stream)
		}
		var p quotaPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.RunID == "" {
			return st, fmt.Errorf("%w: evento %d do stream %q", ErrPrincipalQuotaUnreadable, ev.Seq, stream)
		}
		switch {
		case ev.Type == EventTypeQuotaReserved:
			p.Reserva = ev.Seq
			st.reservas[p.RunID] = p
		case p.Final:
			st.fechos[p.RunID] = p
		case p.Entregue && p.Geracao > 0:
			if st.entregas[p.RunID] == nil {
				st.entregas[p.RunID] = map[int]quotaPayload{}
			}
			st.entregas[p.RunID][p.Geracao] = p
		case p.Geracao > 0:
			if st.parcelas[p.RunID] == nil {
				st.parcelas[p.RunID] = map[int]quotaPayload{}
			}
			st.parcelas[p.RunID][p.Geracao] = p
		default:
			st.liquidadas[p.RunID] = p
		}
	}
	return st, nil
}

// excede diz se reservar `mais` sobre `gasto` passa o limite em alguma dimensão.
func (q *quotaPorPrincipal) excede(gasto, mais budget.Amount) bool {
	return somaSaturada(gasto.Tokens, mais.Tokens) > q.limite.Tokens ||
		somaSaturada(gasto.CostMicroUSD, mais.CostMicroUSD) > q.limite.CostMicroUSD
}

// reservar reserva o tecto por-run de runID contra a quota do principal, no mês corrente.
// Idempotente por run: um run já reservado neste mês passa sem nova reserva.
func (q *quotaPorPrincipal) reservar(ctx context.Context, principal, runID string) error {
	return q.reservarQuantia(ctx, principal, runID, q.reserva, false)
}

// chaveDoPlano é a chave sob a qual o planeamento de um pedido de `POST /plans` reserva (AOS-466).
// Vive no espaço reservado (AOS-417), que o `POST /runs` e o `POST /plans` recusam como `run_id`:
// nenhum run pode ter a mesma chave que o planeamento de um pedido.
func chaveDoPlano(runID string) string { return streamsReservados + "plan/" + runID }

// reservarPlaneamento reserva a quantia de planeamento de um pedido de `POST /plans` (AOS-466).
//
// Idempotente no mês corrente E no anterior: um re-`POST` do mesmo pedido no mês seguinte não
// reserva de novo — o `Append` do pedido deduplica, ninguém voltaria a planear, e a reserva nova
// ficaria presa até ao fim do mês.
func (q *quotaPorPrincipal) reservarPlaneamento(ctx context.Context, principal, runID string) error {
	return q.reservarQuantia(ctx, principal, chaveDoPlano(runID), q.reservaDoPlano, true)
}

// reservarQuantia reserva `quantia` sob `chave` contra a quota do principal, no mês corrente.
// Idempotente por chave no mês corrente e, com `tambemMesAnterior`, no anterior.
func (q *quotaPorPrincipal) reservarQuantia(ctx context.Context, principal, chave string, quantia budget.Amount, tambemMesAnterior bool) error {
	if principal == "" {
		return ErrPrincipalQuotaNoPrincipal
	}
	defer q.bloquear(principal)()
	agora := q.agora()
	mes := mesUTC(agora)
	stream := quotaStreamDe(principal, mes)
	if tambemMesAnterior {
		anterior, err := q.ler(ctx, quotaStreamDe(principal, mesAnteriorUTC(agora)))
		if err != nil {
			return err
		}
		if _, ja := anterior.reservas[chave]; ja {
			return nil
		}
	}
	payload, err := json.Marshal(quotaPayload{RunID: chave, Tokens: quantia.Tokens, CostMicroUSD: quantia.CostMicroUSD})
	if err != nil {
		return err
	}
	for i := 0; i < quotaMaxTentativas; i++ {
		st, err := q.ler(ctx, stream)
		if err != nil {
			return err
		}
		if _, ja := st.reservas[chave]; ja {
			return nil
		}
		if gasto := st.gasto(); q.excede(gasto, quantia) {
			repoe := proximoMesUTC(agora)
			return &quotaEsgotadaError{gasto: gasto, limite: q.limite, reserva: quantia, repoe: repoe, faltam: repoe.Sub(agora)}
		}
		_, err = q.es.Append(ctx, stream, eventstore.EventInput{
			Type:    EventTypeQuotaReserved,
			Payload: payload,
			RunID:   quotaRunID,
			// O seq em que a reserva vai ser escrita torna o StepID único: depois de um apagamento, a
			// reserva nova do mesmo run não pode ser deduplicada contra a antiga — seria admitida sem
			// reserva nenhuma (medido pela revisão).
			StepID:   fmt.Sprintf("%s:reserved:%s:%d", chave, mes, st.ultimoSeq+1),
			Producer: eventstore.Producer{NHIID: quotaNHI},
		}, eventstore.WithExpectedSeq(st.ultimoSeq))
		// Outra réplica escreveu no stream entretanto: relê e decide de novo. O store em memória
		// responde ErrAppendOnlyViolation quando o stream avançou, e o JetStream ErrSeqConflict.
		if errors.Is(err, eventstore.ErrSeqConflict) || errors.Is(err, eventstore.ErrAppendOnlyViolation) {
			continue
		}
		return err
	}
	return ErrPrincipalQuotaContention
}

// liquidar grava o consumo real de runID contra a reserva, no mês corrente ou no anterior (um run
// admitido antes da viragem do mês liquida no mês em que reservou). Sem reserva em nenhum dos dois
// não faz nada: o run foi admitido antes de a quota existir, ou a reserva já saiu da janela.
//
// FAIL-CLOSED: se o consumo não se consegue ler (turnos sem `usage`, ledger ilegível), a reserva
// fica inteira. Contar a menos seria a quota a abrir sobre uma cegueira.
func (q *quotaPorPrincipal) liquidar(ctx context.Context, principal, runID string) {
	if q == nil || principal == "" {
		return
	}
	defer q.bloquear(principal)()
	agora := q.agora()
	for _, mes := range []string{mesUTC(agora), mesAnteriorUTC(agora)} {
		stream := quotaStreamDe(principal, mes)
		st, err := q.ler(ctx, stream)
		if err != nil {
			q.logf("quota (AOS-457): ler %q para liquidar o run %q falhou — a reserva fica inteira: %v", stream, runID, err)
			return
		}
		r, reservado := st.reservas[runID]
		if !reservado {
			continue
		}
		if l, ja := st.liquidadas[runID]; ja && l.Reserva == r.Reserva {
			return
		}
		var usado budget.Amount
		if q.consumo != nil {
			usado, err = q.consumo(ctx, runID)
			if err != nil {
				q.logf("quota (AOS-457): consumo do run %q ilegivel — a reserva fica inteira: %v", runID, err)
				return
			}
		}
		payload, err := json.Marshal(quotaPayload{RunID: runID, Tokens: usado.Tokens, CostMicroUSD: usado.CostMicroUSD, Reserva: r.Reserva})
		if err != nil {
			return
		}
		if _, err := q.es.Append(ctx, stream, eventstore.EventInput{
			Type:     EventTypeQuotaSettled,
			Payload:  payload,
			RunID:    quotaRunID,
			StepID:   fmt.Sprintf("%s:settled:%s:%d", runID, mes, r.Reserva),
			Producer: eventstore.Producer{NHIID: quotaNHI},
		}); err != nil {
			q.logf("quota (AOS-457): liquidar o run %q falhou — a reserva fica inteira: %v", runID, err)
		}
		return
	}
}

// consumoDoPlaneamento é o que o drenador declara ter gasto numa geração de planeamento (AOS-466).
// Os `...Medido` a falso dizem «não se sabe», que é diferente de «zero».
type consumoDoPlaneamento struct {
	Tokens        int64
	TokensMedidos bool
	CostMicroUSD  int64
	CustoMedido   bool
}

// registarPlaneamento grava a parcela de UMA geração de planeamento do pedido runID, contra a
// reserva de planeamento no mês corrente ou no anterior (AOS-466). Sem reserva em nenhum dos dois
// não faz nada: o pedido entrou antes de a quota existir, ou a reserva já saiu da janela.
//
// Idempotente por (chave, reserva, geração) — pela idempotency-key do `Append`: a repetição de um
// desfecho, mesmo com outro consumo, não escreve uma segunda parcela. Devolve o erro de escrita: o
// desfecho não se regista sem a parcela, para que uma geração nunca fique contada no pedido e
// ausente da quota.
func (q *quotaPorPrincipal) registarPlaneamento(ctx context.Context, principal, runID string, geracao int, c consumoDoPlaneamento) error {
	if geracao < 1 {
		return nil
	}
	return q.escreverNoPlano(ctx, principal, runID, func(mes string, reserva uint64) (quotaPayload, string) {
		return quotaPayload{
			RunID:            chaveDoPlano(runID),
			Tokens:           max(c.Tokens, 0),
			CostMicroUSD:     max(c.CostMicroUSD, 0),
			Geracao:          geracao,
			ReservaDoPlano:   reserva,
			TokensNaoMedidos: !c.TokensMedidos,
			CustoNaoMedido:   !c.CustoMedido,
		}, fmt.Sprintf("%s:settled:%s:%d:%d", chaveDoPlano(runID), mes, reserva, geracao)
	})
}

// registarEntrega grava que o nó ENTREGOU a geração `geracao` do pedido runID a um drenador. Chama-a
// a reclamação, ANTES de a escrever: se falha, a reclamação também não se faz. Idempotente por
// (chave, reserva, geração).
func (q *quotaPorPrincipal) registarEntrega(ctx context.Context, principal, runID string, geracao int) error {
	if geracao < 1 {
		return nil
	}
	return q.escreverNoPlano(ctx, principal, runID, func(mes string, reserva uint64) (quotaPayload, string) {
		return quotaPayload{RunID: chaveDoPlano(runID), Geracao: geracao, ReservaDoPlano: reserva, Entregue: true},
			fmt.Sprintf("%s:delivered:%s:%d:%d", chaveDoPlano(runID), mes, reserva, geracao)
	})
}

// fecharPlaneamento grava o FECHO do pedido runID: a partir dele a reserva liberta o que as parcelas
// não gastaram. Só se chama DEPOIS de o desfecho terminal ficar no log — é isso que garante que não
// há gerações depois dele.
func (q *quotaPorPrincipal) fecharPlaneamento(ctx context.Context, principal, runID string) error {
	return q.escreverNoPlano(ctx, principal, runID, func(mes string, reserva uint64) (quotaPayload, string) {
		return quotaPayload{RunID: chaveDoPlano(runID), ReservaDoPlano: reserva, Final: true},
			fmt.Sprintf("%s:closed:%s:%d", chaveDoPlano(runID), mes, reserva)
	})
}

// escreverNoPlano procura a reserva de planeamento de runID no mês corrente ou no anterior e grava
// nesse stream o evento que `evento` constrói. Sem reserva, nada.
func (q *quotaPorPrincipal) escreverNoPlano(ctx context.Context, principal, runID string, evento func(mes string, reserva uint64) (quotaPayload, string)) error {
	if q == nil || principal == "" {
		return nil
	}
	defer q.bloquear(principal)()
	chave := chaveDoPlano(runID)
	agora := q.agora()
	for _, mes := range []string{mesUTC(agora), mesAnteriorUTC(agora)} {
		stream := quotaStreamDe(principal, mes)
		st, err := q.ler(ctx, stream)
		if err != nil {
			return err
		}
		r, reservado := st.reservas[chave]
		if !reservado {
			continue
		}
		p, stepID := evento(mes, r.Reserva)
		payload, err := json.Marshal(p)
		if err != nil {
			return err
		}
		_, err = q.es.Append(ctx, stream, eventstore.EventInput{
			Type:     EventTypeQuotaSettled,
			Payload:  payload,
			RunID:    quotaRunID,
			StepID:   stepID,
			Producer: eventstore.Producer{NHIID: quotaNHI},
		})
		return err
	}
	return nil
}

func (q *quotaPorPrincipal) logf(format string, args ...any) {
	if q.log != nil {
		q.log(format, args...)
	}
}

// Name e Shred fazem da quota um store do fluxo DSAR (dsar.ShreddableKeyStore).
func (q *quotaPorPrincipal) Name() string { return "principal-quota" }

// Shred grava a marca de apagamento no stream do mês corrente do titular. É isso que repõe a quota,
// por decisão do dono. Os meses anteriores já não contam para a quota e não recebem marca.
// Idempotente no efeito: duas marcas seguidas lêem-se como uma.
func (q *quotaPorPrincipal) Shred(subjectID string) error {
	if q == nil || subjectID == "" {
		return nil
	}
	defer q.bloquear(subjectID)()
	_, err := q.es.Append(context.Background(), quotaStreamDe(subjectID, mesUTC(q.agora())), eventstore.EventInput{
		Type:     EventTypeQuotaErased,
		Payload:  json.RawMessage(`{}`),
		Producer: eventstore.Producer{NHIID: quotaNHI},
	})
	return err
}

// quotaEsgotadaError carrega os números da recusa para o log do operador. O corpo HTTP não os leva:
// a mensagem ao cliente é uniforme, e os números dizem quanto um principal gastou.
type quotaEsgotadaError struct {
	gasto, limite, reserva budget.Amount
	repoe                  time.Time
	faltam                 time.Duration // até à reposição, no relógio da quota: o Retry-After
}

func (e *quotaEsgotadaError) Error() string {
	return fmt.Sprintf("%v: gasto %d tokens / %d micro-USD, reserva pedida %d tokens / %d micro-USD, quota %d tokens / %d micro-USD; repoe em %s",
		ErrPrincipalQuotaExhausted, e.gasto.Tokens, e.gasto.CostMicroUSD, e.reserva.Tokens, e.reserva.CostMicroUSD,
		e.limite.Tokens, e.limite.CostMicroUSD, e.repoe.Format(time.RFC3339))
}

func (e *quotaEsgotadaError) Unwrap() error { return ErrPrincipalQuotaExhausted }

// Erros de configuração da quota. Todos abortam o arranque: uma quota mal composta ou é
// inexistente e anunciada, ou nega tudo — e nenhuma das duas se descobre sem ser pelo efeito.
var (
	// ErrBadPrincipalQuota — AOS_BUDGET_PRINCIPAL_MAX_TOKENS está definida mas não é um inteiro > 0,
	// ou AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD está definida sem ela ou inválida. 0 não desliga:
	// deixa-se por definir.
	ErrBadPrincipalQuota = errors.New("aos: quota por principal mal configurada — AOS_BUDGET_PRINCIPAL_MAX_TOKENS tem de ser um inteiro > 0 (tokens por principal por mes UTC), e AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD, se definida, um inteiro > 0 e exige a primeira. Deixe as duas POR DEFINIR para correr sem quota; 0 nao desliga, negaria todos os runs")
	// ErrPrincipalQuotaWithoutRunBudget — a quota reserva o tecto POR-RUN na admissão; sem ele não
	// há o que reservar. Em dólares o mesmo: quota em $ exige tecto por-run em $.
	ErrPrincipalQuotaWithoutRunBudget = errors.New("aos: a quota por principal reserva o tecto POR-RUN na admissao e o no nao o tem — AOS_BUDGET_PRINCIPAL_MAX_TOKENS exige AOS_BUDGET_MAX_TOKENS, e AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD exige AOS_BUDGET_MAX_COST_MICRO_USD")
	// ErrPrincipalQuotaBelowRunBudget — o tecto por-run é maior do que a quota numa das dimensões:
	// nenhuma reserva caberia, e o nó recusaria todos os runs. Validado sobre o PAR final.
	ErrPrincipalQuotaBelowRunBudget = errors.New("aos: a quota por principal e MENOR do que o tecto por-run — nenhum run caberia na quota e todos seriam recusados. A quota tem de ser >= AOS_BUDGET_MAX_TOKENS (e >= AOS_BUDGET_MAX_COST_MICRO_USD em dolares)")
	// ErrBadPrincipalPlanQuota — a reserva de planeamento (AOS-466) falta ou é inválida.
	// AOS_BUDGET_PRINCIPAL_PLAN_TOKENS é OBRIGATÓRIA com a quota composta (decisão do dono): sem ela
	// o planeamento dos pedidos de `POST /plans` não teria o que reservar. Definida SEM a quota
	// também aborta — seria uma variável inerte, anunciada na config e sem efeito.
	ErrBadPrincipalPlanQuota = errors.New("aos: reserva de planeamento da quota por principal mal configurada (AOS-466) — com AOS_BUDGET_PRINCIPAL_MAX_TOKENS definida, AOS_BUDGET_PRINCIPAL_PLAN_TOKENS e OBRIGATORIA: um inteiro > 0 e <= a quota (tokens reservados por cada pedido de POST /plans, liquidados pelo consumo real do planeamento); com AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD definida, AOS_BUDGET_PRINCIPAL_PLAN_COST_MICRO_USD tambem, > 0 e <= a quota em dolares. Nenhuma das duas se define sem a quota correspondente")
	// ErrPrincipalQuotaUnverified — a quota está configurada e o principal dos runs não vem de
	// credencial forte verificada. Ver o cabeçalho do ficheiro para porque é fatal e não declarado.
	ErrPrincipalQuotaUnverified = errors.New("aos: a quota por principal exige principal VERIFICADO — sem credencial forte o principal vem de um header que o chamador escreve, e um atacante gastaria a quota de outro principal escrevendo o nome dele. Defina AOS_SOVEREIGN_OIDC_ISSUER+AOS_SOVEREIGN_OIDC_AUDIENCE (com AOS_BOARD_REGIONS), ou deixe AOS_BUDGET_PRINCIPAL_MAX_TOKENS por definir")
)

// principalQuotaFromEnv compõe a quota a partir do ambiente. (nil, nil) quando não está configurada.
// Valida o par com o tecto por-run já composto, e por isso recebe-o.
func principalQuotaFromEnv(rb *integration.RunBudget, es eventstore.EventStore, consumo integration.ConsumoDuravel, agora func() time.Time, log func(string, ...any)) (*quotaPorPrincipal, error) {
	raw := strings.TrimSpace(os.Getenv("AOS_BUDGET_PRINCIPAL_MAX_TOKENS"))
	rawCost := strings.TrimSpace(os.Getenv("AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD"))
	rawPlan := strings.TrimSpace(os.Getenv("AOS_BUDGET_PRINCIPAL_PLAN_TOKENS"))
	rawPlanCost := strings.TrimSpace(os.Getenv("AOS_BUDGET_PRINCIPAL_PLAN_COST_MICRO_USD"))
	if raw == "" {
		if rawCost != "" {
			return nil, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD=%q sem AOS_BUDGET_PRINCIPAL_MAX_TOKENS", ErrBadPrincipalQuota, rawCost)
		}
		if rawPlan != "" || rawPlanCost != "" {
			return nil, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_PLAN_* definida sem AOS_BUDGET_PRINCIPAL_MAX_TOKENS", ErrBadPrincipalPlanQuota)
		}
		return nil, nil
	}
	maxTokens, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || maxTokens <= 0 {
		return nil, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_MAX_TOKENS=%q", ErrBadPrincipalQuota, raw)
	}
	limite := budget.Amount{Tokens: maxTokens, CostMicroUSD: integration.UnlimitedCostMicroUSD}
	if rawCost != "" {
		maxCost, cerr := strconv.ParseInt(rawCost, 10, 64)
		if cerr != nil || maxCost <= 0 {
			return nil, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD=%q", ErrBadPrincipalQuota, rawCost)
		}
		limite.CostMicroUSD = maxCost
	}
	if rb == nil {
		return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_TOKENS por definir", ErrPrincipalQuotaWithoutRunBudget)
	}
	reserva := budget.Amount{Tokens: rb.MaxTokensPerRun()}
	custoPorRun, custoLimitado := rb.MaxCostMicroUSDPerRun()
	if rawCost != "" {
		if !custoLimitado {
			return nil, fmt.Errorf("%w: AOS_BUDGET_MAX_COST_MICRO_USD por definir", ErrPrincipalQuotaWithoutRunBudget)
		}
		reserva.CostMicroUSD = custoPorRun
	}
	if reserva.Tokens > limite.Tokens || reserva.CostMicroUSD > limite.CostMicroUSD {
		return nil, fmt.Errorf("%w: por-run %d tokens / %d micro-USD, quota %d tokens / %d micro-USD",
			ErrPrincipalQuotaBelowRunBudget, reserva.Tokens, reserva.CostMicroUSD, limite.Tokens, limite.CostMicroUSD)
	}
	reservaDoPlano, err := reservaDoPlanoDoAmbiente(rawPlan, rawPlanCost, rawCost != "", limite)
	if err != nil {
		return nil, err
	}
	if agora == nil {
		agora = time.Now
	}
	return &quotaPorPrincipal{es: es, limite: limite, reserva: reserva, reservaDoPlano: reservaDoPlano, consumo: consumo, agora: agora, log: log}, nil
}

// reservaDoPlanoDoAmbiente valida a reserva de planeamento (AOS-466) contra a quota já composta.
// Recebe os valores JÁ LIDOS: os `os.Getenv` ficam literais no chamador (gate env-surface).
func reservaDoPlanoDoAmbiente(rawPlan, rawPlanCost string, quotaEmDolares bool, limite budget.Amount) (budget.Amount, error) {
	tokens, err := strconv.ParseInt(rawPlan, 10, 64)
	if rawPlan == "" || err != nil || tokens <= 0 || tokens > limite.Tokens {
		return budget.Amount{}, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_PLAN_TOKENS=%q (quota %d tokens)", ErrBadPrincipalPlanQuota, rawPlan, limite.Tokens)
	}
	r := budget.Amount{Tokens: tokens}
	if !quotaEmDolares {
		if rawPlanCost != "" {
			return budget.Amount{}, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_PLAN_COST_MICRO_USD=%q sem AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD", ErrBadPrincipalPlanQuota, rawPlanCost)
		}
		return r, nil
	}
	custo, err := strconv.ParseInt(rawPlanCost, 10, 64)
	if rawPlanCost == "" || err != nil || custo <= 0 || custo > limite.CostMicroUSD {
		return budget.Amount{}, fmt.Errorf("%w: AOS_BUDGET_PRINCIPAL_PLAN_COST_MICRO_USD=%q (quota %d micro-USD)", ErrBadPrincipalPlanQuota, rawPlanCost, limite.CostMicroUSD)
	}
	r.CostMicroUSD = custo
	return r, nil
}

// storesDeApagamento compõe a lista de stores do fluxo DSAR. A quota só entra quando composta: um
// `*quotaPorPrincipal` nil metido na interface produziria uma interface NÃO-nil, e o fluxo
// chamaria `Shred` num store que não existe — a armadilha do nil tipado que o bootstrap já
// documenta para o step-ledger. Aqui evita-se na origem em vez de depender da guarda de receptor.
func storesDeApagamento(shredder *audit.Shredder, ledger *durable.StepLedger, quota *quotaPorPrincipal) []dsar.ShreddableKeyStore {
	stores := []dsar.ShreddableKeyStore{
		dsar.AuditStore("audit", shredder),
		dsar.StepLedgerStore("step-ledger", ledger),
	}
	if quota != nil {
		stores = append(stores, quota)
	}
	return stores
}

// principalQuotaPostureBanner declara a quota COMPOSTA, não a intenção da config. Os dois estados
// possíveis: por configurar, ou composta sobre principal verificado — o terceiro (configurada sobre
// principal forjável) não arranca ([ErrPrincipalQuotaUnverified]).
func principalQuotaPostureBanner(q *quotaPorPrincipal) []string {
	if q == nil {
		return []string{"quota por principal (AOS-457): NAO CONFIGURADA — AOS_BUDGET_PRINCIPAL_MAX_TOKENS por definir, logo o unico tecto de despesa e o POR-RUN: um principal que submeta N runs gasta ate N vezes esse tecto, sem limite mensal"}
	}
	custo := "sem tecto mensal em dolares (AOS_BUDGET_PRINCIPAL_MAX_COST_MICRO_USD por definir)"
	if q.limite.CostMicroUSD != integration.UnlimitedCostMicroUSD {
		custo = fmt.Sprintf("%d micro-USD", q.limite.CostMicroUSD)
	}
	return []string{fmt.Sprintf("quota por principal (AOS-457): LIGADA sobre principal VERIFICADO — %d tokens e %s por principal por mes UTC (repoe as 00:00 UTC do dia 1). DURA: cada admissao RESERVA o tecto por-run inteiro (%d tokens / %d micro-USD) e so liquida pelo consumo real quando o desfecho do run fica no log duravel; esgotada, POST /runs responde 429 com Retry-After ate a reposicao. Nao se ultrapassa pelo que se RESERVA; pode ultrapassar-se pelo transbordo do ULTIMO turno de cada run em curso acima do tecto por-run (a resposta so se mede depois de chegar), que a liquidacao conta. O PLANEAMENTO dos pedidos de POST /plans conta (AOS-466): cada pedido RESERVA %d tokens / %d micro-USD e cada geracao conta o consumo que o drenador (aos-orq) mede e declara; uma geracao NAO MEDIDA (ou entregue sem desfecho) custa o que se mediu MAIS a reserva. So o fecho do pedido (desfecho terminal no log) liberta o que sobra da reserva. Os dolares nunca sao medidos pelo aos-orq: cada geracao que chamou o modelo custa a reserva em dolares. Uma geracao pode gastar mais do que a reserva, e o excesso conta depois; o numero de geracoes por pedido nao tem tecto (AOS-467); o consumo e DECLARADO pelo drenador, que o no nao verifica. Um run_id com desfecho no log nao volta a executar (re-submissao idempotente). A reserva pertence ao mes da ADMISSAO: um run que nunca termina (suspenso, pausado, orfao) segura-a ate o mes acabar. DURAVEL no Event Store (aos-internal/quota-<pseudonimo>-<AAAAMM>), um restart nao a repoe. O /dsar/erase grava uma marca que a REPOE (decisao do dono); os registos sao metadados de uso em claro, como o turn.recorded, e o principal aparece como pseudonimo (hash), nao anonimizado",
		q.limite.Tokens, custo, q.reserva.Tokens, q.reserva.CostMicroUSD, q.reservaDoPlano.Tokens, q.reservaDoPlano.CostMicroUSD)}
}
