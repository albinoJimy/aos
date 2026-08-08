package audit

import (
	"encoding/binary"
	"sort"
	"time"
)

// Decision é o veredicto de responsabilização registado no audit. Espelha o
// Effect do Reference Monitor (permit→allow) num vocabulário estável de audit.
type Decision string

const (
	// DecisionAllow — a tool call foi autorizada (permit do RM).
	DecisionAllow Decision = "allow"
	// DecisionDeny — a tool call foi negada (fail-closed).
	DecisionDeny Decision = "deny"
	// DecisionEscalate — a tool call requereu gate humano.
	DecisionEscalate Decision = "escalate"
)

// DelegationHop é um elo da cadeia on-behalf-of do principal (raiz humana →
// agente actual). Espelha o modelo do RM/Event Store (ADR-003).
type DelegationHop struct {
	Sub   string
	ActAs string
}

// Principal é a identidade não-humana (NHI) responsável pela decisão auditada e
// a sua cadeia de delegação. Só metadados de responsabilização entram no audit;
// nunca segredos nem o payload da tool.
type Principal struct {
	NHIID           string
	DelegationChain []DelegationHop
}

// PayloadRef é a referência (opcional) ao payload pessoal associado à decisão.
// O payload NUNCA é gravado in-line: só o seu ContentHash (hash do ciphertext/
// metadados) entra na cadeia, mais a KeyRef (chave por titular) e o SubjectID.
//
// Crypto-shredding (EPIC-09): destruir a chave KeyRef torna o payload ilegível
// SEM quebrar a cadeia, porque o EntryHash sela o ContentHash — não o plaintext.
type PayloadRef struct {
	// ContentHash é o hash do payload cifrado (não o plaintext). Selado na cadeia.
	ContentHash []byte
	// KeyRef identifica a chave por titular no KMS (destruída num DSAR).
	KeyRef string
	// SubjectID é o titular dos dados pessoais (alvo do crypto-shredding).
	SubjectID string
}

// Resource identifica o alvo concreto de uma tool call mediada, selado na cadeia
// para que cada registo seja atribuível ao recurso sobre o qual a decisão incidiu
// (ADR-010/tecnica-13 §5). São metadados de responsabilização, nunca o payload.
type Resource struct {
	// Type ex.: "url", "file", "db".
	Type string
	// Value ex.: "https://api.example.com/orders" (identificador do recurso).
	Value string
	// Region ex.: "eu" (soberania de dados).
	Region string
}

// CallContext é o contexto de decisão selado na cadeia (taint, reversibilidade,
// sensibilidade) — a base factual sobre a qual a política decidiu.
type CallContext struct {
	// Taint ex.: "trusted", "untrusted".
	Taint string
	// Reversibility ex.: "reversible", "irreversible".
	Reversibility string
	// Sensitivity ex.: "public", "confidential".
	Sensitivity string
}

// Obligation é uma obrigação imposta pela política à decisão (ex.: redact_pii,
// ttl). Faz parte da prova de accountability do schema §5 (obligations JSONB) e é
// selada na cadeia para ser tamper-evident, não apenas registada no Event Store.
type Obligation struct {
	// Type ex.: "redact_pii", "audit", "ttl".
	Type string
	// Fields ex.: ["email", "phone"].
	Fields []string
	// Params parâmetros genéricos (ex.: {"seconds": "3600"}).
	Params map[string]string
}

