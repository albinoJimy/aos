package planmigrate_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planmigrate"
	pe "github.com/aos-ref/control-plane/orchestrator/plannerevents"
)

// schemaline_test.go — A PROVA DAS DUAS DIRECÇÕES do carimbo de schema de ADR-022
// (AOS-273), sobre documentos das linhas ANTERIORES congelados em `testdata/schemaline`.
//
// # A DECISÃO QUE ESTES TESTES SUSTENTAM: MINOR, NÃO MAJOR
//
// As três extensões de ADR-022 acrescentaram ao `Node` dois campos novos
// (`outputs`/`consumes`, AOS-272) e reservaram um literal de um campo existente
// (`role: verifier`, AOS-271) — depois do MINOR 1.1.0 que AOS-270 já tinha levado por
// `conditional_on`. Nenhuma delas remove um campo, muda o tipo de um campo existente
// ou altera o significado de um documento que não os use: é a definição de MINOR
// ([plan.CurrentPlanVersion], que carrega a justificação). A tentação era carimbar um
// MAJOR para ter uma transformação de dados a exercitar no `planmigrate` — seria
// quebrar compatibilidade de graça e dar-lhe trabalho a fingir.
//
// A consequência honesta é que a «migração da versão anterior» desta linha é a
// AUSÊNCIA de migração, e uma ausência prova-se em vez de se declarar. Por isso os
// testes cobrem as duas direcções, e nenhuma delas é vacuosa:
//
//	(a) ADMISSIBILIDADE E REPRODUÇÃO — um documento carimbado numa linha ANTERIOR,
//	    lido por ESTE binário (que tem os campos novos), continua a decodificar, a
//	    re-serializar-se nos MESMOS BYTES e a produzir o MESMO [planmigrate.HashPlan];
//	    o replay devolve um manifesto na versão em que o plano foi APROVADO, nunca na
//	    corrente.
//	(b) REJEIÇÃO — um MAJOR fora da janela de suporte DECLARADA da linha 1.x é
//	    inadmissível ([planmigrate.ErrOutsideSupportWindow]), na leitura E na escrita.
//
// # PORQUE UM GOLDEN LITERAL, E NÃO UM ROUND-TRIP NO MESMO BINÁRIO
//
// Decodificar e re-codificar o mesmo documento duas vezes no mesmo processo não prova
// nada: prova que uma função é uma função. O que tem de se manter idêntico é o
// RESULTADO DA RECONSTRUÇÃO face ao que o documento valia QUANDO FOI APROVADO — e
// isso só se ancora fora do binário. As constantes abaixo são esse ponto fixo:
// a forma canónica exacta e o `sha256:` que o binding reader↔captura usa
// (`plan.approved`). Se um campo novo passasse a deixar rasto na serialização de um
// documento que não o usa — um `omitempty` esquecido, um campo com valor-zero
// visível, uma reordenação —, o hash mudava, o binding de TODOS os runs aprovados
// nessa linha partia-se, e estes testes ficam vermelhos ANTES de isso chegar a um run.
//
// A afirmação «isto é o que o binário anterior produzia» é AUDITÁVEL À VISTA, não uma
// promessa: [baselineCanonical] tem exactamente as chaves da linha 1.0.0, pela ordem
// de declaração da struct, e NENHUMA das três chaves de ADR-022; [conditionalCanonical]
// tem `conditional_on` (é um documento de 1.1.0) e nenhuma das duas de AOS-272. É
// isso que [TestFrozenLinesLeaveNoTraceOfLaterFields] confere explicitamente, para
// que a propriedade não dependa de quem lê a constante.

