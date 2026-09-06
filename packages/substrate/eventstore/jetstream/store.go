package jetstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/eventstore/natsjs"
)

// Padrões da implantação de referência. O nome do stream e o prefixo dos subjects são
// configuráveis porque um cluster serve mais do que um board.
const (
	NomeStreamPorOmissao = "AOS_EVENTS"
	// PrefixoSubjectOmissao e a RAIZ dos subjects. O prefixo efectivo deriva dela e do
	// nome do stream (ver prefixoDe) para que dois streams no mesmo cluster nao se
	// sobreponham.
	PrefixoSubjectOmissao = "aos.es"
	PrazoPorOmissao       = 10 * time.Second
	ReplicasPorOmissao    = 3
	maxRetentativasSemCAS = 8
	janelaDedupDoServidor = 2 * time.Minute
)

// Store implementa [eventstore.EventStore] sobre JetStream.
type Store struct {
	cn      *natsjs.Conn
	stream  string
	prefixo string
	regiao  string
	board   string
	prazo   time.Duration
	obs     eventstore.Observer
	rastro  eventstore.Rastreador
	now     func() time.Time

	mu      sync.Mutex
	streams map[string]*estado
	subs    map[string]*subscricao
	proxSub uint64
	fechado bool
	usado   bool // fecha a janela de LigarRastreador
}

// estado é a vista LOCAL de um stream do AOS. É uma cache, não a verdade: a verdade
// está no servidor, e qualquer divergência é detectada pelo CAS — que é precisamente o
// ponto. Uma cache obsoleta faz a escrita ser RECUSADA, nunca aceite por engano.
type estado struct {
	mu        sync.Mutex
	hidratado bool
	aosSeq    uint64 // último Event.Seq committed (gapless desde 1)
	jsSeq     uint64 // seq JetStream da última mensagem do subject (token de CAS)
	// dedup guarda o EVENTO, nao o seq: o caminho do duplicado responde sem ir ao
	// servidor, e a hidratacao ja o tinha em maos.
	dedup map[string]eventstore.Event
}

type config struct {
	stream    string
	prefixo   string
	prazo     time.Duration
	replicas  int
	criar     bool
	regiao    string
	board     string
	fronteira bool
	obs       eventstore.Observer
	rastro    eventstore.Rastreador
	now       func() time.Time
}

// Option configura o Store.
type Option func(*config)

// ComNomeDeStream fixa o nome do stream JetStream.
func ComNomeDeStream(n string) Option { return func(c *config) { c.stream = n } }

// ComPrefixoDeSubject fixa o prefixo dos subjects (um por stream do AOS).
func ComPrefixoDeSubject(p string) Option { return func(c *config) { c.prefixo = p } }

// ComPrazo fixa o prazo por operação usado quando o contexto não traz deadline.
func ComPrazo(d time.Duration) Option { return func(c *config) { c.prazo = d } }

// ComReplicas fixa o factor de replicação do stream (3 ou 5; 1 é só dev).
func ComReplicas(n int) Option { return func(c *config) { c.replicas = n } }

// SemCriarStream assume que o stream já existe e não tenta criá-lo.
func SemCriarStream() Option { return func(c *config) { c.criar = false } }

