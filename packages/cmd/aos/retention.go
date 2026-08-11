// AOS-213 (CON-02/DEF-903) — EXPIRAÇÃO POR TTL composta no nó. Fecha a lacuna que a Opção C do
// dono sequenciou para DEPOIS de o apagamento ser real: o núcleo do AOS-093 tornou o conteúdo
// dos runs cifrado POR-TITULAR e o /dsar/erase tornou-o irrecuperável, pelo que já HÁ apagamento
// real para EXPIRAR por retenção. Este ficheiro entrega as duas PORTAS que faltavam ao
// [audit.ExpirationJob] (AOS-092) para ele agir sobre o substrato REAL do nó:
//
//   - [eventStoreRecordSource] — a fonte de leitura ([audit.RecordSource]) que expõe os registos
//     CLASSIFICADOS do Event Store: cada evento cifrado por-titular (AOS-093) é um
//     [audit.ExpirableRecord] de classe pii_operational, com o TITULAR, a PARTIÇÃO (o stream do
//     run) e o carimbo de criação (o ts observacional do evento). NÃO lê o conteúdo (que é
//     opaco/cifrado) — só os metadados que decidem e auditam a expiração. Cobre as DUAS famílias
//     que selam sob a KEK por-titular: "replay.captured" (captura de não-determinismo, campo
//     sealed_subject) e "step.ledger.applied" (o Result.Payload memorizado pelo step-ledger,
//     campo subject — selado desde AOS-245; antes ia em claro e nada havia a expirar).
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

	"github.com/aos-ref/kernel/agent-runtime/durable"
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

// ledgerSubjectWire é a projecção MÍNIMA do payload de "step.ledger.applied" (AOS-245): o TITULAR
// sob cuja KEK o Result.Payload — o OUTPUT da tool call — foi cifrado. Nunca desserializa o
// resultado (opaco/cifrado). O campo é [durable.ledgerRecord].Subject.
type ledgerSubjectWire struct {
	Subject string `json:"subject"`
}

// subjectOf devolve o TITULAR de um evento CLASSIFICADO do Event Store, ou "" se o evento não for
// conteúdo cifrado por-titular. Cobre as DUAS famílias que selam sob a KEK por-titular de AOS-093:
// a captura de não-determinismo ("replay.captured": resposta do modelo + resultados de tools) e o
// step-ledger ("step.ledger.applied": o Result.Payload memorizado de cada passo, selado desde
// AOS-245). Enumerar só a primeira deixaria um titular cujo conteúdo vive apenas no ledger
// invisível à expiração por TTL — não porque o crypto-shred lhe não chegasse (a KEK é a MESMA e o
// /dsar/erase alcança ambos), mas porque o job nunca veria o registo que faz o relógio correr.
func subjectOf(e eventstore.Event) string {
	switch e.Type {
	case replay.EventTypeCaptured:
		var p capturedSubjectWire
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return "" // payload não-projectável ⇒ sem titular a expirar
		}
		return p.SealedSubject
	case durable.EventTypeLedgerApplied:
		var p ledgerSubjectWire
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return ""
		}
		return p.Subject
	default:
		return ""
	}
}

