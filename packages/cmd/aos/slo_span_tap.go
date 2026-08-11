package main

// AOS-274 (F8) — A TORNEIRA DE SPANS que dá DADOS REAIS ao avaliador de SLOs.
//
// O PROBLEMA QUE FECHA. `BuildDashboard`/`EvaluateAlerts`/`EvaluateOperationalAlerts` (AOS-085/
// 086/104/105) estavam correctos e completos — e só corriam em testes. O nó exportava spans para
// um colector OTLP e ficava sem NENHUMA agregação local: os SLOs viviam como documento, os
// runbooks (AOS-106) mapeavam alertas que ninguém produzia. Um avaliador precisa de uma FONTE, e
// a única fonte honesta são os spans que o nó REALMENTE emite.
//
// PORQUÊ UMA TORNEIRA E NÃO UM SEGUNDO PRODUTOR. A tentação seria instrumentar de novo o kernel
// para alimentar o avaliador — o que produziria uma segunda contabilidade, divergente da que o
// colector vê, e um SLI que ninguém consegue reconciliar com o trace. Esta torneira NÃO produz
// nada: intercepta a MESMA [otelgenai.SpanData] que já ia para o exportador, guarda-a num anel
// limitado e deixa-a seguir INALTERADA. O que o avaliador agrega é, byte a byte, o que o
// colector recebeu.
//
// FAIL-OPEN, DECLARADO (a excepção do nó). Tudo o resto neste binário é fail-closed: na dúvida
// nega-se, na dúvida não se arranca. AQUI NÃO. [sloTeeExporter.Export] devolve SEMPRE o erro do
// exportador PRIMÁRIO e NUNCA um erro seu; a torneira não valida, não recusa e não bloqueia. A
// razão é de risco, não de conveniência: a torneira está no caminho de End() de CADA span, ou
// seja, dentro de cada tool call mediada. Um erro (ou um bloqueio) desta camada propagar-se-ia
// para o caminho de execução e transformaria a observabilidade — a coisa que existe para EXPLICAR
// avarias — numa CAUSA de avarias. Observar não pode ser um modo de falha. Ver o comentário
// gémeo, com a mesma declaração, em slo_evaluator.go.
//
// LIMITES QUE SÃO POLÍTICA, NÃO ACIDENTE:
//
//   - ANEL DE CAPACIDADE FIXA. Um buffer que cresce com o tráfego seria um vector de OOM
//     conduzido pela carga — precisamente o incidente que o SLI deveria ajudar a diagnosticar. O
//     anel sobrescreve o mais ANTIGO: perde-se história, nunca memória.
//   - JANELA TEMPORAL na LEITURA. Reter os últimos N spans não basta: um nó ocioso continuaria a
//     alertar sobre spans de há horas, e o alerta deixaria de dizer alguma coisa sobre AGORA.
//     [sloSpanTap.snapshot] devolve só os spans que FECHARAM dentro da janela; fora dela o SLI
//     fica sem amostras e a semântica anti-vacuidade de AOS-085 (Samples==0 ⇒ nem breach nem
//     cumprimento afirmado) trata o resto.
//   - SEM SEGREDOS, por construção. Não se copia nada que os spans já não carreguem: a disciplina
//     de semconv (só ids/metadados/hashes, nunca payload — ADR-005) é herdada, não re-verificada.

import (
	"sync"

	otelgenai "github.com/aos-ref/substrate/otel-genai"
)

// defaultSLOTapCapacity é a profundidade do anel de spans retidos para agregação.
//
// 4096 é escolhido pelo pior caso do que se quer medir, não por gosto redondo: um run com
// `MaxTurns` no tecto emite ordem de dezenas de spans (invoke_agent + chat + execute_tool +
// audit_seal por passo), pelo que o anel cobre confortavelmente as dezenas de runs concorrentes
// que cabem no tecto de in-flight (AOS-277) dentro de uma janela de minutos. Acima disso o SLI
// passa a descrever a cauda mais recente — que é a leitura correcta de um percentil sob carga —
// em vez de mentir por omissão de memória.
const defaultSLOTapCapacity = 4096

