package main

// modelo_disjuntor.go — O DISJUNTOR DO MODELO CANDIDATO (AOS-521, ADR-041 §2.9).
//
// O canary do AOS-520 só fecha a fatia do candidato ao 40.º plano. O disjuntor tira-lha ANTES, sem
// ninguém intervir, quando o candidato falha mais do que o limiar (D5): 3 falhas nos últimos 20 runs
// do candidato, ou o primeiro turno com o modelo servido diferente do esperado. Não volta sozinho.
//
// O ESTADO VIVE NO LIVRO DO CANARY (`aos-internal/model-canary`), em quatro tipos NOVOS da família
// `model.*` — o contrato de evolução do livro (ADR-041 §2.6). Todos presos ao DIGEST DO REGISTO de
// entrada assinado: um registo novo não tem observações nem marcas, e por isso começa fechado — é
// esse o acto do dono que reabre. Repor um registo antigo repõe o que o livro tem para ele.
//
//   - `model.breaker.run_outcome` — o desfecho de um run do candidato: o run, a unidade da janela
//     e o sinal ([sinalDoDesfecho]). Um por run.
//   - `model.breaker.route_mismatch` — um turno do candidato com o modelo servido diferente (F5).
//     Um por run.
//   - `model.breaker.opened` — a regra disparou com o nó em `enforce`: a regra, os cinco contadores
//     e a janela. Um por registo.
//   - `model.breaker.would_open` — o mesmo, com o nó em `observe`: o que `enforce` teria feito.
//
// QUEM DECIDE É O QUE ESTÁ NO LIVRO, e não um contador em memória: a regra é uma função pura da
// sequência das observações do registo ([avaliarDisjuntor]), relida do livro em cada decisão. Um
// reinício não perde nada, e duas réplicas vêem o mesmo.
//
// OS MODOS (`AOS_MODEL_BREAKER`, omissão `off`):
//
//   - `off` — não avalia, não grava, não publica métricas, e não faz uma leitura a mais. Mas uma
//     marca `opened` que JÁ esteja no livro para o registo em vigor continua a valer: `off` desliga
//     o disjuntor, não desfaz o que ele selou. Só um registo novo reabre.
//   - `observe` — grava as observações, avalia, e quando a regra dispara grava `would_open` e
//     di-lo em métrica. NÃO corta a fatia.
//   - `enforce` — o mesmo, e quando a regra dispara grava `opened` e corta: a decisão é a da regra
//     sobre as observações que o livro tem, tenham sido gravadas em `observe` ou em `enforce`. Passar
//     a `enforce` com um registo cujo `observe` teria aberto abre na primeira leitura.
//
// ABERTO: um run novo de um plano novo é do titular (razão própria); um run novo de um plano já
// atribuído ao candidato é RECUSADO (causa própria) — o plano falha, não passa os nós que faltam ao
// titular; os runs em curso do candidato acabam, e a retoma de um run já fixado continua.
//
// O TITULAR NUNCA É TOCADO: nenhuma observação de um run do titular chega ao livro, e nenhum estado
// do disjuntor muda por causa dele. Nenhuma decisão lê o relógio.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/kernel/agent-runtime/state"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento do disjuntor, no livro do canary (família `model.*`, tecnica/13).
const (
	// EventTypeModelBreakerRunOutcome — o desfecho de um run do candidato.
	EventTypeModelBreakerRunOutcome = "model.breaker.run_outcome"
	// EventTypeModelBreakerRouteMismatch — um turno do candidato com o modelo servido diferente.
	EventTypeModelBreakerRouteMismatch = "model.breaker.route_mismatch"
	// EventTypeModelBreakerOpened — o disjuntor abriu (nó em `enforce`).
	EventTypeModelBreakerOpened = "model.breaker.opened"
	// EventTypeModelBreakerWouldOpen — o disjuntor teria aberto (nó em `observe`).
	EventTypeModelBreakerWouldOpen = "model.breaker.would_open"
)

// disjuntorNHI é a identidade emissora: o facto é do NÓ.
const disjuntorNHI = "nhi:aos-node/model-breaker"

// disjuntorVersaoDoPayload é a versão do payload dos quatro eventos.
const disjuntorVersaoDoPayload = "1.0"

// disjuntorMaxTentativas limita o ciclo de concorrência optimista de uma observação: cada passo
// (a observação, a marca) consome uma volta, e cada conflito outra.
const disjuntorMaxTentativas = 16

// disjuntorMaxObservacoes é o tecto de observações que o livro guarda POR REGISTO de entrada. O livro
// lê-se em cada run novo, e em observação as observações continuam depois do «teria aberto»: sem
// tecto crescia com a vida do registo. 400 são os 40 planos do canary com dez runs cada (nós e
// tentativas) — muito acima do que um canary produz. Atingido, não se grava mais nada para esse
// registo: a janela e os contadores ficam como estavam, e o arranque di-lo.
const disjuntorMaxObservacoes = 400

// disjuntorPrazoDeIO é quanto tempo o disjuntor espera pelo Event Store nos dois sítios em que escreve
// fora de uma decisão: dentro de um turno (o turno com o modelo servido diferente) e na saída da
// hospedagem (o desfecho). Sem prazo, um Event Store pendurado prendia o turno e o fim do run. Cinco
// segundos: uma observação são duas leituras e duas escritas pequenas, que em produção levam
// milissegundos — o prazo só se esgota com o Event Store avariado. NÃO é uma decisão: esgotado, a
// observação conta como não gravada, e o turno e o run seguem.
const disjuntorPrazoDeIO = 5 * time.Second