// Formas canónicas e hashes CONGELADOS dos documentos de `testdata/schemaline`. Não se
// regeneram por conveniência: mudá-los é declarar que o wire de uma linha anterior
// mudou — o que é, por definição, uma quebra de MAJOR.
const (
	baselineCanonical = `{"plan_version":"1.0.0","objective":"recolher fontes e redigir o resumo executivo","budget_total":{"tokens":1400,"cost_micro_usd":7000},"planner_meta":{"model":"planner-model-a","prompt_version":"2.1.0","capabilities_hash":"sha256:snap-baseline"},"nodes":[{"node_id":"recolha","role":"searcher","objective":"recolher fontes candidatas","tools":[{"name":"search","version":"1.0.0","digest":"sha256:search"}],"depends_on":[],"budget_estimate":{"tokens":800,"cost_micro_usd":4000},"risk_class":"safe"},{"node_id":"resumo","role":"summarizer","objective":"redigir o resumo executivo","tools":[],"depends_on":["recolha"],"budget_estimate":{"tokens":600,"cost_micro_usd":3000},"risk_class":"safe"}]}`
	baselineHash      = "sha256:c8c06e739cfd4961da0e653ba97966f87ced5c890bc0673dbf118f9d01f0e614"

	conditionalCanonical = `{"plan_version":"1.1.0","objective":"recolher fontes, com pesquisa suplementar se a recolha falhar","budget_total":{"tokens":2200,"cost_micro_usd":11000},"planner_meta":{"model":"planner-model-a","prompt_version":"2.2.0","capabilities_hash":"sha256:snap-conditional"},"nodes":[{"node_id":"recolha","role":"searcher","objective":"recolher fontes candidatas","tools":[{"name":"search","version":"1.0.0","digest":"sha256:search"}],"depends_on":[],"budget_estimate":{"tokens":800,"cost_micro_usd":4000},"risk_class":"safe"},{"node_id":"suplementar","role":"searcher","objective":"pesquisa suplementar quando a recolha falha","tools":[{"name":"search","version":"1.0.0","digest":"sha256:search"}],"depends_on":[],"budget_estimate":{"tokens":800,"cost_micro_usd":4000},"risk_class":"safe","conditional_on":[{"from":"recolha","when":[{"subject":"terminal_state","op":"eq","enum":"failed"}]}]},{"node_id":"resumo","role":"summarizer","objective":"redigir o resumo executivo","tools":[],"depends_on":[],"budget_estimate":{"tokens":600,"cost_micro_usd":3000},"risk_class":"safe","conditional_on":[{"from":"recolha","when":[{"subject":"terminal_state","op":"eq","enum":"complete"}]}]}]}`
	conditionalHash      = "sha256:09f58a174064605da6c60f15f08cbd8025797de41c6f75e2fbf7f82be46e780b"
)

// frozenLine é UM documento congelado de uma linha anterior do schema: o ficheiro, a
// versão em que foi aprovado, a forma canónica e o hash — mais as chaves de ADR-022
// que o seu wire NÃO pode conter (o que o carimbo dessa linha promete).
type frozenLine struct {
	File      string
	Version   plan.PlanVersion
	Canonical string
	Hash      string
	// AbsentKeys são as chaves que a linha em que o documento foi aprovado ainda não
	// tinha. Vê-las no wire seria um campo novo a deixar rasto num documento antigo.
	AbsentKeys []string
}

// frozenLines são as linhas anteriores cobertas. Cresce com cada MINOR: o documento de
// uma linha entra aqui quando essa linha deixa de ser a corrente, e nunca sai enquanto
// o seu MAJOR estiver na janela de suporte.
func frozenLines() []frozenLine {
	return []frozenLine{
		{
			File:      "plan-1.0.0-baseline.json",
			Version:   plan.PlanVersion{Major: 1, Minor: 0, Patch: 0},
			Canonical: baselineCanonical,
			Hash:      baselineHash,
			// A linha base é PRÉ-ADR-022 por inteiro: nenhuma das três extensões.
			AbsentKeys: []string{`"conditional_on"`, `"outputs"`, `"consumes"`},
		},
		{
			File:      "plan-1.1.0-conditional.json",
			Version:   plan.PlanVersion{Major: 1, Minor: 1, Patch: 0},
			Canonical: conditionalCanonical,
			Hash:      conditionalHash,
			// 1.1.0 já tem `conditional_on` (AOS-270) mas ainda não os contratos de
			// payload (AOS-272) — que é exactamente o que separa 1.1.0 de 1.2.0.
			AbsentKeys: []string{`"outputs"`, `"consumes"`},
		},
	}
}

