package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"

	"github.com/aos-ref/substrate/eventstore"
)

// Substrato abre n handles INDEPENDENTES sobre o MESMO log — escritores distintos do
// ponto de vista do substrato, que é o que dois processos são um para o outro. Devolve
// os handles, a função que os liberta e um erro se não conseguir abri-los.
//
// O contrato é exigente de propósito: se a fábrica devolver n vezes o MESMO handle (ou
// n handles que partilhem estado em memória por construção), as sondas medem o
// interior de um processo e passam sempre. [Sondar] não consegue detectar essa fraude —
// quem escreve a fábrica é quem garante a independência.
//
// # Cada chamada devolve um substrato LIMPO
//
// [Sondar] chama a fábrica UMA VEZ POR SONDA e exige que cada chamada entregue um
// substrato em estado conhecido-vazio. Não é zelo: MEDIDO a 2026-08-31, a sonda do CAS
// deixa o log do Event Store de referência com DOIS registos no mesmo seq, e o `Open`
// seguinte RECUSA-O inteiro com E_RESTORE_ORDER — sem isolamento, a primeira sonda
// torna todas as seguintes inconclusivas. Um substrato partilhado entre sondas mede a
// contaminação, não a propriedade.
type Substrato func(n int) (handles []eventstore.EventStore, largar func(), err error)

// Resultado é o veredicto de uma sonda. Detalhe está SEMPRE preenchido: um veredicto
// sem o que foi observado não é verificável e o repo já pagou por afirmações assim.
type Resultado struct {
	// Sonda é o nome estável da propriedade medida.
	Sonda string
	// Tem indica se o substrato TEM a propriedade. Só tem significado com Erro nil.
	Tem bool
	// Detalhe descreve o que foi OBSERVADO (seqs, estados, erros), não o que se
	// concluiu — para que a conclusão possa ser contestada a partir do registo.
	Detalhe string
	// Erro sinaliza uma sonda INCONCLUSIVA: não chegou a medir. Uma sonda
	// inconclusiva nunca é «ausência» — confundir as duas foi o erro de método que
	// a auditoria de 2026-08-31 registou.
	Erro error
}

// sonda é uma medição individual. Recebe handles já abertos e o stream isolado onde
// deve trabalhar (isolado para que as sondas não interfiram entre si).
type sonda struct {
	nome    string
	handles int // quantos handles independentes precisa
	medir   func(hs []eventstore.EventStore, stream string) Resultado
}

// sondas é o conjunto mínimo. Cada uma falha SOZINHA num substrato plausível — ver o
// último parágrafo do doc do pacote.
var sondas = []sonda{
	{"visibilidade-entre-handles", 2, medirVisibilidade},
	{"cas-entre-handles", 2, medirCAS},
	{"dedup-entre-handles", 2, medirDedup},
	{"corrida-um-so-vencedor", 4, medirCorrida},
}

// Sondar corre todas as sondas contra o substrato e devolve o relatório SEM falhar.
// É a via para um SENSOR: um teste que asserta a ausência de hoje e acusa o dia em que
// ela desaparecer. Para o gate de um backend candidato usa-se [RunArbitragem].
func Sondar(s Substrato) []Resultado {
	out := make([]Resultado, 0, len(sondas))
	for i, sd := range sondas {
		hs, largar, err := s(sd.handles)
		if err != nil {
			out = append(out, Resultado{Sonda: sd.nome, Detalhe: "substrato não abriu",
				Erro: fmt.Errorf("abrir %d handles: %w", sd.handles, err)})
			continue
		}
		if len(hs) < sd.handles {
			largar()
			out = append(out, Resultado{Sonda: sd.nome, Detalhe: fmt.Sprintf("recebi %d handles", len(hs)),
				Erro: fmt.Errorf("substrato devolveu %d handles, a sonda precisa de %d", len(hs), sd.handles)})
			continue
		}
		r := sd.medir(hs, fmt.Sprintf("sonda-%d-%s", i, sd.nome))
		r.Sonda = sd.nome
		largar()
		out = append(out, r)
	}
	return out
}

// RunArbitragem é o GATE do AOS-100: falha se qualquer sonda acusar ausência ou ficar
// inconclusiva. Um substrato que passe aqui tem a propriedade de que dependem o
// LeaseManager, o FencedAppender e a composição de ADR-023.
func RunArbitragem(t *testing.T, s Substrato) {
	t.Helper()
	for _, r := range Sondar(s) {
		switch {
		case r.Erro != nil:
			t.Errorf("[%s] INCONCLUSIVA: %v — %s\n"+
				"Uma sonda que não mediu NÃO é uma propriedade ausente, e também não é uma presente: "+
				"o substrato não pode ser aceite com base nela.", r.Sonda, r.Erro, r.Detalhe)
		case !r.Tem:
			t.Errorf("[%s] AUSENTE — %s\n"+
				"O substrato NÃO arbitra entre escritores independentes. Toda a disciplina de posse "+
				"(LeaseManager/AOS-018, FencedAppender, ADR-023) é condicional a esta propriedade e "+
				"não pode assentar neste substrato.", r.Sonda, r.Detalhe)
		default:
			t.Logf("[%s] presente — %s", r.Sonda, r.Detalhe)
		}
	}
}

