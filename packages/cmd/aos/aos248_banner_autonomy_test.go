package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	audit "github.com/aos-ref/platform/audit"
)

// AOS-248 — provas FALSIFICÁVEIS das duas metades do ticket.
//
// F11 (o defeito de wiring): [buildAutonomyOracle] construía o registo com
// `autonomy.NewLevelRegistry()` SEM `WithSink` e registava os níveis ali mesmo, na fronteira de
// config. Consequência: a autonomia com que o nó ia correr — o que decide se uma acção perigosa
// exige um humano — mudava sem ficar registada em lado nenhum. Nenhum `autonomy.level_changed`
// existia no WORM, e portanto nenhuma auditoria conseguia responder "quem pôs este agente em
// L5?". [TestAOS248_ProvisionamentoSelaNoWORM] falharia contra o código antigo: não havia selo
// nenhum para contar.
//
// F14 (o banner mudo): o nó não dizia nada sobre orçamento, broker, modelo e autonomia. A
// omissão mais cara era a última — sem AOS_AUTONOMY_LEVELS NENHUM `escalate` é emitido e todo o
// bridge de aprovação humana fica inalcançável, o que é uma postura de segurança enorme e
// invisível. Os testes de banner abaixo fixam cada afirmação AO ESTADO que a suporta; a regra que
// protegem é "postura anunciada = postura ligada", pelo que os pares positivo/negativo importam
// tanto como as asserções positivas.

// TestAOS248_ProvisionamentoSelaNoWORM é a asserção central de F11: provisionar níveis SELA um
// evento autonomy.level_changed por par, na partição de autonomia do WORM composto, com o MOTIVO
// e o ACTOR. Antes do fix não havia sink nenhum e a contagem seria zero.
func TestAOS248_ProvisionamentoSelaNoWORM(t *testing.T) {
	ctx := context.Background()
	worm := audit.NewMemStore()

	specs := []autonomyLevelSpec{
		{agent: "agt-1", domain: "http", level: autonomy.L4},
		{agent: "agt-1", domain: "fs", level: autonomy.L5},
	}
	wiring := buildAutonomyOracle(specs, autonomy.L0)
	if wiring == nil {
		t.Fatal("com specs devia haver cablagem")
	}
	if err := wiring.provision(ctx, worm); err != nil {
		t.Fatalf("provision: %v", err)
	}

	recs := aos248SelosDeAutonomia(t, worm)
	if len(recs) != len(specs) {
		t.Fatalf("selos autonomy.level_changed = %d, quero %d (um por par provisionado) — sem WithSink ligado ao WORM composto nao ha selo NENHUM, que era o defeito F11", len(recs), len(specs))
	}
	for _, rec := range recs {
		if len(rec.Obligations) == 0 {
			t.Fatalf("selo sem obrigacao autonomy.level_changed: %+v", rec)
		}
		params := rec.Obligations[0].Params
		if params["reason"] != autonomyProvisionReason {
			t.Errorf("motivo selado = %q, quero %q — o registo recusa promocoes sem justificacao e o selo tem de a carregar", params["reason"], autonomyProvisionReason)
		}
		if params["actor"] != autonomyProvisionActor {
			t.Errorf("actor selado = %q, quero %q — sem atribuicao o selo nao responsabiliza ninguem", params["actor"], autonomyProvisionActor)
		}
	}

	// A cablagem continua a servir de oráculo ao PDP com os níveis pedidos (o selo não substitui
	// o efeito: se o SetLevel não tivesse corrido, isto seria L0).
	if got := wiring.oracle().LevelFor("agt-1", "fs"); got != autonomy.L5 {
		t.Fatalf("agt-1:fs devia ser L5 depois do provisionamento, veio %v", got)
	}
}

// TestAOS248_SemWORMOProvisionamentoRecusa é o fail-closed da ligação tardia: se alguém inverter
// a ordem do arranque e provisionar antes de o WORM existir, o nó RECUSA em vez de aplicar
// níveis sem rasto. É a guarda que impede F11 de regressar por uma reordenação inocente.
func TestAOS248_SemWORMOProvisionamentoRecusa(t *testing.T) {
	wiring := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "http", level: autonomy.L4}}, autonomy.L0)
	if err := wiring.provision(context.Background(), nil); !errors.Is(err, ErrAutonomySinkUnbound) {
		t.Fatalf("provisionar sem WORM devia dar ErrAutonomySinkUnbound, veio: %v", err)
	}
	// E o nível NÃO entrou em vigor: fail-closed é recusar, não aplicar-e-avisar.
	if got := wiring.oracle().LevelFor("agt-1", "http"); got != autonomy.L0 {
		t.Fatalf("sem selo o nivel nao pode entrar em vigor; agt-1:http veio %v (quero L0 fail-closed)", got)
	}
}