// Os valores de `AOS_MODEL_BREAKER`. Vocabulário FECHADO.
const (
	modelBreakerOff     = "off"
	modelBreakerObserve = "observe"
	modelBreakerEnforce = "enforce"
)

// razaoDisjuntorAberto é a razão, no `run.model_pinned`, de um run novo que ficou no titular porque
// o disjuntor do candidato está aberto.
const razaoDisjuntorAberto = "disjuntor_aberto"

// causaDisjuntorAberto é a causa de recusa de um run novo de um plano JÁ atribuído ao candidato,
// com o disjuntor aberto: o plano falha, não passa para o titular.
const causaDisjuntorAberto = "plano_do_candidato_com_disjuntor_aberto"

var (
	// ErrBadModelBreaker — `AOS_MODEL_BREAKER` tem um valor fora do vocabulário. Fail-closed: o nó
	// não arranca a adivinhar se o disjuntor corta ou não.
	ErrBadModelBreaker = errors.New("aos: AOS_MODEL_BREAKER invalida — os valores sao \"off\" (a omissao: o disjuntor do candidato nao avalia nem grava), \"observe\" (avalia, grava o que teria feito e nao corta a fatia) e \"enforce\" (corta a fatia do candidato quando a regra dispara)")
	// ErrModelBreakerWithoutRouteGovernance — `AOS_MODEL_BREAKER=enforce` com a governação da rota
	// desligada. O sinal «modelo servido diferente» vem da comparação da rota; sem ela o disjuntor
	// prometia apanhar a troca de modelo sem a poder ver. Fail-closed: o nó não arranca.
	ErrModelBreakerWithoutRouteGovernance = errors.New("aos: AOS_MODEL_BREAKER=enforce com a governacao da rota desligada — o sinal «modelo servido diferente» vem de AOS_MODEL_ROUTE_GOVERNANCE (observe ou enforce); sem ela o disjuntor nao o ve. Ligue a governacao da rota, ou ponha AOS_MODEL_BREAKER=observe ou off")
	// ErrCandidateBreakerOpen — o plano está atribuído ao candidato e o disjuntor do candidato está
	// aberto: o run novo não é servido pelo candidato, e não passa para o titular.
	ErrCandidateBreakerOpen = errors.New("aos: o disjuntor do modelo candidato esta aberto e o plano esta atribuido ao candidato (AOS-521)")
)

// modelBreakerFromEnv lê `AOS_MODEL_BREAKER`. Ausente, vazia ou `off` ⇒ vazio (desligado).
func modelBreakerFromEnv() (string, error) {
	switch v := strings.TrimSpace(os.Getenv("AOS_MODEL_BREAKER")); v {
	case "", modelBreakerOff:
		return "", nil
	case modelBreakerObserve, modelBreakerEnforce:
		return v, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrBadModelBreaker, v)
	}
}

// conferirDisjuntorComARota é a conferência de ARRANQUE: `enforce` exige a governação da rota
// ligada. `modoDaRota` é o modo já lido de `AOS_MODEL_ROUTE_GOVERNANCE`.
func conferirDisjuntorComARota(disjuntor, modoDaRota string) error {
	if disjuntor == modelBreakerEnforce && !rotaGovernada(modoDaRota) {
		return ErrModelBreakerWithoutRouteGovernance
	}
	return nil
}

// rotaGovernada diz se a governação da rota compara o modelo servido (observe ou enforce).
func rotaGovernada(modoDaRota string) bool {
	return modoDaRota == modelgateway.RouteGovernanceObserve || modoDaRota == modelgateway.RouteGovernanceEnforce
}

// modelBreakerBanner declara o modo do disjuntor. Com `off` (a omissão) não sai linha nenhuma: o
// arranque é o de antes.
func modelBreakerBanner(p *perfisAssinados, titular, modoDaRota string) []string {
	if p == nil {
		if modo, err := modelBreakerFromEnv(); err == nil && modo != "" {
			return []string{fmt.Sprintf("AVISO (EPIC-06/AOS-521): AOS_MODEL_BREAKER=%s e este no NAO tem perfis de rota assinados carregados — nao ha candidato, e o disjuntor nao tem o que vigiar", modo)}
		}
		return nil
	}
	if p.disjuntor == "" {
		return nil
	}
	linhas := []string{}
	if _, serve := p.candidatoAServir(titular); !serve {
		linhas = append(linhas, fmt.Sprintf("disjuntor do modelo candidato (EPIC-06/AOS-521): AOS_MODEL_BREAKER=%s, e este no NAO serve candidato — o disjuntor nao avalia nem grava nada", p.disjuntor))
		return linhas
	}
	switch p.disjuntor {
	case modelBreakerObserve:
		linhas = append(linhas, fmt.Sprintf("disjuntor do modelo candidato EM OBSERVACAO (EPIC-06/AOS-521, ADR-041 2.9): conta os desfechos dos runs do candidato no livro duravel (%s) e, quando a regra disparar (%d falhas nas ultimas %d unidades do candidato, ou o primeiro turno com o modelo servido diferente), grava «teria aberto» e di-lo em aos_model_breaker_would_open. NAO corta a fatia. AOS_MODEL_BREAKER=enforce passa a cortar — e corta logo se a regra ja tiver disparado para o registo em vigor", canaryStream, disjuntorFalhasParaAbrir, disjuntorJanela))
	case modelBreakerEnforce:
		linhas = append(linhas, fmt.Sprintf("disjuntor do modelo candidato EM IMPOSICAO (EPIC-06/AOS-521, ADR-041 2.9): %d falhas nas ultimas %d unidades do candidato, ou o primeiro turno com o modelo servido diferente, e o candidato deixa de receber runs novos — os planos novos vao para o titular, um run novo de um plano ja atribuido ao candidato e recusado (%s), e os runs em curso acabam. NAO volta sozinho: reabrir e assinar um registo de entrada novo", disjuntorFalhasParaAbrir, disjuntorJanela, causaDisjuntorAberto))
	}
	if !rotaGovernada(modoDaRota) {
		linhas = append(linhas, "AVISO (EPIC-06/AOS-521): a governacao da rota esta DESLIGADA (AOS_MODEL_ROUTE_GOVERNANCE) — o sinal «modelo servido diferente» NAO EXISTE: o disjuntor so ve os outros quatro sinais. Com AOS_MODEL_BREAKER=enforce o no nao arrancava assim")
	}
	return linhas
}

