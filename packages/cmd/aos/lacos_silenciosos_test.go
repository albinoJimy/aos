package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// corpoMetrics devolve o /metrics de um handler.
func corpoMetrics(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics devolveu %d", rec.Code)
	}
	return rec.Body.String()
}

// UM VARREDOR QUE PARA TEM DE SER DISTINGUIVEL DE UM VARREDOR SEM TRABALHO.
//
// Achado da validacao longitudinal de 2026-08-25. Dos oito lacos periodicos do no, tres
// varredores de SERVICO nao expunham NADA. Se qualquer um parasse, o efeito era SILENCIOSO:
// as aprovacoes nunca expirariam, os runs nunca esgotariam prazo, os orfaos nunca seriam
// retomados — e nenhuma serie se movia.
//
// A PROPRIEDADE SAO TRES ESTADOS, e o teste tem de os separar:
//
//	(1) laco NAO COMPOSTO          => serie AUSENTE
//	(2) composto, ainda sem passar => total presente, IDADE ausente
//	(3) ja passou                  => total e idade presentes
//
// O estado (2) e o que a disciplina desta casa exige: emitir idade 0 antes da primeira
// passagem diria "acabou de varrer" sobre um no que ainda nao varreu nada.
func TestLacos_TresEstadosDistinguiveis(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	// (1) NAO COMPOSTO: nenhum contador foi tocado.
	corpo := corpoMetrics(t, h)
	for _, s := range []string{"aos_approval_sweeps_total", "aos_deadline_sweeps_total", "aos_orphan_sweeps_total"} {
		if strings.Contains(corpo, s) {
			t.Fatalf("(1) o no nunca varreu e a serie %q APARECE — um zero aqui le-se como 'armado e parado'", s)
		}
	}

	// (2) COMPOSTO, SEM PASSAR AINDA: total a 1 mas sem instante registado.
	svc.passagensAprovacao.Store(1)
	corpo = corpoMetrics(t, h)
	if !strings.Contains(corpo, "aos_approval_sweeps_total") {
		t.Fatal("(2) o total devia aparecer assim que o varredor conta uma passagem")
	}
	if strings.Contains(corpo, "aos_approval_last_sweep_age_seconds") {
		t.Fatal("(2) a IDADE apareceu sem instante registado — emitiria 0 e diria 'acabou de varrer'")
	}

	// (3) JA PASSOU: instante registado => idade presente.
	svc.ultimaAprovacaoUnix.Store(time.Now().Add(-90 * time.Second).Unix())
	corpo = corpoMetrics(t, h)
	linha := ""
	for _, l := range strings.Split(corpo, "\n") {
		if strings.HasPrefix(l, "aos_approval_last_sweep_age_seconds ") {
			linha = l
			break
		}
	}
	if linha == "" {
		t.Fatal("(3) com instante registado a idade devia aparecer")
	}
	// A idade tem de REFLECTIR o instante, nao ser um valor fixo.
	campos := strings.Fields(linha)
	if len(campos) != 2 {
		t.Fatalf("linha de metrica com forma inesperada: %q", linha)
	}
	idade, err := strconv.ParseFloat(campos[1], 64)
	if err != nil {
		t.Fatalf("idade ilegivel: %q", linha)
	}
	if idade < 80 || idade > 120 {
		t.Fatalf("idade=%v para uma passagem ha 90s — a serie nao reflecte o instante", idade)
	}
}

// CONTROLO ANTI-VACUIDADE: OS TRES VARREDORES SAO INDEPENDENTES.
//
// Sem este caso, uma implementacao que emitisse as tres series a partir do MESMO contador
// passaria o teste de cima — e um varredor parado ficaria escondido atras de outro que corre.
func TestLacos_VarredoresNaoSePartilham(t *testing.T) {
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()
	svc, h := newAPI(t, node)

	// So o de PRAZOS corre.
	svc.passagensPrazo.Store(7)
	svc.ultimoPrazoUnix.Store(time.Now().Unix())
	corpo := corpoMetrics(t, h)

	if !strings.Contains(corpo, "aos_deadline_sweeps_total 7") {
		t.Fatalf("o varredor de prazos correu 7 vezes e a serie nao o diz")
	}
	for _, s := range []string{"aos_approval_sweeps_total", "aos_orphan_sweeps_total"} {
		if strings.Contains(corpo, s) {
			t.Fatalf("%q apareceu com o varredor dele PARADO — as series estao a partilhar contador", s)
		}
	}
}

