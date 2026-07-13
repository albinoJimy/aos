package record

// RecordView é a VISTA READ-ONLY do registo de trajectória entregue à camada de
// projecção. É o coração da barreira arquitectural do Princípio 4 (AOS-036): expõe
// EXCLUSIVAMENTE métodos de LEITURA — não há AppendTurn, AppendSpan, Delete, Mutate
// nem Persist. A segregação é a NÍVEL DE TIPO, não por convenção:
//
//   - a função de projecção (projection.ProjectContext) recebe um RecordView e, por
//     isso, o seu conjunto de métodos NÃO inclui qualquer operação de escrita — uma
//     tentativa de `view.AppendTurn(...)` ou `view.Delete(...)` FALHA A COMPILAR
//     (erro de tipo), não em runtime;
//   - [View] devolve um wrapper de tipo NÃO exportado ([readOnlyRecord]), pelo que
//     nem sequer um type-assertion `view.(*TrajectoryRecord)` recupera o registo
//     mutável — a fuga por asserção está fechada.
//
// A projecção vê apenas o material higienizado (o Summary e o manifesto por turno),
// nunca o RawContent completo nem a árvore de spans: descartar/truncar isto na
// projecção é economia legítima; o registo completo continua intacto na via persist.
type RecordView interface {
	// TraceID liga a trajectória ao trace (para ligar o resumo ao backend).
	TraceID() string
	// TurnCount é o nº de turnos da trajectória.
	TurnCount() int
	// TurnSummaries devolve, por ordem de registo, a vista higienizada por turno
	// (índice, resumo e manifesto por turno) — SEM o conteúdo cru.
	TurnSummaries() []TurnSummary
	// SpanCount é o nº de nós de span da trajectória COMPLETA no registo (a
	// projecção pode observá-lo, mas não os spans em si — vão para o backend).
	SpanCount() int

	// isRecordView é um método NÃO exportado que veda a interface: só tipos deste
	// pacote a podem satisfazer, impedindo implementações externas que contornem a
	// barreira.
	isRecordView()
}

// TurnSummary é a projecção read-only de um turno: o resumo higienizado e o
// manifesto por turno (prompt_hash/model-id/versões). Deliberadamente NÃO carrega
// RawContent — a projecção não tem acesso ao conteúdo cru do registo.
type TurnSummary struct {
	Index                 int
	Summary               string
	PromptHash            string
	ModelID               string
	AssemblyVersion       string
	ManifestSchemaVersion string
}

// readOnlyRecord é o wrapper NÃO exportado devolvido por [View]. Detém um ponteiro
// para o registo mas SÓ expõe métodos de leitura. Por ser de tipo não exportado, um
// consumidor da projecção não consegue reconvertê-lo para *TrajectoryRecord.
type readOnlyRecord struct {
	rec *TrajectoryRecord
}

// View devolve a vista read-only do registo, própria para entregar à projecção.
//
// Fail-closed no ponto de entrada: se rec for nil, devolve uma RecordView nil (e
// NÃO um readOnlyRecord{rec: nil}). Embrulhar um ponteiro nil num valor de interface
// produziria uma interface typed-nil (NÃO-nil) que ATRAVESSARIA o guarda `view == nil`
// da projecção e provocaria um nil-deref em TurnSummaries — um fail-OPEN. Guardando o
// nil aqui, a única via de construção da vista, ProjectContext(View(nil), pol) rejeita
// com ErrNilView como prometido, em vez de entrar em panic.
func View(rec *TrajectoryRecord) RecordView {
	if rec == nil {
		return nil
	}
	return readOnlyRecord{rec: rec}
}

func (v readOnlyRecord) isRecordView()   {}
func (v readOnlyRecord) TraceID() string { return v.rec.traceID }
func (v readOnlyRecord) TurnCount() int  { return len(v.rec.turns) }
func (v readOnlyRecord) SpanCount() int  { return len(v.rec.spans) }

// TurnSummaries projecta cada turno para a sua vista read-only. Devolve cópias
// (nunca partilha o mapa Params nem expõe RawContent), preservando a ordem de
// registo (determinismo).
func (v readOnlyRecord) TurnSummaries() []TurnSummary {
	out := make([]TurnSummary, len(v.rec.turns))
	for i, t := range v.rec.turns {
		out[i] = TurnSummary{
			Index:                 t.Index,
			Summary:               t.Summary,
			PromptHash:            t.PromptHash,
			ModelID:               t.ModelID,
			AssemblyVersion:       t.AssemblyVersion,
			ManifestSchemaVersion: t.ManifestSchemaVersion,
		}
	}
	return out
}
