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
	regiao  string
	board   string
	prazo   time.Duration
	obs     eventstore.Observer
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
				return eventstore.AppendResult{Seq: ev.Seq, Status: eventstore.StatusDuplicate, Event: ev.Clone()}, nil
			}
		}

		// 2) Concorrência optimista / append-only, contra a nossa vista do stream.
		if temEsperado {
			switch {
			case esperado == st.aosSeq: // ok
			case esperado < st.aosSeq:
				s.obs.AppendRejected(streamID, eventstore.ErrAppendOnlyViolation)
				return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
			default:
				s.obs.AppendRejected(streamID, eventstore.ErrSeqConflict)
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
					return eventstore.AppendResult{}, eventstore.ErrAppendOnlyViolation
				}
				s.obs.AppendRejected(streamID, eventstore.ErrSeqConflict)
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
// janelaDeLeitura é quantos eventos se trazem por lote.
//
// Não é um número arbitrário: é o tecto da fila de entrega que o lote exige, e essa fila
// tem de comportar o lote INTEIRO. O leitor do cliente DESCARTA quando a fila enche (um
// subscritor lento não pode bloquear o leitor, que serve todas as subscrições) — e um log
// truncado não é um log lento, é um log ERRADO. Ler em janelas mantém a fila limitada sem
// voltar ao pedido-por-evento.
const janelaDeLeitura = 2048

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

	evs := make([]eventstore.Event, 0, total)
	var inicio uint64 // 0 = desde o princípio do stream
	for uint64(len(evs)) < total {
		// A janela fica toda em uint64: converter a contagem do servidor para int
		// seria uma conversão que o compilador não pode provar segura (G115), e a
		// resposta certa a isso é não a fazer — não silenciá-la.
		quantos := total - uint64(len(evs))
		if quantos > janelaDeLeitura {
			quantos = janelaDeLeitura
		}
		lote, ultimoJS, err := s.lerLote(ctx, subject, inicio, quantos, prazo)
		if err != nil {
			return nil, 0, nil, 0, err
		}
		if len(lote) == 0 {
			return nil, 0, nil, 0, fmt.Errorf(
				"jetstream: lote vazio a meio da leitura de %q (%d de %d eventos lidos) — o log não pode ser servido truncado",
				subject, len(evs), total)
		}
		evs = append(evs, lote...)
		inicio = ultimoJS + 1
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

// lerLote traz até `quantos` eventos do subject a partir do seq físico `inicio`, por
// entrega push num consumidor efémero.
func (s *Store) lerLote(ctx context.Context, subject string, inicio, quantos uint64, prazo time.Duration) ([]eventstore.Event, uint64, error) {
	entrega, err := natsjs.NewInbox()
	if err != nil {
		return nil, 0, err
	}
	// A fila comporta o lote INTEIRO mais folga: ver [janelaDeLeitura].
	// A fila é dimensionada pela JANELA (constante), não por `quantos`: o tecto é o
	// mesmo e não há conversão de um valor vindo do servidor.
	ch, cancelar, err := s.cn.SubscribeSubjectBuffered(entrega, janelaDeLeitura+16)
	if err != nil {
		return nil, 0, err
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
		return nil, 0, err
	}

	evs := make([]eventstore.Event, 0, janelaDeLeitura)
	var ultimoJS uint64
	temporizador := time.NewTimer(prazo)
	defer temporizador.Stop()
	for uint64(len(evs)) < quantos {
		select {
		case m, ok := <-ch:
			if !ok {
				return nil, 0, fmt.Errorf("jetstream: a subscrição do lote de %q fechou com %d de %d eventos", subject, len(evs), quantos)
			}
			var ev eventstore.Event
			if err := json.Unmarshal(m.Data, &ev); err != nil {
				return nil, 0, fmt.Errorf("jetstream: envelope ilegível na leitura de %q: %w", subject, err)
			}
			evs = append(evs, ev)
		case <-temporizador.C:
			return nil, 0, fmt.Errorf("jetstream: leitura de %q parou em %d de %d eventos ao fim de %s — um log servido truncado seria pior do que este erro",
				subject, len(evs), quantos, prazo)
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		}
	}
	// O seq físico do último é pedido ao servidor por [Store.lerSubject]; aqui basta
	// avançar a janela, e o avanço é feito pelo seq do ÚLTIMO evento lido.
	ultimoJS, err = s.cn.UltimoSeqDoSubject(s.stream, subject, prazo)
	if err != nil {
		return nil, 0, err
	}
	return evs, ultimoJS, nil
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
			for m := range ch {
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