// Abrir liga-se ao cluster em addr ("host:porta") e devolve o Event Store.
//
// Por omissão CRIA o stream com a configuração que o AOS-100 exige — R3, storage de
// ficheiro, e `deny_delete`/`deny_purge`, que é o append-only imposto pelo SERVIDOR e
// não por convenção do cliente. Se o stream já existir com configuração DIFERENTE, o
// servidor recusa e Abrir falha: mudar num_replicas ou deny_delete por baixo de um log
// vivo é uma migração, não um efeito secundário de arrancar um nó.
func Abrir(addr string, opts ...Option) (*Store, error) {
	cfg := config{
		stream:   NomeStreamPorOmissao,
		prazo:    PrazoPorOmissao,
		replicas: ReplicasPorOmissao,
		criar:    true,
		now:      time.Now,
		obs:      observadorNulo{},
		rastro:   eventstore.NopRastreador{},
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.prefixo == "" {
		cfg.prefixo = prefixoDe(cfg.stream)
	}
	if err := validarPrefixo(cfg.prefixo); err != nil {
		return nil, err
	}
	// Fronteira sem região é recusada ANTES de haver ligação: uma configuração
	// auto-contraditória não merece um socket aberto para descobrir isso.
	if err := cfg.validarFronteira(); err != nil {
		return nil, err
	}
	regiao := normalizarRegiao(cfg.regiao)

	// addr aceita VÁRIOS endereços separados por vírgula, e a razão é a que a medição de
	// 2026-09-01 tornou concreta: com um só, a morte desse nó deixa o cliente a tentar
	// sempre o mesmo. O AC1 diz que a perda de uma réplica não interrompe escritas — com
	// um endereço só, isso é verdade apenas se o nó morto não for o nosso.
	cn, err := natsjs.ConnectServers(enderecos(addr), cfg.prazo)
	if err != nil {
		return nil, err
	}
	if cfg.criar {
		if err := cn.CreateStream(natsjs.StreamConfig{
			Name:        cfg.stream,
			Subjects:    []string{cfg.prefixo + ".>"},
			NumReplicas: cfg.replicas,
			Storage:     "file",
			DenyDelete:  true,
			DenyPurge:   true,
			Duplicates:  int64(janelaDedupDoServidor),
			Placement:   cfg.colocacaoExigida(),
		}, cfg.prazo); err != nil {
			_ = cn.Close()
			// «Nenhum par satisfaz a colocação» é o servidor a RECUSAR a fronteira, não
			// um erro qualquer de criação — e o chamador tem de os distinguir. Só este
			// código é traduzido: mapear todas as falhas de criação para
			// E_SOVEREIGNTY_VIOLATION esconderia um nome-em-uso atrás de um problema de
			// soberania que não existe.
			//
			// E SÓ COM FRONTEIRA DECLARADA. O DEFEITO QUE ISTO FECHA, medido a
			// 2026-09-01: o mesmo código 10005 é devolvido por «peer offline» — um
			// stream R3 não é criável com um nó em baixo, HAJA OU NÃO colocação pedida.
			// Sem esta condição, um cluster degradado dava «violação de soberania» a
			// quem nunca declarou fronteira nenhuma (com a região a aparecer VAZIA na
			// mensagem), mandando o operador arranjar uma política em vez de um nó.
			var js *natsjs.JSError
			if cfg.fronteira && errors.As(err, &js) && js.ErrCode == natsjs.CodeNoSuitablePeers {
				return nil, fmt.Errorf("%w: nenhum servidor do cluster anuncia %q — as réplicas não podem ser colocadas dentro da fronteira do board (%v)",
					eventstore.ErrSovereigntyViolation, tagDaRegiao(regiao), err)
			}
			return nil, fmt.Errorf("jetstream: criar stream %q: %w", cfg.stream, err)
		}
	}
	// SOBERANIA (AC5, ADR-011): a fronteira é verificada contra a configuração
	// ARMAZENADA, não contra a que pedimos. Ver soberania.go para os três modos de
	// falha que isto cobre — o pior deles é ligar-se a um stream pré-existente SEM
	// colocação e julgar-se soberano.
	//
	// # Porque SÓ a colocação é lida de volta, e não NumReplicas/DenyDelete/DenyPurge
	//
	// [natsjs.StreamConfigLida] traz os quatro campos, e só `Placement` é verificado
	// aqui. A assimetria é DELIBERADA, e a razão é o tipo de prova que cada propriedade
	// admite.
	//
	// O append-only (`deny_delete`/`deny_purge`) tem prova COMPORTAMENTAL: um apagar e
	// um purgar contra o stream real são RECUSADOS pelo servidor, e a recusa é medida
	// pelos códigos `10057` e `10110` (EPIC-10:190). Uma configuração que dissesse
	// «append-only» e não o fosse seria apanhada por essa medição — logo, lê-la de volta
	// no arranque não acrescentaria garantia nenhuma; acrescentaria um round-trip e a
	// ILUSÃO de que a leitura é que garante a imutabilidade. O mesmo vale para
	// `num_replicas`: uma réplica a menos aparece na escrita, não na configuração.
	//
	// A soberania não tem esse recurso. A colocação só se manifesta em ONDE os bytes
	// ficam, e nenhuma operação do cliente a exercita: um stream sem `placement` aceita
	// escritas exactamente como um com ele. A prova é DECLARATIVA — a configuração
	// armazenada é a única testemunha — e é por isso que só esta precisa de ser lida de
	// volta. Não é esquecimento: é a única das quatro que ninguém mais pode verificar.
	if cfg.fronteira {
		lida, err := cn.ConfigDoStream(cfg.stream, cfg.prazo)
		if err != nil {
			_ = cn.Close()
			return nil, fmt.Errorf("jetstream: ler a configuração de %q para verificar a fronteira de soberania: %w", cfg.stream, err)
		}
		if err := verificarColocacao(lida, regiao, cfg.stream); err != nil {
			_ = cn.Close()
			return nil, err
		}
	}
	return &Store{
		cn:      cn,
		stream:  cfg.stream,
		prefixo: cfg.prefixo,
		prazo:   cfg.prazo,
		now:     cfg.now,
		regiao:  regiao,
		board:   cfg.board,
		obs:     cfg.obs,
		rastro:  cfg.rastro,
		streams: map[string]*estado{},
		subs:    map[string]*subscricao{},
	}, nil
}

// --- Append ----------------------------------------------------------------

// Append escreve um evento no fim do stream.
//
// # A ordem de verificação é a do contrato C2, e não é arbitrária
//
// Idempotência PRIMEIRO (o duplicado ganha, e ganha mesmo que o expected_seq já não
// bata — um retry de um passo já aplicado não é um conflito), depois expected_seq,
// depois a escrita. É a mesma ordem do modelo de referência.
//
// # O que acontece quando o CAS é recusado
//
// Depende de quem pediu o quê. Se o chamador afirmou um expected_seq, a recusa é DELE e
// é devolvida — [eventstore.ErrAppendOnlyViolation] se afirmou o passado,
// [eventstore.ErrSeqConflict] se afirmou à frente do log. Se NÃO afirmou nada, pediu
// apenas «escreve no fim»: a recusa é ruído da nossa cache, re-hidrata-se e tenta-se de
// novo. Devolver-lhe um conflito que ele não pediu seria transformar a concorrência
// entre streams num erro do chamador.
// indisponibilidadeTransitoria traduz o sentinela do substrato replicado para o sentinela
// CANÓNICO de indisponibilidade momentânea do Event Store, preservando a causa na cadeia.
//
// # AOS-354 — A TOLERÂNCIA QUE O BANNER PROMETE NUNCA ERA ARMADA
//
// [eventstore.ErrNoQuorum] era produzido APENAS pelo store de referência. Em `jetstream/` e
// `natsjs/` havia ZERO ocorrências: o erro canónico de indisponibilidade transitória deste
// substrato é [natsjs.ErrDesligado]. Dois consumidores ramificam no sentinela, e a
// consequência era desigual:
//
//   - `cmd/aos/trajectory.go` — a perda era cosmética: HTTP 500 em vez de 503;
//   - `cmd/aos/progress_wiring.go` — não era. `burndownTransitorio` é a lista FECHADA que
//     decide se uma leitura falhada do burn-down é indisponibilidade momentânea ou CEGUEIRA.
//     Sobre JetStream, um `ErrDesligado` caía em «cegueira» e MATAVA O RUN À PRIMEIRA, em vez
//     de tolerar N fronteiras consecutivas — que é o que `posture_banner.go` promete por
//     escrito. Sobre o substrato que AOS-100 tornou preferencial, essa promessa não tinha
//     como ser cumprida.
//
// # PORQUÊ TRADUZIR AQUI, E NÃO ALARGAR A LISTA DO CONSUMIDOR
//
// A alternativa era `burndownTransitorio` passar a reconhecer também [natsjs.ErrDesligado].
// Funcionaria, e repetia o defeito: o sentinela é o CONTRATO da porta [EventStorePort], e um
// contrato que só uma das implementações consegue produzir não é contrato — é um detalhe da
// implementação de referência a vazar para o plano de controlo. Cada consumidor futuro teria
// de saber a lista de sentinelas de cada backend, e o primeiro que se esquecesse de um
// reintroduzia este mesmo bug. Traduzir na fronteira do backend fecha-o de uma vez, e para
// todos os consumidores.
//
// A causa NÃO se perde: o erro devolvido embrulha os dois, pelo que `errors.Is` responde
// `true` ao canónico E ao específico, e a mensagem que o operador lê nomeia a desligação.
//
// RESIDUAL DECLARADO: traduz-se o que embrulha [natsjs.ErrDesligado] e mais nada. Um
// TIMEOUT de request durante a janela de reconexão — o socket ainda de pé, o servidor a
// não responder — não traz esse sentinela e continua a cair no ramo de «cegueira» de
// `burndownTransitorio`. Fechá-lo exigiria decidir que um timeout é transitório, o que é
// verdade quase sempre e falso exactamente quando importa (um servidor que aceita a
// ligação e nunca responde). Fica por decidir, não por esquecer.
func indisponibilidadeTransitoria(err error) error {
	if err == nil || !errors.Is(err, natsjs.ErrDesligado) {
		return err
	}
	if errors.Is(err, eventstore.ErrNoQuorum) {
		return err // já traduzido por uma camada de baixo; não voltar a embrulhar
	}
	return fmt.Errorf("%w: %w", eventstore.ErrNoQuorum, err)
}

func (s *Store) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (_ eventstore.AppendResult, err error) {
	// AOS-354: a tradução cobre TODOS os caminhos de saída, e é por isso que é um defer.
	defer func() { err = indisponibilidadeTransitoria(err) }()
	// O span abre com o `ctx` do CHAMADOR, e é isso que o põe por baixo do span do passo
	// que causou a escrita em vez de o deixar órfão (EPIC-08 / AOS-077).
	s.marcarUsado()
	ctx, span := s.rastro.Iniciar(ctx, eventstore.OperacaoAppend)
	defer span.Fim()
	span.Atributo(eventstore.AtributoStream, streamID)
	if err := ctx.Err(); err != nil {
		registarRecusa(span, err)
		return eventstore.AppendResult{}, err
	}
	if s.estaFechado() {
		return eventstore.AppendResult{}, eventstore.ErrClosed
	}
	subject, err := s.subjectDe(streamID)
	if err != nil {
		return eventstore.AppendResult{}, err
	}
	esperado, temEsperado := eventstore.ExpectedSeqOf(opts)
	prazo := s.prazoDe(ctx)
	inicio := s.now()

	st := s.estadoDe(streamID)
	st.mu.Lock()
	defer st.mu.Unlock()

	for tentativa := 0; ; tentativa++ {
		if err := s.hidratar(ctx, st, subject, prazo); err != nil {
			return eventstore.AppendResult{}, err
		}

		// 1) Idempotência.
		if eventstore.HasIdempotency(in.RunID, in.StepID) {
			chave := eventstore.IdempotencyKey(in.RunID, in.StepID)
			if ev, ok := st.dedup[chave]; ok {
				// O evento vem do indice derivado, sem ir ao servidor: a hidratacao
				// ja o tinha em maos, e um round-trip para o repetir seria trabalho
				// a mais no caminho mais quente que existe (o retry de um passo).
				s.obs.AppendDuplicate(streamID, ev.Seq)
				span.Atributo(eventstore.AtributoDesfecho, "duplicate")
				span.Atributo(eventstore.AtributoSeq, ev.Seq)
				return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusDuplicate, Event: ev.Clone()}, nil
			}
		}

		// 2) Concorrência optimista / append-only, contra a nossa vista do stream.
		if temEsperado {
			switch {
			case esperado == st.aosSeq: // ok
			case esperado < st.aosSeq:
				s.obs.AppendRejected(streamID, eventstore.ErrAppendOnlyViolation)
				registarRecusa(span, eventstore.ErrAppendOnlyViolation)
				return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
			default:
				s.obs.AppendRejected(streamID, eventstore.ErrSeqConflict)
				registarRecusa(span, eventstore.ErrSeqConflict)
				return eventstore.AppendResult{}, eventstore.ErrSeqConflict
			}
		}

		// 3) Envelope e escrita com CAS sobre o token do servidor.
		ev := eventstore.NewEvent(streamID, st.aosSeq+1, in, s.now())
		corpo, err := json.Marshal(ev)
		if err != nil {
			return eventstore.AppendResult{}, fmt.Errorf("jetstream: serializar envelope: %w", err)
		}
		h := natsjs.Header{}
		if eventstore.HasIdempotency(in.RunID, in.StepID) {
			// Rede de segurança para retries imediatos; a garantia é o índice derivado.
			h[natsjs.HdrMsgID] = streamID + "|" + eventstore.IdempotencyKey(in.RunID, in.StepID)
		}
		ack, err := s.cn.PublishExpectingSeq(subject, st.jsSeq, h, corpo, prazo)

		switch {
		case err == nil && ack.Duplicate:
			// O servidor deduplicou dentro da JANELA, e a nossa vista não sabia. A
			// nossa vista é que está errada: re-hidrata e responde pelo índice.
			st.hidratado = false
			continue

		case err == nil:
			st.aosSeq, st.jsSeq = ev.Seq, ack.Seq
			if eventstore.HasIdempotency(in.RunID, in.StepID) {
				st.dedup[eventstore.IdempotencyKey(in.RunID, in.StepID)] = ev
			}
			s.obs.AppendCommitted(streamID, ev.Seq, s.now().Sub(inicio))
			span.Atributo(eventstore.AtributoDesfecho, "committed")
			span.Atributo(eventstore.AtributoSeq, ev.Seq)
			return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusCommitted, Event: ev}, nil

		case errors.Is(err, natsjs.ErrWrongLastSeq):
			// NADA ficou durável — o servidor recusou. Outro escritor avançou.
			st.hidratado = false
			if temEsperado {
				if err := s.hidratar(ctx, st, subject, prazo); err != nil {
					return eventstore.AppendResult{}, err
				}
				if esperado < st.aosSeq {
					s.obs.AppendRejected(streamID, eventstore.ErrAppendOnlyViolation)
					registarRecusa(span, eventstore.ErrAppendOnlyViolation)
					return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
				}
				s.obs.AppendRejected(streamID, eventstore.ErrSeqConflict)
				registarRecusa(span, eventstore.ErrSeqConflict)
				return eventstore.AppendResult{}, eventstore.ErrSeqConflict
			}
			if tentativa >= maxRetentativasSemCAS {
				return eventstore.AppendResult{}, fmt.Errorf("jetstream: %d recusas de CAS seguidas em %q: %w",
					tentativa, streamID, eventstore.ErrSeqConflict)
			}
			continue

		case errors.Is(err, natsjs.ErrIndeterminate):
			// NÃO SE SABE se ficou durável. Resolve-se OLHANDO: re-hidrata e procura o
			// nosso EventID, que é único e foi gerado agora.
			aplicado, errV := s.escritaAplicada(ctx, st, subject, ev.EventID, prazo)
			if errV != nil {
				return eventstore.AppendResult{}, fmt.Errorf("jetstream: escrita indeterminada e não verificável: %w (causa: %w)", errV, err)
			}
			if aplicado {
				return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusCommitted, Event: ev}, nil
			}
			return eventstore.AppendResult{}, err

		default:
			s.obs.AppendRejected(streamID, err)
			registarRecusa(span, err)
			return eventstore.AppendResult{}, err
		}
	}
}

