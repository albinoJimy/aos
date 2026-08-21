package main

import (
	"crypto/ed25519"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	integration "github.com/aos-ref/integration"
	audit "github.com/aos-ref/platform/audit"
)

// ---------------------------------------------------------------------------------------------
// O `/metrics` INTEIRO TEM DE SER LEGÍVEL POR UM SCRAPER — E UM ERRO REJEITA TUDO, NÃO SÓ A LINHA.
//
// O QUE ISTO PROTEGE, e não é hipotético: o formato de exposição do Prometheus recusa o payload
// COMPLETO quando o mesmo nome de métrica traz dois `# HELP` ou dois `# TYPE`. Um nome repetido
// numa família nova não estraga a série nova — faz perder as ~89 que já lá estavam, e o sintoma
// é um `Failed to scrape` sem dizer porquê.
//
// Acrescentei nove séries num dia (âncora do WORM, folga do orçamento, saúde da retenção) e não
// havia guarda nenhuma. Este teste é a guarda, e vale para tudo o que vier a seguir.
// ---------------------------------------------------------------------------------------------

// nomeValido é o identificador de métrica que a exposição aceita.
var nomeValido = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// noComTodasAsFamilias compõe um handler onde o MÁXIMO de famílias emite — senão a guarda
// varreria um endpoint quase vazio e daria verde sobre o que não viu.
func noComTodasAsFamilias(t *testing.T) *apiHandler {
	t.Helper()
	worm := audit.NewMemStore()
	if _, err := worm.Append(t.Context(), audit.AuditRecord{
		Partition: "run-a", RunID: "run-a", StepID: "s", ToolID: "t", Capability: "cap:fs.read",
	}); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	rb, err := integration.NewRunBudget(200000)
	if err != nil {
		t.Fatal(err)
	}
	// Exportador REAL e nao um literal em bruto: o `Close` do no fecha-o, e um zero-value tem
	// canal nil — o teste rebentava em `close of nil channel`. Em producao nao acontece (o no
	// constroi-o sempre pelo `New`), mas o atalho nao serve aqui.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	exp, eerr := NewOTLPHTTPExporter(srv.URL, WithOTLPHTTPClient(srv.Client()), WithOTLPMaxRetries(0))
	if eerr != nil {
		t.Fatalf("NewOTLPHTTPExporter: %v", eerr)
	}
	exp.exported.Add(3)

	// Nó REAL do `Bootstrap`, e nao um literal: o handler das metricas le dezenas de campos que
	// um literal deixa a nil, e a guarda varreria um endpoint pela metade. Sobre ele penduram-se
	// as familias que so existem quando compostas (ancora, orcamento, OTLP).
	n, berr := Bootstrap(t.Context(), tnBaseConfig(), io.Discard)
	if berr != nil {
		t.Fatalf("Bootstrap: %v", berr)
	}
	t.Cleanup(func() { _ = n.Close() })
	n.otlp = exp
	n.ancora = &WormAnchor{
		Public:        pub,
		Checkpoints:   []audit.Checkpoint{{Partition: "run-a", AuditSeq: 1, Timestamp: time.Now()}},
		ExpectedHeads: map[string]uint64{"run-a": 1},
	}
	n.orcamento = rb
	svc, serr := NewNodeService(n, WithApprovalSweepInterval(0), WithDeadlineSweepInterval(0),
		WithRetentionSweepInterval(time.Hour), WithSLOEvalInterval(0))
	if serr != nil {
		t.Fatalf("NewNodeService: %v", serr)
	}
	return &apiHandler{node: n, svc: svc}
}

func TestMetricsRespeitaOFormatoDeExposicao(t *testing.T) {
	corpo := metricasDe(t, noComTodasAsFamilias(t))

	// CONTROLO ANTES: o endpoint tem de estar POVOADO. Sem isto, um handler que devolvesse vazio
	// passaria em todas as asserções abaixo — a guarda daria verde sobre nada.
	if n := strings.Count(corpo, "# TYPE "); n < 18 {
		t.Fatalf("so %d familias no endpoint — o cenario nao esta montado e a guarda varreria pouco", n)
	}

	helps := map[string]int{}
	types := map[string]int{}
	for _, linha := range strings.Split(corpo, "\n") {
		if linha == "" {
			continue
		}
		switch {
		case strings.HasPrefix(linha, "# HELP "):
			campos := strings.SplitN(strings.TrimPrefix(linha, "# HELP "), " ", 2)
			helps[campos[0]]++
		case strings.HasPrefix(linha, "# TYPE "):
			campos := strings.SplitN(strings.TrimPrefix(linha, "# TYPE "), " ", 2)
			if len(campos) != 2 {
				t.Errorf("TYPE sem tipo: %q", linha)
				continue
			}
			types[campos[0]]++
			switch campos[1] {
			case "counter", "gauge", "histogram", "summary", "untyped":
			default:
				t.Errorf("tipo desconhecido %q em %q", campos[1], linha)
			}
		case strings.HasPrefix(linha, "#"):
			// comentário livre: legal.
		default:
			// AMOSTRA: nome[{rótulos}] valor
			nome, valor, ok := strings.Cut(strings.TrimSpace(linha), " ")
			if !ok {
				t.Errorf("amostra sem valor: %q", linha)
				continue
			}
			if i := strings.IndexByte(nome, '{'); i >= 0 {
				nome = nome[:i]
			}
			if !nomeValido.MatchString(nome) {
				t.Errorf("nome invalido %q na linha %q", nome, linha)
			}
			if _, err := strconv.ParseFloat(valor, 64); err != nil {
				t.Errorf("valor ilegivel %q na linha %q", valor, linha)
			}
		}
	}

	// O DEFEITO QUE REJEITA O PAYLOAD INTEIRO: um nome com dois HELP ou dois TYPE.
	for nome, n := range helps {
		if n > 1 {
			t.Errorf("a metrica %q tem %d linhas HELP — o Prometheus REJEITA o payload COMPLETO, "+
				"e perdem-se todas as outras series com ela", nome, n)
		}
	}
	for nome, n := range types {
		if n > 1 {
			t.Errorf("a metrica %q tem %d linhas TYPE — mesma consequencia: o scrape inteiro falha", nome, n)
		}
	}
	// E toda a amostra tem de trazer a sua declaração.
	for nome := range helps {
		if types[nome] == 0 {
			t.Errorf("a metrica %q tem HELP e nao tem TYPE", nome)
		}
	}
}

// TestHelpNaoParteALinha — um HELP com uma newline transforma o resto numa linha órfã.
//
// É o outro modo de partir o payload, e o mais fácil de introduzir sem dar por isso: os textos
// de HELP deste nó são parágrafos longos, escritos à mão, e alguns têm 400 caracteres.
func TestHelpNaoParteALinha(t *testing.T) {
	corpo := metricasDe(t, noComTodasAsFamilias(t))
	for _, linha := range strings.Split(corpo, "\n") {
		if !strings.HasPrefix(linha, "# HELP ") {
			continue
		}
		if strings.ContainsAny(strings.TrimPrefix(linha, "# HELP "), "\r") {
			t.Errorf("HELP com retorno de carro: %q", linha)
		}
	}
	// CONTROLO: houve HELPs para inspeccionar.
	if strings.Count(corpo, "# HELP ") < 18 {
		t.Fatal("poucos HELP no endpoint — o cenario nao esta montado")
	}
}
