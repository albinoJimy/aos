package main

// aos409_eixo_de_mutacao_test.go — AOS-409 (fecha o DEF-275) na metade do `aos-orq`: o eixo de
// mutação entra no DIGEST do snapshot selado e chega ao CARTÃO do gate como `danger`.
//
// E o critério `IsEffectTool` com o 4.º eixo sobre o CATÁLOGO de produção (o manifesto do nó). A
// carga obrigatória e a conferência com o nó provam-se em snapshot_test.go e
// aos441_snapshot_vs_catalogo_test.go; o que o nó SERVE do mesmo manifesto, em
// packages/cmd/aos/aos409_eixo_de_mutacao_test.go.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/control-plane/orchestrator/planvalidate"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// aos409SnapshotComEscrita tem UMA tool que escreve localmente e se desfaz: `egress none` +
// `reversible` + `mutates`. Pelos três eixos do classificador é uma leitura.
const aos409SnapshotComEscrita = `{
  "hash": "sha256:snap-aos409",
  "tools": [
    {"name":"doc_edit","version":"1.0.0","digest":"sha256:eee","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible","mutation":"mutates"}
  ]
}`

// aos409ManifestoDeProducao é o manifesto de tools que o nó de produção oferece ao modelo — o
// MESMO ficheiro que packages/cmd/aos/aos441_catalogo_de_tools_test.go lê, pelo mesmo caminho.
const aos409ManifestoDeProducao = "../../../deploy/server/model-tools/tools.json"

// aos409CapabilityDoManifesto lê o manifesto do nó e devolve a capability do `doc_read` com os
// eixos que o nó DECLARA, traduzidos pelas tabelas deste `aos-orq` (as mesmas da carga do
// snapshot e da conferência). Os vazios seguem a semântica fail-closed que o nó aplica ao servir
// `GET /tools` (packages/cmd/aos/catalogo_de_tools.go): egress ⇒ `unknown`, reversibilidade ⇒
// `irreversible`, mutação ⇒ `mutates`. O nó não se importa daqui (é outro `main`), e o lado dele
// prova-se em packages/cmd/aos/aos409_eixo_de_mutacao_test.go.
func aos409CapabilityDoManifesto(t *testing.T, raw []byte) planvalidate.Capability {
	t.Helper()
	var specs []toolDoNo // os nomes JSON dos eixos são os mesmos no manifesto e no catálogo
	if err := json.Unmarshal(raw, &specs); err != nil {
		t.Fatalf("manifesto ilegível: %v", err)
	}
	for _, s := range specs {
		if s.Name != "doc_read" {
			continue
		}
		vazio := func(v, def string) string {
			if v == "" {
				return def
			}
			return v
		}
		eg, ok1 := egressos[vazio(s.Egress, "unknown")]
		rev, ok2 := reversibilidades[vazio(s.Reversibility, "irreversible")]
		mut, ok3 := mutacoes[vazio(s.Mutation, "mutates")]
		if !ok1 || !ok2 || !ok3 {
			t.Fatalf("doc_read declara um eixo fora do vocabulário: %+v", s)
		}
		return planvalidate.Capability{Name: s.Name, Version: "1.0.0", Digest: "sha256:d", Admissible: true,
			Sensitivity: risk.SensitivityPublic, Egress: eg, Reversibility: rev, Mutation: mut}
	}
	t.Fatal("o manifesto de produção deixou de nomear `doc_read`")
	return planvalidate.Capability{}
}