// disjuntorPayload é o corpo dos quatro eventos. Só identificadores, digests, sinais e contagens.
type disjuntorPayload struct {
	V string `json:"v"`
	// EntryDigest é o digest do registo de entrada assinado a que o facto pertence; Alias e
	// ProfileDigest são os do candidato desse registo.
	EntryDigest   string `json:"entry_digest"`
	Alias         string `json:"alias"`
	ProfileDigest string `json:"profile_digest"`
	// Do `run_outcome` e do `route_mismatch`: o run.
	RunID string `json:"run_id,omitempty"`
	// Do `run_outcome`: a unidade da janela e o sinal.
	Unit   string `json:"unit,omitempty"`
	Signal string `json:"signal,omitempty"`
	// Do `opened` e do `would_open`: a regra que disparou, os cinco contadores (por sinal de falha)
	// e a janela no momento do disparo.
	Rule     string         `json:"rule,omitempty"`
	Counters map[string]int `json:"counters,omitempty"`
	Window   []itemDaJanela `json:"window,omitempty"`
	// Do `opened` e do `would_open`: o registo NÃO era o que o nó que gravou a marca tinha em vigor
	// (um run antigo, fixado sob um registo anterior, acabou depois de o registo mudar). A marca vale
	// para o registo dela; a fatia do candidato em vigor não muda por ela.
	OutOfForce bool `json:"out_of_force,omitempty"`
}

// registoDoCandidato identifica o candidato de um registo de entrada: é a que o estado do disjuntor
// está preso.
type registoDoCandidato struct {
	entryDigest, alias, profileDigest string
}

func (c candidatoEmVigor) registo() registoDoCandidato {
	return registoDoCandidato{entryDigest: c.entryDigest, alias: c.alias, profileDigest: c.profileDigest}
}

// disjuntorNoLivro é o que o livro tem sobre o disjuntor de UM registo de entrada.
type disjuntorNoLivro struct {
	// obs são as observações, pela ordem do livro.
	obs []observacaoDoDisjuntor
	// runs e turnos são os runs que já têm, respectivamente, o desfecho e o turno F5 gravados.
	runs, turnos map[string]bool
	// aberto e teriaAberto são as marcas, se lá estiverem.
	aberto, teriaAberto *disjuntorPayload
}

// disjuntorDe devolve o que o livro tem para o registo dado. Nunca nil.
func (st estadoDoCanary) disjuntorDe(entryDigest string) *disjuntorNoLivro {
	if d := st.disjuntores[entryDigest]; d != nil {
		return d
	}
	return &disjuntorNoLivro{}
}

// tipoDoDisjuntor diz se o tipo é um dos quatro do disjuntor.
func tipoDoDisjuntor(tipo string) bool {
	switch tipo {
	case EventTypeModelBreakerRunOutcome, EventTypeModelBreakerRouteMismatch, EventTypeModelBreakerOpened, EventTypeModelBreakerWouldOpen:
		return true
	}
	return false
}

