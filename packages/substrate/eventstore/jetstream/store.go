package jetstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	prazo   time.Duration
	now     func() time.Time

	mu      sync.Mutex
	streams map[string]*estado
	subs    map[string]*subscricao
	proxSub uint64
	fechado bool
}

// estado é a vista LOCAL de um stream do AOS. É uma cache, não a verdade: a verdade
// está no servidor, e qualquer divergência é detectada pelo CAS — que é precisamente o
// ponto. Uma cache obsoleta faz a escrita ser RECUSADA, nunca aceite por engano.
type estado struct {
	mu        sync.Mutex
	hidratado bool
	aosSeq    uint64            // último Event.Seq committed (gapless desde 1)
	jsSeq     uint64            // seq JetStream da última mensagem do subject (token de CAS)
	dedup     map[string]uint64 // idempotency_key -> seq JetStream do evento original
}

type config struct {
	stream   string
	prefixo  string
	prazo    time.Duration
	replicas int
	criar    bool
	now      func() time.Time
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

	cn, err := natsjs.Connect(addr, cfg.prazo)
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
		}, cfg.prazo); err != nil {
			_ = cn.Close()
			return nil, fmt.Errorf("jetstream: criar stream %q: %w", cfg.stream, err)
		}
	}
	return &Store{
		cn:      cn,
		stream:  cfg.stream,
		prefixo: cfg.prefixo,
		prazo:   cfg.prazo,
		now:     cfg.now,
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
func (s *Store) Append(ctx context.Context, streamID string, in eventstore.EventInput, opts ...eventstore.AppendOption) (eventstore.AppendResult, error) {
	if err := ctx.Err(); err != nil {
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
			if jsSeq, ok := st.dedup[chave]; ok {
				ev, err := s.eventoPorJSSeq(ctx, jsSeq, prazo)
				if err != nil {
					return eventstore.AppendResult{}, err
				}
				return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusDuplicate, Event: ev}, nil
			}
		}

		// 2) Concorrência optimista / append-only, contra a nossa vista do stream.
		if temEsperado {
			switch {
			case esperado == st.aosSeq: // ok
			case esperado < st.aosSeq:
				return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
			default:
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
				st.dedup[eventstore.IdempotencyKey(in.RunID, in.StepID)] = ack.Seq
			}
			return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusCommitted, Event: ev}, nil

		case errors.Is(err, natsjs.ErrWrongLastSeq):
			// NADA ficou durável — o servidor recusou. Outro escritor avançou.
			st.hidratado = false
			if temEsperado {
				if err := s.hidratar(ctx, st, subject, prazo); err != nil {
					return eventstore.AppendResult{}, err
				}
				if esperado < st.aosSeq {
					return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
				}
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
func (s *Store) Read(ctx context.Context, streamID string, fromSeq uint64) ([]eventstore.Event, error) {
	if err := ctx.Err(); err != nil {
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
		return nil, eventstore.ErrStreamNotFound
	}
	out := make([]eventstore.Event, 0, len(evs))
	for _, ev := range evs {
		if ev.Seq >= fromSeq {
			out = append(out, ev.Clone())
		}
	}
	return out, nil
}

// lerSubject caminha o subject do princípio ao fim e devolve os eventos, o seq
// JetStream do último, o índice de dedup derivado e o último seq do AOS.
func (s *Store) lerSubject(ctx context.Context, subject string, prazo time.Duration) ([]eventstore.Event, uint64, map[string]uint64, uint64, error) {
	var (
		evs      []eventstore.Event
		jsUltimo uint64
		aosSeq   uint64
		dedup           = map[string]uint64{}
		proximo  uint64 = 1
	)
	for {
		if err := ctx.Err(); err != nil {
			return nil, 0, nil, 0, err
		}
		jsSeq, dados, err := s.cn.NextMessageOnSubject(s.stream, proximo, subject, prazo)
		if errors.Is(err, natsjs.ErrNoMessage) {
			return evs, jsUltimo, dedup, aosSeq, nil
		}
		if err != nil {
			return nil, 0, nil, 0, err
		}
		var ev eventstore.Event
		if err := json.Unmarshal(dados, &ev); err != nil {
			return nil, 0, nil, 0, fmt.Errorf("jetstream: envelope ilegível em seq=%d do stream físico: %w", jsSeq, err)
		}
		evs = append(evs, ev)
		jsUltimo, aosSeq = jsSeq, ev.Seq
		if eventstore.HasIdempotency(ev.RunID, ev.StepID) {
			dedup[ev.IdempotencyKey] = jsSeq
		}
		proximo = jsSeq + 1
	}
}

func (s *Store) eventoPorJSSeq(ctx context.Context, jsSeq uint64, prazo time.Duration) (eventstore.Event, error) {
	if err := ctx.Err(); err != nil {
		return eventstore.Event{}, err
	}
	_, dados, err := s.cn.MessageBySeq(s.stream, jsSeq, prazo)
	if err != nil {
		return eventstore.Event{}, err
	}
	var ev eventstore.Event
	if err := json.Unmarshal(dados, &ev); err != nil {
		return eventstore.Event{}, fmt.Errorf("jetstream: envelope ilegível em seq=%d: %w", jsSeq, err)
	}
	return ev, nil
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

func (st *estado) aplicar(_ []eventstore.Event, jsUltimo uint64, dedup map[string]uint64, aosSeq uint64) {
	st.jsSeq, st.aosSeq, st.dedup, st.hidratado = jsUltimo, aosSeq, dedup, true
}

// --- Subscribe -------------------------------------------------------------

type subscricao struct {
	id       string
	store    *Store
	cancelar func()
	feito    chan struct{}
	uma      sync.Once
}

func (sub *subscricao) ID() string { return sub.id }

func (sub *subscricao) Unsubscribe() {
	sub.uma.Do(func() {
		sub.cancelar()
		<-sub.feito
		sub.store.esquecerSub(sub.id)
	})
}

// Subscribe entrega por PUSH os eventos escritos A PARTIR DE AGORA que passem o filtro.
//
// A semântica é a do modelo de referência (fanout do que é escrito depois da
// subscrição), materializada por um consumidor EFÉMERO com deliver_policy "new". Ver os
// limites no doc do pacote: sem acks, sem flow control, sem heartbeats.
func (s *Store) Subscribe(ctx context.Context, filtro eventstore.Filter, h eventstore.Handler) (eventstore.Subscription, error) {
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

	entrega, err := natsjs.NewInbox()
	if err != nil {
		return nil, err
	}
	ch, cancelar, err := s.cn.SubscribeSubject(entrega)
	if err != nil {
		return nil, err
	}
	if err := s.cn.CreateEphemeralConsumer(s.stream, natsjs.ConsumerConfig{
		DeliverSubject: entrega,
		DeliverPolicy:  "new",
		AckPolicy:      "none",
		ReplayPolicy:   "instant",
		FilterSubject:  s.prefixo + ".>",
	}, s.prazoDe(ctx)); err != nil {
		cancelar()
		return nil, err
	}

	sub := &subscricao{id: id, store: s, cancelar: cancelar, feito: make(chan struct{})}
	go func() {
		defer close(sub.feito)
		for m := range ch {
			var ev eventstore.Event
			if err := json.Unmarshal(m.Data, &ev); err != nil {
				continue // envelope ilegível: não é do AOS, ou é de outra versão
			}
			if filtro.Matches(ev) {
				h(ev.Clone())
			}
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
		st = &estado{dedup: map[string]uint64{}}
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
func (s *Store) Healthy() bool { return !s.estaFechado() }

// Streams devolve os stream_ids do AOS com pelo menos um evento.
//
// Num backend partilhado esta pergunta deixa de ter resposta local: os streams que
// existem são os que QUALQUER escritor criou, não os que este processo viu. Por isso é
// respondida pelo servidor — e é essa diferença que a torna correcta entre processos,
// ao contrário de um índice em memória.
func (s *Store) Streams() []string {
	if s.estaFechado() {
		return nil
	}
	subjects, err := s.cn.SubjectsWithMessages(s.stream, s.prefixo+".>", s.prazo)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(subjects))
	for subject, n := range subjects {
		if n == 0 {
			continue
		}
		out = append(out, strings.TrimPrefix(subject, s.prefixo+"."))
	}
	sort.Strings(out)
	return out
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