// escritaAplicada re-hidrata e diz se o evento com este EventID está no log. É o que
// torna um [natsjs.ErrIndeterminate] resolúvel em vez de fatal.
func (s *Store) escritaAplicada(ctx context.Context, st *estado, subject, eventID string, prazo time.Duration) (bool, error) {
	st.hidratado = false
	evs, jsUltimo, dedup, aosSeq, err := s.lerSubject(ctx, subject, prazo)
	if err != nil {
		return false, err
	}
	st.aplicar(evs, jsUltimo, dedup, aosSeq)
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].EventID == eventID {
			return true, nil
		}
	}
	return false, nil
}

// --- Read ------------------------------------------------------------------

// Read devolve os eventos committed do stream com seq >= fromSeq.
//
// Lê SEMPRE do servidor, nunca de uma cache local: é a diferença entre um Event Store
// partilhado e N cópias in-process, e é o defeito que o DEF-282 mediu.
func (s *Store) Read(ctx context.Context, streamID string, fromSeq uint64) (_ []eventstore.Event, err error) {
	defer func() { err = indisponibilidadeTransitoria(err) }()
	s.marcarUsado()
	ctx, span := s.rastro.Iniciar(ctx, eventstore.OperacaoRead)
	defer span.Fim()
	span.Atributo(eventstore.AtributoStream, streamID)
	if err := ctx.Err(); err != nil {
		registarRecusa(span, err)
		return nil, err
	}
	if s.estaFechado() {
		return nil, eventstore.ErrClosed
	}
	subject, err := s.subjectDe(streamID)
	if err != nil {
		return nil, err
	}
	evs, _, _, _, err := s.lerSubject(ctx, subject, s.prazoDe(ctx))
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		registarRecusa(span, eventstore.ErrStreamNotFound)
		return nil, eventstore.ErrStreamNotFound
	}
	out := make([]eventstore.Event, 0, len(evs))
	for _, ev := range evs {
		if ev.Seq >= fromSeq {
			out = append(out, ev.Clone())
		}
	}
	span.Atributo(eventstore.AtributoDesfecho, "ok")
	span.Atributo(eventstore.AtributoContagem, len(out))
	return out, nil
}

