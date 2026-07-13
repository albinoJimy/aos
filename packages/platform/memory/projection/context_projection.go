package projection

import (
	"strconv"
	"strings"

	"github.com/aos-ref/platform/memory/record"
)

// InjectedView é a VISTA INJECTADA NO MODELO: o resumo higienizado e limitado em
// tokens que o sub-agente entrega ao contexto do pai. É deliberadamente pobre —
// leva Summary por turno até ao orçamento, NUNCA o conteúdo cru nem a árvore de
// spans (essa vai para o backend pela via record.Persist). Está ligada ao trace por
// TraceID, para o pai correlacionar o resumo com a trajectória completa no backend.
type InjectedView struct {
	// TraceID liga o resumo à trajectória completa no backend (EPIC-08).
	TraceID string
	// PolicyVersion é a versão da política que produziu esta injecção (reprodutibilidade).
	PolicyVersion string
	// Separator é o separador comportamental aplicado entre resumos de turnos. É um
	// parâmetro da política que MUDA a injecção byte-a-byte sem, por si só, mudar a
	// PolicyVersion; por isso é carimbado no artefacto de reprodutibilidade (Bytes),
	// para que a injecção não dependa de um parâmetro sem rasto.
	Separator string
	// TokenBudget é o tecto de tokens aplicado.
	TokenBudget int
	// TokenCount é a estimativa de tokens do Summary (sempre <= TokenBudget).
	TokenCount int
	// IncludedTurns é o nº de turnos cujo resumo coube no orçamento.
	IncludedTurns int
	// TotalTurns é o nº total de turnos da trajectória (>= IncludedTurns). Se
	// IncludedTurns < TotalTurns, houve descarte de CONTEXTO — legítimo; o registo
	// mantém os TotalTurns.
	TotalTurns int
	// Summary é o resumo concatenado e higienizado, dentro do orçamento.
	Summary string
}

// ProjectContext é a via de PROJECÇÃO: project_context(record) -> injected_view.
// Produz o que o modelo vê a partir de uma vista READ-ONLY do registo — resumo
// higienizado, limitado ao orçamento de tokens da política. É IMPOSSÍVEL, a nível
// de tipo, apagar/mutar o registo daqui: view é uma record.RecordView, cujo
// conjunto de métodos não inclui qualquer escrita.
//
// Determinística e reproduzível: sem time.Now/rand; a estimativa de tokens é função
// pura do texto; a ordem dos turnos é a de registo. A mesma trajectória + a mesma
// política produzem a MESMA InjectedView (e o mesmo Bytes()).
//
// Fail-closed: view nil ou política inválida devolvem erro, nunca uma injecção
// silenciosa.
func ProjectContext(view record.RecordView, policy Policy) (InjectedView, error) {
	if view == nil {
		return InjectedView{}, ErrNilView
	}
	if err := policy.Validate(); err != nil {
		return InjectedView{}, err
	}

	summaries := view.TurnSummaries()

	var b strings.Builder
	tokens := 0
	included := 0
	sep := policy.Separator
	for _, ts := range summaries {
		piece := ts.Summary
		// Custo em tokens do próximo fragmento (mais o separador, se não for o 1º).
		cost := estimateTokens(piece)
		if included > 0 {
			cost += estimateTokens(sep)
		}
		if tokens+cost > policy.TokenBudget {
			// Este turno não cabe no CONTEXTO — descarta-se do resumo (legítimo) e
			// PROSSEGUE-SE para os seguintes, que podem caber. Um `break` aqui deixaria
			// um único turno grande no início a suprimir todos os posteriores que
			// caberiam, subutilizando o orçamento. A ordem de registo é preservada
			// entre os turnos incluídos (podendo saltar os que não cabem); o registo
			// mantém todos e a via persist emite-os na íntegra. Determinístico: a
			// decisão de inclusão é função pura do texto e do orçamento.
			continue
		}
		if included > 0 {
			b.WriteString(sep)
		}
		b.WriteString(piece)
		tokens += cost
		included++
	}

	return InjectedView{
		TraceID:       view.TraceID(),
		PolicyVersion: policy.Version,
		Separator:     policy.Separator,
		TokenBudget:   policy.TokenBudget,
		TokenCount:    tokens,
		IncludedTurns: included,
		TotalTurns:    len(summaries),
		Summary:       b.String(),
	}, nil
}

// Bytes serializa a InjectedView de forma ESTÁVEL e determinística (byte-a-byte
// reproduzível). É o artefacto que a reprodutibilidade compara: a mesma trajectória
// + a mesma política -> os mesmos bytes. Layout de texto simples, sem mapas.
func (v InjectedView) Bytes() []byte {
	var b strings.Builder
	b.WriteString("trace_id=")
	b.WriteString(v.TraceID)
	b.WriteString("\npolicy_version=")
	b.WriteString(v.PolicyVersion)
	// Separador carimbado (quoted) para manter o campo numa única linha e sem
	// ambiguidade — mesmo quando é "\n" — garantindo que o artefacto de
	// reprodutibilidade rastreia este parâmetro comportamental da política.
	b.WriteString("\nseparator=")
	b.WriteString(strconv.Quote(v.Separator))
	b.WriteString("\ntoken_budget=")
	b.WriteString(strconv.Itoa(v.TokenBudget))
	b.WriteString("\ntoken_count=")
	b.WriteString(strconv.Itoa(v.TokenCount))
	b.WriteString("\nincluded_turns=")
	b.WriteString(strconv.Itoa(v.IncludedTurns))
	b.WriteString("\ntotal_turns=")
	b.WriteString(strconv.Itoa(v.TotalTurns))
	b.WriteString("\nsummary=\n")
	b.WriteString(v.Summary)
	return []byte(b.String())
}

// estimateTokens é uma estimativa DETERMINÍSTICA de tokens de um texto. Aproxima
// por palavras (campos separados por espaços em branco), com um mínimo por
// caracteres para textos sem espaços — uma heurística pura, sem estado nem
// aleatoriedade. A contagem exacta por modelo é de EPIC-06 (Model Gateway); aqui só
// precisamos de um limite reproduzível e monótono no comprimento.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	words := len(strings.Fields(s))
	// Piso por caracteres (~4 chars/token) para conteúdo denso/sem espaços.
	chars := (len([]rune(s)) + 3) / 4
	if words > chars {
		return words
	}
	return chars
}