// --- Sondas ----------------------------------------------------------------

// medirVisibilidade: o que A escreveu, B lê. É a raiz de tudo — um substrato onde cada
// handle tem a SUA cabeça falha aqui antes de chegar ao CAS.
func medirVisibilidade(hs []eventstore.EventStore, stream string) Resultado {
	ctx := context.Background()
	a, b := hs[0], hs[1]

	res, err := a.Append(ctx, stream, facto(TipoVisibilidade, "a"))
	if err != nil {
		return Resultado{Detalhe: "o primeiro escritor não conseguiu escrever", Erro: fmt.Errorf("A.Append: %w", err)}
	}

	evs, err := b.Read(ctx, stream, 1)
	switch {
	case errors.Is(err, eventstore.ErrStreamNotFound):
		return Resultado{Tem: false, Detalhe: fmt.Sprintf(
			"A escreveu seq=%d; B lê o mesmo stream e vê E_STREAM_NOT_FOUND — cada handle tem a sua cabeça", res.Seq)}
	case err != nil:
		return Resultado{Detalhe: "B não conseguiu ler", Erro: fmt.Errorf("B.Read: %w", err)}
	case len(evs) == 0:
		return Resultado{Tem: false, Detalhe: fmt.Sprintf(
			"A escreveu seq=%d; B lê o mesmo stream e vê 0 eventos", res.Seq)}
	}
	return Resultado{Tem: true, Detalhe: fmt.Sprintf(
		"A escreveu seq=%d; B lê %d evento(s), o primeiro em seq=%d", res.Seq, len(evs), evs[0].Seq)}
}

// medirCAS: dois escritores afirmam o MESMO expected_seq. Exactamente um pode ganhar; o
// outro tem de ver E_SEQ_CONFLICT. É a propriedade que o DEF-282 mediu ausente.
func medirCAS(hs []eventstore.EventStore, stream string) Resultado {
	ctx := context.Background()
	a, b := hs[0], hs[1]

	primeiro, err := a.Append(ctx, stream, facto(TipoCAS, "a"), eventstore.WithExpectedSeq(0))
	if err != nil {
		return Resultado{Detalhe: "o primeiro CAS não passou num stream vazio", Erro: fmt.Errorf("A.Append: %w", err)}
	}

	segundo, err := b.Append(ctx, stream, facto(TipoCAS, "b"), eventstore.WithExpectedSeq(0))
	switch {
	case eRecusaDeCAS(err):
		return Resultado{Tem: true, Detalhe: fmt.Sprintf(
			"A ficou com seq=%d; B afirmou o mesmo expected_seq=0 e foi recusado com %v", primeiro.Seq, err)}
	case err != nil:
		return Resultado{Detalhe: "B falhou com um erro que não é uma recusa de CAS",
			Erro: fmt.Errorf("B.Append: quero E_APPEND_ONLY_VIOLATION ou E_SEQ_CONFLICT, veio %w", err)}
	}
	return Resultado{Tem: false, Detalhe: fmt.Sprintf(
		"A e B afirmaram AMBOS expected_seq=0 e AMBOS ficaram committed (seq=%d e seq=%d) — "+
			"o expected_seq não é atómico entre escritores", primeiro.Seq, segundo.Seq)}
}