// lerSubject caminha o subject do princípio ao fim e devolve os eventos, o seq
// JetStream do último, o índice de dedup derivado e o último seq do AOS.
// janelaDeLeitura é quantos eventos se trazem por lote.
//
// Não é um número arbitrário: é o tecto da fila de entrega que o lote exige, e essa fila
// tem de comportar o lote INTEIRO. O leitor do cliente DESCARTA quando a fila enche (um
// subscritor lento não pode bloquear o leitor, que serve todas as subscrições) — e um log
// truncado não é um log lento, é um log ERRADO. Ler em janelas mantém a fila limitada sem
// voltar ao pedido-por-evento.
const janelaDeLeitura = 2048

// batimentoDaSubscricao é de quanto em quanto tempo o servidor dá sinal de vida numa
// subscrição ociosa, e silencioMaximo é quanto tempo sem NADA — nem evento nem batimento —
// se tolera antes de dar o consumidor por morto.
//
// Sem isto, um consumidor apagado do lado do servidor é indistinguível de um stream
// sossegado: o subscritor fica à espera para sempre e ninguém dá por isso. Silêncio não é
// paz — é a única falha que este pacote combate desde a primeira linha.
const (
	batimentoDaSubscricao = 5 * time.Second
	silencioMaximo        = 3 * batimentoDaSubscricao
)

// lerSubject lê os eventos do subject EM LOTES e devolve-os, o seq JetStream do último
// (o token de CAS) e o índice de deduplicação derivado.
//
// # O defeito que isto fecha, medido
//
// A versão anterior caminhava o subject com `next_by_subj`, UM PEDIDO POR EVENTO.
// MEDIDO a 2026-09-01, co-localizado com o cluster: **113–120 eventos/s**, contra 3,9–5,4
// MILHÕES/s do WAL local. Um run de 200 eventos pagava ~1,7 s de re-hidratação POR
// ARRANQUE — e a re-hidratação está no caminho de arranque de todos os runs.
//
// Agora um lote é um consumidor efémero que EMPURRA a janela inteira: dois round-trips
// por janela em vez de um por evento.
//
// # Porque a contagem é pedida ao servidor primeiro
//
// Com entrega push e `ack_policy: none` não há nada que diga «acabou» — só mensagens que
// param de chegar, que é indistinguível de uma que se perdeu. Perguntar ao servidor
// quantas há ANTES torna a completude VERIFICÁVEL: ou chegam todas, ou o erro diz que
// faltaram. Devolver um log truncado em silêncio seria o pior desfecho possível, porque o
// replay reconstruiria estado errado sem ninguém dar por isso.
func (s *Store) lerSubject(ctx context.Context, subject string, prazo time.Duration) ([]eventstore.Event, uint64, map[string]eventstore.Event, uint64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, nil, 0, err
	}
	contagens, err := s.cn.SubjectsWithMessages(s.stream, subject, prazo)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	total := contagens[subject]
	dedup := map[string]eventstore.Event{}
	if total == 0 {
		return nil, 0, dedup, 0, nil
	}

	evs, err := lerEmLotes(subject, total, janelaDeLeitura,
		func(inicio, quantos uint64) (loteLido, error) {
			return s.lerLote(ctx, subject, inicio, quantos, prazo)
		})
	if err != nil {
		return nil, 0, nil, 0, err
	}

	var aosSeq uint64
	for _, ev := range evs {
		aosSeq = ev.Seq
		if eventstore.HasIdempotency(ev.RunID, ev.StepID) {
			dedup[ev.IdempotencyKey] = ev
		}
	}

	// O token de CAS vem do servidor, não do lote: é a afirmação sobre a qual a próxima
	// escrita vai assentar, e derivá-la de uma leitura seria assentá-la numa suposição.
	jsUltimo, err := s.cn.UltimoSeqDoSubject(s.stream, subject, prazo)
	if err != nil {
		return nil, 0, nil, 0, err
	}
	return evs, jsUltimo, dedup, aosSeq, nil
}

// lerEmLotes percorre os `total` eventos de um subject em lotes de até `janela` e
// devolve-os por ordem.
//
// `lote(inicio, quantos)` traz até `quantos` eventos a partir do seq FÍSICO `inicio` e
// devolve, além deles, o seq físico do ÚLTIMO que trouxe — que é por onde a janela avança.
//
// # O defeito que isto fecha
//
// A versão anterior avançava a janela com `UltimoSeqDoSubject`, que é o seq da última
// mensagem do SUBJECT INTEIRO e não do lote. Enquanto o log coube numa janela os dois
// valores coincidiram e a diferença foi invisível. Acima de [janelaDeLeitura] eventos num
// só stream_id, o segundo lote arrancava DEPOIS do fim do log, não recebia nada, e a
// leitura morria no prazo — e como `hidratar` precede as escritas, o stream ficava
// ilegível E inescrevível.
//
// A via de alcance não era a carga, era o RELÓGIO: o laço de retenção renova a posse de
// 40 em 40 s e cada renovação é um evento no stream do laço, o que põe 2048 eventos lá
// ao fim de ~22,7 h de uptime — num nó completamente ocioso.
//
// A separação em função própria não é estética: é o que permite exercitar a aritmética da
// janela e o critério de avanço SEM cluster, com um log falso onde o seq do subject é
// deliberadamente maior do que o do lote. Ver janela_test.go.
// loteLido é o que um lote devolve a [lerEmLotes]. `ultimoSeq == 0` significa DESCONHECIDO
// — não «princípio do log» —, e nesse caso `porqueDesconhecido` diz porquê. A distinção
// existe porque a esmagadora maioria das leituras nunca precisa do seq físico (o log cabe
// numa janela), e derrubá-las por causa de um valor que não vão usar seria trocar um
// defeito raro por um comum. Ver o laço de entrega de [Store.lerLote].
type loteLido struct {
	eventos            []eventstore.Event
	ultimoSeq          uint64
	porqueDesconhecido error
}

