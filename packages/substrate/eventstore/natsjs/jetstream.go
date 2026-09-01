package natsjs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Códigos de erro do JetStream que o Event Store tem de distinguir. São do servidor;
// o valor foi CONFIRMADO contra um cluster real (resposta literal registada em
// docs/reports/medicao-jetstream-arbitragem-2026-08-31.md §1).
const (
	// CodeWrongLastSeq — o expected_seq afirmado não corresponde ao último seq do
	// subject. É a RECUSA que dá a arbitragem entre escritores.
	CodeWrongLastSeq = 10071

	// CodeNoSuitablePeers — nenhum par do cluster satisfaz a `placement` pedida.
	//
	// É o FAIL-CLOSED do SERVIDOR à fronteira de soberania, e é o mais forte que existe
	// porque não depende de nós verificarmos nada: se ninguém pode alojar as réplicas
	// dentro da região, o stream simplesmente NÃO é criado.
	//
	// Valor CONFIRMADO contra um cluster real a 2026-08-31 — resposta literal:
	// «no suitable peers for placement, tags not matched ['region:...']»,
	// code=400 err_code=10005.
	CodeNoSuitablePeers = 10005
)

// ErrWrongLastSeq — o servidor recusou a escrita por o expected_seq não bater. Nada ficou
// durável (o PubAck traz seq:0), o que é exactamente o que o contrato C2 do Event Store
// exige de uma recusa.
var ErrWrongLastSeq = errors.New("natsjs: expected_seq não corresponde ao último seq do subject")

// PubAck é a resposta do JetStream a uma publicação.
type PubAck struct {
	Stream string `json:"stream"`
	Seq    uint64 `json:"seq"`
	// Duplicate indica que a Nats-Msg-Id já existia DENTRO DA JANELA de deduplicação
	// e que Seq é o seq ORIGINAL. Ver a advertência em [HdrMsgID]: é uma janela, não
	// um índice — nunca é a garantia de idempotência do AOS.
	Duplicate bool     `json:"duplicate"`
	Error     *JSError `json:"error"`
}

// JSError é o erro estruturado que o JetStream devolve dentro do PubAck.
type JSError struct {
	Code        int    `json:"code"`
	ErrCode     int    `json:"err_code"`
	Description string `json:"description"`
}

func (e *JSError) Error() string {
	return fmt.Sprintf("jetstream: %s (code=%d err_code=%d)", e.Description, e.Code, e.ErrCode)
}