// TestFrozenLinesCoverEveryPreviousMinor é o META-TESTE que impede a cobertura
// byte-a-byte de ficar para trás da linha. [frozenLines] documenta que «cresce com cada
// MINOR», mas nada o verificava: um ticket futuro que levasse a linha a 1.3.0 sem
// congelar a fixture de 1.2.0 deixava todos os testes verdes — e a linha 1.2.0, sob a
// qual há runs aprovados e congelados, ficava sem prova de reprodução. Uma alteração
// posterior de `omitempty`/ordem de campos que só a afectasse partia o binding
// `plan.approved`↔reader desses runs sem nenhum teste vermelho, que é exactamente o modo
// de falha que este ficheiro existe para tornar visível.
//
// A regra é a mais simples que serve: os MINORs cobertos têm de ser EXACTAMENTE
// {0 … CurrentPlanVersion.Minor-1} no MAJOR corrente.
func TestFrozenLinesCoverEveryPreviousMinor(t *testing.T) {
	t.Parallel()
	cobertos := make(map[int]string, len(frozenLines()))
	for _, fl := range frozenLines() {
		if fl.Version.Major != plan.CurrentPlanVersion.Major {
			t.Fatalf("fixture %s e do MAJOR %d, fora da linha corrente %d", fl.File, fl.Version.Major, plan.CurrentPlanVersion.Major)
		}
		if fl.Version.Minor >= plan.CurrentPlanVersion.Minor {
			t.Fatalf("fixture %s carimba %s, que NAO e uma linha anterior a corrente (%s)", fl.File, fl.Version, plan.CurrentPlanVersion)
		}
		if outra, dup := cobertos[fl.Version.Minor]; dup {
			t.Fatalf("MINOR %d congelado duas vezes (%s e %s)", fl.Version.Minor, outra, fl.File)
		}
		cobertos[fl.Version.Minor] = fl.File
	}
	for m := 0; m < plan.CurrentPlanVersion.Minor; m++ {
		if _, ok := cobertos[m]; !ok {
			t.Fatalf("bumpaste a linha para %s e nao congelaste a anterior: falta a fixture de 1.%d.x em testdata/schemaline",
				plan.CurrentPlanVersion, m)
		}
	}
}

// loadFrozen lê e desserializa um documento congelado pelo desserializador SANCIONADO
// ([plan.Decode], fail-closed com `DisallowUnknownFields`). Que ele passe é ele
// próprio metade da prova da direcção (a): um leitor 1.2.0 continua a ACEITAR a forma
// de uma linha anterior.
func loadFrozen(t *testing.T, name string) plan.PlanDocument {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "schemaline", name))
	if err != nil {
		t.Fatalf("ler fixture congelada %q: %v", name, err)
	}
	doc, err := plan.Decode(raw)
	if err != nil {
		t.Fatalf("decode da fixture congelada %q pelo leitor %s: %v (um documento de uma linha anterior TEM de continuar admissivel)", name, plan.CurrentPlanVersion, err)
	}
	return doc
}