func lerEmLotes(subject string, total, janela uint64, lote func(inicio, quantos uint64) (loteLido, error)) ([]eventstore.Event, error) {
	evs := make([]eventstore.Event, 0, total)
	var inicio uint64 // 0 = desde o princípio do stream
	for uint64(len(evs)) < total {
		// A janela fica toda em uint64: converter a contagem do servidor para int
		// seria uma conversão que o compilador não pode provar segura (G115), e a
		// resposta certa a isso é não a fazer — não silenciá-la.
		quantos := total - uint64(len(evs))
		if quantos > janela {
			quantos = janela
		}
		lido, err := lote(inicio, quantos)
		if err != nil {
			return nil, err
		}
		trazidos, ultimoDoLote := lido.eventos, lido.ultimoSeq
		if len(trazidos) == 0 {
			return nil, fmt.Errorf(
				"jetstream: lote vazio a meio da leitura de %q (%d de %d eventos lidos) — o log não pode ser servido truncado",
				subject, len(evs), total)
		}
		evs = append(evs, trazidos...)
		if uint64(len(evs)) >= total {
			break
		}

		// Daqui para baixo é o caminho de QUEM AINDA TEM DE AVANÇAR, e só ele. A ordem
		// não é cosmética: um log que cabe numa janela nunca chega aqui, e é por isso
		// que uma falha em derivar o seq físico não o pode derrubar.
		//
		// `ultimoDoLote == 0` é «não sei onde este lote acabou» — [Store.lerLote]
		// devolve-o quando a entrega não trouxe o `$JS.ACK…` de onde o seq físico sai.
		// Adivinhar aqui seria escolher entre reler o mesmo prefixo para sempre (0) ou
		// saltar o resto do log (o fim do subject, que é exactamente o defeito que esta
		// função fecha). Não se adivinha: diz-se.
		if ultimoDoLote == 0 {
			return nil, fmt.Errorf(
				"jetstream: a leitura de %q não conseguiu o seq físico do lote e faltam eventos (%d de %d) — "+
					"a janela não avança às cegas, e um log servido truncado seria pior do que este erro. Causa: %w",
				subject, len(evs), total, lido.porqueDesconhecido)
		}
		// Um lote que não faz a janela avançar é um laço infinito à espera de acontecer.
		// Só pode vir de um seq físico mal derivado, e a resposta é falhar em vez de
		// girar para sempre a reler o mesmo prefixo.
		if ultimoDoLote < inicio {
			return nil, fmt.Errorf(
				"jetstream: o lote de %q devolveu o seq físico %d, que não avança a janela iniciada em %d",
				subject, ultimoDoLote, inicio)
		}
		inicio = ultimoDoLote + 1
	}
	return evs, nil
}

// lerLote traz até `quantos` eventos do subject a partir do seq físico `inicio`, por
// entrega push num consumidor efémero. Devolve também o seq FÍSICO da última mensagem
// que trouxe — o valor por onde [lerEmLotes] avança a janela.
func (s *Store) lerLote(ctx context.Context, subject string, inicio, quantos uint64, prazo time.Duration) (loteLido, error) {
	entrega, err := natsjs.NewInbox()
	if err != nil {
		return loteLido{}, err
	}
	// A fila comporta o lote INTEIRO mais folga: ver [janelaDeLeitura].
	// A fila é dimensionada pela JANELA (constante), não por `quantos`: o tecto é o
	// mesmo e não há conversão de um valor vindo do servidor.
	ch, cancelar, err := s.cn.SubscribeSubjectBuffered(entrega, janelaDeLeitura+16)
	if err != nil {
		return loteLido{}, err
	}
	defer cancelar()

	cfg := natsjs.ConsumerConfig{
		DeliverSubject: entrega,
		DeliverPolicy:  "all",
		AckPolicy:      "none",
		ReplayPolicy:   "instant",
		FilterSubject:  subject,
		// R1 em memoria: ler nao precisa de replicacao, e heranca das 3 replicas do
		// stream tornava a LEITURA indisponivel com um no em baixo — medido.
		NumReplicas: 1,
		MemStorage:  true,
	}
	if inicio > 0 {
		cfg.DeliverPolicy, cfg.OptStartSeq = "by_start_sequence", inicio
	}
	if err := s.cn.CreateEphemeralConsumer(s.stream, cfg, prazo); err != nil {
		return loteLido{}, err
	}

	evs := make([]eventstore.Event, 0, janelaDeLeitura)
	var ultimoJS uint64
	var semSeqFisico bool
	var motivoSemSeq error
	temporizador := time.NewTimer(prazo)
	defer temporizador.Stop()
	for uint64(len(evs)) < quantos {
		select {
		case m, ok := <-ch:
			if !ok {
				return loteLido{}, fmt.Errorf("jetstream: a subscrição do lote de %q fechou com %d de %d eventos", subject, len(evs), quantos)
			}
			var ev eventstore.Event
			if err := json.Unmarshal(m.Data, &ev); err != nil {
				return loteLido{}, fmt.Errorf("jetstream: envelope ilegível na leitura de %q: %w", subject, err)
			}
			// O seq FÍSICO desta mensagem vem do subject de resposta que o servidor lhe
			// põe (`$JS.ACK…`). É a única fonte que fala da MENSAGEM entregue: o
			// `Event.Seq` é o seq do AOS (gapless por stream, alheio ao log físico) e o
			// último seq do subject é do LOG, não do lote.
			//
			// # Porque a AUSÊNCIA de resposta não derruba o lote, e a MALFORMAÇÃO sim
			//
			// São duas coisas diferentes. Um `$JS.ACK…` que não se consegue ler é uma
			// violação de protocolo — o servidor disse algo que este cliente não
			// entende — e fica fail-closed no sítio, como todo o resto deste pacote.
			//
			// Um subject de resposta AUSENTE é outra hipótese: um servidor ou uma
			// política de entrega que simplesmente não o põe. Não é impossível, e não
			// foi medido contra um cluster (ver AOS-345). Derrubar o lote aqui trocaria
			// um defeito que só aparece acima de [janelaDeLeitura] eventos por outro que
			// apareceria em TODAS as leituras — pior, e por causa de um valor que a
			// esmagadora maioria delas nunca usa. Marca-se «não sei» com 0 e deixa-se
			// [lerEmLotes] decidir: se o log cabe nesta janela, o seq nunca é preciso;
			// se não cabe, aí sim a leitura falha, e falha a dizer exactamente isto.
			// AUSENTE e MALFORMADO caem no MESMO lado, e a revisão adversarial de
			// AOS-345 mostrou porquê. A versão anterior derrubava o lote na primeira
			// mensagem cujo `$JS.ACK` não tivesse 9 ou 12 tokens — dentro do laço de
			// entrega, por mensagem, incondicionalmente. O argumento de proporcionalidade
			// que justifica tolerar a AUSÊNCIA aplica-se palavra por palavra à
			// MALFORMAÇÃO: nos dois casos o que se tem é «não sei qual é o seq físico»,
			// e nos dois casos a esmagadora maioria das leituras nunca precisa dele.
			//
			// Com a versão anterior, uma forma de reply inesperada — uma versão de
			// servidor com um token a mais, um leafnode ou gateway que reescreva o
			// subject — tornava ILEGÍVEL um stream de três eventos que hoje lê
			// perfeitamente. E como `hidratar` precede as escritas, ficava também
			// INESCREVÍVEL. Estritamente pior do que o defeito que AOS-345 fecha, que só
			// se manifestava acima de [janelaDeLeitura] eventos.
			//
			// A causa não se perde: viaja até [lerEmLotes], que só falha quando precisa
			// mesmo de avançar — e aí nomeia-a.
			if seq, err := seqDoStreamNaResposta(m.Reply); err != nil {
				if !semSeqFisico {
					semSeqFisico, motivoSemSeq = true, err
				}
			} else {
				ultimoJS = seq
			}
			evs = append(evs, ev)
		case <-temporizador.C:
			return loteLido{}, fmt.Errorf("jetstream: leitura de %q parou em %d de %d eventos ao fim de %s — um log servido truncado seria pior do que este erro",
				subject, len(evs), quantos, prazo)
		case <-ctx.Done():
			return loteLido{}, ctx.Err()
		}
	}
	// `ultimoJS` é o seq físico da ÚLTIMA MENSAGEM DESTE LOTE, colhido do `$JS.ACK…` de
	// cada entrega no laço acima — não o do subject.
	//
	// A distinção é a correcção inteira: a entrega push é por ordem de log, logo a
	// última mensagem do lote traz o maior seq do lote, e `ultimoJS+1` é exactamente
	// onde o lote seguinte tem de arrancar. Pedir `UltimoSeqDoSubject` aqui devolveria o
	// fim do LOG, e o lote seguinte arrancaria para lá dele.
	//
	// O token de CAS continua a ser pedido ao servidor, uma vez só, por
	// [Store.lerSubject] — e é outra coisa: é a afirmação sobre a qual a próxima ESCRITA
	// assenta, e essa não pode ser derivada de uma leitura.
	//
	// 0 é «não sei»: se ALGUMA mensagem do lote veio sem subject de resposta, o fim do
	// lote não é conhecido e não se finge que é.
	if semSeqFisico {
		return loteLido{eventos: evs, porqueDesconhecido: motivoSemSeq}, nil
	}
	return loteLido{eventos: evs, ultimoSeq: ultimoJS}, nil
}

