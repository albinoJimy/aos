package integration

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/aos-ref/platform/audit"
	memory "github.com/aos-ref/platform/memory"
	"github.com/aos-ref/platform/memory/domain"
	"github.com/aos-ref/platform/memory/provenance"
	otelgenai "github.com/aos-ref/substrate/otel-genai"
	"github.com/aos-ref/substrate/redaction"
)

// AOS-208 — LIGAÇÃO SUBSTANTIVA do motor de redacção (AOS-091) ao fecho transitivo
// do nó. O motor existia e era importado por três módulos de governação, mas NÃO
// entrava no fecho transitivo de packages/cmd/aos nem de packages/integration (só de
// cmd/aos-demo, via approval-card). O `doc.go` do motor prometia — desde AOS-188/195
// — que "quando esses módulos importarem o motor, usarão o mesmo Ingestor e a mesma
// política". Este ficheiro cumpre essa promessa: é o ponto de composição onde o MESMO
// [redaction.Ingestor] alimenta as quatro portas — Event Store, platform/memory,
// substrate/otel-genai e platform/audit — a partir de UMA única passagem de redacção,
// pelo que a consistência entre portas é ESTRUTURAL (há um só motor e uma só política),
// não uma coincidência de configuração.
//
// # A fronteira de ingestão
//
// O objectivo de um run é INPUT DO UTILIZADOR (trusted, mas potencialmente carregando
// PII em claro). [IngestionGateway.IngestObjective] é a fronteira onde a MINIMIZAÇÃO
// acontece ANTES de qualquer persistência (a "primeira camada da conformidade GDPR por
// desenho" do doc.go): o objectivo é redigido, e o valor REDIGIDO — nunca o cru — é o
// que alcança os quatro destinos e o que segue para o loop do run.
//
// # As quatro portas, UMA passagem
//
//   - Event Store + platform/memory: o objectivo redigido é escrito como um registo de
//     memória EPISÓDICA via [memory.Service] sobre o [adapters.EventStoreAdapter] — o
//     backend fonte-de-verdade que persiste cada escrita como evento append-only
//     `memory.record.written` no Event Store. Uma escrita satisfaz as duas portas.
//   - substrate/otel-genai: um span `aos.ingest.redacted` transporta o objectivo
//     redigido como atributo, exportado pela mesma porta [otelgenai.Tracer] do nó.
//   - platform/audit: um [audit.AuditRecord] sela o HASH do payload JÁ TRATADO na
//     hash-chain WORM (o audit sela o redigido, nunca o cru — doc.go §Integridade).
//
// # Falsificabilidade (sem capacidade-fantasma)
//
// O motor pode ser DESLIGADO (ingestor nil) — usado SÓ para a prova falsificável: com
// o motor desligado o objectivo EM CLARO alcança o Event Store e os spans, e a
// asserção de redacção falha. Em produção o [Bootstrap] injecta SEMPRE um ingestor
// (o default não deixa o motor inalcançável — é precisamente o defeito que AOS-208
// fecha), e o banner declara o estado ligado/desligado sem ambiguidade.

// ErrNoIngestionMemory — sem [memory.Service] não há porta de memória/Event Store.
var ErrNoIngestionMemory = errors.New("integration: ingestion gateway sem memory.Service (porta Event Store/memory)")

// ErrNoIngestionTracer — sem [otelgenai.Tracer] não há porta de spans.
var ErrNoIngestionTracer = errors.New("integration: ingestion gateway sem tracer (porta otel-genai)")

// ErrNoIngestionWORM — sem [audit.Store] não há porta de audit tamper-evident.
var ErrNoIngestionWORM = errors.New("integration: ingestion gateway sem WORM (porta platform/audit)")

// Operação e atributos do span de ingestão (semconv aos.*).
const (
	// opIngestRedacted é o nome do span da ingestão redigida.
	opIngestRedacted = "aos.ingest.redacted"
	// attrObjectiveRedacted transporta o objectivo JÁ REDIGIDO (nunca o cru).
	attrObjectiveRedacted = "aos.ingest.objective_redacted"
	// attrRedactionEnabled declara se o motor esteve ligado nesta ingestão.
	attrRedactionEnabled = "aos.redaction.enabled"
	// attrRedactionTokens é o nº de TokenRef produzidos (tokenização).
	attrRedactionTokens = "aos.redaction.token_count"
)

// ingestionSchemaVersion é a versão do schema do registo de memória escrito na
// ingestão (metadado obrigatório da fachada de memória).
const ingestionSchemaVersion = "1.0"