// TestFrozenLinesReproduceByteForByte — AC3, direcção (a). Um documento aprovado numa
// linha ANTERIOR, lido por este binário, re-serializa-se nos MESMOS BYTES e produz o
// MESMO hash de binding que produzia então.
//
// FALSIFICÁVEL: as constantes são pontos fixos fora do binário. Se qualquer um dos
// três campos de ADR-022 perdesse o `omitempty` — ou se a ordem de declaração da
// struct mudasse — a forma canónica mudava, o hash mudava, e o `plan.approved` de
// todos os runs aprovados nessa linha deixava de casar com o seu reader
// ([planmigrate.ErrReaderMismatch]). O teste torna isso um erro de compilação-do-CI em
// vez de um run irreproduzível.
func TestFrozenLinesReproduceByteForByte(t *testing.T) {
	t.Parallel()
	for _, fl := range frozenLines() {
		t.Run(fl.File, func(t *testing.T) {
			doc := loadFrozen(t, fl.File)

			// O carimbo lido é o da linha em que o documento foi aprovado — e NÃO a
			// linha corrente (senão o teste não exercitava versão anterior nenhuma).
			if doc.PlanVersion != fl.Version {
				t.Fatalf("plan_version=%s, esperado %s", doc.PlanVersion, fl.Version)
			}
			if doc.PlanVersion == plan.CurrentPlanVersion {
				t.Fatalf("fixture vacuosa: a versao congelada %s e a corrente", doc.PlanVersion)
			}

			enc, err := plan.Encode(doc)
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			if string(enc) != fl.Canonical {
				t.Fatalf("forma canonica DIVERGENTE de %s (o wire de uma linha anterior mudou — isso e quebra de MAJOR)\n got=%s\nwant=%s", fl.Version, enc, fl.Canonical)
			}
			h, err := planmigrate.HashPlan(doc)
			if err != nil {
				t.Fatalf("HashPlan: %v", err)
			}
			if h != fl.Hash {
				t.Fatalf("hash de binding DIVERGENTE de %s: got=%s want=%s (o `plan.approved` dos runs dessa linha deixaria de casar)", fl.Version, h, fl.Hash)
			}
		})
	}
}

// TestFrozenLinesLeaveNoTraceOfLaterFields — o ARGUMENTO por trás de o bump ser MINOR,
// tornado verificável: os campos que uma linha POSTERIOR acrescentou não aparecem no
// wire de um documento dessa linha anterior. É esta propriedade — e não uma promessa
// no comentário — que faz de «a migração é a ausência de migração» uma afirmação
// falsificável.
//
// FALSIFICÁVEL: um `outputs` sem `omitempty` serializaria `"outputs":null` no
// documento de 1.0.0, o teste acusaria a chave, e a conclusão certa seria a oposta —
// que a extensão NÃO era aditiva no wire e o bump devia ter sido MAJOR.
func TestFrozenLinesLeaveNoTraceOfLaterFields(t *testing.T) {
	t.Parallel()
	for _, fl := range frozenLines() {
		t.Run(fl.File, func(t *testing.T) {
			enc, err := plan.Encode(loadFrozen(t, fl.File))
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			for _, key := range fl.AbsentKeys {
				if strings.Contains(string(enc), key) {
					t.Fatalf("a chave %s de uma linha POSTERIOR apareceu no wire de um documento %s — a extensao nao e aditiva no wire:\n%s", key, fl.Version, enc)
				}
			}
			// Não-vacuidade da varredura: a chave que a linha JÁ tinha continua lá.
			// Sem isto, um `strings.Contains` sobre bytes vazios passaria sempre.
			if !strings.Contains(string(enc), `"node_id"`) {
				t.Fatalf("varredura vacuosa: nem a chave `node_id` esta no wire:\n%s", enc)
			}
		})
	}
}