// sloSpanTap é o anel limitado de [otelgenai.SpanData] que alimenta o avaliador. Seguro para uso
// concorrente: o End() de cada span corre na goroutine do run que o abriu, e a leitura corre na
// goroutine do laço de avaliação.
type sloSpanTap struct {
	mu   sync.Mutex
	buf  []otelgenai.SpanData
	next int  // índice da próxima escrita (posição mais antiga quando cheio)
	full bool // o anel já deu a volta

	// observed conta TODOS os spans que passaram pela torneira desde o arranque, incluindo os
	// que o anel já sobrescreveu. Existe para o operador distinguir "não há alertas porque o nó
	// está calmo" de "não há alertas porque a torneira nunca viu um span" — duas situações com
	// o mesmo output vazio e remédios opostos.
	observed uint64
}

// newSLOSpanTap constrói a torneira com a capacidade dada (<= 0 ⇒ [defaultSLOTapCapacity]).
func newSLOSpanTap(capacity int) *sloSpanTap {
	if capacity <= 0 {
		capacity = defaultSLOTapCapacity
	}
	return &sloSpanTap{buf: make([]otelgenai.SpanData, capacity)}
}

// record guarda um span no anel, sobrescrevendo o mais antigo quando cheio. Nunca falha.
func (t *sloSpanTap) record(spans []otelgenai.SpanData) {
	if t == nil || len(spans) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, sd := range spans {
		t.buf[t.next] = sd
		t.next++
		t.observed++
		if t.next == len(t.buf) {
			t.next = 0
			t.full = true
		}
	}
}

// snapshot devolve uma CÓPIA dos spans retidos que fecharam em `cutoffUnixNano` ou depois, por
// ordem cronológica de retenção. cutoffUnixNano <= 0 devolve tudo o que está no anel.
//
// Um span ainda ABERTO nunca chega aqui (a torneira só vê spans fechados, exportados no End()),
// mas um span com relógio ausente ([otelgenai.NoopClock] em teste) tem EndUnixNano == 0 e seria
// silenciosamente descartado pela janela. Retém-se: sem relógio, "fora da janela" não é uma
// afirmação que os dados suportem — e descartá-lo transformaria um teste determinista sem relógio
// num SLI vazio sem razão visível.
func (t *sloSpanTap) snapshot(cutoffUnixNano int64) []otelgenai.SpanData {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	n := t.next
	start := 0
	if t.full {
		n = len(t.buf)
		start = t.next
	}
	out := make([]otelgenai.SpanData, 0, n)
	for i := 0; i < n; i++ {
		sd := t.buf[(start+i)%len(t.buf)]
		if cutoffUnixNano > 0 && sd.EndUnixNano > 0 && sd.EndUnixNano < cutoffUnixNano {
			continue
		}
		out = append(out, sd)
	}
	return out
}

// observedTotal devolve quantos spans passaram pela torneira desde o arranque (incluindo os já
// sobrescritos). Ver o campo [sloSpanTap.observed] para a razão de existir.
func (t *sloSpanTap) observedTotal() uint64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.observed
}

// sloTeeExporter é o T no caminho de exportação: entrega ao exportador PRIMÁRIO (o OTLP do nó,
// AOS-173) e guarda uma cópia na torneira. A ordem é deliberada — o primário PRIMEIRO, para que
// nem o custo nem um eventual pânico da agregação local se interponham à telemetria que sai do
// nó.
type sloTeeExporter struct {
	primary otelgenai.Exporter
	tap     *sloSpanTap
}

// Export implementa [otelgenai.Exporter]. FAIL-OPEN por contrato: devolve EXCLUSIVAMENTE o erro
// do exportador primário — a torneira nunca inventa um erro que o chamador (o End() de um span,
// dentro de um run) teria de tratar. Ver o cabeçalho deste ficheiro para a justificação da
// excepção ao fail-closed do resto do binário.
func (e sloTeeExporter) Export(spans []otelgenai.SpanData) error {
	var err error
	if e.primary != nil {
		err = e.primary.Export(spans)
	}
	e.recordSafely(spans)
	return err
}

// recordSafely é a torneira sob RECOVER. Hoje `record` é lock + índice e a capacidade é
// protegida em [newSLOSpanTap], pelo que um pânico é inalcançável — mas o alcance de um pânico
// AQUI não é o laço de fundo: é o `End()` de um span, DENTRO do caminho de execução de um run.
// A declaração fail-open deste ficheiro ("a torneira nunca faz o run falhar") tem de valer por
// construção e não por inspecção, senão uma alteração futura ao agregador converte-se em
// derrube do run que estava a ser observado.
func (e sloTeeExporter) recordSafely(spans []otelgenai.SpanData) {
	defer func() { _ = recover() }()
	e.tap.record(spans)
}
