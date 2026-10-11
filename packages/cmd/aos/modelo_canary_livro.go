package main

// modelo_canary_livro.go — O LIVRO DO CANARY (AOS-520, ADR-041 §2.6).
//
// O canary de um modelo candidato é uma percentagem de PLANOS, e acaba ao fim de 40 planos
// servidos pelo candidato. Para isso o nó tem de saber, de forma durável e igual em todas as
// réplicas: que planos já foram atribuídos ao candidato, quantos foram, e se o canary fechou.
//
// DURÁVEL NO EVENT STORE, COM CAS. Um stream sob o espaço reservado `aos-internal/` (AOS-417),
// escrito com concorrência optimista — o molde da quota por principal (`quota_por_principal.go`).
// O audit de governação do gateway é local à réplica e não serve para contar: duas réplicas
// dariam ao candidato o dobro dos planos.
//
// TRÊS FACTOS:
//
//   - `model.canary.opened` — um por registo de entrada assinado (pelo digest do registo): o
//     alias, o digest do perfil, a fatia, o limite e a MARCA de abertura (o último `seq` da fila
//     de pedidos de plano nesse momento). Os planos pedidos até à marca são do titular.
//   - `model.canary.plan_assigned` — um por plano atribuído ao CANDIDATO. A primeira escrita
//     ganha: a chave de idempotência é a do plano, sem o registo, pelo que um plano só é
//     atribuído uma vez, em qualquer abertura.
//   - `model.canary.closed` — a contagem chegou ao limite.
//
// QUEM DECIDE É O QUE ESTÁ GRAVADO. A fatia e o limite usados na decisão são os do `opened`, e não
// os que o nó tem em memória; um `opened` que não case com o registo assinado em vigor (o mesmo
// digest de registo com outro alias, perfil ou fatia) não é reinterpretado — recusa.
//
// O LIVRO EVOLUI POR TIPOS NOVOS. Um evento de um tipo que este binário não conhece, dentro da
// família `model.*`, não derruba o nó nem recusa os runs do titular: é ignorado na contagem, e o
// canary passa a «não decidível por esta versão» — nenhum plano novo vai para o candidato. É o que
// deixa uma versão mais recente escrever aqui (o disjuntor do AOS-521) durante um deploy rolante
// ou antes de um recuo de imagem, num stream que é append-only e não se repara.
//
// FAIL-CLOSED. Um evento de um tipo CONHECIDO que não se sabe ler, um stream ilegível, ou o ciclo de concorrência
// esgotado: a decisão falha, e o run que a pediu não arranca. Nunca «na dúvida, o titular» — um
// plano do candidato servido pelo titular por causa de uma leitura falhada é o defeito que este
// livro existe para impedir. Nenhuma decisão lê o relógio.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/aos-ref/platform/model-gateway/policy/canary"
	"github.com/aos-ref/substrate/eventstore"
)

// Tipos de evento do livro (família `model.*`, tecnica/13 §3.3). Constantes junto do emissor.
const (
	// EventTypeModelCanaryOpened — o canary de um registo de entrada assinado abriu.
	EventTypeModelCanaryOpened = "model.canary.opened"
	// EventTypeModelCanaryPlanAssigned — um plano foi atribuído ao candidato.
	EventTypeModelCanaryPlanAssigned = "model.canary.plan_assigned"
	// EventTypeModelCanaryClosed — o candidato serviu o limite de planos e deixou de receber novos.
	EventTypeModelCanaryClosed = "model.canary.closed"
)

// familiaDoLivro é o prefixo dos tipos de evento que podem existir no stream do livro. É o CONTRATO
// DE EVOLUÇÃO do livro (ADR-041 §2.6): quem acrescentar factos a este stream — o disjuntor do
// AOS-521 — usa tipos NOVOS desta família, e nunca muda o significado nem a forma de um tipo que já
// existe. Um binário que não conheça um tipo da família ignora-o e deixa de atribuir planos novos.
const familiaDoLivro = "model."

// canaryStream é o stream do livro. Sob o espaço reservado, que o `POST /runs` e o `POST /plans`
// recusam como `run_id`; sem pontos (ADR-029).
const canaryStream = streamsReservados + "model-canary"