// seqDoStreamNaResposta extrai o `stream_seq` do subject de resposta que o JetStream põe
// em cada mensagem entregue.
//
// O formato tem duas versões em uso, e ambas têm de ser aceites: a antiga com 9 tokens
// (`$JS.ACK.<stream>.<consumidor>.<entregas>.<stream_seq>.<consumidor_seq>.<ts>.<pendentes>`)
// e a de domínio com 12 (`$JS.ACK.<domínio>.<hash-de-conta>.<stream>.…<aleatório>`).
// Distinguem-se pela contagem de tokens, que é como o cliente oficial também as separa.
//
// Falha fail-closed: uma resposta que não seja um `$JS.ACK` reconhecível não dá 0 nem um
// palpite. Zero seria «princípio do log», e devolvê-lo faria a janela recomeçar do início
// para sempre; um palpite serviria um log truncado em silêncio, que este ficheiro declara
// desde a primeira linha ser o pior desfecho possível.
func seqDoStreamNaResposta(reply string) (uint64, error) {
	if reply == "" {
		return 0, fmt.Errorf("%w: mensagem entregue sem subject de resposta — sem ele não há seq físico por onde avançar a janela",
			natsjs.ErrProtocol)
	}
	t := strings.Split(reply, ".")
	var bruto string
	switch len(t) {
	case 9: // sem domínio
		bruto = t[5]
	case 12: // com domínio e hash de conta
		bruto = t[7]
	default:
		return 0, fmt.Errorf("%w: subject de resposta %q tem %d tokens — não é um $JS.ACK de 9 nem de 12",
			natsjs.ErrProtocol, reply, len(t))
	}
	if t[0] != "$JS" || t[1] != "ACK" {
		return 0, fmt.Errorf("%w: subject de resposta %q não começa por $JS.ACK", natsjs.ErrProtocol, reply)
	}
	seq, err := strconv.ParseUint(bruto, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: stream_seq %q ilegível em %q: %v", natsjs.ErrProtocol, bruto, reply, err)
	}
	if seq == 0 {
		return 0, fmt.Errorf("%w: stream_seq 0 em %q — o log do JetStream começa em 1", natsjs.ErrProtocol, reply)
	}
	return seq, nil
}

func (s *Store) hidratar(ctx context.Context, st *estado, subject string, prazo time.Duration) error {
	if st.hidratado {
		return nil
	}
	evs, jsUltimo, dedup, aosSeq, err := s.lerSubject(ctx, subject, prazo)
	if err != nil {
		return err
	}
	st.aplicar(evs, jsUltimo, dedup, aosSeq)
	return nil
}

func (st *estado) aplicar(_ []eventstore.Event, jsUltimo uint64, dedup map[string]eventstore.Event, aosSeq uint64) {
	st.jsSeq, st.aosSeq, st.dedup, st.hidratado = jsUltimo, aosSeq, dedup, true
}

// --- Subscribe -------------------------------------------------------------

type subscricao struct {
	id      string
	store   *Store
	duravel string // nome do consumidor no servidor; apagado no Unsubscribe
	entrega string // deliver subject ESTAVEL: e o que permite ao duravel retomar
	inicio  uint64 // seq de partida FIXADO na criacao; reafirmar com outro seria recusado
	feito   chan struct{}
	parar   chan struct{}
	uma     sync.Once

	mu       sync.Mutex
	cancelar func() // o da ligação CORRENTE; troca a cada re-estabelecimento
}

func (sub *subscricao) trocarCancelar(f func()) {
	sub.mu.Lock()
	sub.cancelar = f
	sub.mu.Unlock()
}

func (sub *subscricao) cancelarCorrente() {
	sub.mu.Lock()
	f := sub.cancelar
	sub.mu.Unlock()
	if f != nil {
		f()
	}
}

func (sub *subscricao) ID() string { return sub.id }

func (sub *subscricao) Unsubscribe() {
	sub.uma.Do(func() {
		close(sub.parar) // interrompe uma re-tentativa em curso
		sub.cancelarCorrente()
		<-sub.feito
		// O DURÁVEL é nosso para apagar. Um consumidor durável que ninguém apaga fica
		// no servidor para sempre a acumular estado de entrega — o preço de ele
		// sobreviver a uma quebra é alguém ser dono do seu fim.
		if sub.duravel != "" {
			_ = sub.store.cn.DeleteConsumer(sub.store.stream, sub.duravel, sub.store.prazo)
		}
		sub.store.esquecerSub(sub.id)
	})
}