// IngestionGateway aplica o motor de redacção na fronteira de ingestão do nó e faz o
// fan-out do valor REDIGIDO para as quatro portas com o MESMO [redaction.Ingestor].
// Construir com [NewIngestionGateway]. Seguro para uso concorrente na medida em que os
// colaboradores subjacentes o são (o Ingestor é uma função pura; memory/tracer/WORM
// são concorrente-seguros).
type IngestionGateway struct {
	// ingestor é o MESMO motor+política partilhado por todas as portas. nil ⇒ motor
	// DESLIGADO (bypass em claro) — só para a prova falsificável; o Bootstrap injecta-o
	// sempre em produção.
	ingestor *redaction.Ingestor
	memory   *memory.Service
	tracer   otelgenai.Tracer
	worm     audit.Store
	now      func() time.Time
}

// IngestionOption configura o [IngestionGateway].
type IngestionOption func(*IngestionGateway)

// WithIngestionClock injecta o relógio do carimbo do registo de audit (determinismo em
// teste). nil é ignorado.
func WithIngestionClock(now func() time.Time) IngestionOption {
	return func(g *IngestionGateway) {
		if now != nil {
			g.now = now
		}
	}
}

// NewIngestionGateway compõe o gateway sobre as quatro portas. mem/tracer/worm são
// OBRIGATÓRIOS (fail-closed). ingestor pode ser nil (motor desligado — declarado pelo
// chamador e pelo banner); em produção o [Bootstrap] injecta-o sempre.
func NewIngestionGateway(ingestor *redaction.Ingestor, mem *memory.Service, tracer otelgenai.Tracer, worm audit.Store, opts ...IngestionOption) (*IngestionGateway, error) {
	switch {
	case mem == nil:
		return nil, ErrNoIngestionMemory
	case tracer == nil:
		return nil, ErrNoIngestionTracer
	case worm == nil:
		return nil, ErrNoIngestionWORM
	}
	g := &IngestionGateway{
		ingestor: ingestor,
		memory:   mem,
		tracer:   tracer,
		worm:     worm,
		now:      time.Now,
	}
	for _, o := range opts {
		o(g)
	}
	return g, nil
}

// Enabled indica se o motor de redacção está LIGADO neste gateway (ingestor != nil). O
// banner de arranque lê-o para declarar o estado sem ambiguidade.
func (g *IngestionGateway) Enabled() bool { return g.ingestor != nil }

// IngestedObjective é o resultado de uma ingestão: o objectivo TRATADO (pronto a seguir
// para o loop do run) e as coordenadas dos registos produzidos nas portas.
type IngestedObjective struct {
	// Redacted é o objectivo JÁ TRATADO (== objectivo cru quando o motor está desligado).
	Redacted string
	// Tokens são os [redaction.TokenRef] produzidos (vazio na política de minimização).
	Tokens []redaction.TokenRef
	// MemoryID é o ID do registo de memória episódica escrito (== RunID).
	MemoryID string
	// AuditSeq é a posição do registo selado na partição de audit da ingestão.
	AuditSeq uint64
}

