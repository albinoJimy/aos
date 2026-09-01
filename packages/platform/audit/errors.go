package audit

import (
	"errors"
	"fmt"
)

// Sentinelas de erro do audit. Use errors.Is para ramificar. Todos os erros de
// adulteração detectados por [Verify]/[VerifyFromCheckpoint] desembrulham para
// [ErrTampered], permitindo um teste único "a cadeia foi adulterada?".
var (
	// ErrTampered é o sentinela-raiz de qualquer adulteração detectada na cadeia.
	ErrTampered = errors.New("audit: cadeia adulterada")
	// ErrInvalidRange — intervalo [from,to] inválido (from<1 ou to<from).
	ErrInvalidRange = errors.New("audit: intervalo invalido")
	// ErrRangeBeyondHead — o intervalo pedido excede o último audit_seq da partição.
	ErrRangeBeyondHead = errors.New("audit: intervalo alem do head da particao")
	// ErrUnknownPartition — partição sem registos.
	ErrUnknownPartition = errors.New("audit: particao desconhecida")
	// ErrCheckpointSignature — a assinatura do checkpoint não valida contra a
	// chave pública (âncora de confiança rejeitada).
	ErrCheckpointSignature = errors.New("audit: assinatura de checkpoint invalida")
	// ErrCheckpointAnchor — o EntryHash do checkpoint não corresponde ao registo
	// desse audit_seq na cadeia (âncora não ancora nada verificável).
	ErrCheckpointAnchor = errors.New("audit: ancora de checkpoint nao corresponde ao registo")
	// ErrCheckpointStale — o checkpoint apresentado sela um audit_seq INFERIOR ao
	// head que o verificador conhece de forma independente do store (ver
	// [VerifyFromCheckpointAtHead]). Sinaliza rollback de checkpoint: a
	// reapresentação de um checkpoint antigo, validamente assinado, para mascarar
	// truncatura do tail dos registos posteriores a ele.
	ErrCheckpointStale = errors.New("audit: checkpoint anterior ao head conhecido (rollback)")
	// ErrInvalidKey — material de chave ed25519 com dimensão inválida.
	ErrInvalidKey = errors.New("audit: chave ed25519 invalida")
	// ErrNoSubject — ingestão de PII sem SubjectID, ou shred sem titular. Sem
	// titular não há chave por-titular a que cifrar/apagar (fail-closed).
	ErrNoSubject = errors.New("audit: subject_id obrigatorio para PII")
	// ErrDecrypt — o payload cifrado não autentica sob a chave/blob dados
	// (adulteração do ciphertext, ou KEK errada). Fail-closed na decifragem.
	ErrDecrypt = errors.New("audit: decifragem de payload falhou")
	// ErrShredded — a chave por titular foi destruída (crypto-shredding): o payload
	// pessoal é IRRECUPERÁVEL (GDPR Art. 17). A cadeia mantém-se íntegra.
	ErrShredded = errors.New("audit: chave do titular destruida (payload irrecuperavel)")
	// ErrPayloadMissing — a referência não localiza ciphertext no PayloadStore (ref
	// vazia/desconhecida). Distinto de [ErrShredded]: aqui falta o blob, não a chave.
	ErrPayloadMissing = errors.New("audit: payload cifrado ausente")
	// ErrLegalHold — tentativa de shred/purge de um titular sob legal hold. A
	// obrigação de preservação SUSPENDE o apagamento (fail-closed), mesmo após a
	// retenção expirar.
	ErrLegalHold = errors.New("audit: titular sob legal hold (shred suspenso)")
	// ErrRetentionActive — purge automático pedido antes de a retenção da classe
	// expirar. O DSAR explícito ([Shredder.Shred]) não está sujeito a esta barreira.
	ErrRetentionActive = errors.New("audit: retencao ainda activa (purge negado)")
	// ErrNoSigner — [IngestPipeline.Seal] sem um Signer ligado (ver WithIngestSigner).
	ErrNoSigner = errors.New("audit: pipeline sem signer para selagem")
	// ErrInvalidRetentionConfig — a política de retenção (policy-as-code) é
	// malformada: versão SemVer inválida ou período de classe <= 0. Fail-closed: uma
	// config inválida é recusada em vez de aplicada silenciosamente (AOS-092/AC4).
	ErrInvalidRetentionConfig = errors.New("audit: configuracao de retencao invalida")
	// ErrNoExpirationSource — o [ExpirationJob] foi construído sem uma fonte de
	// registos classificados ([RecordSource]) ou sem um sink ([ExpirationSink]).
	ErrNoExpirationSource = errors.New("audit: job de expiracao sem fonte/sink de registos")
	// ErrPartitionsUnavailable — [VerifyStore] recebeu um [Store] que não implementa
	// [PartitionLister], pelo que não pode enumerar as partições a re-encadear. Fail-closed
	// (AOS-221): não se declara verificado o que não se pôde percorrer.
	ErrPartitionsUnavailable = errors.New("audit: store nao expoe as particoes (PartitionLister) para verificacao integral")
)

// TamperType classifica a natureza da adulteração detectada.
type TamperType string