// TestAOS248_SelagemRecusadaAbortaOArranque prova que uma falha do WORM NÃO é engolida: o
// registo devolve o erro do sink e o provisionamento propaga-o como [ErrAutonomyProvisioning],
// que em [Bootstrap] aborta. Um nó que não consegue AUDITAR a autonomia com que vai correr não
// deve correr com ela.
func TestAOS248_SelagemRecusadaAbortaOArranque(t *testing.T) {
	wiring := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "http", level: autonomy.L4}}, autonomy.L0)
	err := wiring.provision(context.Background(), aos248WormQueRecusa{})
	if !errors.Is(err, ErrAutonomyProvisioning) {
		t.Fatalf("uma selagem recusada devia dar ErrAutonomyProvisioning, veio: %v", err)
	}
}

// TestAOS248_SemSpecsNaoHaCablagem delimita o opt-in: sem AOS_AUTONOMY_LEVELS não há cablagem,
// não há oráculo e nada muda (o comportamento retro-compatível descrito em autonomy_levels.go).
func TestAOS248_SemSpecsNaoHaCablagem(t *testing.T) {
	w := buildAutonomyOracle(nil, autonomy.L0)
	if w != nil {
		t.Fatalf("sem specs nao devia haver cablagem, veio %v", w)
	}
	if w.oracle() != nil {
		t.Fatal("uma cablagem nil tem de dar oraculo nil — um oraculo-fantasma ligaria o gate de autonomia com um registo vazio (tudo escala)")
	}
	if err := w.provision(context.Background(), nil); err != nil {
		t.Fatalf("provisionar uma cablagem nil e no-op, veio: %v", err)
	}
}

// --- Banner (F14) ----------------------------------------------------------------------

// TestAOS248_BannerAutonomiaNosDoisSentidos: a linha SEGUE o estado composto. O par é o que
// torna a prova não-vacuosa — uma linha incondicional passaria metade dele.
func TestAOS248_BannerAutonomiaNosDoisSentidos(t *testing.T) {
	t.Parallel()

	desligado := strings.Join(autonomyPostureBanner(nil), "\n")
	if !strings.Contains(desligado, "ORACULO NAO LIGADO") {
		t.Fatalf("sem oraculo o banner tem de o DECLARAR:\n%s", desligado)
	}
	// O remédio tem de nomear AS DUAS variáveis: AOS_AUTONOMY_LEVELS sozinha é ignorada em
	// silêncio (loadPolicyBundleFromEnv só a lê com AOS_POLICY_BUNDLE_DIR definida). Uma linha
	// que nomeasse só a primeira mandaria o operador para um remédio que não funciona.
	for _, marcador := range []string{"AOS_AUTONOMY_LEVELS", "AOS_POLICY_BUNDLE_DIR", "escalate", "AOS-021"} {
		if !strings.Contains(desligado, marcador) {
			t.Errorf("o banner do oraculo desligado devia conter %q:\n%s", marcador, desligado)
		}
	}

	// A cablagem TEM DE ESTAR PROVISIONADA para o ramo "LIGADO" sair: a linha afirma que cada
	// par está SELADO na hash-chain, e essa afirmação deriva dos selos que existem, não das
	// entradas declaradas (F-A6).
	wiring := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "http", level: autonomy.L4}}, autonomy.L0)
	if err := wiring.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	ligado := strings.Join(autonomyPostureBanner(wiring), "\n")
	if !strings.Contains(ligado, "ORACULO LIGADO") {
		t.Fatalf("com oraculo provisionado o banner tem de o declarar ligado:\n%s", ligado)
	}
	if !strings.Contains(ligado, "1 par(es)") {
		t.Errorf("o banner devia declarar a CARDINALIDADE realmente provisionada:\n%s", ligado)
	}
	if !strings.Contains(ligado, autonomy.DefaultAutonomyPartition) || !strings.Contains(ligado, "autonomy.level_changed") {
		t.Errorf("o banner devia nomear o evento e a particao onde o operador vai procurar o selo:\n%s", ligado)
	}
	if strings.Contains(desligado, "ORACULO LIGADO") {
		t.Error("o ramo desligado NAO pode anunciar oraculo ligado — e exactamente a promessa falsa que este ticket proibe")
	}
}