// lerDoDisjuntor junta ao estado um evento do disjuntor. Um evento de um tipo conhecido que não se
// sabe ler é ilegível, como os do canary: fail-closed.
func (st *estadoDoCanary) lerDoDisjuntor(ev eventstore.Event) error {
	var p disjuntorPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil || p.EntryDigest == "" || p.Alias == "" || p.ProfileDigest == "" {
		return fmt.Errorf("%w: evento %d do disjuntor", ErrCanaryUnreadable, ev.Seq)
	}
	if st.disjuntores == nil {
		st.disjuntores = map[string]*disjuntorNoLivro{}
	}
	d := st.disjuntores[p.EntryDigest]
	if d == nil {
		d = &disjuntorNoLivro{runs: map[string]bool{}, turnos: map[string]bool{}}
		st.disjuntores[p.EntryDigest] = d
	}
	switch ev.Type {
	case EventTypeModelBreakerRunOutcome:
		if p.RunID == "" || p.Unit == "" || !sinalDeRunConhecido(p.Signal) {
			return fmt.Errorf("%w: desfecho sem run, sem unidade ou com um sinal desconhecido no evento %d", ErrCanaryUnreadable, ev.Seq)
		}
		// A PRIMEIRA ESCRITA GANHA: um run tem um desfecho.
		if !d.runs[p.RunID] {
			d.runs[p.RunID] = true
			d.obs = append(d.obs, observacaoDoDisjuntor{run: p.RunID, unidade: p.Unit, sinal: p.Signal})
		}
	case EventTypeModelBreakerRouteMismatch:
		if p.RunID == "" {
			return fmt.Errorf("%w: turno com o modelo diferente sem run no evento %d", ErrCanaryUnreadable, ev.Seq)
		}
		if !d.turnos[p.RunID] {
			d.turnos[p.RunID] = true
			d.obs = append(d.obs, observacaoDoDisjuntor{turno: true, run: p.RunID})
		}
	case EventTypeModelBreakerOpened, EventTypeModelBreakerWouldOpen:
		if p.Rule != regraFalhasNaJanela && p.Rule != regraModeloDiferente {
			return fmt.Errorf("%w: marca do disjuntor sem uma regra conhecida no evento %d", ErrCanaryUnreadable, ev.Seq)
		}
		if ev.Type == EventTypeModelBreakerOpened && d.aberto == nil {
			d.aberto = &p
		}
		if ev.Type == EventTypeModelBreakerWouldOpen && d.teriaAberto == nil {
			d.teriaAberto = &p
		}
	}
	return nil
}

// disjuntorDoModelo é o disjuntor LIGADO de um nó (`observe` ou `enforce`). Um ponteiro nil é `off`.
type disjuntorDoModelo struct {
	modo string
	// emVigor é o digest do registo do candidato que o nó serve: é o estado DELE que o `/metrics`
	// publica.
	emVigor string
	// O que a ÚLTIMA leitura do livro por esta réplica viu para o registo em vigor. Não decide nada.
	abertoVisto, disparouVisto, falhasVistas, unidadesVistas atomic.Int64
	sinaisVistos                                             map[string]*atomic.Int64
	// naoGravadas conta as observações que esta réplica NÃO conseguiu gravar no livro.
	naoGravadas atomic.Int64
	// prazoDeIO é o prazo das escritas fora de uma decisão ([disjuntorPrazoDeIO]).
	prazoDeIO time.Duration
	// turnoPorGravar diz que esta réplica VIU um turno do candidato em vigor com o modelo servido
	// diferente e NÃO o conseguiu gravar no livro. Em `enforce` é o lado fechado: enquanto estiver
	// de pé, esta réplica trata o disjuntor como aberto — um turno com o modelo trocado não passa em
	// silêncio por o registo ter falhado. Vive em memória: um reinício perde-o (ADR-041 §5).
	turnoPorGravar atomic.Bool
	// turnosTratados são os runs cujo turno F5 esta réplica já levou ao livro: poupa uma leitura por
	// turno a um run que continua a ser servido por outro modelo.
	turnosTratados sync.Map
}

func novoDisjuntorDoModelo(modo, emVigor string) *disjuntorDoModelo {
	d := &disjuntorDoModelo{modo: modo, emVigor: emVigor, prazoDeIO: disjuntorPrazoDeIO, sinaisVistos: map[string]*atomic.Int64{}}
	for _, s := range sinaisDeFalha() {
		d.sinaisVistos[s] = new(atomic.Int64)
	}
	return d
}

// julgarDisjuntor diz, do estado lido do livro, se o disjuntor do registo dado está ABERTO para as
// decisões deste nó, o que a regra dá, e que marca falta gravar (vazio ⇒ nenhuma).
//
//   - a marca `opened` vale em TODOS os modos, `off` incluído;
//   - com o nó em `enforce`, a regra disparada sobre as observações do livro abre, haja ou não marca
//     — a marca que falte é reposta;
//   - com o nó em `observe`, a regra disparada pede a marca `would_open` e não abre.
func (l *livroDoCanary) julgarDisjuntor(st estadoDoCanary, entryDigest string) (aberto bool, regra estadoDaRegra, falta string) {
	d := st.disjuntorDe(entryDigest)
	aberto = d.aberto != nil
	if l.disjuntor == nil {
		return aberto, estadoDaRegra{}, ""
	}
	regra = avaliarDisjuntor(d.obs)
	if l.disjuntor.modo == modelBreakerEnforce && entryDigest == l.disjuntor.emVigor && l.disjuntor.turnoPorGravar.Load() {
		aberto = true
	}
	if regra.disparou {
		switch l.disjuntor.modo {
		case modelBreakerEnforce:
			aberto = true
			if d.aberto == nil {
				falta = EventTypeModelBreakerOpened
			}
		case modelBreakerObserve:
			if d.aberto == nil && d.teriaAberto == nil {
				falta = EventTypeModelBreakerWouldOpen
			}
		}
	}
	if entryDigest == l.disjuntor.emVigor {
		v := l.disjuntor
		v.abertoVisto.Store(boolParaInt(aberto))
		v.disparouVisto.Store(boolParaInt(regra.disparou || d.aberto != nil))
		// A janela e os contadores do FIM da sequência: em `enforce` e com a marca de aberto são os
		// do disparo (não se grava mais nada); em `observe` continuam a andar.
		v.falhasVistas.Store(int64(regra.falhasAgora()))
		v.unidadesVistas.Store(int64(len(regra.janelaAgora)))
		for s, c := range v.sinaisVistos {
			c.Store(int64(regra.contadoresAgora[s]))
		}
	}
	return aberto, regra, falta
}

func boolParaInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// passoDoDisjuntor é o StepID de um evento do disjuntor por run: um resumo do id do run.
func passoDoDisjuntor(prefixo, runID string) string {
	h := sha256.Sum256([]byte(runID))
	return prefixo + hex.EncodeToString(h[:])
}

// gravarMarcaDoDisjuntor grava `opened` ou `would_open` para o registo, com o `seq` que se leu. A
// chave de idempotência é a do REGISTO: só uma marca de cada tipo fica, escreva quem escrever.
// `gravada` diz que a marca ESTÁ no livro (ficou agora, ou já lá estava): quem chama relê. Falso sem
// erro é a perda do CAS — outra escrita entrou, e a marca fica por gravar.
//
// A MARCA É O SINAL, NÃO A DECISÃO. Quem decide é a regra sobre as observações; por isso um conflito
// ou uma falha ao gravar a marca nunca recusa um run nem impede o arranque: tenta-se outra vez na
// decisão seguinte.
func (l *livroDoCanary) gravarMarcaDoDisjuntor(ctx context.Context, reg registoDoCandidato, seq uint64, tipo string, regra estadoDaRegra) (gravada bool, err error) {
	contadores := map[string]int{}
	for _, s := range sinaisDeFalha() {
		contadores[s] = regra.contadores[s]
	}
	payload, err := json.Marshal(disjuntorPayload{
		V: disjuntorVersaoDoPayload, EntryDigest: reg.entryDigest, Alias: reg.alias, ProfileDigest: reg.profileDigest,
		Rule: regra.regra, Counters: contadores, Window: regra.janela,
		OutOfForce: reg.entryDigest != l.disjuntor.emVigor,
	})
	if err != nil {
		return false, err
	}
	passo := "breaker-opened-" + reg.entryDigest
	if tipo == EventTypeModelBreakerWouldOpen {
		passo = "breaker-would-open-" + reg.entryDigest
	}
	evento := eventstore.EventInput{
		Type: tipo, Payload: payload, RunID: canaryRunID, StepID: passo,
		Producer: eventstore.Producer{NHIID: disjuntorNHI},
	}
	res, err := l.es.Append(ctx, canaryStream, evento, eventstore.WithExpectedSeq(seq))
	if conflitoDeCAS(err) {
		// PERDEU O CAS: TENTA SEM ELE. A marca não precisa do `seq` para ser única — a chave de
		// idempotência é a do registo, e o que ela sela é o estado do PRIMEIRO disparo, que é o
		// mesmo seja qual for a escrita que entrou entretanto. É o que deixa a marca ficar gravada
		// numa réplica cuja vista do stream está atrasada (AOS-524), onde nenhum CAS passa — o
		// molde da marca de fecho do canary.
		res, err = l.es.Append(ctx, canaryStream, evento)
	}
	if err != nil {
		// CADA TENTATIVA FALHADA CONTA. Uma marca de aberto por gravar é a via pela qual um corte
		// feito em `enforce` deixava de valer ao mudar para `off` ou `observe`: tem de se ver.
		l.disjuntor.naoGravadas.Add(1)
		if conflitoDeCAS(err) {
			return false, nil
		}
		return false, err
	}
	if res.Status != eventstore.StatusDuplicate && reg.entryDigest != l.disjuntor.emVigor {
		// O REGISTO NÃO É O EM VIGOR. O alias pode ser o mesmo do candidato que este nó serve: a
		// linha diz o digest e diz que NÃO é o registo em vigor, para ninguém ler aqui que o
		// candidato de agora perdeu a fatia.
		l.logf("disjuntor do modelo candidato (AOS-521): a regra %q disparou para um REGISTO FORA DE VIGOR neste no (digest do registo %s, alias %q; o registo em vigor e %s) — marca %s gravada para ESSE registo, com %d falha(s) em %d unidade(s). O candidato do registo em vigor NAO e afectado: a sua fatia nao muda por esta marca",
			regra.regra, reg.entryDigest, reg.alias, l.disjuntor.emVigor, tipo, regra.falhasNaJanela(), len(regra.janela))
	} else if res.Status != eventstore.StatusDuplicate {
		if tipo == EventTypeModelBreakerOpened {
			l.logf("DISJUNTOR DO MODELO CANDIDATO ABERTO (AOS-521): regra %q, %d falha(s) em %d unidade(s) da janela, contadores %v — o candidato %q (digest do registo %s) DEIXA DE RECEBER RUNS NOVOS: os planos novos vao para o titular (%s), um run novo de um plano ja atribuido ao candidato e recusado (%s), os runs em curso acabam. NAO volta sozinho: reabrir e assinar um registo de entrada novo",
				regra.regra, regra.falhasNaJanela(), len(regra.janela), contadores, reg.alias, reg.entryDigest, razaoDisjuntorAberto, causaDisjuntorAberto)
		} else {
			l.logf("disjuntor do modelo candidato TERIA ABERTO (AOS-521, em observacao): regra %q, %d falha(s) em %d unidade(s) da janela, contadores %v — candidato %q (digest do registo %s). A fatia NAO muda. Com AOS_MODEL_BREAKER=enforce este registo abre na primeira leitura",
				regra.regra, regra.falhasNaJanela(), len(regra.janela), contadores, reg.alias, reg.entryDigest)
		}
	}
	return true, nil
}

