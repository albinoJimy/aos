// Package compaction impõe a ZONA 3 do layout cache-estável (AOS-060, tecnica/06
// §7, ADR-009): a compressão/sumarização de contexto corre SÓ EM CHECKPOINTS
// ASSÍNCRONOS, FORA DA HOT PATH da montagem do prompt. Comprimir de forma síncrona
// no turno reordena/muta o prompt, invalida a cache do prefixo e degrada custo e
// latência silenciosamente (o risco "cache thrash invisível").
//
// # Prova ESTRUTURAL off-hot-path
//
// A prova de que a compressão não corre na hot path é estrutural, não convencional:
//
//   - a API de MONTAGEM (cache/freeze [freeze.RunPrefix.Assemble] e a guarda
//     cache/layout [layout.Guard.Admit]) NÃO tem qualquer referência ao
//     [CheckpointCompactor] — não o pode invocar por construção;
//   - o compactor tem UM único ponto de entrada, [CheckpointCompactor.Compact], que
//     é uma operação de CHECKPOINT (chamada fora do turno, tipicamente como activity
//     durável ADR-001); o contador [CheckpointCompactor.Runs] prova por teste que
//     fica a 0 durante os turnos;
//   - o RESULTADO da compactação é um [agentruntime.TailSegment] — entra num turno
//     FUTURO como parte do TAIL, NUNCA reescrevendo o prefixo do run corrente. O
//     compactor sequer RECEBE o prefixo: só a região de tail anterior. A invariância
//     do prefixo é, por isso, estrutural (PrefixHashBefore == PrefixHashAfter sempre).
//
// # Compactação ADITIVA no run corrente (limitação semântica, não bug)
//
// A "compressão" da Zona 3 é ADITIVA dentro de um run: o segmento sumário produzido
// por [CheckpointCompactor.Compact] é ANEXADO ao tail append-only de um turno FUTURO
// — nunca substitui nem remove segmentos anteriores (o guard de layout impõe que o
// tail estende byte-a-byte o do turno anterior; remover/reescrever segmentos
// disparava KindTailShrunk/KindTailRewritten). Consequência: a compactação FAZ
// CRESCER o tail materializado do run corrente; NENHUM contexto/token é reclamado a
// MEIO do run.
//
// Isto é CONSISTENTE com as invariantes declaradas (o prefixo nunca muta; o tail só
// cresce) e NÃO é um bug de cache-thrash — o compactor sequer recebe o prefixo. A
// poupança REAL de contexto materializa-se apenas no PRÓXIMO congelamento/run (um
// novo RunPrefix cujo prefixo/baseline já incorpora o sumário em vez da região
// sumarizada). Uma redução de contexto IN-RUN exigiria um novo prefixo congelado
// (novo run), nunca uma reescrita do tail do run em curso — que a guarda proíbe.
//
// # Composição com o padrão de AOS-043
//
// Espelha a fronteira hot path vs. checkpoint da compressão assíncrona de memória
// (AOS-043, platform/memory/compression): aqui é a guarda equivalente no GW,
// mantendo o data-plane zero-dep (stdlib). A sumarização é uma FUNÇÃO PURA da origem
// + política versionada (determinista: mesma origem ⇒ mesmo sumário e mesmo Digest,
// sem time.Now/rand), preservando o replay fiel (ADR-010).
package compaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"sync/atomic"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
)

// ErrNoSource — Compact sem origem (runID vazio). Fail-closed.
var ErrNoSource = errors.New("compaction: origem de compactacao ausente (runID vazio)")

// tailSummaryKind é a natureza do segmento de tail produzido pela compactação. Vai
// no TAIL append-only de um turno futuro — nunca no prefixo.
const tailSummaryKind agentruntime.TailKind = "summary"

// digestPrefix é o prefixo do digest determinista do sumário ("sha256:<hex>").
const digestPrefix = "sha256:"