// TestAOS409IsEffectToolSobreOCatalogoDeProducao é o critério 2 do ticket: `IsEffectTool` com o
// 4.º eixo, sobre o CATÁLOGO de produção e não sobre um literal de teste.
//
// O `doc_read` de produção é `egress none` + `reversible`: pelos dois eixos antigos é uma leitura
// em QUALQUER caso, e por isso só o terceiro decide. Mede-se nos três estados do manifesto, com
// tudo o resto igual: `"none"` ⇒ sem efeito (o verificador pode pinar); sem o campo ⇒ de efeito
// (fail-closed); `"mutates"` ⇒ de efeito.
//
// FALHA-ANTES (DEF-275): os dois últimos davam «sem efeito» — uma escrita local com undo passava
// por leitura, e um verificador podia pinar a tool que mexe no que revê.
func TestAOS409IsEffectToolSobreOCatalogoDeProducao(t *testing.T) {
	raw, err := os.ReadFile(aos409ManifestoDeProducao)
	if err != nil {
		t.Fatalf("ler o manifesto de produção: %v", err)
	}
	prod := aos409CapabilityDoManifesto(t, raw)
	if prod.Egress != risk.EgressNone || prod.Reversibility != risk.Reversible {
		t.Fatalf("o doc_read de produção deixou de ser egress none + reversible (%+v) — o teste já não isola o eixo de mutação", prod)
	}
	if prod.Mutation != planvalidate.MutationNone {
		t.Fatalf("o manifesto de produção tem de declarar `\"mutation\":\"none\"` no doc_read (transição do AOS-409), veio %v", prod.Mutation)
	}
	if planvalidate.IsEffectTool(prod) {
		t.Fatal("o doc_read de produção (leitura declarada) foi classificado DE EFEITO — o verificador perdia a única tool que tem")
	}

	for nome, trocar := range map[string][2]string{
		"sem o campo": {`"mutation": "none",`, ``},
		"mutates":     {`"mutation": "none"`, `"mutation": "mutates"`},
	} {
		t.Run(nome, func(t *testing.T) {
			derivado := strings.Replace(string(raw), trocar[0], trocar[1], 1)
			if derivado == string(raw) {
				t.Fatalf("o manifesto de produção mudou de forma: %q não se encontra", trocar[0])
			}
			c := aos409CapabilityDoManifesto(t, []byte(derivado))
			if !c.Mutation.Mutates() || !planvalidate.IsEffectTool(c) {
				t.Fatalf("uma tool mutadora sem egress e reversível foi classificada SEM efeito — é o DEF-275 (%+v)", c)
			}
		})
	}
}

// TestAOS409DigestMudaSoComAMutacao — o eixo de mutação ENTRA no digest do conteúdo.
//
// FALHA-ANTES (o buraco A1 do AOS-408, reaberto por um eixo novo): um snapshot igual em tudo menos
// na mutação dava o MESMO digest, e o selo do `plan.validated` não distinguia o catálogo sob o qual
// o humano decidiu de outro que declarasse a escrita como leitura.
func TestAOS409DigestMudaSoComAMutacao(t *testing.T) {
	snap, err := carregarSnapshot(escreverTmp(t, aos409SnapshotComEscrita))
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	outro := planvalidate.Snapshot{Hash: snap.Hash, Tools: append([]planvalidate.Capability(nil), snap.Tools...)}
	outro.Tools[0].Mutation = planvalidate.MutationNone
	if digestDoSnapshot(snap) == digestDoSnapshot(outro) {
		t.Fatal("mudar SÓ a mutação não mudou o digest do snapshot — o selo deixava trocar a escrita por leitura")
	}
	// Não-vacuidade: o digest é estável para o mesmo conteúdo.
	if digestDoSnapshot(snap) != digestDoSnapshot(planvalidate.Snapshot{Hash: snap.Hash, Tools: snap.Tools}) {
		t.Fatal("o digest não é determinístico para o mesmo conteúdo")
	}
}

// TestAOS409EscritaChegaAoCartaoComoDanger — R1 no caminho do `aos-orq`: o snapshot lido do
// FICHEIRO (o carregador real) → [planvalidate.ResolveRisks] → [planoParaGate]. O nó que usa a
// tool que escreve declara-se `safe` e chega ao cartão `danger`, irreversível, a exigir humano.
//
// FALHA-ANTES: derivava `safe` e o gate auto-aprovava uma escrita.
func TestAOS409EscritaChegaAoCartaoComoDanger(t *testing.T) {
	doc := plan.PlanDocument{
		PlanVersion: plan.CurrentPlanVersion,
		Objective:   "AOS-409: uma escrita local com undo",
		PlannerMeta: plan.PlannerMeta{Model: "fixture", PromptVersion: "1.2.0", CapabilitiesHash: "sha256:snap-aos409"},
		Nodes: []plan.Node{{
			NodeID: "n1.editar", Role: "worker", Objective: "editar o documento",
			Tools:     []plan.ToolRef{{Name: "doc_edit", Version: "1.0.0", Digest: "sha256:eee"}},
			RiskClass: plan.RiskSafe,
		}},
	}
	cartao := func(snapJSON string) (risk.Class, bool, bool) {
		snap, err := carregarSnapshot(escreverTmp(t, snapJSON))
		if err != nil {
			t.Fatalf("carregarSnapshot: %v", err)
		}
		pl := planoParaGate(doc, planvalidate.ResolveRisks(doc, snap, nil), "run-aos409", "agt-run-aos409", "orq")
		return pl.Nodes[0].Class, pl.Nodes[0].Irreversible, nosQueExigemHumano(pl)["n1.editar"]
	}

	classe, irrev, humano := cartao(aos409SnapshotComEscrita)
	if classe != risk.ClassDanger || !irrev || !humano {
		t.Fatalf("a escrita tinha de chegar ao cartão danger, irreversível e a exigir humano; veio classe=%v irreversível=%v humano=%v", classe, irrev, humano)
	}

	// Não-vacuidade: a MESMA tool declarada leitura é `safe` e passa sem humano.
	classe, _, humano = cartao(strings.Replace(aos409SnapshotComEscrita, `"mutation":"mutates"`, `"mutation":"none"`, 1))
	if classe != risk.ClassSafe || humano {
		t.Fatalf("a leitura declarada tinha de ser safe sem humano; veio classe=%v humano=%v", classe, humano)
	}
}