// Subscribe entrega por PUSH os eventos escritos A PARTIR DE AGORA que passem o filtro.
//
// A semântica é a do modelo de referência (fanout do que é escrito depois da
// subscrição), materializada por um consumidor EFÉMERO com deliver_policy "new". Ver os
// limites no doc do pacote: sem acks, sem flow control, sem heartbeats.
func (s *Store) Subscribe(ctx context.Context, filtro eventstore.Filter, h eventstore.Handler) (_ eventstore.Subscription, err error) {
	defer func() { err = indisponibilidadeTransitoria(err) }()
	s.marcarUsado()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, fmt.Errorf("jetstream: handler nil")
	}
	s.mu.Lock()
	if s.fechado {
		s.mu.Unlock()
		return nil, eventstore.ErrClosed
	}
	s.proxSub++
	id := fmt.Sprintf("sub-%d", s.proxSub)
	s.mu.Unlock()

	// O ponto de partida é fixado AGORA: só o que for escrito a seguir é entregue, que é
	// a semântica do modelo de referência.
	estado, err := s.cn.FetchStreamState(s.stream, s.prazoDe(ctx))
	if err != nil {
		return nil, fmt.Errorf("jetstream: ler o estado de %q para fixar o início da subscrição: %w", s.stream, err)
	}
	entrega, err := natsjs.NewInbox()
	if err != nil {
		return nil, err
	}
	sub := &subscricao{
		id: id, store: s, duravel: "aos-" + strings.ReplaceAll(entrega, "_INBOX.", ""),
		entrega: entrega,
		inicio:  estado.LastSeq + 1,
		feito:   make(chan struct{}), parar: make(chan struct{}),
	}
	ch, cancelar, err := s.cn.SubscribeSubject(entrega)
	if err != nil {
		return nil, err
	}
	if err := s.criarDuravel(ctx, sub); err != nil {
		cancelar()
		return nil, err
	}
	sub.cancelar = cancelar

	go func() {
		defer close(sub.feito)
		for {
			silencio := time.NewTimer(silencioMaximo)
		entrega:
			for {
				select {
				case <-sub.parar:
					silencio.Stop()
					return

				case <-silencio.C:
					// Nem um evento, nem um batimento. O consumidor morreu do lado do
					// servidor (o nó R1 que o alojava caiu, alguém apagou-o) e a nossa
					// subscrição continua tecnicamente viva a não receber nada.
					//
					// É a falha SILENCIOSA, e é a razão de haver batimento: sem ele isto
					// seria indistinguível de um stream sossegado, e a subscrição ficava
					// morta para sempre sem ninguém saber.
					sub.cancelarCorrente()
					break entrega

				case m, ok := <-ch:
					if !ok {
						silencio.Stop()
						break entrega
					}
					if !silencio.Stop() {
						select {
						case <-silencio.C:
						default:
						}
					}
					silencio.Reset(silencioMaximo)

					switch m.Controlo() {
					case natsjs.PedidoDeFluxo:
						// TEM de ser respondido: sem resposta a entrega PÁRA, e pára em
						// silêncio. É o que impede um subscritor lento de ser atropelado.
						_ = s.cn.PublishSemResposta(m.Reply, nil)
						continue
					case natsjs.BatimentoOcioso:
						continue // sinal de vida; nada a entregar
					}

					var ev eventstore.Event
					if err := json.Unmarshal(m.Data, &ev); err != nil {
						continue // envelope ilegível: não é do AOS, ou é de outra versão
					}
					if filtro.Matches(ev) {
						h(ev.Clone())
					}
					// O ACK vai DEPOIS do handler, e é isso que o torna útil: confirma o
					// que foi ENTREGUE E PROCESSADO. Confirmar antes tornaria o durável um
					// efémero com passos extra — perder-se-ia na mesma o que estivesse em
					// voo quando a ligação parte.
					if m.Reply != "" {
						_ = s.cn.PublishSemResposta(m.Reply, nil)
					}
				}
			}
			// O canal fechou. Ou foi o dono (Unsubscribe/Close), ou a LIGAÇÃO PARTIU-SE.
			//
			// Se aqui se saísse em silêncio, a subscrição deixava de entregar sem que
			// ninguém soubesse — pior do que falhar, porque quem depende dela nunca
			// desconfia. Re-subscreve-se o MESMO deliver subject: o consumidor é DURÁVEL
			// e retoma no último ACK, pelo que o que foi escrito no intervalo é entregue.
			select {
			case <-sub.parar:
				return
			default:
			}
			if s.estaFechado() {
				return
			}
			novoCh, novoCancelar, err := s.reestabelecerEntrega(sub)
			if err != nil {
				return // o dono mandou parar durante a espera
			}
			ch = novoCh
			sub.trocarCancelar(novoCancelar)
		}
	}()

	s.mu.Lock()
	s.subs[id] = sub
	s.mu.Unlock()
	return sub, nil
}

func (s *Store) esquecerSub(id string) {
	s.mu.Lock()
	delete(s.subs, id)
	s.mu.Unlock()
}

// --- Ciclo de vida ---------------------------------------------------------

// Close termina o store e liberta todas as subscrições.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.fechado {
		s.mu.Unlock()
		return nil
	}
	s.fechado = true
	subs := make([]*subscricao, 0, len(s.subs))
	for _, sub := range s.subs {
		subs = append(subs, sub)
	}
	s.mu.Unlock()

	for _, sub := range subs {
		sub.Unsubscribe()
	}
	return s.cn.Close()
}

// --- Auxiliares ------------------------------------------------------------

func (s *Store) estaFechado() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fechado
}

func (s *Store) estadoDe(streamID string) *estado {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.streams[streamID]
	if st == nil {
		st = &estado{dedup: map[string]eventstore.Event{}}
		s.streams[streamID] = st
	}
	return st
}

func (s *Store) prazoDe(ctx context.Context) time.Duration {
	if prazo, ok := ctx.Deadline(); ok {
		if d := time.Until(prazo); d > 0 {
			return d
		}
	}
	return s.prazo
}

// subjectDe mapeia um stream do AOS num subject.
//
// O stream_id do AOS é livre (`run-1`, `lease:run-1`), mas um subject do NATS não é: o
// ponto separa tokens e `*`/`>` são curingas. Um stream_id com qualquer um deles não é
// representável — e a resposta é RECUSAR, não escapar em silêncio para um subject
// vizinho onde outro stream leria os nossos eventos.
func (s *Store) subjectDe(streamID string) (string, error) {
	if streamID == "" {
		return "", fmt.Errorf("%w: stream_id vazio", eventstore.ErrConfig)
	}
	if strings.ContainsAny(streamID, ". *>\t\r\n") {
		return "", fmt.Errorf("%w: stream_id %q contém um carácter que não é representável num subject NATS (. * > ou espaço)",
			eventstore.ErrConfig, streamID)
	}
	return s.prefixo + "." + streamID, nil
}

func validarPrefixo(p string) error {
	if p == "" || strings.ContainsAny(p, " *>\t\r\n") {
		return fmt.Errorf("%w: prefixo de subject inválido %q", eventstore.ErrConfig, p)
	}
	return nil
}

// asserção de conformidade: o Store implementa o contrato. Se um dia deixar de o
// implementar, o compilador diz — e não um teste de integração que ninguém corre.
var _ eventstore.EventStore = (*Store)(nil)

// Healthy indica se o store está utilizável. É `false` depois de [Store.Close].
//
// NÃO sonda o cluster: um `Healthy` que faz I/O transforma um health-check num gerador
// de tráfego e pode ele próprio falhar por timeout. A saúde do cluster observa-se pelas
// operações reais, que devolvem erro nomeado quando ele não responde.
// Healthy é a sonda de PRONTIDÃO do backend replicado.
//
// # AOS-350 — OS DOIS BACKENDS PARTILHAVAM O MESMO MODO DE FALHA
//
// Era `return !s.estaFechado()` — gémeo exacto do `!s.closed.Load()` do store de
// referência, e com o mesmo defeito: `estaFechado` só muda em [Store.Close]. Um cliente
// desligado a reconectar indefinidamente — que devolve [natsjs.ErrDesligado] a TODAS as
// escritas, sem elas sequer saírem — mantinha isto a `true`, e com ele o `/readyz` a 200,
// o gauge `aos_eventstore_healthy` a 1 e o SLI `controlPlaneAvailable` a 1.0. O
// orquestrador de contentores continuava a encaminhar tráfego para um nó que não escreve.
//
// A prontidão passa a ser as duas coisas que têm de valer: o store não foi fechado pelo
// dono, E há socket vivo agora. É um instantâneo — entre este `true` e a escrita seguinte
// a ligação pode cair —, e é o que uma sonda de prontidão pode honestamente afirmar.
//
// # NOTA OPERACIONAL — ISTO OSCILA DURANTE UMA RECONEXÃO
//
// O recuo da reconexão do cliente vai até 5 s ([natsjs.Conn.reconectar]), pelo que num
// upgrade rolante do cluster NATS TODOS os nós vão a 503 ao mesmo tempo por até esse
// tempo. É a resposta correcta — durante esse intervalo o nó não escreve nada —, mas quem
// configura sondas tem de o saber: com os valores por omissão do Kubernetes (period 10 s,
// failureThreshold 3) não há dano; com sondas agressivas o serviço inteiro sai do
// balanceador. NÃO existe o modo de falha pior (503 permanente num nó saudável): `ligada`
// é reposto a `true` na ligação bem-sucedida, e esta sonda usa exactamente a mesma
// condição que produz [natsjs.ErrDesligado] no caminho de escrita — a sonda e o
// enforcement concordam por construção.
func (s *Store) Healthy() bool {
	if s.estaFechado() {
		return false
	}
	return s.cn.Ligada()
}