// canaryRunID é o «run» sintético que, com o StepID, forma a idempotency-key (molde de [quotaRunID]).
const canaryRunID = streamsReservados + "model-canary"

// canaryNHI é a identidade emissora: o facto é do NÓ.
const canaryNHI = "nhi:aos-node/model-canary"

// canaryVersaoDoLivro é a versão do payload dos três eventos.
const canaryVersaoDoLivro = "1.0"

// canaryLimiteDePlanos é quantos planos o candidato serve antes de o canary fechar (decisão D4 do
// dono). Fica gravado no `opened`, e é o valor gravado que decide.
const canaryLimiteDePlanos = 40

// canaryMaxTentativas limita o ciclo de concorrência optimista. Cada conflito significa que outra
// réplica (ou outro run desta) escreveu no livro entretanto.
const canaryMaxTentativas = 8

// Razões de uma escolha. Vocabulário FECHADO: é o que fica no `run.model_pinned`.
const (
	// razaoFuncao — a função de escolha decidiu (ADR-041 §2.3).
	razaoFuncao = "funcao"
	// razaoPlanoJaAtribuido — o plano já tinha sido atribuído ao candidato.
	razaoPlanoJaAtribuido = "plano_ja_atribuido"
	// razaoPlanoAnteriorAAbertura — o pedido de plano é anterior à abertura do canary.
	razaoPlanoAnteriorAAbertura = "plano_anterior_a_abertura"
	// razaoCanaryFechado — o candidato já serviu o limite de planos.
	razaoCanaryFechado = "canary_fechado"
	// razaoLivroMaisRecente — o livro tem eventos de uma versão mais recente do nó, e esta versão
	// não sabe se o candidato ainda pode receber planos: não atribui.
	razaoLivroMaisRecente = "livro_de_versao_mais_recente"
)

// Erros do livro. Todos recusam o run que pediu a decisão.
var (
	// ErrCanaryUnreadable — o livro tem um evento que não se sabe ler, não se conseguiu ler, ou não
	// tem a abertura do registo em vigor.
	ErrCanaryUnreadable = errors.New("aos: livro do canary ilegivel (AOS-520)")
	// ErrCanaryContention — o ciclo de concorrência optimista esgotou as tentativas.
	ErrCanaryContention = errors.New("aos: contencao ao escrever no livro do canary (AOS-520)")
	// ErrCanaryInconsistent — o livro tem uma abertura para o digest do registo em vigor com outro
	// alias, outro perfil ou outra fatia. Não se reinterpreta: recusa.
	ErrCanaryInconsistent = errors.New("aos: a abertura do canary gravada no livro nao e a do registo assinado em vigor (AOS-520)")
	// ErrPlanAssignedToAbsentCandidate — o plano foi atribuído a um candidato que este nó não está a
	// servir (outro alias, outro perfil, o interruptor desligado, ou o registo retirado).
	// ErrCanaryNewerVersion — o plano está atribuído ao candidato e o livro tem eventos de uma versão
	// mais recente do nó: esta versão não sabe se ainda o pode servir. Não o serve, e não o passa
	// para o titular.
	ErrCanaryNewerVersion            = errors.New("aos: o livro do canary tem eventos de uma versao mais recente do no, e o plano esta atribuido ao candidato — esta versao nao sabe se o pode servir (AOS-520)")
	ErrPlanAssignedToAbsentCandidate = errors.New("aos: o plano foi atribuido a um candidato que este no nao esta a servir (AOS-520)")
)

// canaryPayload é o corpo dos três eventos. Só identificadores, digests e contagens.
type canaryPayload struct {
	V string `json:"v"`
	// EntryDigest é o digest do registo de entrada assinado — o que a assinatura do dono cobre, e o
	// que identifica uma abertura.
	EntryDigest   string `json:"entry_digest"`
	Alias         string `json:"alias"`
	ProfileDigest string `json:"profile_digest"`
	// Do `opened`.
	SharePercent    int    `json:"share_percent,omitempty"`
	PlanRequestMark uint64 `json:"plan_request_mark,omitempty"`
	// Do `opened` e do `closed`.
	Limit int `json:"limit,omitempty"`
	// Do `plan_assigned`: a chave do plano e o seu tipo ([chaveCanary]).
	PlanKey     string `json:"plan_key,omitempty"`
	PlanKeyKind string `json:"plan_key_kind,omitempty"`
	// Do `closed`.
	Assigned int `json:"assigned,omitempty"`
}