// TestFrozenLineReplaysOnItsApprovedVersion — AC3, direcção (a) ponta-a-ponta: um run
// aprovado na linha ANTERIOR reproduz-se por este binário, e o manifesto reflecte a
// versão em que foi APROVADO — nunca [plan.CurrentPlanVersion].
//
// FALSIFICÁVEL em dois eixos ao mesmo tempo: (i) o hash aprovado gravado na captura é
// o LITERAL congelado, não um valor recalculado no teste — se o binário novo hasheasse
// o documento de outra maneira, o binding partia-se com [planmigrate.ErrReaderMismatch];
// (ii) o manifesto tem de vir na versão da fixture, e o teste acusa explicitamente a
// auto-migração para a corrente.
func TestFrozenLineReplaysOnItsApprovedVersion(t *testing.T) {
	t.Parallel()
	for _, fl := range frozenLines() {
		t.Run(fl.File, func(t *testing.T) {
			store := newStore(t)
			planID := "plan-schemaline-" + fl.Version.String()
			doc := loadFrozen(t, fl.File)

			hash, proposer := seedApproval(t, store, planID, doc, nil)
			// O hash com que a captura foi sementeada É o literal congelado: a âncora
			// do binding não se recalcula de dentro do teste.
			if hash != fl.Hash {
				t.Fatalf("hash aprovado=%s, esperado o congelado %s", hash, fl.Hash)
			}
			seedMaterialized(t, store, planID, doc, hash)

			// A janela DECLARADA da linha 1.x (ver `tecnica/18` §3.6.1).
			mig := planmigrate.NewMigrator(mustPolicy(t, declaredWindow))
			rp, err := mig.Replay(context.Background(), store, planID, doc)
			if err != nil {
				t.Fatalf("Replay de um documento %s: %v", fl.Version, err)
			}
			if rp.Manifest.PlanVersion != fl.Version {
				t.Fatalf("manifest.PlanVersion=%s, esperado %s (congelado na aprovacao)", rp.Manifest.PlanVersion, fl.Version)
			}
			if rp.Manifest.PlanVersion == plan.CurrentPlanVersion {
				t.Fatalf("auto-migracao: o manifesto assumiu a linha corrente %s", plan.CurrentPlanVersion)
			}
			if rp.Manifest.PlanHash != fl.Hash {
				t.Fatalf("manifest.PlanHash=%s, esperado %s", rp.Manifest.PlanHash, fl.Hash)
			}
			if len(rp.Materialized.Nodes) != len(doc.Nodes) {
				t.Fatalf("materializacao reconstruida com %d nos, o documento tem %d", len(rp.Materialized.Nodes), len(doc.Nodes))
			}
			// O modelo é consultado UMA vez, na captura: o replay não re-planeia.
			if got := proposer.calls.Load(); got != 1 {
				t.Fatalf("proposer chamado %d vezes; esperado 1 (so na captura)", got)
			}
		})
	}
}

