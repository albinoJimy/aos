// AOS-213 (CON-02/DEF-903) — EXPIRAÇÃO POR TTL composta no nó. Fecha a lacuna que a Opção C do
// dono sequenciou para DEPOIS de o apagamento ser real: o núcleo do AOS-093 tornou o conteúdo
// dos runs cifrado POR-TITULAR e o /dsar/erase tornou-o irrecuperável, pelo que já HÁ apagamento
// real para EXPIRAR por retenção. Este ficheiro entrega as duas PORTAS que faltavam ao
// [audit.ExpirationJob] (AOS-092) para ele agir sobre o substrato REAL do nó:
//
//   - [eventStoreRecordSource] — a fonte de leitura ([audit.RecordSource]) que expõe os registos
//     CLASSIFICADOS do Event Store: cada evento "replay.captured" cifrado por-titular (AOS-093) é
//     um [audit.ExpirableRecord] de classe pii_operational, com o TITULAR (sealed_subject), a
//     PARTIÇÃO (o stream do run) e o carimbo de criação (o ts observacional do evento). NÃO lê o
//     conteúdo (que é opaco/cifrado) — só os metadados que decidem e auditam a expiração.
//   - [cryptoShredSink] — o sink de escrita ([audit.ExpirationSink]) que MATERIALIZA a expiração
//     por CRYPTO-SHRED da KEK POR-TITULAR do MESMO vault que o /dsar/erase destrói (AOS-093):
//     apagar a KEK torna o conteúdo do run IRRECUPERÁVEL sem mutar a hash-chain (que sela o HASH
//     do ciphertext, não o plaintext). A expiração é apagamento REAL, não um no-op.
//
// GRANULARIDADE (resolvida honestamente — o eixo residual está nomeado no banner, no
// deploy/node/README.md e em DEF-903). O TTL do AOS-092 é POR-REGISTO/classe, mas o crypto-shred
// do envelope de AOS-093 é POR-CHAVE-DE-TITULAR: uma KEK por titular embrulha as DEKs de TODOS os
// seus registos. Não há, com o envelope actual, forma de expirar UM registo de um titular sem
// destruir a chave que cifra os DEMAIS registos desse titular. A ESCOLHA é, por isso, a expiração
// POR-TITULAR: quando um registo classificado de um titular cruza o TTL da sua classe E o titular
// não está sob legal hold, a KEK do titular é crypto-shredded — expirando de facto TODO o
// conteúdo cifrado desse titular. O job avalia o TTL por-registo (mecânica de AOS-092 preservada:
// idade = clock − CreatedAt, por classe) e materializa por-titular. RESIDUAL nomeado (eixo
// AOS-093/envelope): a retenção DIFERENCIAL por-classe DENTRO de um mesmo titular colapsa para a
// classe que expira primeiro (a KEK é uma só). A granularidade fina por-registo exigiria custódia
// de chave POR-REGISTO independente da KEK do titular, ou tombstones por-registo no Event Store —
// nenhum viável sem re-arquitectar o envelope de AOS-093. Não se over-claim: entrega-se a
// expiração por-titular com o residual nomeado com eixo.
package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/aos-ref/kernel/agent-runtime/replay"
	audit "github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
)

// eventStoreRecordSource é a [audit.RecordSource] do nó sobre o Event Store: varre os streams
// committed e expõe cada evento "replay.captured" cifrado por-titular (AOS-093) como um
// [audit.ExpirableRecord] classificado (pii_operational). Detém o MESMO Event Store que o
// capturer escreve, pelo que a fonte reflecte o substrato real. Seguro para uso concorrente na
// medida em que o Event Store o é (List só faz leituras).
type eventStoreRecordSource struct {
	es *eventstore.Store
}

// capturedSubjectWire é a projecção MÍNIMA do payload de "replay.captured" que a fonte precisa: o
// TITULAR sob cuja KEK o conteúdo foi cifrado. Nunca desserializa o conteúdo (opaco/cifrado).
type capturedSubjectWire struct {
	SealedSubject string `json:"sealed_subject"`
}

// List implementa [audit.RecordSource]. Percorre os streams do Event Store e, para cada evento
// "replay.captured" com um titular selado, produz um [audit.ExpirableRecord] de classe
// pii_operational. Um evento sem titular selado (conteúdo não cifrado por-titular ⇒ sem KEK a
// crypto-shred) é SALTADO; um carimbo de criação não-parseável é SALTADO fail-closed (sem idade
// fiável não se decide a expiração — nunca se expira por omissão). Um es nil devolve vazio.
func (s eventStoreRecordSource) List(ctx context.Context) ([]audit.ExpirableRecord, error) {
	if s.es == nil {
		return nil, nil
	}
	var out []audit.ExpirableRecord
	for _, stream := range s.es.Streams() {
		events, err := s.es.Read(ctx, stream, 1)
		if err != nil {
			return nil, err
		}
		for _, e := range events {
			if e.Type != replay.EventTypeCaptured {
				continue
			}
			var p capturedSubjectWire
			if err := json.Unmarshal(e.Payload, &p); err != nil {
				continue // payload não-projectável ⇒ salta (não há titular a expirar)
			}
			if p.SealedSubject == "" {
				continue // conteúdo não cifrado por-titular ⇒ sem KEK por-titular a crypto-shred
			}
			created, perr := time.Parse(time.RFC3339Nano, e.Ts)
			if perr != nil {
				continue // sem idade fiável ⇒ fail-closed (não expira)
			}
			out = append(out, audit.ExpirableRecord{
				ID:        e.EventID,
				Class:     audit.ClassPIIOperational,
				CreatedAt: created,
				SubjectID: p.SealedSubject,
				Partition: stream,
			})
		}
	}
	return out, nil
}

// cryptoShredSink é a [audit.ExpirationSink] do nó: MATERIALIZA a expiração por crypto-shred da
// KEK POR-TITULAR do vault de AOS-093 — a MESMA chave que o /dsar/erase destrói. Apagar a KEK
// torna o conteúdo do titular IRRECUPERÁVEL ([audit.OpenContent] ⇒ [audit.ErrDecrypt]) sem mutar
// a hash-chain. É IDEMPOTENTE (o Delete de uma chave ausente é no-op ⇒ nil), como o contrato do
// sink exige, pelo que reexecutar o job para o mesmo registo é seguro.
type cryptoShredSink struct {
	vault *audit.InMemoryKeyVault
}

// Expire implementa [audit.ExpirationSink]. Crypto-shred a KEK do titular (idempotente). Um
// registo SEM titular (rec.SubjectID vazio) é no-op: não há chave por-titular a destruir — é o
// residual nomeado (só o conteúdo cifrado por-titular é expirável por crypto-shred).
func (s cryptoShredSink) Expire(_ context.Context, rec audit.ExpirableRecord) error {
	if s.vault == nil || rec.SubjectID == "" {
		return nil
	}
	s.vault.Delete(rec.SubjectID)
	return nil
}

// Asserções de compile-time: os adaptadores do nó satisfazem as portas do [audit.ExpirationJob].
var (
	_ audit.RecordSource   = eventStoreRecordSource{}
	_ audit.ExpirationSink = cryptoShredSink{}
)