// Os tipos de chave de plano. Vocabulário FECHADO.
const (
	// tipoDePedido — a chave é o identificador de um pedido de plano que o nó verificou.
	tipoDePedido = "pedido"
	// tipoDeRun — a chave é o id de um run que não é trabalho de um pedido de plano verificado.
	tipoDeRun = "run"
)

// chaveCanary é a chave de um plano no livro: o identificador e o TIPO.
//
// OS DOIS ESPAÇOS NÃO SE MISTURAM. Quem submete um run avulso escolhe o seu `run_id`, e pode
// escolher o identificador de um pedido de plano que já existe. Se a chave fosse só o
// identificador, a atribuição desse run ao candidato valia como «plano já atribuído» para os nós
// seguintes do plano com o mesmo nome — mesmo de um plano anterior à abertura, cujos nós já
// correram no titular. Com o tipo, a atribuição de um não é a do outro, e cada um conta um plano.
//
// O tipo NÃO entra na função de escolha (ADR-041 §2.3): a função reparte pelo identificador, e os
// vectores do ADR são os mesmos.
type chaveCanary struct {
	tipo, id string
}

func chaveDePedido(id string) chaveCanary { return chaveCanary{tipo: tipoDePedido, id: id} }
func chaveDeRun(id string) chaveCanary    { return chaveCanary{tipo: tipoDeRun, id: id} }

// indice é a chave do mapa de planos atribuídos: o tipo, um byte nulo e o identificador.
func (c chaveCanary) indice() string { return c.tipo + "\x00" + c.id }

// candidatoEmVigor é o candidato que o nó está a servir, lido do registo assinado no arranque.
type candidatoEmVigor struct {
	alias         string
	profileDigest string
	entryDigest   string
	fatia         int
}

// estadoDoCanary é o que o livro diz.
type estadoDoCanary struct {
	aberturas   map[string]canaryPayload // por digest do registo
	atribuidos  map[string]canaryPayload // por chave do plano ([chaveCanary.indice]); a primeira escrita ganha
	porAbertura map[string]int           // planos atribuídos, por digest do registo
	fechadas    map[string]bool          // por digest do registo
	ultimoSeq   uint64
	// futuros conta os eventos de tipos que este binário não conhece, dentro da família do livro.
	// Maior que zero ⇒ o livro foi escrito também por uma versão mais recente do nó, e o que ela
	// lá pôs pode mudar a decisão (um disjuntor aberto, por exemplo): o canary NÃO É DECIDÍVEL por
	// esta versão. Não se atribui nenhum plano novo ao candidato.
	futuros int
}

// decisaoDoCanary é o resultado de uma consulta ao livro para um run novo.
type decisaoDoCanary struct {
	papel canary.Role
	razao string
}

// fimDoCanary é o que se sela quando o canary fecha.
type fimDoCanary struct {
	alias, profileDigest, entryDigest string
	atribuidos, limite                int
}

// livroDoCanary lê e escreve o livro. Um por nó.
type livroDoCanary struct {
	es eventstore.EventStore
	// selar sela o fim do canary no audit de governação do gateway. nil ⇒ não há onde selar.
	selar func(context.Context, fimDoCanary) error
	log   func(string, ...any)
	// mu serializa as decisões DESTE processo: sem ele, dois runs novos da mesma réplica perdiam
	// o CAS um para o outro a cada passo. Entre réplicas, quem arbitra é o `WithExpectedSeq`.
	mu sync.Mutex
	// atribuidosVistos e fechadoVisto são o que a ÚLTIMA leitura desta réplica viu para o
	// candidato em vigor — o que o `/metrics` publica. Não decidem nada.
	atribuidosVistos atomic.Int64
	fechadoVisto     atomic.Int64
	// futurosVistos é quantos eventos de tipos desconhecidos a última leitura viu.
	futurosVistos atomic.Int64
}

func (l *livroDoCanary) logf(format string, args ...any) {
	if l != nil && l.log != nil {
		l.log(format, args...)
	}
}