// conferirDisjuntor lê o livro, avalia o disjuntor do candidato em vigor e garante a marca que
// faltar. É o que o arranque chama, com o disjuntor ligado.
func (l *livroDoCanary) conferirDisjuntor(ctx context.Context, c candidatoEmVigor) (aberto bool, regra estadoDaRegra, err error) {
	if err := l.entrar(ctx); err != nil {
		return false, estadoDaRegra{}, err
	}
	defer l.sair()
	for tentativa := 0; tentativa < disjuntorMaxTentativas; tentativa++ {
		st, err := l.ler(ctx)
		if err != nil {
			return false, estadoDaRegra{}, err
		}
		var falta string
		aberto, regra, falta = l.julgarDisjuntor(st, c.entryDigest)
		if falta == "" || st.futuros > 0 {
			return aberto, regra, nil
		}
		if _, gerr := l.gravarMarcaDoDisjuntor(ctx, c.registo(), st.ultimoSeq, falta, regra); gerr != nil {
			l.logf("disjuntor do modelo candidato (AOS-521): a marca %s NAO foi gravada no arranque: %v — o estado e o da regra sobre as observacoes do livro; a marca sera tentada na proxima decisao", falta, gerr)
			return aberto, regra, nil
		}
		// Gravada, ou perdeu o CAS: relê — na volta seguinte a marca já lá está, ou tenta-se de novo.
	}
	l.logf("disjuntor do modelo candidato (AOS-521): a marca do disjuntor ficou por gravar no arranque (contencao no livro) — o estado e o da regra sobre as observacoes; a marca sera tentada na proxima decisao")
	return aberto, regra, nil
}

// observarNoDisjuntor leva UMA observação ao livro, para o registo dado, e garante a marca se a
// regra disparar com ela. Com o disjuntor desligado não faz nada.
//
// Cada volta relê o livro e faz no máximo UMA escrita, com o `seq` que leu: primeiro a observação —
// se o run ainda não a tem, e se a regra ainda não disparou (depois do disparo o estado é o do
// disparo, e não se grava mais nada) —, depois a marca que falte. Idempotente: a chave de cada
// evento é a do run, ou a do registo.
func (l *livroDoCanary) observarNoDisjuntor(ctx context.Context, reg registoDoCandidato, o observacaoDoDisjuntor) error {
	if l == nil || l.disjuntor == nil {
		return nil
	}
	if reg.entryDigest == "" || reg.alias == "" || reg.profileDigest == "" || o.run == "" {
		return fmt.Errorf("%w: observacao do disjuntor sem registo ou sem run", ErrCanaryUnreadable)
	}
	if err := l.entrar(ctx); err != nil {
		return err
	}
	defer l.sair()
	// noLivro diz que uma leitura já viu que a observação não precisa de ser gravada (está lá, ou a
	// regra já disparou): a partir daí só falta a marca, que é o melhor esforço.
	noLivro := false
	for tentativa := 0; tentativa < disjuntorMaxTentativas; tentativa++ {
		st, err := l.ler(ctx)
		if err != nil {
			if noLivro {
				return nil
			}
			return err
		}
		if st.futuros > 0 {
			// O livro tem eventos de uma versão mais recente: esta versão não escreve nele.
			return fmt.Errorf("%w: o livro tem eventos de uma versao mais recente", ErrCanaryUnreadable)
		}
		d := st.disjuntorDe(reg.entryDigest)
		_, regra, falta := l.julgarDisjuntor(st, reg.entryDigest)
		gravada := d.runs[o.run]
		if o.turno {
			gravada = d.turnos[o.run]
		}
		// QUANDO JÁ NÃO SE GRAVA: com a marca de aberto no livro; em `enforce`, depois de a regra
		// disparar (o estado é o do disparo, e o conjunto das observações fica estável); e atingido o
		// tecto do registo. Em `observe` a regra disparada NÃO pára a medição: o modo de medir
		// continua a medir, e a janela do fim da sequência é a que o `/metrics` mostra.
		parada := d.aberto != nil || (regra.disparou && l.disjuntor.modo == modelBreakerEnforce) || len(d.obs) >= disjuntorMaxObservacoes
		if !gravada && !parada {
			p := disjuntorPayload{V: disjuntorVersaoDoPayload, EntryDigest: reg.entryDigest, Alias: reg.alias, ProfileDigest: reg.profileDigest, RunID: o.run}
			tipo, passo := EventTypeModelBreakerRouteMismatch, passoDoDisjuntor("breaker-f5-", o.run)
			if !o.turno {
				tipo, passo = EventTypeModelBreakerRunOutcome, passoDoDisjuntor("breaker-run-", o.run)
				p.Unit, p.Signal = o.unidade, o.sinal
			}
			payload, merr := json.Marshal(p)
			if merr != nil {
				return merr
			}
			_, aerr := l.es.Append(ctx, canaryStream, eventstore.EventInput{
				Type: tipo, Payload: payload, RunID: canaryRunID, StepID: passo,
				Producer: eventstore.Producer{NHIID: disjuntorNHI},
			}, eventstore.WithExpectedSeq(st.ultimoSeq))
			if aerr != nil && !conflitoDeCAS(aerr) {
				return fmt.Errorf("%w: gravar a observacao do disjuntor: %w", ErrCanaryUnreadable, aerr)
			}
			continue // gravada, duplicada ou em conflito: relê e segue
		}
		noLivro = true
		if falta == "" {
			return nil
		}
		if _, gerr := l.gravarMarcaDoDisjuntor(ctx, reg, st.ultimoSeq, falta, regra); gerr != nil {
			l.logf("disjuntor do modelo candidato (AOS-521): a marca %s NAO foi gravada: %v — a observacao esta no livro; a marca sera tentada na proxima decisao", falta, gerr)
			return nil
		}
	}
	if noLivro {
		return nil
	}
	return ErrCanaryContention
}