// TestIndependentTwinOfBaselineHashesTheSame — a ESTABILIDADE do carimbo provada por um
// GÉMEO INDEPENDENTE, não por `f(x) == f(x)`.
//
// O gémeo é construído por outro CAMINHO — structs montadas a partir de variáveis
// separadas, sem tocar no ficheiro nem no desserializador — com os mesmos valores da
// fixture congelada. Exigir-lhe o MESMO hash prova duas coisas de uma vez: que a forma
// canónica depende só dos VALORES (e não do texto do documento, da sua indentação ou
// da ordem das chaves no ficheiro), e que o contrato em memória e o wire congelado
// continuam a concordar depois de o schema ter crescido.
//
// FALSIFICÁVEL: os campos de ADR-022 ficam deliberadamente por preencher no gémeo — é
// o que um planeador da linha 1.0.0 produzia. Se algum deles contribuísse para a forma
// canónica mesmo vazio, o gémeo hasheava diferente do literal congelado.
func TestIndependentTwinOfBaselineHashesTheSame(t *testing.T) {
	t.Parallel()

	// Montagem por partes: cada peça é uma variável própria, nada é copiado da fixture.
	recolhaTool := plan.ToolRef{Name: "search", Version: "1.0.0", Digest: "sha256:search"}
	recolha := plan.Node{
		NodeID:         "recolha",
		Role:           "searcher",
		Objective:      "recolher fontes candidatas",
		Tools:          []plan.ToolRef{recolhaTool},
		DependsOn:      []string{},
		BudgetEstimate: plan.BudgetEstimate{Tokens: 800, CostMicroUSD: 4000},
		RiskClass:      plan.RiskSafe,
	}
	resumo := plan.Node{
		NodeID:         "resumo",
		Role:           "summarizer",
		Objective:      "redigir o resumo executivo",
		Tools:          []plan.ToolRef{},
		DependsOn:      []string{recolha.NodeID},
		BudgetEstimate: plan.BudgetEstimate{Tokens: 600, CostMicroUSD: 3000},
		RiskClass:      plan.RiskSafe,
	}
	nodes := make([]plan.Node, 0, 2)
	nodes = append(nodes, recolha)
	nodes = append(nodes, resumo)

	twin := plan.PlanDocument{
		PlanVersion: plan.PlanVersion{Major: 1, Minor: 0, Patch: 0},
		Objective:   "recolher fontes e redigir o resumo executivo",
		BudgetTotal: plan.BudgetEstimate{Tokens: 1400, CostMicroUSD: 7000},
		PlannerMeta: plan.PlannerMeta{
			Model:            "planner-model-a",
			PromptVersion:    "2.1.0",
			CapabilitiesHash: "sha256:snap-baseline",
		},
		Nodes: nodes,
	}

	// Sanidade anti-vacuidade: o gémeo NÃO tem nenhum dos campos de ADR-022 preenchidos
	// (é um documento da linha 1.0.0, e é isso que o torna um gémeo e não um sósia).
	for _, n := range twin.Nodes {
		if len(n.ConditionalOn) != 0 || len(n.Outputs) != 0 || len(n.Consumes) != 0 {
			t.Fatalf("gemeo mal construido: o no %q traz campos de ADR-022", n.NodeID)
		}
	}

	h, err := planmigrate.HashPlan(twin)
	if err != nil {
		t.Fatalf("HashPlan(gemeo): %v", err)
	}
	if h != baselineHash {
		t.Fatalf("o gemeo independente hasheia %s; a fixture congelada vale %s", h, baselineHash)
	}

	// E o binding fecha nos dois sentidos: o gémeo é aceite como READER da captura
	// sementeada a partir do documento do ficheiro.
	store := newStore(t)
	planID := "plan-schemaline-twin"
	fromFile := loadFrozen(t, "plan-1.0.0-baseline.json")
	hash, _ := seedApproval(t, store, planID, fromFile, nil)
	seedMaterialized(t, store, planID, fromFile, hash)

	mig := planmigrate.NewMigrator(mustPolicy(t, declaredWindow))
	if _, err := mig.Replay(context.Background(), store, planID, twin); err != nil {
		t.Fatalf("o gemeo devia ser um reader admissivel da mesma captura: %v", err)
	}
}

// declaredWindow é a janela de suporte declarada da linha corrente — e é agora a
// EXPORTADA pelo pacote, não uma cópia local.
//
// A cópia era o defeito: a janela existia em prosa (`tecnica/18` §3.6.1) e numa `var` de
// um ficheiro `_test.go`, e em código não-teste não existia de todo. Documento e código
// podiam divergir sem nada avermelhar — e um teste que consome a sua própria constante
// prova a constante, não a janela. Consumindo [planmigrate.DeclaredWindow], estes testes
// passaram a exercitar o valor que o pacote publica.
var declaredWindow = planmigrate.DeclaredWindow

// TestDeclaredWindowCoversCurrentLine é a coerência mínima entre a decisão de OPERAÇÃO
// (que readers se retêm) e a decisão de SCHEMA (o que se emite): a linha que este binário
// carimba tem de ser legível pela janela que ele declara. Não deriva uma da outra — são
// decisões distintas, e é por isso que a §3.6.1 as escreve separadas —, mas uma janela
// que não cobrisse a linha corrente tornaria inadmissível, à nascença, todo o plano que
// este binário aprova.
func TestDeclaredWindowCoversCurrentLine(t *testing.T) {
	t.Parallel()
	if !planmigrate.DeclaredWindow.Valid() {
		t.Fatalf("janela declarada incoerente: %+v", planmigrate.DeclaredWindow)
	}
	if !planmigrate.DeclaredWindow.Covers(plan.CurrentPlanVersion) {
		t.Fatalf("a janela declarada %+v nao cobre a linha corrente %s (tecnica/18 §3.6.1)",
			planmigrate.DeclaredWindow, plan.CurrentPlanVersion)
	}
}