// conflitoDeCAS diz se o erro de um Append é a perda do CAS. O perdedor pode receber QUALQUER dos
// dois (`eventstore/store.go`: quando o vencedor já materializou, o perdedor cai em
// `expected < last` e recebe a violação append-only, e não o conflito de seq).
func conflitoDeCAS(err error) bool {
	return errors.Is(err, eventstore.ErrSeqConflict) || errors.Is(err, eventstore.ErrAppendOnlyViolation)
}

// passoDoPlano é o StepID do `plan_assigned` de um plano: um resumo da chave (com o tipo), porque a
// chave é um identificador escolhido por quem pede o plano. NÃO leva o digest do registo: um plano é atribuído
// no máximo uma vez, seja qual for a abertura.
func passoDoPlano(chave chaveCanary) string {
	h := sha256.Sum256([]byte(chave.indice()))
	return "assigned-" + hex.EncodeToString(h[:])
}

// ler devolve o estado do livro. Um stream que não existe é um livro vazio.
func (l *livroDoCanary) ler(ctx context.Context) (estadoDoCanary, error) {
	st := estadoDoCanary{aberturas: map[string]canaryPayload{}, atribuidos: map[string]canaryPayload{}, porAbertura: map[string]int{}, fechadas: map[string]bool{}}
	evs, err := l.es.Read(ctx, canaryStream, 1)
	if errors.Is(err, eventstore.ErrStreamNotFound) {
		return st, nil
	}
	if err != nil {
		return st, fmt.Errorf("%w: %w", ErrCanaryUnreadable, err)
	}
	for _, ev := range evs {
		st.ultimoSeq = ev.Seq
		switch ev.Type {
		case EventTypeModelCanaryOpened, EventTypeModelCanaryPlanAssigned, EventTypeModelCanaryClosed:
		default:
			// UM TIPO QUE ESTE BINÁRIO NÃO CONHECE. Dentro da família do livro (`model.*`) é um evento
			// de uma versão mais recente do nó — o disjuntor do AOS-521 vai escrever aqui —, e não
			// se lê nem se conta: regista-se que foi visto, e quem decide passa a não atribuir nada
			// ([estadoDoCanary.futuros]). Fora da família é lixo no stream: ilegível, como sempre.
			if !strings.HasPrefix(ev.Type, familiaDoLivro) {
				return st, fmt.Errorf("%w: tipo %q no evento %d", ErrCanaryUnreadable, ev.Type, ev.Seq)
			}
			st.futuros++
			continue
		}
		// Um evento de um tipo CONHECIDO que não se sabe ler continua a ser ilegível.
		var p canaryPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil || p.EntryDigest == "" || p.Alias == "" || p.ProfileDigest == "" {
			return st, fmt.Errorf("%w: evento %d", ErrCanaryUnreadable, ev.Seq)
		}
		switch ev.Type {
		case EventTypeModelCanaryOpened:
			if p.Limit <= 0 || p.SharePercent <= 0 || p.SharePercent > 100 {
				return st, fmt.Errorf("%w: abertura sem limite ou sem fatia valida no evento %d", ErrCanaryUnreadable, ev.Seq)
			}
			if _, ja := st.aberturas[p.EntryDigest]; !ja {
				st.aberturas[p.EntryDigest] = p
			}
		case EventTypeModelCanaryPlanAssigned:
			if p.PlanKey == "" || (p.PlanKeyKind != tipoDePedido && p.PlanKeyKind != tipoDeRun) {
				return st, fmt.Errorf("%w: atribuicao sem plano ou sem o tipo da chave no evento %d", ErrCanaryUnreadable, ev.Seq)
			}
			indice := chaveCanary{tipo: p.PlanKeyKind, id: p.PlanKey}.indice()
			// A PRIMEIRA ESCRITA GANHA. A deduplicação do Event Store já o garante para escritas
			// deste código; uma segunda atribuição do mesmo plano que lá estivesse não muda a
			// primeira nem conta.
			if _, ja := st.atribuidos[indice]; !ja {
				st.atribuidos[indice] = p
				st.porAbertura[p.EntryDigest]++
			}
		case EventTypeModelCanaryClosed:
			st.fechadas[p.EntryDigest] = true
		}
	}
	return st, nil
}