// candidatoDoRun lê do stream do run o que o nó SELOU sobre ele: o registo do candidato em que
// ficou fixado (`run.model_pinned`, com o papel `candidato`, e SÓ o que o nó emitiu — o emissor é
// conferido como no `run.plan_origin`) e a unidade da janela a que pertence —
// o id da PRIMEIRA tentativa do nó do plano (`run.plan_origin`), ou o id do run. `ok` falso ⇒ o log
// não diz que o run é do candidato.
func candidatoDoRun(ctx context.Context, es eventstore.EventStore, runID string) (reg registoDoCandidato, unidade string, ok bool, err error) {
	if es == nil {
		return reg, "", false, errors.New("sem Event Store onde ler o run")
	}
	evs, err := es.Read(ctx, runID, 1)
	if err != nil {
		return reg, "", false, err
	}
	unidade = runID
	var fixacao *modeloFixadoPayload
	for _, ev := range evs {
		switch {
		case ev.Type == EventTypeRunModelPinned && ev.Producer.NHIID == modeloFixadoNHI && fixacao == nil:
			var p modeloFixadoPayload
			if uerr := json.Unmarshal(ev.Payload, &p); uerr != nil {
				return reg, "", false, fmt.Errorf("run.model_pinned ilegivel no evento %d", ev.Seq)
			}
			fixacao = &p
		case ev.Type == EventTypeRunPlanOrigin && ev.Producer.NHIID == origemNHI:
			var o origemDoRunFilho
			if uerr := json.Unmarshal(ev.Payload, &o); uerr != nil {
				return reg, "", false, fmt.Errorf("run.plan_origin ilegivel no evento %d", ev.Seq)
			}
			if o.Attempt >= 2 && o.Pedido.RunID != "" && o.NodeID != "" {
				unidade = idDoRunFilho(o.Pedido.RunID, o.NodeID)
			}
		}
	}
	if fixacao == nil || fixacao.Role != string(canary.RoleCandidate) || fixacao.EntryDigest == "" || fixacao.ProfileDigest == "" {
		return reg, "", false, nil
	}
	return registoDoCandidato{entryDigest: fixacao.EntryDigest, alias: fixacao.Alias, profileDigest: fixacao.ProfileDigest}, unidade, true, nil
}

// disjuntorLigado devolve o disjuntor do nó, ou nil — desligado, sem candidato a servir, ou sem a
// pasta de perfis.
func (c *canaryDoNo) disjuntorLigado() *disjuntorDoModelo {
	if c == nil || !c.servir || c.livro == nil {
		return nil
	}
	return c.livro.disjuntor
}

// disjuntorLigado devolve o disjuntor do nó, ou nil.
func (n *Node) disjuntorLigado() *disjuntorDoModelo {
	if n == nil {
		return nil
	}
	return n.canary.disjuntorLigado()
}

// arrancarDisjuntor é a parte do arranque que é do disjuntor, com um candidato a servir e o canary já
// aberto no livro.
//
// DESLIGADO (`off`): não lê nada. Só diz, se a abertura do canary o viu, que o livro tem o disjuntor
// aberto para o registo em vigor — a marca vale na mesma.
//
// LIGADO: lê o livro, avalia, garante a marca que falte, diz o estado, e liga o despachante para os
// turnos com o modelo servido diferente. Fail-closed: se o livro não se lê, o nó não arranca.
func (c *canaryDoNo) arrancarDisjuntor(ctx context.Context, assinados *perfisAssinados, log func(string, ...any)) error {
	if c.livro.disjuntor == nil {
		if c.livro.disjuntorAbertoVisto.Load() {
			log("AVISO (AOS-521): AOS_MODEL_BREAKER esta desligado e o livro duravel (%s) tem o disjuntor do candidato ABERTO para o registo em vigor (%s) — a marca VALE NA MESMA: o candidato nao recebe runs novos (os planos novos vao para o titular, com a razao %s), e um run novo de um plano ja atribuido ao candidato e recusado (%s). Desligar o disjuntor nao o reabre: reabrir e assinar um registo de entrada novo",
				canaryStream, c.candidato.entryDigest, razaoDisjuntorAberto, causaDisjuntorAberto)
		}
		return nil
	}
	aberto, regra, err := c.livro.conferirDisjuntor(ctx, c.candidato)
	if err != nil {
		return fmt.Errorf("aos: ler o disjuntor do modelo candidato (AOS-521): %w", err)
	}
	c.livro.disjuntorAbertoVisto.Store(aberto)
	estado := "FECHADO"
	switch {
	case aberto:
		estado = "ABERTO (o candidato nao recebe runs novos; so um registo de entrada novo, assinado, o reabre)"
	case regra.disparou:
		estado = "TERIA ABERTO (em observacao: a fatia nao muda; em enforce este registo abre na primeira leitura)"
	}
	log("disjuntor do modelo candidato (AOS-521): %s — modo %s, candidato %q, digest do registo %s, %d falha(s) em %d unidade(s) da janela (lido do livro duravel %s; um reinicio nao perde o estado)",
		estado, c.livro.disjuntor.modo, c.candidato.alias, c.candidato.entryDigest, regra.falhasNaJanela(), len(regra.janela), canaryStream)
	if assinados != nil && assinados.despachante != nil {
		aoTurno := c.observarTurnoNoDisjuntor
		assinados.despachante.aoTurno.Store(&aoTurno)
	}
	return nil
}