// TestDeclaredWindowRejectsForeignMajor — AC1/AC3, direcção (b): a REJEIÇÃO. Um plano
// carimbado num MAJOR fora da janela declarada é inadmissível nas DUAS vias — leitura
// (replay) e escrita (materialização) —, e na escrita a recusa acontece ANTES de tocar
// no REG ou no RM.
//
// PORQUE ESTE TESTE EXISTE MESMO COM O MINOR. É a metade que impede a conclusão errada
// «como foi MINOR, não há nada a rejeitar»: o mecanismo de deprecação continua armado e
// é ele que dá sentido à janela documentada. Um MAJOR 2 hipotético — o que uma quebra
// real de contrato produziria — morre aqui, com sub-código atribuível, em vez de ser
// lido por um reader que não o entende.
//
// FALSIFICÁVEL: o mesmo documento com o MAJOR DENTRO da janela é admitido (controlo de
// não-vacuidade), pelo que a rejeição é da VERSÃO e não do documento.
func TestDeclaredWindowRejectsForeignMajor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	foreign := plan.PlanVersion{Major: 2, Minor: 0, Patch: 0}

	// Sanidade: o MAJOR forasteiro está mesmo fora da janela declarada, e o corrente
	// está dentro (senão o teste provava o contrário do que diz).
	if declaredWindow.Covers(foreign) {
		t.Fatalf("fixture invalida: a janela declarada %+v cobre o MAJOR forasteiro %s", declaredWindow, foreign)
	}
	if !declaredWindow.Covers(plan.CurrentPlanVersion) {
		t.Fatalf("janela declarada %+v nao cobre a linha corrente %s", declaredWindow, plan.CurrentPlanVersion)
	}

	store := newStore(t)
	planID := "plan-schemaline-foreign"
	doc := buildDoc(foreign)
	hash, _ := seedApproval(t, store, planID, doc, nil)
	seedMaterialized(t, store, planID, doc, hash)

	mig := planmigrate.NewMigrator(mustPolicy(t, declaredWindow))
	if _, err := mig.Replay(ctx, store, planID, doc); !errors.Is(err, planmigrate.ErrOutsideSupportWindow) {
		t.Fatalf("Replay de um MAJOR fora da janela devia dar ErrOutsideSupportWindow, deu %v", err)
	}

	// Via de ESCRITA: recusa ANTES de qualquer efeito. Os duplos envenenados falham o
	// teste se forem tocados, e os contadores a zero provam-no também por número.
	reg := &failResolver{t: t}
	rm := &failMonitor{t: t}
	rec, rErr := pe.NewRecorder(store)
	if rErr != nil {
		t.Fatalf("NewRecorder: %v", rErr)
	}
	migW := planmigrate.NewMigrator(mustPolicy(t, declaredWindow),
		planmigrate.WithResolver(reg),
		planmigrate.WithReferenceMonitor(rm),
		planmigrate.WithRecorder(rec),
	)
	if _, err := migW.Materialize(ctx, planID, doc); !errors.Is(err, planmigrate.ErrOutsideSupportWindow) {
		t.Fatalf("Materialize de um MAJOR fora da janela devia dar ErrOutsideSupportWindow, deu %v", err)
	}
	if got := reg.calls.Load(); got != 0 {
		t.Fatalf("REG tocado %d vezes antes da recusa por janela", got)
	}
	if got := rm.calls.Load(); got != 0 {
		t.Fatalf("RM tocado %d vezes antes da recusa por janela", got)
	}

	// NÃO-VACUIDADE: o MESMO documento, carimbado na linha CORRENTE, é admitido pela
	// mesma janela. A rejeição acima é do carimbo, não do conteúdo.
	inWindow := buildDoc(plan.CurrentPlanVersion)
	planID2 := "plan-schemaline-inwindow"
	hash2, _ := seedApproval(t, store, planID2, inWindow, nil)
	seedMaterialized(t, store, planID2, inWindow, hash2)
	if _, err := mig.Replay(ctx, store, planID2, inWindow); err != nil {
		t.Fatalf("um documento na linha corrente devia ser admissivel na janela declarada: %v", err)
	}
}