// Source é a ENTRADA de um checkpoint de compactação: o run/checkpoint e a região
// de TAIL anterior a sumarizar. NÃO inclui o prefixo — o compactor não o vê, pelo
// que não o pode mutar (invariância estrutural). PrefixHash é apenas a TESTEMUNHA
// observada do prefixo no enfileiramento, propagada ao resultado para o alerta de
// cache thrash (a compactação nunca a altera).
type Source struct {
	RunID        string
	CheckpointID string
	// Prior é a região de tail anterior (segmentos append-only) a sumarizar. O
	// compactor lê-a; o prefixo NÃO faz parte da origem.
	Prior []agentruntime.TailSegment
	// PrefixHash é a testemunha do prefixo imutável no momento do checkpoint.
	PrefixHash string
}

// Result é o RESULTADO de um checkpoint de compactação. Summary é o segmento de
// TAIL a injectar num turno FUTURO (nunca no prefixo). PrefixHashBefore/After são
// iguais por construção — o compactor não toca no prefixo — e servem o alerta de
// cache thrash (uma divergência denunciaria invalidação EXTERNA da cache, não
// causada pela compactação).
type Result struct {
	Summary          agentruntime.TailSegment
	Digest           string
	PrefixHashBefore string
	PrefixHashAfter  string
}

// PrefixInvariant confirma que o prefixo NÃO mudou através da compactação. É sempre
// verdadeiro por construção (o compactor não recebe nem devolve o prefixo); expô-lo
// torna a invariante testável e auto-documentada.
func (r Result) PrefixInvariant() bool { return r.PrefixHashBefore == r.PrefixHashAfter }

// Summarizer é a política de sumarização: uma FUNÇÃO PURA da região de tail anterior
// para os bytes do sumário. Determinista e versionável (mesma origem ⇒ mesmo
// sumário). Default: [DefaultSummarizer].
type Summarizer func(prior []agentruntime.TailSegment) []byte

// DefaultSummarizer é a política de referência determinista: um sumário estável que
// declara quantos segmentos e bytes de tail foram compactados. Não inspecciona o
// CONTEÚDO (que pode ser untrusted) — só a sua forma — evitando reintroduzir dados
// crus não higienizados. Produção injecta um sumarizador real (higienizado, ADR-005).
func DefaultSummarizer(prior []agentruntime.TailSegment) []byte {
	var total int
	for _, s := range prior {
		total += len(s.Content)
	}
	out := append([]byte("compacted segments="), strconv.Itoa(len(prior))...)
	out = append(out, " bytes="...)
	out = append(out, strconv.Itoa(total)...)
	return out
}

// CheckpointCompactor executa a compactação SÓ em checkpoints assíncronos. O
// contador de invocações [Runs] é a prova de que a hot path nunca o corre.
// Concorrente-seguro (o contador é atómico; a sumarização é pura).
type CheckpointCompactor struct {
	summarize Summarizer
	runs      atomic.Int64
}

// New constrói o compactor com a política de sumarização (nil ⇒ [DefaultSummarizer]).
func New(s Summarizer) *CheckpointCompactor {
	if s == nil {
		s = DefaultSummarizer
	}
	return &CheckpointCompactor{summarize: s}
}

// Runs devolve o número de compactações executadas. Um teste que corre N turnos da
// hot path SEM checkpoint prova que fica a 0 (a compressão não corre na montagem).
func (c *CheckpointCompactor) Runs() int64 { return c.runs.Load() }

// Compact é o ÚNICO ponto de entrada da compressão — uma operação de CHECKPOINT
// (fora do turno). Sumariza a região de tail anterior de forma determinista e
// devolve um segmento de TAIL para um turno FUTURO. NÃO recebe nem devolve o
// prefixo: a invariância do prefixo é estrutural (PrefixInvariant() sempre true).
// Fail-closed em origem inválida.
func (c *CheckpointCompactor) Compact(_ context.Context, src Source) (Result, error) {
	if src.RunID == "" {
		return Result{}, ErrNoSource
	}
	c.runs.Add(1)
	summary := c.summarize(src.Prior)
	sum := sha256.Sum256(summary)
	digest := digestPrefix + hex.EncodeToString(sum[:])
	return Result{
		Summary: agentruntime.TailSegment{
			Kind:    tailSummaryKind,
			Content: summary,
		},
		Digest:           digest,
		PrefixHashBefore: src.PrefixHash,
		// O compactor não toca no prefixo: o "depois" é IDÊNTICO ao "antes" por
		// construção. Não recalcula nada do prefixo — não o tem.
		PrefixHashAfter: src.PrefixHash,
	}, nil
}