// observarDesfechoNoDisjuntor leva ao livro o desfecho de um run do CANDIDATO que este processo
// acabou de selar. Corre na saída da hospedagem, depois do selo do estado terminal.
//
// O TITULAR NUNCA É TOCADO: um run que não está fixado no alias do candidato sai na segunda linha,
// sem uma leitura. Um run do candidato cujo desfecho não conta ([sinalDoDesfecho]) também.
//
// Uma falha aqui não muda o desfecho do run — já está selado. Fica dita no log e contada em
// `aos_model_breaker_unrecorded_total`: uma observação que não chegou ao livro não entra na janela.
func (s *NodeService) observarDesfechoNoDisjuntor(runID string, goal agentruntime.Goal, selado state.State, res agentruntime.Result, runErr error, panicked bool) {
	d := s.node.disjuntorLigado()
	if d == nil {
		return
	}
	if goal.ModelPinnedBy == "" || goal.Model.ModelID != s.node.canary.candidato.alias {
		return
	}
	sinal, conta := sinalDoDesfecho(selado, res, runErr, panicked)
	if !conta {
		return
	}
	// Sem o contexto do run, que pode já ter acabado — mas com um prazo: um Event Store pendurado
	// não prende a saída da hospedagem.
	ctx, cancelar := context.WithTimeout(context.Background(), d.prazoDeIO)
	defer cancelar()
	reg, unidade, ok, err := candidatoDoRun(ctx, s.node.EventStore, runID)
	if err == nil && !ok {
		return
	}
	if err == nil {
		err = s.node.canary.livro.observarNoDisjuntor(ctx, reg, observacaoDoDisjuntor{run: runID, unidade: unidade, sinal: sinal})
	}
	if err != nil {
		d.naoGravadas.Add(1)
		s.log("disjuntor do modelo candidato (AOS-521): o desfecho do run %q (sinal %s) NAO ficou no livro — nao entra na janela: %v", runID, sinal, err)
	}
}

// observarTurnoNoDisjuntor é o que o despachante chama depois de cada pedido ao vivo, com o
// disjuntor ligado: se o pedido foi ao CANDIDATO e a governação da rota viu o modelo servido
// DIFERENTE — na resposta (governação em observação) ou no erro (em imposição) —, leva o turno ao
// livro, e o disjuntor abre NESSE turno. Qualquer outro pedido sai sem fazer nada.
func (c *canaryDoNo) observarTurnoNoDisjuntor(ctx context.Context, alias string, resp agentruntime.ModelResponse, callErr error) {
	d := c.disjuntorLigado()
	if d == nil || alias != c.candidato.alias {
		return
	}
	diferente := callErr == nil && resp.RouteCheck.Normalizado() == agentruntime.RouteDifferent
	if callErr != nil {
		diferente = modelgateway.ClassifyTurnFailure(callErr) == modelgateway.TurnFailureRouteDifferent
	}
	if !diferente {
		return
	}
	runID, _, ok := agentruntime.ModelCallFromContext(ctx)
	if !ok || runID == "" {
		return
	}
	if _, ja := d.turnosTratados.LoadOrStore(runID, true); ja {
		return
	}
	// O facto sela-se mesmo que o run seja cancelado a seguir — mas com um prazo: isto corre DENTRO do
	// turno, e um Event Store pendurado não o pode prender.
	bg, cancelar := context.WithTimeout(context.WithoutCancel(ctx), d.prazoDeIO)
	defer cancelar()
	reg, _, ok, err := candidatoDoRun(bg, c.livro.es, runID)
	if err == nil && !ok {
		return
	}
	if err == nil {
		err = c.livro.observarNoDisjuntor(bg, reg, observacaoDoDisjuntor{turno: true, run: runID})
	}
	if err != nil {
		d.turnosTratados.Delete(runID)
		d.naoGravadas.Add(1)
		porque := ""
		if d.modo == modelBreakerEnforce && (reg.entryDigest == "" || reg.entryDigest == d.emVigor) {
			// O LADO FECHADO: o nó viu o modelo trocado e não o conseguiu gravar. Corta na mesma.
			d.turnoPorGravar.Store(true)
			porque = " — em enforce, ESTA REPLICA PASSA A TRATAR O DISJUNTOR COMO ABERTO ate reiniciar (os planos novos vao para o titular); o registo e tentado de novo no proximo turno com o modelo trocado"
		}
		c.livro.logf("disjuntor do modelo candidato (AOS-521): o turno com o modelo servido diferente do run %q NAO ficou no livro: %v%s", runID, err, porque)
	}
}