// List implementa [audit.RecordSource]. Percorre os streams do Event Store e, para cada evento
// CLASSIFICADO com um titular selado (ver [subjectOf]), produz um [audit.ExpirableRecord] de
// classe pii_operational. Um evento sem titular selado (conteúdo não cifrado por-titular ⇒ sem KEK
// a crypto-shred) é SALTADO; um carimbo de criação não-parseável é SALTADO fail-closed (sem idade
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
			subject := subjectOf(e)
			if subject == "" {
				continue // não-classificado ou não cifrado por-titular ⇒ sem KEK a crypto-shred
			}
			created, perr := time.Parse(time.RFC3339Nano, e.Ts)
			if perr != nil {
				continue // sem idade fiável ⇒ fail-closed (não expira)
			}
			out = append(out, audit.ExpirableRecord{
				ID:        e.EventID,
				Class:     audit.ClassPIIOperational,
				CreatedAt: created,
				SubjectID: subject,
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
//
// O vault é a PORTA [audit.KeyVault] (AOS-215/DEF-302): o crypto-shred da expiração corre pelo
// MESMO vault (injectado ou de referência) que o /dsar/erase, garantindo que a expiração destrói
// a KEK onde ela realmente vive (custódia externa quando injectada).
type cryptoShredSink struct {
	vault audit.KeyVault
}

// shredConfirmer é a porta OPCIONAL de VERIFICAÇÃO do crypto-shred. A porta [audit.KeyVault]
// não deixa o `Delete` devolver erro — mas nada obriga a IGNORAR o que a custódia sabe. Uma
// custódia que VERIFICA a destruição (AOS-249: o Vault Transit relê a chave e exige 404)
// implementa isto e responde, por titular, se a destruição ficou por confirmar.
//
// Quem não a implementa (o [audit.InMemoryKeyVault] de referência, onde apagar do mapa É a
// destruição) mantém o comportamento anterior — nenhuma custódia ganha um veredicto inventado.
type shredConfirmer interface {
	shredConfirmed(subjectID string) error
}

// shredPendingReporter é a porta OPCIONAL de CONTAGEM das destruições por confirmar (não por
// titular: quantas, no total, continuam pendentes). Alimenta o selo de desfecho do varrimento e
// a métrica de operação — nomear o eixo sem nomear titulares.
type shredPendingReporter interface {
	shredPending() int
}

// shredPendingOf devolve o número de destruições de KEK por confirmar, e se a custódia sabe
// sequer responder à pergunta.
func shredPendingOf(vault audit.KeyVault) (int, bool) {
	if r, ok := vault.(shredPendingReporter); ok {
		return r.shredPending(), true
	}
	return 0, false
}

// Expire implementa [audit.ExpirationSink]. Crypto-shred a KEK do titular (idempotente). Um
// registo SEM titular (rec.SubjectID vazio) é no-op: não há chave por-titular a destruir — é o
// residual nomeado (só o conteúdo cifrado por-titular é expirável por crypto-shred).
//
// FAIL-CLOSED DE AUDITORIA (remediação da W6). O sink devolvia `nil` SEMPRE — mesmo quando a
// custódia acabara de registar que a destruição NÃO foi confirmada. Essa informação existe
// desde AOS-249 ([vaultKeyVault.Delete] relê a chave Transit e exige 404) e era descartada por
// um sink que tem canal de erro na assinatura e não o usava. Com uma política Transit sem
// `deletion_allowed`, uma replicação ou um token sem autoridade, o desfecho era: `retention.expired`
// selado, `report.Expired++`, key de idempotência marcada — e a KEK viva, com o conteúdo
// decifrável. É a "afirmação FALSA de irrecuperabilidade — pior do que não apagar, porque é
// credível" que [ErrVaultShredUnconfirmed] existe para impedir.
//
// O erro devolvido NÃO desfaz a selagem (a WORM é append-only e o facto foi selado ANTES da
// destruição, por desenho): fá-lo AFLORAR. `POST /dsar/expire` passa a responder 500 em vez de
// 200, o varrimento agendado regista o incidente com o eixo nomeado e sela a contagem de
// pendentes, e a sonda de prontidão fica vermelha — a passagem deixa de ser silenciosa.
func (s cryptoShredSink) Expire(_ context.Context, rec audit.ExpirableRecord) error {
	if s.vault == nil || rec.SubjectID == "" {
		return nil
	}
	s.vault.Delete(rec.SubjectID)
	if c, ok := s.vault.(shredConfirmer); ok {
		return c.shredConfirmed(rec.SubjectID)
	}
	return nil
}

// Asserções de compile-time: os adaptadores do nó satisfazem as portas do [audit.ExpirationJob].
var (
	_ audit.RecordSource   = eventStoreRecordSource{}
	_ audit.ExpirationSink = cryptoShredSink{}
)