// TestAOS409SnapshotComAMutacaoTrocadaNaoEOSelado — o mesmo com o BINÁRIO: o plano aprovado sob o
// snapshot S não corre sob um S' que só difere na MUTAÇÃO de uma tool. A troca escolhida não muda
// a classe de risco (o `http.post` continua irreversível e externo ⇒ `danger`), e por isso só o
// digest do conteúdo a apanha — é exactamente o caso que o eixo fora do digest deixaria passar.
func TestAOS409SnapshotComAMutacaoTrocadaNaoEOSelado(t *testing.T) {
	bin := construir(t)
	dir := t.TempDir()
	const run = "run-aos409-snap-mutacao"
	wal, _, doc := aos412Aprovado(t, bin, dir, run)

	trocado := filepath.Join(dir, "snap-mutacao.json")
	conteudo := strings.Replace(aos408SnapshotComPerigo,
		`"reversibility":"irreversible","mutation":"mutates"`,
		`"reversibility":"irreversible","mutation":"none"`, 1)
	if conteudo == aos408SnapshotComPerigo {
		t.Fatal("a substituição não mudou nada — o fixture do AOS-408 mudou de forma")
	}
	escrever(t, trocado, conteudo)

	r := correr(t, bin, "serve", "--wal", wal, "--run", run, "--plan-doc", doc, "--snapshot", trocado, "--worker", "p2")
	if r.code == exitOK || strings.Contains(r.stdout, "materializado:") {
		t.Fatalf("um snapshot com a mutação trocada não é o selado e tinha de ser recusado, saiu %d\n%s", r.code, r.stdout)
	}
	if !strings.Contains(r.stderr, "nao e o selado") {
		t.Fatalf("a recusa tinha de ser a do snapshot selado:\n%s", r.stderr)
	}
}

// TestAOS409MutacaoUnknownExplicitaContaComoMutador — um `"mutation":"unknown"` ESCRITO no
// snapshot é o valor-zero dito em voz alta: carrega, fica `MutationUnknown` e o oráculo trata a
// tool como de EFEITO. Sem este caso, um carregador que normalizasse `unknown` para `none` passava
// toda a suite (medido na revisão adversarial do AOS-409) — e sem `AOS_ORQ_NODE_URL` não há
// conferência com o nó que o apanhasse.
func TestAOS409MutacaoUnknownExplicitaContaComoMutador(t *testing.T) {
	p := escreverTmp(t, `{
  "hash": "sha256:s",
  "tools": [
    {"name":"doc_read","version":"1.0.0","digest":"sha256:a","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible","mutation":"unknown"}
  ]
}`)
	snap, err := carregarSnapshot(p)
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	if got := snap.Tools[0].Mutation; got != planvalidate.MutationUnknown {
		t.Fatalf("mutation = %v, quer unknown — o carregador não pode baixar o valor declarado", got)
	}
	if !snap.EffectOracle()(toolRef("doc_read", "1.0.0", "sha256:a")) {
		t.Fatal("uma tool com mutação `unknown` saiu SEM efeito — o valor por declarar tem de contar como mutador")
	}
}