// AuditRecord é a unidade append-only da hash-chain de audit (ADR-010). Os
// campos de conteúdo são preenchidos pelo produtor; AuditSeq, PrevHash e
// EntryHash são atribuídos por [Store.Append] no momento da escrita encadeada.
type AuditRecord struct {
	// AuditSeq é a ordem total monotónica DENTRO da partição (gapless, começa em 1).
	AuditSeq uint64
	// Partition é a fronteira de encadeamento (ex.: run_id, tenant/board ou "global").
	Partition string
	// Timestamp é observacional (não é fonte de ordem; a ordem é AuditSeq).
	Timestamp time.Time
	// SchemaVersion é a versão do formato do CONTEÚDO SELADO deste registo. É gravada
	// com ele e escolhe a serialização canónica na verificação — é o que permite
	// acrescentar campos ao selo sem invalidar tudo o que já foi selado (a hash-chain de
	// um WORM não se re-escreve; um registo antigo tem de continuar a verificar com as
	// regras da SUA época). Zero ⇒ [SchemaV2], o formato anterior a esta capacidade.
	// Atribuída por [Store.Append] quando o produtor a deixa vazia.
	SchemaVersion uint8
	// Decision é o veredicto (allow/deny/escalate).
	Decision Decision
	// Code, DeniedBy e Reason são a ATRIBUIÇÃO da decisão: o código de enumeração
	// fechada, o gate que a produziu e a explicação. Sem eles o log inviolável prova
	// QUE uma acção foi recusada mas não POR QUEM nem PORQUÊ — e uma recusa que não se
	// consegue atribuir obriga a bissectar o sistema com experiências de controlo para
	// responder à pergunta mais básica de uma auditoria.
	//
	// Não introduzem classe nova de exposição: o sink de mediação do Reference Monitor
	// para o Event Store já persiste os três (ver referencemonitor.mediationPayload), e
	// o WORM é a superfície MAIS protegida das duas (read-path soberano + hash-chain).
	// Vazios num allow, onde não há nada a atribuir.
	Code     string
	DeniedBy string
	Reason   string
	// Principal é a identidade responsável e a cadeia de delegação.
	Principal Principal
	// Capability é o direito escopado exercido/negado (ex.: "fs:write:/reports/*").
	Capability string
	// PolicyVersion é a versão assinada da policy-as-code em vigor na decisão.
	PolicyVersion string
	// RunID e StepID correlacionam o registo selado com a trajectória concreta
	// (schema §5: run_id/step_id). São selados no conteúdo, independentemente da
	// Partition (que pode ser custom, ex.: por tenant), para que a correlação
	// run_id/step_id seja ela própria tamper-evident.
	RunID  string
	StepID string
	// ParentStepID e RequestID completam a atribuição do registo ao passo pai e ao
	// pedido de mediação concretos.
	ParentStepID string
	RequestID    string
	// ToolID é a tool concreta mediada (ex.: "tool.http"); Resource é o alvo.
	ToolID string
	// Resource é o alvo concreto da tool call (ligação tamper-evident do registo
	// ao recurso, não apenas ao run+seq).
	Resource Resource
	// Context é o contexto de decisão (taint/reversibilidade/sensibilidade) selado.
	Context CallContext
	// Obligations são as obrigações impostas à decisão, seladas na cadeia (§5).
	Obligations []Obligation
	// PayloadRef é a referência opcional ao payload pessoal (nil se ausente).
	PayloadRef *PayloadRef
	// PrevHash é o EntryHash do registo anterior na partição (génese no primeiro).
	PrevHash []byte
	// EntryHash = SHA-256(PrevHash || conteúdo canónico). Atribuído em Append.
	EntryHash []byte
}

// canonicalDomain é um separador de domínio versionado prefixado à serialização
// canónica, para que hashes de audit nunca colidam com os de outro subsistema e
// para permitir evolução do formato sem ambiguidade. Passou a v2 quando o
// conteúdo selado passou a incluir a correlação (run_id/step_id/parent_step_id/
// request_id), o alvo (tool_id/resource), o contexto de decisão e as obligations
// (AOS-011). O bump garante que hashes v1 e v2 nunca são comparados como iguais.
const canonicalDomain = "aos.audit.v2"

// Versões do formato do conteúdo selado. Cada uma tem a SUA serialização canónica e o seu
// separador de domínio; um registo verifica-se sempre com as regras da versão com que foi
// escrito, que viaja no próprio registo ([AuditRecord.SchemaVersion]).
//
// PORQUE É POR-REGISTO E NÃO GLOBAL: um WORM é append-only — os selos antigos não se
// re-escrevem. Uma constante global de domínio só serve enquanto o formato nunca muda;
// mudá-la invalidaria de uma vez toda a cadeia já escrita e, como o arranque do nó
// RE-VERIFICA a hash-chain fail-closed, o nó deixaria de arrancar sobre o seu próprio log.
const (
	// SchemaV2 é o formato anterior à atribuição da decisão. É o default de um registo
	// sem versão gravada (tudo o que foi selado antes desta capacidade).
	SchemaV2 uint8 = 2
	// SchemaV3 acrescenta ao selo a ATRIBUIÇÃO: code, denied_by e reason.
	SchemaV3 uint8 = 3
	// CurrentSchemaVersion é a versão com que se selam registos NOVOS.
	CurrentSchemaVersion = SchemaV3
)

// canonicalDomainV3 é o separador de domínio do formato v3. Distinto do v2 para que os
// hashes das duas épocas nunca possam colidir nem ser comparados como iguais.
const canonicalDomainV3 = "aos.audit.v3"

// domainFor devolve o separador de domínio da versão de um registo. Uma versão
// desconhecida (formato de futuro, ou log corrompido) NÃO é adivinhada: devolve "" e o
// chamador trata-a como não-verificável — fail-closed, nunca "verifica" por omissão.
func domainFor(v uint8) string {
	switch v {
	case 0, SchemaV2:
		return canonicalDomain
	case SchemaV3:
		return canonicalDomainV3
	default:
		return ""
	}
}

// NOTA (audit_id — schema §5, deliberadamente adiado para EPIC-08): o schema
// canónico prevê um audit_id (ULID único por registo) além da ordem total
// (Partition, AuditSeq). No MVP in-memory a identidade por (Partition, AuditSeq)
// é suficiente e não se introduz aqui uma dependência de ULID nem um gerador; a
// atribuição/selagem de audit_id é adiada para EPIC-08 (persistência WORM real),
// altura em que entrará também em canonicalContent (novo bump de domínio).