// Streams devolve os stream_ids do AOS com pelo menos um evento.
//
// Num backend partilhado esta pergunta deixa de ter resposta local: os streams que
// existem são os que QUALQUER escritor criou, não os que este processo viu. Por isso é
// respondida pelo servidor — e é essa diferença que a torna correcta entre processos,
// ao contrário de um índice em memória.
func (s *Store) Streams() ([]string, error) {
	if s.estaFechado() {
		return nil, eventstore.ErrClosed
	}
	subjects, err := s.cn.SubjectsWithMessages(s.stream, s.prefixo+".>", s.prazo)
	if err != nil {
		// AOS-352: era `return nil` — sem log, sem sinal. Uma falha transitória de rede
		// ficava INDISTINGUÍVEL de «não há streams», e quatro varredores de arranque
		// consumiam essa resposta em direcções diferentes: o índice titular→partição do
		// LEGAL HOLD voltava vazio (fail-OPEN, e o ExpirationJob podia crypto-shred
		// material sob hold), zero runs órfãos eram retomados, e nada expirava.
		return nil, indisponibilidadeTransitoria(err)
	}
	out := make([]string, 0, len(subjects))
	for subject, n := range subjects {
		if n == 0 {
			continue
		}
		out = append(out, strings.TrimPrefix(subject, s.prefixo+"."))
	}
	sort.Strings(out)
	return out, nil
}

// prefixoDe deriva o prefixo de subjects do NOME DO STREAM.
//
// # O defeito que isto fecha, medido
//
// O prefixo era uma constante única. Consequência: dois streams no MESMO cluster —
// dois boards, ou dois ambientes — declaravam ambos `aos.es.>` e o servidor recusava o
// segundo com «subjects overlap with an existing stream» (10065). Ou seja, dar um nome
// próprio ao stream (ComNomeDeStream) NÃO o separava de facto, e a promessa «um cluster
// serve mais do que um board» era falsa. MEDIDO a 2026-08-31 pelo teste do nó sobre o
// substrato replicado, que não conseguia criar dois streams seguidos.
//
// O prefixo passa a derivar do nome, o que torna a separação uma consequência de dar o
// nome — em vez de uma segunda coisa que alguém tem de se lembrar de configurar. Quem
// quiser um layout próprio continua a poder fixá-lo com [ComPrefixoDeSubject].
func prefixoDe(stream string) string {
	var b strings.Builder
	b.WriteString(PrefixoSubjectOmissao)
	b.WriteByte('.')
	for _, r := range strings.ToLower(stream) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-') // ponto, espaço e curingas nunca chegam ao subject
		}
	}
	return b.String()
}

// ApagarStream apaga o stream JetStream inteiro. Ver a advertência em
// [natsjs.Conn.DeleteStream]: destrói a fonte de verdade, e existe para streams
// EFÉMEROS. Um teste que não o chame deixa lixo acumulado no cluster.
func (s *Store) ApagarStream() error { return s.cn.DeleteStream(s.stream, s.prazo) }

// criarDuravel cria (ou reafirma) o consumidor DURÁVEL da subscrição.
//
// # Porque durável, e não efémero
//
// Um consumidor efémero morre com a ligação, e o que for escrito entre a quebra e o
// retomar PERDE-SE: a subscrição RETOMA mas não RECUPERA. Com um durável, o servidor
// guarda até onde a entrega foi confirmada e recomeça aí — é a diferença entre um buraco
// silencioso no fluxo de eventos e nenhum buraco.
//
// O `CREATE` é idempotente para a mesma configuração, pelo que o caminho de reconexão
// pode reafirmá-lo sem primeiro perguntar se sobreviveu.
//
// # O ponto de partida, e porque é fixado UMA vez
//
// `by_start_sequence` a partir do último seq do stream no momento da subscrição dá a
// semântica do modelo de referência (só o que for escrito DEPOIS). Fixá-lo na criação e
// nunca mais é o que faz o durável retomar do ACK e não do presente — recalcular na
// reconexão reintroduziria exactamente o buraco que ele existe para fechar.
func (s *Store) criarDuravel(ctx context.Context, sub *subscricao) error {
	return s.cn.CreateConsumer(s.stream, natsjs.ConsumerConfig{
		Durable:        sub.duravel,
		DeliverSubject: sub.entrega,
		DeliverPolicy:  "by_start_sequence",
		OptStartSeq:    sub.inicio,
		AckPolicy:      "explicit",
		AckWait:        int64(30 * time.Second),
		IdleHeartbeat:  int64(batimentoDaSubscricao),
		FlowControl:    true,
		ReplayPolicy:   "instant",
		FilterSubject:  s.prefixo + ".>",
		NumReplicas:    1,
	}, s.prazoDe(ctx))
}

// reestabelecerEntrega volta a subscrever o MESMO deliver subject, com recuo, até
// conseguir ou o dono mandar parar.
//
// # Porque NÃO cria um consumidor novo
//
// O consumidor é DURÁVEL e sobreviveu à quebra: ele sabe até onde a entrega foi
// confirmada e retoma aí. Criar um novo (ou recalcular o ponto de partida) reabriria
// exactamente o buraco que o durável fecha — os eventos escritos no intervalo. Reafirma-se
// a criação por idempotência, para o caso de o consumidor ter sido perdido com o nó que o
// alojava, e nesse caso — declarado — o intervalo perde-se na mesma: o consumidor é R1.
func (s *Store) reestabelecerEntrega(sub *subscricao) (<-chan natsjs.Msg, func(), error) {
	espera := 200 * time.Millisecond
	const tecto = 5 * time.Second
	for {
		select {
		case <-sub.parar:
			return nil, nil, errPararSubscricao
		case <-time.After(espera):
		}
		if s.estaFechado() {
			return nil, nil, errPararSubscricao
		}
		ch, cancelar, err := s.cn.SubscribeSubject(sub.entrega)
		if err == nil {
			// Reafirma o durável: se sobreviveu, o CREATE é idempotente; se o nó que o
			// alojava morreu, recria-se — e aí o intervalo perde-se, o que fica dito.
			if errC := s.criarDuravel(context.Background(), sub); errC == nil || errors.Is(errC, eventstore.ErrClosed) {
				return ch, cancelar, nil
			}
			cancelar()
		}
		if espera < tecto {
			espera *= 2
		}
	}
}

var errPararSubscricao = errors.New("jetstream: subscrição parada pelo dono")

// enderecos parte uma lista separada por vírgulas em endereços, ignorando vazios.
func enderecos(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