// aberturaDe devolve a abertura gravada para o candidato em vigor, conferida com ele.
func (st estadoDoCanary) aberturaDe(c candidatoEmVigor) (canaryPayload, bool, error) {
	ab, ok := st.aberturas[c.entryDigest]
	if !ok {
		return canaryPayload{}, false, nil
	}
	if ab.Alias != c.alias || ab.ProfileDigest != c.profileDigest || ab.SharePercent != c.fatia {
		return canaryPayload{}, true, ErrCanaryInconsistent
	}
	return ab, true, nil
}

// publicar guarda o que esta leitura viu, para o `/metrics`.
func (l *livroDoCanary) publicar(atribuidos int, fechado bool) {
	l.atribuidosVistos.Store(int64(atribuidos))
	if fechado {
		l.fechadoVisto.Store(1)
	} else {
		l.fechadoVisto.Store(0)
	}
}

// abrir garante que o livro tem a abertura do candidato em vigor. Idempotente: uma abertura já
// gravada para o mesmo registo é conferida e fica como está — a contagem não volta a zero num
// reinício. `marca` devolve o último `seq` da fila de pedidos de plano; só é chamada se a abertura
// tiver de ser escrita.
func (l *livroDoCanary) abrir(ctx context.Context, c candidatoEmVigor, marca func(context.Context) (uint64, error)) error {
	if c.alias == "" || c.profileDigest == "" || c.entryDigest == "" || c.fatia <= 0 || c.fatia > 100 {
		return fmt.Errorf("%w: candidato sem alias, digests ou fatia valida", ErrCanaryInconsistent)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for tentativa := 0; tentativa < canaryMaxTentativas; tentativa++ {
		st, err := l.ler(ctx)
		if err != nil {
			return err
		}
		ab, tem, err := st.aberturaDe(c)
		if err != nil {
			return err
		}
		l.futurosVistos.Store(int64(st.futuros))
		if tem {
			n := st.porAbertura[c.entryDigest]
			l.publicar(n, st.fechadas[c.entryDigest] || n >= ab.Limit)
			return nil
		}
		if st.futuros > 0 {
			// O livro tem eventos de uma versão mais recente: esta versão não abre nada nele.
			return nil
		}
		m, err := marca(ctx)
		if err != nil {
			return fmt.Errorf("%w: marca de abertura: %w", ErrCanaryUnreadable, err)
		}
		payload, err := json.Marshal(canaryPayload{
			V: canaryVersaoDoLivro, EntryDigest: c.entryDigest, Alias: c.alias, ProfileDigest: c.profileDigest,
			SharePercent: c.fatia, PlanRequestMark: m, Limit: canaryLimiteDePlanos,
		})
		if err != nil {
			return err
		}
		res, err := l.es.Append(ctx, canaryStream, eventstore.EventInput{
			Type: EventTypeModelCanaryOpened, Payload: payload,
			RunID: canaryRunID, StepID: "opened-" + c.entryDigest,
			Producer: eventstore.Producer{NHIID: canaryNHI},
		}, eventstore.WithExpectedSeq(st.ultimoSeq))
		if conflitoDeCAS(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: gravar a abertura: %w", ErrCanaryUnreadable, err)
		}
		if res.Status == eventstore.StatusDuplicate {
			continue // outra réplica abriu primeiro: relê e confere
		}
		l.publicar(0, false)
		l.logf("canary do modelo candidato ABERTO (AOS-520): alias %q, digest do perfil %s, digest do registo %s, fatia %d%%, limite %d planos, marca de abertura %d (os planos pedidos ate aqui sao do titular)",
			c.alias, c.profileDigest, c.entryDigest, c.fatia, canaryLimiteDePlanos, m)
		return nil
	}
	return ErrCanaryContention
}

// decidir diz quem serve um run NOVO do plano de chave `chave`, com o candidato `c` a servir.
// `seqDoPedido` é o `seq`, na fila de pedidos de plano, do pedido de que o run é trabalho; zero num
// run sem pedido de plano.
//
// A ordem das regras é a do ADR-041 §2.6: o plano já atribuído; o plano anterior à abertura; o
// canary fechado; e só então a função. «Candidato» só é devolvido depois de a atribuição ficar
// gravada com o `seq` que se leu.
func (l *livroDoCanary) decidir(ctx context.Context, c candidatoEmVigor, chave chaveCanary, seqDoPedido uint64) (decisaoDoCanary, error) {
	if chave.id == "" || (chave.tipo != tipoDePedido && chave.tipo != tipoDeRun) {
		return decisaoDoCanary{}, fmt.Errorf("%w: run sem chave de plano", ErrCanaryUnreadable)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for tentativa := 0; tentativa < canaryMaxTentativas; tentativa++ {
		st, err := l.ler(ctx)
		if err != nil {
			return decisaoDoCanary{}, err
		}
		ab, tem, err := st.aberturaDe(c)
		if err != nil {
			return decisaoDoCanary{}, err
		}
		l.futurosVistos.Store(int64(st.futuros))
		// (0) O LIVRO TEM EVENTOS DE UMA VERSÃO MAIS RECENTE DO NÓ. O que lá está pode mudar a
		// decisão, e esta versão não o sabe ler: NÃO atribui nenhum plano novo ao candidato — o
		// run é do titular, com a razão própria — e NÃO serve um plano já atribuído ao candidato,
		// que também não passa para o titular.
		if st.futuros > 0 {
			if _, ja := st.atribuidos[chave.indice()]; ja {
				return decisaoDoCanary{}, ErrCanaryNewerVersion
			}
			return decisaoDoCanary{papel: canary.RoleIncumbent, razao: razaoLivroMaisRecente}, nil
		}
		if !tem {
			return decisaoDoCanary{}, fmt.Errorf("%w: sem abertura para o registo em vigor", ErrCanaryUnreadable)
		}
		atribuidosNaAbertura := st.porAbertura[c.entryDigest]
		fechado := st.fechadas[c.entryDigest] || atribuidosNaAbertura >= ab.Limit
		l.publicar(atribuidosNaAbertura, fechado)

		// (1) O plano já foi atribuído ao candidato — nesta ou noutra abertura.
		if a, ja := st.atribuidos[chave.indice()]; ja {
			if a.Alias != c.alias || a.ProfileDigest != c.profileDigest {
				return decisaoDoCanary{}, ErrPlanAssignedToAbsentCandidate
			}
			return decisaoDoCanary{papel: canary.RoleCandidate, razao: razaoPlanoJaAtribuido}, nil
		}
		// (2) O pedido de plano é anterior à abertura: os nós que já correu foram do titular.
		if seqDoPedido != 0 && seqDoPedido <= ab.PlanRequestMark {
			return decisaoDoCanary{papel: canary.RoleIncumbent, razao: razaoPlanoAnteriorAAbertura}, nil
		}
		// (3) O canary fechou. Se a marca de fecho falta (o nó que escreveu a última atribuição
		// morreu antes de a gravar), grava-se agora.
		if fechado {
			if !st.fechadas[c.entryDigest] {
				l.fechar(ctx, c, atribuidosNaAbertura, ab.Limit)
			}
			return decisaoDoCanary{papel: canary.RoleIncumbent, razao: razaoCanaryFechado}, nil
		}
		// (4) A função decide, com a fatia GRAVADA na abertura.
		if canary.Choose(chave.id, ab.SharePercent) != canary.RoleCandidate {
			return decisaoDoCanary{papel: canary.RoleIncumbent, razao: razaoFuncao}, nil
		}
		payload, err := json.Marshal(canaryPayload{
			V: canaryVersaoDoLivro, EntryDigest: c.entryDigest, Alias: c.alias, ProfileDigest: c.profileDigest, PlanKey: chave.id, PlanKeyKind: chave.tipo,
		})
		if err != nil {
			return decisaoDoCanary{}, err
		}
		res, err := l.es.Append(ctx, canaryStream, eventstore.EventInput{
			Type: EventTypeModelCanaryPlanAssigned, Payload: payload,
			RunID: canaryRunID, StepID: passoDoPlano(chave),
			Producer: eventstore.Producer{NHIID: canaryNHI},
		}, eventstore.WithExpectedSeq(st.ultimoSeq))
		if conflitoDeCAS(err) {
			continue // outra escrita entrou: a contagem pode ter mudado — relê e decide de novo
		}
		if err != nil {
			return decisaoDoCanary{}, fmt.Errorf("%w: gravar a atribuicao: %w", ErrCanaryUnreadable, err)
		}
		if res.Status == eventstore.StatusDuplicate {
			continue // o plano foi atribuído por outra escrita: a regra (1) decide na releitura
		}
		atribuidosNaAbertura++
		l.publicar(atribuidosNaAbertura, atribuidosNaAbertura >= ab.Limit)
		if atribuidosNaAbertura >= ab.Limit {
			l.fechar(ctx, c, atribuidosNaAbertura, ab.Limit)
		}
		return decisaoDoCanary{papel: canary.RoleCandidate, razao: razaoFuncao}, nil
	}
	return decisaoDoCanary{}, ErrCanaryContention
}

// fechar grava o fim do canary. A chave de idempotência é a do registo: só uma escrita fica, e é
// quem a fez que sela o facto no audit de governação e o diz no log. Uma falha aqui NÃO desfaz
// nada nem reabre o canary: o fecho decorre da contagem, que já está no livro; a marca é o sinal.
func (l *livroDoCanary) fechar(ctx context.Context, c candidatoEmVigor, atribuidos, limite int) {
	payload, err := json.Marshal(canaryPayload{
		V: canaryVersaoDoLivro, EntryDigest: c.entryDigest, Alias: c.alias, ProfileDigest: c.profileDigest,
		Assigned: atribuidos, Limit: limite,
	})
	if err != nil {
		l.logf("canary do modelo candidato (AOS-520): o fecho NAO foi gravado: %v — o candidato ja nao recebe planos novos (a contagem decide); a marca sera gravada na proxima decisao", err)
		return
	}
	res, err := l.es.Append(ctx, canaryStream, eventstore.EventInput{
		Type: EventTypeModelCanaryClosed, Payload: payload,
		RunID: canaryRunID, StepID: "closed-" + c.entryDigest,
		Producer: eventstore.Producer{NHIID: canaryNHI},
	})
	if err != nil {
		l.logf("canary do modelo candidato (AOS-520): o fecho NAO foi gravado: %v — o candidato ja nao recebe planos novos (a contagem decide); a marca sera gravada na proxima decisao", err)
		return
	}
	if res.Status == eventstore.StatusDuplicate {
		return // outra réplica já o gravou, selou e disse
	}
	l.logf("CANARY DO MODELO CANDIDATO TERMINADO (AOS-520): o candidato %q (digest do perfil %s, digest do registo %s) serviu %d planos, o limite de %d. Deixa de receber planos novos; os planos ja atribuidos acabam com ele. O candidato NAO passa a titular: decida com os numeros. Reabrir a fatia e assinar um registo novo",
		c.alias, c.profileDigest, c.entryDigest, atribuidos, limite)
	if l.selar == nil {
		return
	}
	if serr := l.selar(ctx, fimDoCanary{alias: c.alias, profileDigest: c.profileDigest, entryDigest: c.entryDigest, atribuidos: atribuidos, limite: limite}); serr != nil {
		l.logf("canary do modelo candidato (AOS-520): o SELO do fim no audit de governacao do gateway nao foi gravado: %v — o fim esta no livro (%s), falta a prova no audit", serr, canaryStream)
	}
}

// planosAtribuidos devolve, por chave do plano, os planos que o livro diz atribuídos ao candidato.
// É o que um nó SEM candidato a servir lê no arranque, para recusar os runs novos desses planos.
func (l *livroDoCanary) planosAtribuidos(ctx context.Context) (map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st, err := l.ler(ctx)
	if err != nil {
		return nil, err
	}
	l.futurosVistos.Store(int64(st.futuros))
	out := make(map[string]bool, len(st.atribuidos))
	for chave := range st.atribuidos {
		out[chave] = true
	}
	return out, nil
}

// marcaDaFilaDePedidos devolve o último `seq` da fila de pedidos de plano — a marca de abertura do
// canary. Uma fila que não existe tem a marca zero: nenhum plano é anterior à abertura.
func marcaDaFilaDePedidos(es eventstore.EventStore) func(context.Context) (uint64, error) {
	return func(ctx context.Context) (uint64, error) {
		evs, err := es.Read(ctx, planRequestStream, 1)
		if errors.Is(err, eventstore.ErrStreamNotFound) {
			return 0, nil
		}
		if err != nil {
			return 0, err
		}
		var ultimo uint64
		for _, ev := range evs {
			if ev.Seq > ultimo {
				ultimo = ev.Seq
			}
		}
		return ultimo, nil
	}
}