// IngestObjective redige o OBJECTIVO do run (input do utilizador) e faz o fan-out do
// valor REDIGIDO para as quatro portas, devolvendo o objectivo tratado para o loop do
// run o usar. É a fronteira de minimização: com o motor ligado, nenhuma PII em claro
// alcança o Event Store, os spans ou o audit; com o motor desligado (bypass), o valor
// cru alcança-os — é o pivot da prova falsificável.
//
// subject é o identificador usado como KeyRef de tokenização e como
// [audit.PayloadRef.SubjectID]; agentID é a NHI produtora; runID correlaciona todos os
// registos. Fail-closed: uma falha de redacção OU de escrita numa porta aborta a
// ingestão (o run não arranca sobre uma minimização parcial).
//
// ESCOPO AOS-208 — semântica de `subject`: o único chamador de produção ([NodeService]
// em cmd/aos) passa aqui o PRINCIPAL DO RUN (a NHI do AGENTE que corre o run), NÃO o
// titular dos dados (GDPR data subject) cujos dados possam aparecer no objectivo. Com a
// política de MINIMIZAÇÃO ([redaction.RemoveAllPolicy], sem KeySource nem tokenização —
// a política que o Bootstrap injecta) isto é inócuo: `subject` só alimenta o metadado
// SubjectID do registo de audit selado, e a evidência de redacção fica indexada pela NHI
// do run. AVISO para evolução: se a política passar a TOKENIZAR (KeySource por-titular),
// a KeyRef derivada de `subject` ficaria ancorada no AGENTE e não no titular — minando o
// crypto-shredding por-titular (RGPD Art. 17). Antes de habilitar tokenização, derivar/
// propagar o titular REAL dos dados até aqui e passá-lo como `subject`.
func (g *IngestionGateway) IngestObjective(ctx context.Context, subject, runID, agentID, objective string) (IngestedObjective, error) {
	if runID == "" {
		return IngestedObjective{}, errors.New("integration: ingestao sem runID")
	}

	// (1) REDACÇÃO na fronteira — o MESMO Ingestor+política para todas as portas. Com o
	// motor desligado (ingestor nil) o valor segue EM CLARO (bypass declarado).
	redacted := objective
	var tokens []redaction.TokenRef
	if g.ingestor != nil {
		ing, err := g.ingestor.UserInput(subject, objective)
		if err != nil {
			return IngestedObjective{}, fmt.Errorf("integration: redaccao do objectivo (AOS-091): %w", err)
		}
		s, ok := ing.Payload.(string)
		if !ok {
			return IngestedObjective{}, fmt.Errorf("integration: payload redigido de tipo inesperado %T (esperado string)", ing.Payload)
		}
		redacted = s
		tokens = ing.Tokens
	}

	// (2) SPAN (porta otel-genai): transporta o objectivo REDIGIDO como atributo. O
	// SpanTracer exporta-o em End() pela MESMA porta do nó; o NoopTracer descarta-o.
	_, span := g.tracer.StartSpan(ctx, opIngestRedacted)
	span.SetAttribute(otelgenai.AttrOperationName, opIngestRedacted)
	span.SetAttribute(otelgenai.AttrRunID, runID)
	span.SetAttribute(otelgenai.AttrPrincipalNHI, agentID)
	span.SetAttribute(attrObjectiveRedacted, redacted)
	span.SetAttribute(attrRedactionEnabled, g.ingestor != nil)
	span.SetAttribute(attrRedactionTokens, len(tokens))
	defer span.End()

	// (3) MEMÓRIA + EVENT STORE (uma escrita, duas portas): o objectivo redigido é o Goal
	// de um registo de memória EPISÓDICA. O [memory.Service] sobre o EventStoreAdapter
	// persiste-o como `memory.record.written` no Event Store — a leitura do stream
	// `memory.episodic` prova o valor redigido no Event Store.
	body := domain.EpisodicBody{
		TraceID: runID,
		Goal:    redacted,
		Outcome: "ingested",
	}
	meta := domain.Metadata{
		AgentID:       agentID,
		RunID:         runID,
		TTLClass:      domain.TTLPermanent,
		SchemaVersion: ingestionSchemaVersion,
	}
	rec, err := g.memory.Put(ctx, domain.Record{
		ID:       runID,
		Class:    domain.ClassEpisodic,
		Metadata: meta,
		Body:     body,
	}, provenance.SourceAuthenticatedUser)
	if err != nil {
		return IngestedObjective{}, fmt.Errorf("integration: ingestao em memoria/event store: %w", err)
	}

	// (4) AUDIT (porta platform/audit): sela o HASH do payload JÁ TRATADO na hash-chain
	// WORM — nunca o cru (doc.go §Integridade). A partição é DEDICADA (não colide com a
	// cadeia de mediação do run, cuja partição é o próprio RunID).
	sum := sha256.Sum256([]byte(redacted))
	sealed, err := g.worm.Append(ctx, audit.AuditRecord{
		Partition:  "ingestion:" + runID,
		Timestamp:  g.now(),
		Decision:   audit.DecisionAllow,
		Principal:  audit.Principal{NHIID: agentID},
		Capability: "redact:pii",
		RunID:      runID,
		StepID:     "ingest",
		Obligations: []audit.Obligation{{
			Type:   "redact_pii",
			Params: map[string]string{"enabled": boolText(g.ingestor != nil)},
		}},
		PayloadRef: &audit.PayloadRef{
			ContentHash: sum[:],
			SubjectID:   subject,
		},
	})
	if err != nil {
		return IngestedObjective{}, fmt.Errorf("integration: selagem de audit da ingestao: %w", err)
	}

	return IngestedObjective{
		Redacted: redacted,
		Tokens:   tokens,
		MemoryID: rec.ID,
		AuditSeq: sealed.AuditSeq,
	}, nil
}

// boolText devolve o texto estável de um bool para o parâmetro da obrigação de audit.
func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