// Publish escreve em subject e devolve o PubAck.
//
// Um erro devolvido aqui significa que NADA FICOU DURÁVEL, com uma excepção nomeada:
// [ErrTimeout], que significa INDETERMINADO. Ver [Conn.Request] para porque é seguro
// repetir nesse caso — e é seguro só porque a escrita leva expected_seq.
func (cn *Conn) Publish(subject string, h Header, data []byte, timeout time.Duration) (PubAck, error) {
	m, err := cn.Request(subject, h, data, timeout)
	if err != nil {
		return PubAck{}, err
	}
	var ack PubAck
	if err := json.Unmarshal(m.Data, &ack); err != nil {
		return PubAck{}, fmt.Errorf("%w: PubAck ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if ack.Error != nil {
		if ack.Error.ErrCode == CodeWrongLastSeq {
			return ack, fmt.Errorf("%w: %s", ErrWrongLastSeq, ack.Error.Description)
		}
		return ack, ack.Error
	}
	return ack, nil
}

// PublishExpectingSeq é a via que o Event Store usa: afirma qual é o último seq do subject e
// só escreve se ainda for verdade.
//
// É esta chamada — e não o cliente, e não o lease — que arbitra entre escritores. A
// disciplina de posse do AOS (LeaseManager/AOS-018, FencedAppender, ADR-023) é correcta
// SOB esta propriedade e vacuosa sem ela.
// O Header do chamador NUNCA é mutado — ver [Header.Clone] para o defeito que isso
// causava.
func (cn *Conn) PublishExpectingSeq(subject string, lastSeq uint64, h Header, data []byte, timeout time.Duration) (PubAck, error) {
	cab := h.Clone()
	cab[HdrExpectedLastSubjectSeq] = strconv.FormatUint(lastSeq, 10)
	return cn.Publish(subject, cab, data, timeout)
}

// --- Gestão mínima de streams ----------------------------------------------

// StreamConfig é o subconjunto da configuração de stream que o Event Store fixa. Os
// campos que aqui NÃO aparecem ficam no default do servidor de propósito: cada opção
// que fixamos é uma que temos de justificar e manter.
type StreamConfig struct {
	Name     string   `json:"name"`
	Subjects []string `json:"subjects"`
	// NumReplicas é o quórum do AOS-100. 3 ou 5 (R3/R5); 1 é dev.
	NumReplicas int `json:"num_replicas"`
	// Storage "file" — o log é a fonte de verdade e tem de sobreviver a reinício.
	Storage string `json:"storage"`
	// DenyDelete/DenyPurge impõem o append-only DO LADO DO SERVIDOR. MEDIDO: sem
	// eles o log é apagável por API; com eles o servidor recusa (10057/10110).
	DenyDelete bool `json:"deny_delete"`
	DenyPurge  bool `json:"deny_purge"`
	// Duplicates é a janela de deduplicação, em nanossegundos. NÃO é a garantia de
	// idempotência do AOS — ver [HdrMsgID].
	Duplicates int64 `json:"duplicate_window,omitempty"`
	// Placement restringe onde as replicas podem ser colocadas. nil = sem restricao —
	// e sem restricao NAO ha fronteira de soberania nenhuma (ADR-011).
	Placement *Placement `json:"placement,omitempty"`
}

// streamResponse é o envelope das respostas da API de streams.
type streamResponse struct {
	Error *JSError `json:"error"`
}

// CreateStream cria o stream. Um stream já existente com configuração diferente é
// recusado pelo servidor — deliberadamente não se faz update às cegas: mudar
// num_replicas ou deny_delete por baixo de um log vivo é uma operação de migração, não
// um efeito secundário de arrancar um nó.
func (cn *Conn) CreateStream(cfg StreamConfig, timeout time.Duration) error {
	corpo, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	m, err := cn.Request("$JS.API.STREAM.CREATE."+cfg.Name, nil, corpo, timeout)
	if err != nil {
		return err
	}
	var r streamResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return fmt.Errorf("%w: resposta de CREATE ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

// StreamState devolve o número de mensagens e o último seq do stream. É o mínimo de que
// o Event Store precisa para se re-hidratar; não é uma vista completa.
type StreamState struct {
	Messages uint64
	LastSeq  uint64
}

type streamInfoResponse struct {
	Error *JSError `json:"error"`
	State struct {
		Messages uint64 `json:"messages"`
		LastSeq  uint64 `json:"last_seq"`
	} `json:"state"`
}

// FetchStreamState lê o estado do stream.
func (cn *Conn) FetchStreamState(name string, timeout time.Duration) (StreamState, error) {
	m, err := cn.Request("$JS.API.STREAM.INFO."+name, nil, nil, timeout)
	if err != nil {
		return StreamState{}, err
	}
	var r streamInfoResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return StreamState{}, fmt.Errorf("%w: resposta de INFO ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return StreamState{}, r.Error
	}
	return StreamState{Messages: r.State.Messages, LastSeq: r.State.LastSeq}, nil
}

// --- Leitura directa de mensagens ------------------------------------------

type msgGetRequest struct {
	Seq           uint64 `json:"seq,omitempty"`
	LastBySubject string `json:"last_by_subj,omitempty"`
	NextBySubject string `json:"next_by_subj,omitempty"`
}

type msgGetResponse struct {
	Error   *JSError `json:"error"`
	Message struct {
		Subject string `json:"subject"`
		Seq     uint64 `json:"seq"`
		Data    []byte `json:"data"` // base64 no fio; encoding/json descodifica para []byte
	} `json:"message"`
}

// MessageBySeq lê uma mensagem do stream pelo seu seq.
//
// Existe para uma pergunta que o PubAck sozinho não responde: depois de uma escrita
// RECUSADA, o que está no log? Sem esta leitura, «a recusa não deixou rasto» seria
// inferido de `seq:0` na resposta em vez de observado no log — e a inferência é
// exactamente o que a regra de método do repo proíbe.
func (cn *Conn) MessageBySeq(stream string, seq uint64, timeout time.Duration) (subject string, data []byte, err error) {
	corpo, err := json.Marshal(msgGetRequest{Seq: seq})
	if err != nil {
		return "", nil, err
	}
	m, err := cn.Request("$JS.API.STREAM.MSG.GET."+stream, nil, corpo, timeout)
	if err != nil {
		return "", nil, err
	}
	var r msgGetResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return "", nil, fmt.Errorf("%w: resposta de MSG.GET ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return "", nil, r.Error
	}
	return r.Message.Subject, r.Message.Data, nil
}

// ErrNoMessage — não há mensagem que satisfaça o pedido. É a resposta NORMAL a
// caminhar um subject até ao fim, não uma falha: quem lê um log para o reconstruir
// precisa de distinguir «acabou» de «avariou».
var ErrNoMessage = errors.New("natsjs: não há mensagem")

// codeNoMessage é o err_code do servidor para «no message found».
const codeNoMessage = 10037

// NextMessageOnSubject devolve a PRIMEIRA mensagem com seq >= fromSeq publicada em
// subject, e o seq que ela ocupa no stream.
//
// É a via de LEITURA de um log por subject sem criar consumidores: caminha-se com
// fromSeq = seq devolvido + 1 até vir [ErrNoMessage]. Cada passo é um round-trip — é
// deliberadamente a operação mais simples que reconstrói um stream, e o custo está
// declarado em quem a usa.
func (cn *Conn) NextMessageOnSubject(stream string, fromSeq uint64, subject string, timeout time.Duration) (seq uint64, data []byte, err error) {
	body, err := json.Marshal(msgGetRequest{Seq: fromSeq, NextBySubject: subject})
	if err != nil {
		return 0, nil, err
	}
	m, err := cn.Request("$JS.API.STREAM.MSG.GET."+stream, nil, body, timeout)
	if err != nil {
		return 0, nil, err
	}
	var r msgGetResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return 0, nil, fmt.Errorf("%w: resposta de MSG.GET ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		if r.Error.ErrCode == codeNoMessage {
			return 0, nil, ErrNoMessage
		}
		return 0, nil, r.Error
	}
	return r.Message.Seq, r.Message.Data, nil
}

// --- Subscrição e consumidores push ----------------------------------------

// SubscribeSubject subscreve subject e devolve o canal de entrega e a função que a
// cancela. É a via de PUSH: o servidor empurra, ninguém faz polling.
func (cn *Conn) SubscribeSubject(subject string) (<-chan Msg, func(), error) {
	if err := validateToken("subject", subject); err != nil {
		return nil, nil, err
	}
	ch, sid, err := cn.subscribe(subject)
	if err != nil {
		return nil, nil, err
	}
	return ch, func() { cn.unsubscribe(sid) }, nil
}

// NewInbox devolve um subject único, próprio para servir de destino de entrega de um
// consumidor push.
func NewInbox() (string, error) { return newInbox() }

// ConsumerConfig é o subconjunto da configuração de consumidor que o Event Store usa.
//
// Os campos ausentes são tão significativos como os presentes: NÃO se declara
// flow-control nem heartbeats, e o `AckPolicy` é "none". É uma escolha DECLARADA e
// medida: o transporte push foi verificado contra um cluster nesta configuração e só
// nesta. Um consumidor DURÁVEL de produção quer acks e flow control, e é aí que o custo
// de termos o cliente cresce — não se finge o contrário construindo campos que ninguém
// exerceu.
type ConsumerConfig struct {
	// Durable dá nome ao consumidor e torna-o PERSISTENTE no servidor. Vazio = efémero.
	//
	// É o que distingue «a subscrição retoma» de «a subscrição recupera»: um consumidor
	// efémero morre com a ligação e o que foi escrito no intervalo perde-se; um durável
	// sobrevive, e a entrega recomeça no ponto em que o último ACK ficou.
	Durable string `json:"durable_name,omitempty"`
	// AckWait, em nanossegundos, é quanto o servidor espera por um ACK antes de
	// reentregar. Só tem efeito com AckPolicy "explicit".
	AckWait int64 `json:"ack_wait,omitempty"`
	// IdleHeartbeat, em nanossegundos, manda o servidor emitir um sinal de vida quando
	// não há nada a entregar.
	//
	// É o que torna DETECTÁVEL um consumidor que morreu: sem batimento, um consumidor
	// apagado do lado do servidor é indistinguível de um stream sossegado — o subscritor
	// fica à espera para sempre e ninguém dá por isso. Silêncio não é a mesma coisa que
	// paz.
	IdleHeartbeat int64 `json:"idle_heartbeat,omitempty"`
	// FlowControl faz o servidor pedir confirmação periódica antes de continuar a
	// empurrar. Sem ele, um subscritor lento é ATROPELADO — as mensagens acumulam-se e o
	// cliente descarta-as.
	FlowControl bool `json:"flow_control,omitempty"`
	// DeliverSubject é o subject para onde o servidor empurra. Vazio = pull.
	DeliverSubject string `json:"deliver_subject"`
	// DeliverPolicy "new" entrega só o que for publicado a partir de agora; "all"
	// entrega o histórico e depois o novo.
	DeliverPolicy string `json:"deliver_policy"`
	AckPolicy     string `json:"ack_policy"`
	ReplayPolicy  string `json:"replay_policy"`
	FilterSubject string `json:"filter_subject,omitempty"`
	// NumReplicas do CONSUMIDOR, que NAO tem de ser o do stream. Para LER, 1 e o
	// valor certo: um consumidor de leitura e transitorio e nao guarda nada que
	// precise de sobreviver a nada.
	//
	// O DEFEITO QUE FECHA, medido: com o consumidor a herdar as 3 replicas do stream,
	// a sua criacao e uma operacao REPLICADA — e com um no em baixo ela EXPIROU (20s),
	// deixando a leitura indisponivel exactamente no cenario que o AC4 mede. A leitura
	// ficava MENOS disponivel do que a escrita, que continuou a passar. Ver perda_test.go.
	NumReplicas int `json:"num_replicas,omitempty"`
	// MemStorage mantem o estado do consumidor em memoria: nao ha nada a persistir
	// num consumidor que morre com a leitura.
	MemStorage bool `json:"mem_storage,omitempty"`
	// OptStartSeq e o seq do STREAM FISICO por onde a entrega comeca. So tem efeito
	// com DeliverPolicy "by_start_sequence"; e o que permite ler um log em JANELAS em
	// vez de o trazer todo de uma vez para uma fila que teria de o comportar.
	OptStartSeq uint64 `json:"opt_start_seq,omitempty"`
}

type consumerCreateRequest struct {
	Stream string         `json:"stream_name"`
	Config ConsumerConfig `json:"config"`
}

// CreateEphemeralConsumer cria um consumidor efémero (sem durable_name) sobre stream.
// Efémero serve a leitura de um lote: vive o que a leitura viver e não deixa estado.
//
// Para uma SUBSCRIÇÃO, um efémero é a escolha errada — morre com a ligação e o que for
// escrito no intervalo perde-se. Ver [Conn.CreateConsumer] com `Durable`.
func (cn *Conn) CreateEphemeralConsumer(stream string, cfg ConsumerConfig, timeout time.Duration) error {
	return cn.CreateConsumer(stream, cfg, timeout)
}

// CreateConsumer cria um consumidor sobre stream. Com `Durable` preenchido é persistente;
// sem ele, efémero.
//
// Um `CREATE` sobre um durável que JÁ EXISTE com a mesma configuração é idempotente do
// lado do servidor — e é isso que permite a um subscritor voltar a pedir o seu consumidor
// depois de uma reconexão sem se preocupar em saber se ele sobreviveu.
func (cn *Conn) CreateConsumer(stream string, cfg ConsumerConfig, timeout time.Duration) error {
	body, err := json.Marshal(consumerCreateRequest{Stream: stream, Config: cfg})
	if err != nil {
		return err
	}
	assunto := "$JS.API.CONSUMER.CREATE." + stream
	if cfg.Durable != "" {
		assunto = "$JS.API.CONSUMER.DURABLE.CREATE." + stream + "." + cfg.Durable
	}
	m, err := cn.Request(assunto, nil, body, timeout)
	if err != nil {
		return err
	}
	var r streamResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return fmt.Errorf("%w: resposta de CONSUMER.CREATE ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

// --- Subjects de um stream --------------------------------------------------

type streamInfoRequest struct {
	SubjectsFilter string `json:"subjects_filter,omitempty"`
}

type streamSubjectsResponse struct {
	Error *JSError `json:"error"`
	State struct {
		Subjects map[string]uint64 `json:"subjects"`
	} `json:"state"`
}

// SubjectsWithMessages devolve os subjects que casam com filter e que TÊM mensagens,
// com a contagem de cada um.
//
// O servidor só devolve este mapa quando `subjects_filter` é pedido explicitamente — por
// omissão o INFO traz o estado agregado e não a lista. É a via de descoberta de streams
// para um backend em que «que streams existem?» não é uma pergunta local.
func (cn *Conn) SubjectsWithMessages(stream, filter string, timeout time.Duration) (map[string]uint64, error) {
	body, err := json.Marshal(streamInfoRequest{SubjectsFilter: filter})
	if err != nil {
		return nil, err
	}
	m, err := cn.Request("$JS.API.STREAM.INFO."+stream, nil, body, timeout)
	if err != nil {
		return nil, err
	}
	var r streamSubjectsResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return nil, fmt.Errorf("%w: resposta de INFO ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.State.Subjects, nil
}

// DeleteStream apaga um stream e tudo o que ele contém.
//
// NÃO é uma operação do Event Store — o log do AOS é append-only e o `deny_delete` do
// servidor impede apagar mensagens. Isto apaga o CONTENTOR, e existe para o ciclo de
// vida de streams EFÉMEROS (testes, ambientes descartáveis). Usar isto sobre um stream
// de produção destrói a fonte de verdade; não há aqui rede de segurança nenhuma.
func (cn *Conn) DeleteStream(stream string, timeout time.Duration) error {
	m, err := cn.Request("$JS.API.STREAM.DELETE."+stream, nil, nil, timeout)
	if err != nil {
		return err
	}
	var r streamResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return fmt.Errorf("%w: resposta de DELETE ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

// --- Colocação (soberania regional, ADR-011) ---------------------------------

// Placement restringe ONDE o servidor pode colocar as réplicas de um stream.
//
// É a única via pela qual a fronteira regional de soberania é IMPOSTA no substrato: sem
// ela, o JetStream coloca as réplicas em quaisquer pares elegíveis do cluster, e a
// promessa «as réplicas nunca cruzam a fronteira» seria uma frase num documento.
//
// As Tags casam com o `server_tags` declarado na configuração de cada servidor. Se
// nenhum par tiver as tags pedidas, o servidor RECUSA criar o stream — o fail-closed é
// dele, e é isso que o torna confiável: não depende de nós verificarmos nada.
type Placement struct {
	Cluster string   `json:"cluster,omitempty"`
	Tags    []string `json:"tags,omitempty"`
}

// StreamConfigLida é o subconjunto da configuração ARMAZENADA que se lê de volta.
//
// Existe por uma razão de segurança, não de conveniência: quem se LIGA a um stream já
// existente (sem o criar) não sabe com que colocação ele foi criado. Sem ler isto de
// volta, um nó com fronteira declarada poderia ligar-se a um stream SEM colocação e
// julgar-se soberano — a falha mais silenciosa possível, porque tudo funciona.
type StreamConfigLida struct {
	NumReplicas int        `json:"num_replicas"`
	Placement   *Placement `json:"placement"`
	DenyDelete  bool       `json:"deny_delete"`
	DenyPurge   bool       `json:"deny_purge"`
}

type streamConfigResponse struct {
	Error  *JSError          `json:"error"`
	Config *StreamConfigLida `json:"config"`
}

// ConfigDoStream lê a configuração ARMAZENADA do stream, tal como o servidor a tem.
func (cn *Conn) ConfigDoStream(stream string, timeout time.Duration) (StreamConfigLida, error) {
	m, err := cn.Request("$JS.API.STREAM.INFO."+stream, nil, nil, timeout)
	if err != nil {
		return StreamConfigLida{}, err
	}
	var r streamConfigResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return StreamConfigLida{}, fmt.Errorf("%w: resposta de INFO ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return StreamConfigLida{}, r.Error
	}
	if r.Config == nil {
		return StreamConfigLida{}, fmt.Errorf("%w: INFO sem `config`", ErrProtocol)
	}
	return *r.Config, nil
}

// UltimoSeqDoSubject devolve o seq JetStream da ÚLTIMA mensagem publicada em subject.
//
// É o token de CAS de quem se re-hidrata: sem ele, uma escrita afirmaria uma posição que
// não conhece. Um subject vazio devolve (0, nil) — 0 é a afirmação correcta para «ainda
// não há nada aqui», e não um erro.
func (cn *Conn) UltimoSeqDoSubject(stream, subject string, timeout time.Duration) (uint64, error) {
	body, err := json.Marshal(msgGetRequest{LastBySubject: subject})
	if err != nil {
		return 0, err
	}
	m, err := cn.Request("$JS.API.STREAM.MSG.GET."+stream, nil, body, timeout)
	if err != nil {
		return 0, err
	}
	var r msgGetResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return 0, fmt.Errorf("%w: resposta de MSG.GET ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		if r.Error.ErrCode == codeNoMessage {
			return 0, nil
		}
		return 0, r.Error
	}
	return r.Message.Seq, nil
}

// SubscribeSubjectBuffered é [Conn.SubscribeSubject] com a profundidade da fila escolhida
// pelo chamador.
//
// Existe porque a fila por omissão é pequena e o leitor DESCARTA quando ela enche (um
// subscritor lento não pode bloquear o leitor, que serve todas as subscrições). Isso é
// certo para request-reply, e é RUINOSO para ler um log: um lote entregue a um canal
// pequeno perderia mensagens, e um log truncado não é um log lento — é um log ERRADO.
// Quem lê um lote de N mensagens dimensiona a fila para N.
func (cn *Conn) SubscribeSubjectBuffered(subject string, buf int) (<-chan Msg, func(), error) {
	if err := validateToken("subject", subject); err != nil {
		return nil, nil, err
	}
	ch, sid, err := cn.subscribeBuffered(subject, buf)
	if err != nil {
		return nil, nil, err
	}
	return ch, func() { cn.unsubscribe(sid) }, nil
}

// --- Onde as réplicas caíram ------------------------------------------------

// ColocacaoEfectiva é ONDE o stream está, e não onde foi pedido que estivesse.
//
// A distinção é a que separa uma restrição declarada de uma restrição CUMPRIDA. A
// `placement` diz o que se exigiu; isto diz o que o servidor fez.
type ColocacaoEfectiva struct {
	Lider    string
	Replicas []string // pares NÃO-líderes
}

// Todos devolve o conjunto completo de pares que alojam o stream (líder incluído).
func (c ColocacaoEfectiva) Todos() []string {
	out := make([]string, 0, len(c.Replicas)+1)
	if c.Lider != "" {
		out = append(out, c.Lider)
	}
	return append(out, c.Replicas...)
}

type streamClusterResponse struct {
	Error   *JSError `json:"error"`
	Cluster *struct {
		Leader   string `json:"leader"`
		Replicas []struct {
			Name    string `json:"name"`
			Current bool   `json:"current"`
		} `json:"replicas"`
	} `json:"cluster"`
}

// ColocacaoDoStream lê que pares do cluster alojam o stream.
//
// Um stream não-replicado (R1 fora de cluster) não traz bloco `cluster`; devolve-se o
// zero-value, que é a resposta honesta a «onde está replicado?» quando não está.
func (cn *Conn) ColocacaoDoStream(stream string, timeout time.Duration) (ColocacaoEfectiva, error) {
	m, err := cn.Request("$JS.API.STREAM.INFO."+stream, nil, nil, timeout)
	if err != nil {
		return ColocacaoEfectiva{}, err
	}
	var r streamClusterResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return ColocacaoEfectiva{}, fmt.Errorf("%w: resposta de INFO ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return ColocacaoEfectiva{}, r.Error
	}
	if r.Cluster == nil {
		return ColocacaoEfectiva{}, nil
	}
	out := ColocacaoEfectiva{Lider: r.Cluster.Leader}
	for _, rep := range r.Cluster.Replicas {
		out.Replicas = append(out.Replicas, rep.Name)
	}
	return out, nil
}

// PublishSemResposta publica sem esperar resposta. É o que um ACK é: uma mensagem vazia
// para o subject de resposta que a entrega trouxe.
func (cn *Conn) PublishSemResposta(subject string, data []byte) error {
	return cn.publish(subject, "", nil, data)
}

// DeleteConsumer apaga um consumidor durável.
//
// Um durável que ninguém apaga fica no servidor para sempre a acumular estado de
// entrega — e, pior, a reter mensagens que o stream julgaria já consumidas. Quem cria um
// durável é dono de o apagar.
func (cn *Conn) DeleteConsumer(stream, durable string, timeout time.Duration) error {
	m, err := cn.Request("$JS.API.CONSUMER.DELETE."+stream+"."+durable, nil, nil, timeout)
	if err != nil {
		return err
	}
	var r streamResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return fmt.Errorf("%w: resposta de CONSUMER.DELETE ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return r.Error
	}
	return nil
}

// EstadoDeControlo classifica uma mensagem de CONTROLO do push (as que não são eventos).
type EstadoDeControlo int

const (
	// NaoEControlo — é uma mensagem normal, com dados.
	NaoEControlo EstadoDeControlo = iota
	// BatimentoOcioso — sinal de vida do servidor; não há nada a entregar.
	BatimentoOcioso
	// PedidoDeFluxo — o servidor pede confirmação para continuar a empurrar. TEM de ser
	// respondido, senão a entrega PÁRA. É a diferença entre um subscritor lento ser
	// travado e ser atropelado.
	PedidoDeFluxo
)

// Controlo classifica a mensagem. Uma mensagem de estado 100 com subject de resposta é um
// pedido de fluxo; sem ele, um batimento.
//
// Distingui-los importa: responder a um batimento é inofensivo mas inútil, e NÃO responder
// a um pedido de fluxo pára a entrega — em silêncio, que é o modo de falha caro.
func (m Msg) Controlo() EstadoDeControlo {
	if m.Status != 100 {
		return NaoEControlo
	}
	if m.Reply != "" {
		return PedidoDeFluxo
	}
	return BatimentoOcioso
}

type consumerNamesResponse struct {
	Error     *JSError `json:"error"`
	Consumers []string `json:"consumers"`
}

// ConsumidoresDoStream lista os nomes dos consumidores de um stream.
//
// Existe para operação — e para o teste que mata um consumidor pelas costas do subscritor,
// que é a única forma de verificar que o silêncio é DETECTADO em vez de tolerado.
func (cn *Conn) ConsumidoresDoStream(stream string, timeout time.Duration) ([]string, error) {
	m, err := cn.Request("$JS.API.CONSUMER.NAMES."+stream, nil, nil, timeout)
	if err != nil {
		return nil, err
	}
	var r consumerNamesResponse
	if err := json.Unmarshal(m.Data, &r); err != nil {
		return nil, fmt.Errorf("%w: resposta de CONSUMER.NAMES ilegível (%q): %v", ErrProtocol, m.Data, err)
	}
	if r.Error != nil {
		return nil, r.Error
	}
	return r.Consumers, nil
}