// canonicalContent devolve a serialização canónica, determinística e estável
// cross-SO do CONTEÚDO do registo (todos os campos EXCEPTO PrevHash e EntryHash).
// O PrevHash entra no EntryHash pela concatenação (ver [ComputeEntryHash]), não
// aqui, cumprindo "H(prev_hash || conteúdo canónico)".
//
// Determinismo: ordem de campos fixa, inteiros em big-endian de largura fixa,
// strings e blobs com length-prefixing (uvarint). Não há mapas nem ordenação
// dependente de runtime — o mesmo conteúdo produz sempre os mesmos bytes.
func canonicalContent(rec AuditRecord) []byte {
	buf := make([]byte, 0, 256)
	buf = putString(buf, domainFor(rec.SchemaVersion))
	buf = putUint64(buf, rec.AuditSeq)
	buf = putString(buf, rec.Partition)
	// Timestamp observacional: UnixNano em UTC, largura fixa (determinístico,
	// sem componente monotónica nem fuso).
	buf = putInt64(buf, rec.Timestamp.UTC().UnixNano())
	buf = putString(buf, string(rec.Decision))
	buf = putString(buf, rec.Principal.NHIID)
	buf = putUint64(buf, uint64(len(rec.Principal.DelegationChain)))
	for _, h := range rec.Principal.DelegationChain {
		buf = putString(buf, h.Sub)
		buf = putString(buf, h.ActAs)
	}
	buf = putString(buf, rec.Capability)
	buf = putString(buf, rec.PolicyVersion)
	// Correlação (schema §5): run_id/step_id/parent_step_id/request_id.
	buf = putString(buf, rec.RunID)
	buf = putString(buf, rec.StepID)
	buf = putString(buf, rec.ParentStepID)
	buf = putString(buf, rec.RequestID)
	// Alvo concreto: tool_id e resource (type/value/region).
	buf = putString(buf, rec.ToolID)
	buf = putString(buf, rec.Resource.Type)
	buf = putString(buf, rec.Resource.Value)
	buf = putString(buf, rec.Resource.Region)
	// Contexto de decisão selado.
	buf = putString(buf, rec.Context.Taint)
	buf = putString(buf, rec.Context.Reversibility)
	buf = putString(buf, rec.Context.Sensitivity)
	// Obligations: contagem length-prefixed; para cada uma, Type, Fields (ordem
	// preservada do produtor) e Params ordenados por chave para determinismo
	// (mapas não têm ordem de iteração estável).
	buf = putUint64(buf, uint64(len(rec.Obligations)))
	for _, ob := range rec.Obligations {
		buf = putString(buf, ob.Type)
		buf = putUint64(buf, uint64(len(ob.Fields)))
		for _, f := range ob.Fields {
			buf = putString(buf, f)
		}
		buf = putUint64(buf, uint64(len(ob.Params)))
		keys := make([]string, 0, len(ob.Params))
		for k := range ob.Params {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			buf = putString(buf, k)
			buf = putString(buf, ob.Params[k])
		}
	}
	// PayloadRef opcional: byte de presença (0/1) evita ambiguidade entre "ausente"
	// e "presente-mas-vazio".
	if rec.PayloadRef == nil {
		buf = append(buf, 0)
	} else {
		buf = append(buf, 1)
		buf = putBytes(buf, rec.PayloadRef.ContentHash)
		buf = putString(buf, rec.PayloadRef.KeyRef)
		buf = putString(buf, rec.PayloadRef.SubjectID)
	}
	// v3 e seguintes: ATRIBUIÇÃO da decisão, no FIM e só nesta versão — um registo v2
	// produz exactamente os mesmos bytes de antes, e por isso continua a verificar.
	if rec.SchemaVersion >= SchemaV3 {
		buf = putString(buf, rec.Code)
		buf = putString(buf, rec.DeniedBy)
		buf = putString(buf, rec.Reason)
	}
	return buf
}

// putUint64 escreve um uint64 em big-endian (8 bytes, largura fixa).
func putUint64(buf []byte, v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return append(buf, b[:]...)
}

// putInt64 escreve um int64 em big-endian (8 bytes, largura fixa).
func putInt64(buf []byte, v int64) []byte {
	return putUint64(buf, uint64(v))
}

// putBytes escreve um blob com length-prefix (uvarint) seguido dos bytes.
func putBytes(buf []byte, b []byte) []byte {
	var lb [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(lb[:], uint64(len(b)))
	buf = append(buf, lb[:n]...)
	return append(buf, b...)
}

// putString escreve uma string com length-prefix (uvarint) seguido dos bytes.
func putString(buf []byte, s string) []byte {
	return putBytes(buf, []byte(s))
}