// medirDedup: a mesma idempotency_key (run_id:step_id) vista por outro escritor tem de
// devolver StatusDuplicate com o seq ORIGINAL.
//
// Esta sonda é a que apanha um backend cuja deduplicação é uma JANELA temporal em vez de
// um índice permanente: passa enquanto a janela durar e volta a duplicar depois. Um
// StatusDuplicate aqui é condição NECESSÁRIA, não suficiente — a suficiência exige medir
// também para lá da janela, e isso não se mede num teste rápido.
func medirDedup(hs []eventstore.EventStore, stream string) Resultado {
	ctx := context.Background()
	a, b := hs[0], hs[1]

	in := facto(TipoDedup, "a")
	in.RunID, in.StepID = stream, "passo-1"

	primeiro, err := a.Append(ctx, stream, in)
	if err != nil {
		return Resultado{Detalhe: "o primeiro escritor não conseguiu escrever", Erro: fmt.Errorf("A.Append: %w", err)}
	}
	segundo, err := b.Append(ctx, stream, in)
	if err != nil {
		return Resultado{Detalhe: "B falhou em vez de ver o duplicado", Erro: fmt.Errorf("B.Append: %w", err)}
	}

	if segundo.Status == eventstore.StatusDuplicate && segundo.Seq == primeiro.Seq {
		return Resultado{Tem: true, Detalhe: fmt.Sprintf(
			"a chave %q escrita por A em seq=%d é vista por B como duplicate no mesmo seq", in.RunID+":"+in.StepID, primeiro.Seq)}
	}
	return Resultado{Tem: false, Detalhe: fmt.Sprintf(
		"a chave %q ficou committed DUAS vezes: A em seq=%d, B em seq=%d com status=%q — "+
			"o índice de deduplicação não é partilhado entre escritores",
		in.RunID+":"+in.StepID, primeiro.Seq, segundo.Seq, segundo.Status)}
}

// medirCorrida: N escritores em simultâneo sobre o mesmo expected_seq. É a manifestação
// sob contenção do que medirCAS mede sequencialmente — um substrato pode ter CAS e
// perdê-lo quando as escritas se cruzam.
func medirCorrida(hs []eventstore.EventStore, stream string) Resultado {
	ctx := context.Background()
	n := len(hs)

	var wg sync.WaitGroup
	seqs := make([]uint64, n)
	errs := make([]error, n)
	for i := range hs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := hs[i].Append(ctx, stream, facto(TipoCorrida, strconv.Itoa(i)), eventstore.WithExpectedSeq(0))
			seqs[i], errs[i] = r.Seq, err
		}(i)
	}
	wg.Wait()

	vencedores, recusados, outros := 0, 0, 0
	for i := range hs {
		switch {
		case errs[i] == nil:
			vencedores++
		case eRecusaDeCAS(errs[i]):
			recusados++
		default:
			outros++
		}
	}
	detalhe := fmt.Sprintf("%d escritores sobre expected_seq=0: vencedores=%d recusados=%d outros=%d (seqs=%v)",
		n, vencedores, recusados, outros, seqs)

	if outros > 0 {
		return Resultado{Detalhe: detalhe, Erro: fmt.Errorf(
			"%d escritor(es) falharam com erro que não é recusa de CAS: recusar tem de ser distinguível de avariar", outros)}
	}
	return Resultado{Tem: vencedores == 1 && recusados == n-1, Detalhe: detalhe}
}

// eRecusaDeCAS indica se o erro é o substrato a RECUSAR um escritor cujo expected_seq
// já não corresponde ao log — que é a propriedade a medir.
//
// São DOIS sentinelas, e não é laxismo: o contrato C2 distingue-os pela posição
// afirmada (ver [eventstore.Store.Append] §2). Quem perde uma corrida de CAS afirma
// expected_seq=0 quando o último committed já é 1 — isto é, afirma o PASSADO — e leva
// E_APPEND_ONLY_VIOLATION; E_SEQ_CONFLICT é para quem afirma à FRENTE do log. Um
// oráculo que só aceitasse E_SEQ_CONFLICT nunca reconheceria a propriedade — foi
// exactamente o que TestNaoVacuidade_UmSubstratoQueArbitraEDetectado apanhou nesta
// sonda, antes de ela ser usada contra qualquer backend.
func eRecusaDeCAS(err error) bool {
	return errors.Is(err, eventstore.ErrAppendOnlyViolation) || errors.Is(err, eventstore.ErrSeqConflict)
}

// Tipos de evento das sondas. São CONSTANTES junto do emissor, e não composições — o
// gate `event-catalog` recusa um `Type` construído por concatenação, e tem razão: um
// tipo composto em runtime não é enumerável a partir do código, e o catálogo de
// tecnica/13 §3 deixa de poder ser verificado contra a árvore.
const (
	TipoVisibilidade = "conformance.visibilidade"
	TipoCAS          = "conformance.cas"
	TipoDedup        = "conformance.dedup"
	TipoCorrida      = "conformance.corrida"
)

// facto devolve um EventInput mínimo e válido. O envelope (event_id, seq, ts,
// idempotency_key) é atribuído pelo store, nunca pelo chamador.
//
// QUEM escreve vai no payload, não no tipo: distinguir escritores é dado da sonda, não
// uma família nova de factos.
func facto(tipo, escritor string) eventstore.EventInput {
	return eventstore.EventInput{
		Type:    tipo,
		Payload: json.RawMessage(`{"escritor":` + strconv.Quote(escritor) + `}`),
	}
}
