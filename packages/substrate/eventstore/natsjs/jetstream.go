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
	Seq uint64 `json:"seq,omitempty"`
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