const (
	// TamperMutation — um campo do registo foi alterado (EntryHash recalculado
	// diverge do armazenado).
	TamperMutation TamperType = "mutation"
	// TamperRemoval — um ou mais registos foram removidos (gap ascendente em
	// audit_seq, ou falta de registos até `to`).
	TamperRemoval TamperType = "removal"
	// TamperInsertion — um registo foi inserido/duplicado (audit_seq fora de
	// ordem ou repetido).
	TamperInsertion TamperType = "insertion"
	// TamperChainBroken — o PrevHash de um registo não corresponde ao EntryHash do
	// anterior (encadeamento quebrado; sintoma de inserção/remoção/mutação vizinha).
	TamperChainBroken TamperType = "chain_broken"
	// TamperUnknownSchema — o registo declara uma versão de formato que este binário
	// não sabe serializar, logo o seu conteúdo canónico não é computável e a
	// integridade NÃO é verificável. Fail-closed como qualquer outra adulteração, mas
	// com tipo PRÓPRIO: a causa provável é um binário mais ANTIGO do que o log (ou um
	// campo de versão corrompido), e reportá-lo como "mutação" mandaria o operador
	// procurar um adulterador onde há um problema de versões.
	TamperUnknownSchema TamperType = "unknown_schema"
	// TamperFork — DOIS registos legítimos disputam o mesmo audit_seq, cada um bem
	// formado: PrevHash encadeia no elo anterior e EntryHash recomputa. Não é o
	// sintoma de quem adultera um log — é o de dois ESCRITORES a partir da mesma
	// vista, que é o que duas réplicas são uma para a outra quando partilham partição
	// (AOS-284, medido).
	//
	// TIPO PRÓPRIO, pela mesma razão que [TamperUnknownSchema] o tem: reportar isto
	// como "insertion" manda o operador procurar um atacante onde há um defeito de
	// deployment. E o custo dessa confusão não é teórico — é a mensagem que ele lê
	// quando o nó SE RECUSA A ARRANCAR sobre o WORM bifurcado.
	//
	// O QUE ISTO NÃO É, e tem de ficar dito: prova de inocência. O EntryHash é um
	// hash e não um MAC, pelo que um adulterador com acesso ao ficheiro CONSEGUE
	// fabricar um ramo bem formado. O que se distingue são FORMAS, não intenções: a
	// forma de uma corrida é esta; a de uma adulteração desastrada é outra. Quem quer
	// prova de integridade contra um adversário usa o checkpoint ASSINADO — que
	// continua a ser a raiz de confiança e continua a verificar.
	TamperFork TamperType = "fork"
)

// VerifyError identifica o registo e o tipo de adulteração detectados na
// verificação da cadeia. Desembrulha para [ErrTampered].
type VerifyError struct {
	// Type é a natureza da adulteração.
	Type TamperType
	// Partition é a partição afectada.
	Partition string
	// Seq é o audit_seq do registo onde a adulteração foi detectada (ou o seq
	// esperado, no caso de remoção).
	Seq uint64
	// Detail descreve o sintoma concreto.
	Detail string
}

func (e *VerifyError) Error() string {
	// A PALAVRA IMPORTA, e é metade do AC2 do AOS-284: «adulteracao» manda chamar o
	// segurança; uma bifurcação manda arranjar a disciplina de partição do deployment.
	// Classificar bem e continuar a escrever a palavra errada não corrigiria nada — quem
	// lê o erro lê a frase, não o campo Type.
	if e.Type == TamperFork {
		return fmt.Sprintf("audit: cadeia BIFURCADA na particao %q, audit_seq=%d: %s",
			e.Partition, e.Seq, e.Detail)
	}
	return fmt.Sprintf("audit: adulteracao %s na particao %q, audit_seq=%d: %s",
		e.Type, e.Partition, e.Seq, e.Detail)
}

// Unwrap liga o VerifyError ao sentinela-raiz [ErrTampered].
func (e *VerifyError) Unwrap() error { return ErrTampered }

// ErrChainForked — sentinela para a BIFURCAÇÃO por escritores concorrentes ([TamperFork]).
//
// Acrescentado e não substituído: um erro de fork continua a desembrulhar para
// [ErrTampered], pelo que quem já testava «a cadeia é de confiar?» continua a ver falha e
// NADA enfraquece. Quem quiser AGIR sobre a causa — e a acção é diferente: arranjar a
// disciplina de partição, não chamar o segurança — testa este.
var ErrChainForked = errors.New("audit: cadeia bifurcada por escritores concorrentes")

// Is deixa um erro de fork casar TAMBÉM com [ErrChainForked], sem tocar no
// desembrulhamento para [ErrTampered] (devolver false aqui deixa o errors.Is seguir para
// o Unwrap). É o que permite acrescentar a distinção sem remover a garantia antiga.
func (e *VerifyError) Is(target error) bool {
	return target == ErrChainForked && e.Type == TamperFork
}

// tamper constrói um *VerifyError.
func tamper(t TamperType, partition string, seq uint64, detail string) *VerifyError {
	return &VerifyError{Type: t, Partition: partition, Seq: seq, Detail: detail}
}