// TestAOS248_BannerNaoAfirmaSeloSemProvisionamento é a guarda de F-A6, na direcção certa: uma
// cablagem CONSTRUÍDA mas ainda NÃO provisionada não pode anunciar selos. A guarda anterior fazia
// exactamente o contrário — chamava o banner sem provisionar e assertava que a linha "SELADO"
// estava correcta, validando o único estado em que a afirmação é falsa. Hoje o binário está certo
// porque [Bootstrap] provisiona antes de imprimir; esta asserção é o que faz uma REORDENAÇÃO do
// boot falhar em vez de passar com um banner mentiroso.
func TestAOS248_BannerNaoAfirmaSeloSemProvisionamento(t *testing.T) {
	t.Parallel()

	naoProvisionado := strings.Join(autonomyPostureBanner(buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "http", level: autonomy.L4},
	}, autonomy.L0)), "\n")
	if strings.Contains(naoProvisionado, "SELADO") || strings.Contains(naoProvisionado, "ORACULO LIGADO") {
		t.Fatalf("sem provisionamento NAO ha selo nenhum na hash-chain — o banner nao pode afirmar que ha:\n%s", naoProvisionado)
	}
	if !strings.Contains(naoProvisionado, "NAO PROVISIONADO") {
		t.Errorf("o estado intermedio tem de ser DECLARADO (nem mentir nem calar):\n%s", naoProvisionado)
	}
	if !strings.Contains(naoProvisionado, "L0") {
		t.Errorf("a linha devia dizer a postura REAL desse estado — registo vazio ⇒ L0 fail-closed, tudo escala:\n%s", naoProvisionado)
	}
}

// TestAOS248_BannerContaParesDistintos: a cardinalidade anunciada é de PARES agente:domínio, não
// de entradas declaradas. Duas entradas para o MESMO par selam dois eventos mas governam um par —
// "2 par(es)" seria falso (nota menor do achado F-A6).
func TestAOS248_BannerContaParesDistintos(t *testing.T) {
	t.Parallel()

	wiring := buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "http", level: autonomy.L4},
		{agent: "agt-1", domain: "http", level: autonomy.L5},
	}, autonomy.L0)
	if err := wiring.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	linha := strings.Join(autonomyPostureBanner(wiring), "\n")
	if !strings.Contains(linha, "1 par(es)") {
		t.Fatalf("duas entradas para o mesmo par governam UM par; o banner anunciou outra coisa:\n%s", linha)
	}
}

// TestAOS248_BannerModeloTresEstados: a linha do modelo segue o valor REALMENTE composto em
// Config.Model, não a intenção da config. O estado "injectado" existe porque o nó não sabe o que
// lhe injectaram e não deve fingir que sabe.
func TestAOS248_BannerModeloTresEstados(t *testing.T) {
	t.Parallel()

	referencia := strings.Join(modelPostureBanner(nil, true), "\n")
	if !strings.Contains(referencia, "MODELO DE REFERENCIA") {
		t.Fatalf("Config.Model nil ⇒ referenceModel, e o banner tem de o dizer:\n%s", referencia)
	}
	if !strings.Contains(referencia, "AOS_MODEL_ENDPOINT") {
		t.Errorf("o ramo de referencia devia nomear o remedio alcancavel pelo operador:\n%s", referencia)
	}
	if strings.Contains(referencia, "GATEWAY REAL") {
		t.Errorf("o modelo de referencia NAO pode ser anunciado como gateway:\n%s", referencia)
	}

	injectado := strings.Join(modelPostureBanner(aos248ModeloInjectado{}, true), "\n")
	if !strings.Contains(injectado, "INJECTADO") {
		t.Fatalf("um ModelClient que nao e o gateway nem a referencia tem de ser declarado como injectado:\n%s", injectado)
	}
	if strings.Contains(injectado, "GATEWAY REAL") || strings.Contains(injectado, "MODELO DE REFERENCIA") {
		t.Errorf("o no nao pode atribuir a um modelo injectado uma postura que nao verificou:\n%s", injectado)
	}
}