// A CABLAGEM: O LACO REAL MARCA A PASSAGEM.
//
// Os dois testes acima INJECTAM o contador, e por isso nao provam que alguem o incrementa —
// a mutacao denunciou-o: pôr `if false { s.passagensAprovacao.Add(1) }` no laco NAO os fazia
// cair. E a decima setima vez que este padrao aparece neste repositorio.
//
// Aqui corre-se o laco a serio, com cadencia curta, e exige-se que o contador AVANCE e que o
// instante fique registado. Sem a instrumentacao no sitio certo, este teste expira.
func TestLacos_CablagemOLacoRealMarcaAPassagem(t *testing.T) {
	// O `aos263Node` compoe os APROVADORES; o `newAPINode` nao. Sem `PendingApprovals` o
	// laco sai na guarda de entrada e nada corre — o teste falharia por preconditcao, nao
	// por defeito. Descoberto por sonda ao escrever isto.
	node, _, _, _ := aos263Node(t)
	svc, err := NewNodeService(node, WithApprovalSweepInterval(40*time.Millisecond))
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	// PRECONDICAO: o contador comeca a zero. Sem isto, um contador ja adiantado faria o
	// teste passar sem o laco ter corrido.
	if n := svc.passagensAprovacao.Load(); n != 0 {
		t.Fatalf("PRECONDICAO: o contador devia comecar a 0, esta em %d", n)
	}

	stop := make(chan struct{})
	defer close(stop)
	go svc.sweepApprovals(stop)

	prazo := time.Now().Add(3 * time.Second)
	for svc.passagensAprovacao.Load() < 2 {
		if time.Now().After(prazo) {
			t.Fatalf("o laco correu mas o contador ficou em %d — a passagem NAO esta a ser marcada", svc.passagensAprovacao.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// E o INSTANTE tambem: sem ele a idade nunca sairia e o detector nao existiria.
	if u := svc.ultimaAprovacaoUnix.Load(); u == 0 {
		t.Fatal("o contador avancou mas o INSTANTE ficou a 0 — a idade nunca apareceria")
	}
}

// CONTROLO ANTI-VACUIDADE: COM O LACO DESLIGADO, O CONTADOR NAO SE MEXE.
//
// Sem este caso, um contador incrementado noutro sitio qualquer — ou por outro laco — faria
// o teste de cima passar sem que o varredor de aprovacoes tivesse corrido.
func TestLacos_ControloLacoDesligadoNaoConta(t *testing.T) {
	node, _, _, _ := aos263Node(t)
	svc, err := NewNodeService(node, WithApprovalSweepInterval(0)) // <=0 DESLIGA
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}

	stop := make(chan struct{})
	defer close(stop)
	go svc.sweepApprovals(stop)
	time.Sleep(300 * time.Millisecond)

	if n := svc.passagensAprovacao.Load(); n != 0 {
		t.Fatalf("CONTROLO: o varredor esta DESLIGADO e o contador foi a %d — alguem conta fora do laco", n)
	}
}

// A OPCAO DE CADENCIA TEM DE CHEGAR AO SERVICO.
//
// Defeito encontrado ao instrumentar os lacos: `WithApprovalSweepInterval` escrevia
// `c.sweepInterval` e NAO `c.sweepIntervalSet`, e o [NewNodeService] decide com
// `if cfg.sweepIntervalSet`. O valor era escrito e IGNORADO.
//
// CONSEQUENCIA EM PRODUCAO: o composition-root entrega `AOS_APPROVAL_SWEEP_INTERVAL` por
// esta opcao. A variavel era lida, VALIDADA com um fail-closed que aborta o arranque em
// valores maus — e depois deitada fora. Medido antes da correccao: 5m e 30s davam ambos 1m.
//
// A irma `WithDeadlineSweepInterval` sempre pos as duas. Este teste exige o mesmo das duas,
// para a assimetria nao voltar por uma delas ser esquecida outra vez.
func TestLacos_OpcaoDeCadenciaChegaAoServico(t *testing.T) {
	node, _, _, _ := aos263Node(t)

	for _, tc := range []struct {
		nome  string
		opt   NodeServiceOption
		quero time.Duration
		le    func(*NodeService) time.Duration
	}{
		{"aprovacoes 30s", WithApprovalSweepInterval(30 * time.Second), 30 * time.Second,
			func(s *NodeService) time.Duration { return s.sweepInterval }},
		{"aprovacoes 5m", WithApprovalSweepInterval(5 * time.Minute), 5 * time.Minute,
			func(s *NodeService) time.Duration { return s.sweepInterval }},
		{"prazos 20ms (a irma, que sempre funcionou)", WithDeadlineSweepInterval(20 * time.Millisecond), 20 * time.Millisecond,
			func(s *NodeService) time.Duration { return s.deadlineSweepInterval }},
	} {
		t.Run(tc.nome, func(t *testing.T) {
			svc, err := NewNodeService(node, tc.opt)
			if err != nil {
				t.Fatalf("NewNodeService: %v", err)
			}
			if got := tc.le(svc); got != tc.quero {
				t.Fatalf("pedido %v, efectivo %v — a opcao NAO chegou ao servico", tc.quero, got)
			}
		})
	}

	// CONTROLO ANTI-VACUIDADE: SEM opcao, o default TEM de valer. Sem este caso, uma
	// implementacao que ignorasse o `...Set` e aceitasse sempre o valor passaria os casos de
	// cima — e a distincao entre "o operador pediu" e "ninguem pediu" desapareceria.
	svc, err := NewNodeService(node)
	if err != nil {
		t.Fatalf("NewNodeService: %v", err)
	}
	if svc.sweepInterval != DefaultApprovalSweepInterval {
		t.Fatalf("CONTROLO: sem opcao o default devia valer (%v), veio %v", DefaultApprovalSweepInterval, svc.sweepInterval)
	}
}