// TestAOS248_BannerOrcamentoEBrokerNaoPrometem fixa o essencial das duas linhas estáticas: elas
// existem para declarar uma AUSÊNCIA. Se alguém as reescrever para soar tranquilizador sem
// compor nada, estas asserções caem.
func TestAOS248_BannerOrcamentoEBrokerNaoPrometem(t *testing.T) {
	t.Parallel()

	// O estado NÃO-COMPOSTO é o DEFAULT do binário — `AOS_BUDGET_MAX_TOKENS` por definir.
	// (Desde AOS-256/AOS-257 o binário COMPÕE o orçamento quando a variável está definida;
	// o argumento deixou de ser um literal e passou a derivar do estado, pelo que este
	// teste cobre um dos dois estados alcançáveis, não «o binário de hoje».)
	orcamento := strings.Join(budgetPostureBanner(false), "\n")
	if !strings.Contains(orcamento, "NAO COMPOSTO") || !strings.Contains(orcamento, "AOS-008") {
		t.Fatalf("a linha do orcamento tem de declarar NAO COMPOSTO e nomear o eixo:\n%s", orcamento)
	}
	if !strings.Contains(orcamento, "AOS-246") {
		t.Errorf("a linha devia dizer porque os tectos de velocidade tambem nao sao ligaveis hoje:\n%s", orcamento)
	}

	broker := strings.Join(credentialBrokerPostureBanner(materialPrivadoDoNo{Endurecido: true}), "\n")
	if !strings.Contains(broker, "AUSENTE") {
		t.Fatalf("a linha do broker tem de declarar a AUSENCIA:\n%s", broker)
	}
	for _, marcador := range []string{"AOS_MODEL_API_KEY_PATH", "ADR-006", "EPIC-07"} {
		if !strings.Contains(broker, marcador) {
			t.Errorf("a linha do broker devia conter %q (o que entra por ficheiro, e o eixo):\n%s", marcador, broker)
		}
	}
}

// TestAOS248_BannerSaiNoArranqueReal liga as funções puras ao caminho de arranque REAL: se
// alguém as escrever e nunca as chamar, o defeito F14 continuaria de pé e este teste cai.
func TestAOS248_BannerSaiNoArranqueReal(t *testing.T) {
	banner := runWithoutTouchingBoardRegions(t)

	for _, marcador := range []string{
		"orcamento / tecto de custo (AOS-008): NAO COMPOSTO",
		"credential broker (AOS-070/EPIC-07, ADR-006): AUSENTE",
		"modelo (EPIC-06): MODELO DE REFERENCIA",
		"autonomia / escalate (AOS-087): ORACULO NAO LIGADO",
	} {
		if !strings.Contains(banner, marcador) {
			t.Errorf("o arranque real devia imprimir %q — as quatro superficies de F14 tem de sair do BANNER, nao so das funcoes puras.\nBanner:\n%s", marcador, banner)
		}
	}
}

// --- Auxiliares ------------------------------------------------------------------------

// aos248SelosDeAutonomia lê a partição de autonomia do WORM e devolve os registos selados.
func aos248SelosDeAutonomia(t *testing.T, store audit.Store) []audit.AuditRecord {
	t.Helper()
	var out []audit.AuditRecord
	for seq := 1; ; seq++ {
		rec, ok, err := store.At(context.Background(), autonomy.DefaultAutonomyPartition, uint64(seq))
		if err != nil {
			t.Fatalf("ler particao %q seq %d: %v", autonomy.DefaultAutonomyPartition, seq, err)
		}
		if !ok {
			return out
		}
		out = append(out, rec)
	}
}

// aos248WormQueRecusa é um [audit.Store] que RECUSA selar — o substituto do WORM cheio/partido
// que, sem esta guarda, deixaria o nível entrar em vigor sem registo.
type aos248WormQueRecusa struct{ audit.Store }

func (aos248WormQueRecusa) Append(context.Context, audit.AuditRecord) (audit.AuditRecord, error) {
	return audit.AuditRecord{}, errors.New("worm indisponivel (teste)")
}

// aos248ModeloInjectado é um [agentruntime.ModelClient] que não é o gateway nem a referência —
// o terceiro estado que o banner tem de saber declarar sem inventar.
type aos248ModeloInjectado struct{}

func (aos248ModeloInjectado) Call(context.Context, agentruntime.PromptView) (agentruntime.ModelResponse, error) {
	return agentruntime.ModelResponse{Final: true}, nil
}
