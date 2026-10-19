package main

// AOS-530 (peça 2 de 3) — UM REGISTO DE USO `tool` NUNCA É CANDIDATO (ADR-042 §2.3, emenda ao
// ADR-041 §2.1).
//
// Com `AOS_MODEL_CANDIDATE=on`, um candidato real e um registo de uso `tool` na mesma pasta: o nó
// arranca (dois registos não-titulares de uso `run` impediam-no) e serve o candidato; o registo
// `tool` não tem adaptador de run, não entra no canary nem no disjuntor, e um run fixado nele é
// recusado. Um registo `tool` com fatia, com um relatório que não é `ocr`, ou para o alias do
// titular, recusa o arranque.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	integration "github.com/aos-ref/integration"
	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos530AllowlistExterna é a pasta da allowlist externa que [aos486ComporCom] põe no ambiente.
// Vazia — a omissão — ⇒ nenhuma, como sempre. Só [aos530ComAllowlistExterna] a muda, e repõe-na.
var aos530AllowlistExterna string

// aos530AliasTool é o alias do registo de uso `tool` destes testes: um terceiro nome, que só a
// allowlist externa do teste autoriza (a embebida autoriza só o do titular e o do candidato).
const aos530AliasTool = "ocr-visao"

// aos530PerfilTool é o perfil do registo de uso `tool`: a capacidade `imagem`, sem `tools`.
var aos530PerfilTool = strings.Replace(rt.ToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos530AliasTool+`"`, 1)

// aos530ComAllowlistExterna assina uma allowlist externa que autoriza, no board e na região dos
// testes, o titular, o candidato e o alias do registo `tool`, e põe-na no ambiente.
func aos530ComAllowlistExterna(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	policyJSON, err := json.Marshal(map[string]any{
		"version": "gw-allowlist/v1", "default": "deny",
		"rules": []map[string]any{{"id": "ext-aos530", "board": rt.Board, "models": []string{aos486Modelo, aos520AliasCand, aos530AliasTool}, "regions": []string{rt.Region}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, err := allowlist.Digest(policyJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundlePolicyFile), policyJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundleSigFile), []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(digest)))), 0o600); err != nil {
		t.Fatal(err)
	}
	aos530AllowlistExterna = dir
	t.Cleanup(func() { aos530AllowlistExterna = "" })
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", dir)
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", base64.StdEncoding.EncodeToString(pub))
}

// aos530Tool é um registo de uso `tool` a pôr na pasta; `estragar` muda-lhe o relatório ou o
// registo antes de selado e assinado.
type aos530Tool struct {
	rotulo, perfil string
	relatorio      func(doc map[string]any)
	registo        func(doc map[string]any)
	// qualificacao cita um relatório de qualificação (o dos candidatos) em vez de um `ocr`.
	qualificacao bool
	// video cita um relatório `video` (AOS-536) em vez de um `ocr`.
	video bool
}

// aos530PastaCom cria uma pasta de perfis assinados com os registos de uso `run` e de uso `tool`
// dados, e põe as duas variáveis no ambiente.
func aos530PastaCom(t *testing.T, runs []aos520Registo, tools ...aos530Tool) {
	t.Helper()
	dir, key := t.TempDir(), rt.NewKey(t)
	for _, r := range runs {
		report, reportDigest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, r.perfil)))
		doc := rt.EntryDoc(t, r.perfil, rt.Board, rt.Region, reportDigest)
		doc["fatia_inicial_percentagem"] = r.fatia
		entry := rt.Marshal(t, doc)
		rt.Write(t, dir, r.rotulo, entry, routeentry.Sign(entry, key.Priv), report)
	}
	for _, x := range tools {
		rdoc := rt.OCRReportDoc(rt.ProfileDigest(t, x.perfil))
		switch {
		case x.qualificacao:
			rdoc = rt.ReportDoc(rt.ProfileDigest(t, x.perfil))
		case x.video:
			rdoc = rt.VideoReportDoc(rt.ProfileDigest(t, x.perfil))
		}
		if x.relatorio != nil {
			x.relatorio(rdoc)
		}
		report, reportDigest := rt.SealReport(t, rdoc)
		doc := rt.ToolEntryDoc(t, x.perfil, rt.Board, rt.Region, reportDigest)
		if x.registo != nil {
			x.registo(doc)
		}
		entry := rt.Marshal(t, doc)
		rt.Write(t, dir, x.rotulo, entry, routeentry.Sign(entry, key.Priv), report)
	}
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", dir)
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", key.Anchor())
}

// O CASO DO CRITÉRIO: `on`, um candidato real a 100% e um registo de uso `tool`. O nó arranca, o
// run é servido pelo candidato, e o registo `tool` não é candidato por via nenhuma.
func TestAOS530_No_CandidatoMaisRegistoToolArrancaEServeOCandidato(t *testing.T) {
	aos530ComAllowlistExterna(t)
	aos530PastaCom(t, []aos520Registo{aos520cCand(100)}, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	aos520Interruptor(t, "on")
	pinBreakerEnv(t, "0", "0", "0", "0")
	t.Setenv("AOS_MODEL_BREAKER", "observe")
	n := aos520ComporComPasta(t, aos520NovoSubstrato(t))

	assinados := n.node.perfisDaRota
	if assinados == nil || len(assinados.entradas) != 2 {
		t.Fatalf("a pasta tinha de carregar os dois registos: %+v", assinados)
	}
	// Nenhuma via de candidato vê o registo `tool`.
	nt := assinados.naoTitulares(aos486Modelo)
	if len(nt) != 1 || nt[0].Profile.Requested != aos520AliasCand {
		t.Fatalf("naoTitulares = %+v; quer so o candidato", nt)
	}
	cand, serve := assinados.candidatoAServir(aos486Modelo)
	if !serve || cand.alias != aos520AliasCand || n.node.canary == nil || !n.node.canary.servir || n.node.canary.candidato.alias != aos520AliasCand {
		t.Fatalf("o candidato a servir e o do canary tinham de ser %q: %+v", aos520AliasCand, cand)
	}
	if n.node.canary.livro.disjuntor == nil || n.node.canary.livro.disjuntor.emVigor != cand.entryDigest {
		t.Fatal("o disjuntor tinha de vigiar o registo do candidato, e so ele")
	}
	d := assinados.despachante
	if d == nil || len(d.porAlias) != 2 || d.porAlias[aos486Modelo] == nil || d.porAlias[aos520AliasCand] == nil {
		t.Fatalf("o despachante tinha de ter so o titular e o candidato: %+v", d)
	}
	if _, tem := d.porAlias[aos530AliasTool]; tem {
		t.Fatal("o registo tool ganhou um adaptador de run")
	}
	// O perfil do registo `tool` está no conjunto do gateway: é por lá que a tool o vai chamar.
	if p, tem := assinados.perfis().For(aos530AliasTool); !tem || p.Digest() != rt.ProfileDigest(t, aos530PerfilTool) {
		t.Fatal("o perfil do registo tool tinha de estar no conjunto de perfis do gateway")
	}

	// O run é servido pelo candidato.
	m := n.correr(t, "run-530-do-candidato", nil)
	if modelos := aos520cModelosPedidos(t, n.aos486No, 0); len(modelos) == 0 || !aos520cTodos(modelos, aos520AliasCand) {
		t.Fatalf("os pedidos pediram %q; todos tinham de pedir o candidato %q", modelos, aos520AliasCand)
	}
	if len(m.eventos) == 0 {
		t.Fatal("o run nao gravou eventos")
	}

	// O arranque declara o registo `tool` à parte, e só o candidato como candidato.
	arranque := strings.Join(modelCandidateBanner(assinados, aos486Modelo), "\n")
	if strings.Contains(arranque, aos530AliasTool) || !strings.Contains(arranque, "A SERVIR") {
		t.Fatalf("a linha do candidato fala do registo tool, ou nao diz o candidato a servir: %s", arranque)
	}
	perfis := modelRouteProfilesBanner(assinados)
	if len(perfis) != 2 || !strings.Contains(perfis[1], "USO TOOL") || !strings.Contains(perfis[1], `"`+aos530AliasTool+`"`) || strings.Contains(perfis[0], "USO TOOL") {
		t.Fatalf("o arranque tinha de declarar o registo tool a parte: %q", perfis)
	}
}

// UM RUN NÃO SE FIXA NUM REGISTO DE USO `tool`: a retoma recusa-o com «modelo não servido», e o
// despachante, sem adaptador para o alias, recusa-o com a mesma causa — nunca o passa ao titular.
func TestAOS530_No_RunFixadoNoRegistoToolERecusado(t *testing.T) {
	aos519AmbienteDoGateway(t)
	aos530ComAllowlistExterna(t)
	aos530PastaCom(t, []aos520Registo{aos520cCand(10)}, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	aos520Interruptor(t, "on")
	cliente, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("candidato e registo tool com on: %v", err)
	}
	assinados := perfisDoCliente(cliente)
	no := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: assinados}
	if no.aliasServido(aos530AliasTool) {
		t.Fatal("o alias de um registo tool e servido a runs")
	}
	if !no.aliasServido(aos520AliasCand) || !no.aliasServido(aos486Modelo) {
		t.Fatal("o titular e o candidato tinham de ser servidos")
	}
	fixado := integration.ResumeRecord{RunID: "r", Model: agentruntime.ModelConfig{ModelID: aos530AliasTool},
		RouteProfileDigest: rt.ProfileDigest(t, aos530PerfilTool), ModelPinnedBy: versaoDaEscolha}
	if err := no.conferirPerfilFixado(fixado); causaDoPerfilFixado(err) != modelgateway.PinnedCauseModelNotServed {
		t.Fatalf("run fixado no registo tool: quer %s; veio %v", modelgateway.PinnedCauseModelNotServed, err)
	}
	d, ok := aos520cDespachante(cliente)
	if !ok {
		t.Fatal("com um candidato carregado o cliente e um despachante")
	}
	_, err = d.Call(withModeloDoRun(context.Background(), aos530AliasTool), agentruntime.PromptView{})
	if !errors.Is(err, ErrModelNotServed) || !strings.Contains(err.Error(), modelgateway.PinnedCauseModelNotServed) {
		t.Fatalf("o despachante tinha de recusar o alias do registo tool: %v", err)
	}
}

// SÓ UM REGISTO DE USO `tool`, com `on`: não há candidato, o cliente não ganha despachante, e o
// interruptor não tem o que servir.
func TestAOS530_No_SoORegistoToolNaoECandidato(t *testing.T) {
	aos519AmbienteDoGateway(t)
	aos530ComAllowlistExterna(t)
	aos530PastaCom(t, nil, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	aos520Interruptor(t, "on")
	cliente, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatalf("so o registo tool com on: %v", err)
	}
	assinados := perfisDoCliente(cliente)
	if _, serve := assinados.candidatoAServir(aos486Modelo); serve || len(assinados.naoTitulares(aos486Modelo)) != 0 {
		t.Fatal("o registo tool foi tratado como candidato")
	}
	if _, ok := aos520cDespachante(cliente); ok {
		t.Fatal("sem candidato de uso run nao ha despachante")
	}
}

// AS RECUSAS DE ARRANQUE de um registo de uso `tool`, uma de cada vez: com fatia, a citar um
// relatório que não é `ocr`, um `ocr` que não é qualificado no modo real, e o alias do titular.
func TestAOS530_No_RegistoToolQueRecusaOArranque(t *testing.T) {
	casos := []struct {
		nome  string
		tool  aos530Tool
		causa string
		erro  error
	}{
		{nome: "fatia maior que zero", causa: routeentry.CauseToolWithShare,
			tool: aos530Tool{perfil: aos530PerfilTool, registo: func(d map[string]any) { d["fatia_inicial_percentagem"] = 10 }}},
		{nome: "relatorio de qualificacao", causa: routeentry.CauseReportOfOtherExperiment,
			tool: aos530Tool{perfil: aos530PerfilTool, qualificacao: true}},
		{nome: "relatorio ocr recusado", causa: routeentry.CauseNotQualified,
			tool: aos530Tool{perfil: aos530PerfilTool, relatorio: func(d map[string]any) { rt.OCRVerdict(d)["veredicto"] = "recusado" }}},
		{nome: "relatorio ocr do modo falso", causa: routeentry.CauseVerdictNotForProduction,
			tool: aos530Tool{perfil: aos530PerfilTool, relatorio: func(d map[string]any) { d["modo"] = "falso" }}},
		{nome: "perfil com tools", causa: routeentry.CauseToolProfileCapabilities,
			tool: aos530Tool{perfil: strings.Replace(aos530PerfilTool, `["imagem"]`, `["imagem","tools"]`, 1)}},
		{nome: "uso fora do vocabulario", causa: routeentry.CauseBadUse,
			tool: aos530Tool{perfil: aos530PerfilTool, registo: func(d map[string]any) { d["uso"] = "ferramenta" }}},
		{nome: "alias do titular", erro: ErrModelToolEntryForTitular,
			tool: aos530Tool{perfil: strings.Replace(rt.ToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos486Modelo+`"`, 1)}},
	}
	for _, c := range casos {
		for _, interruptor := range []string{"on", "off"} {
			t.Run(c.nome+" "+interruptor, func(t *testing.T) {
				aos519AmbienteDoGateway(t)
				aos530ComAllowlistExterna(t)
				c.tool.rotulo = "ocr"
				aos530PastaCom(t, []aos520Registo{aos520cCand(10)}, c.tool)
				aos520Interruptor(t, interruptor)
				_, _, err := parseModelFromEnv(false)
				if c.erro != nil {
					if !errors.Is(err, c.erro) || !strings.Contains(err.Error(), `"ocr"`) {
						t.Fatalf("quer %v com o rotulo; veio %v", c.erro, err)
					}
					return
				}
				if !errors.Is(err, ErrBadModelRouteProfiles) || routeentry.CauseOf(err) != c.causa {
					t.Fatalf("quer ErrBadModelRouteProfiles com causa=%s; veio %v", c.causa, err)
				}
			})
		}
	}
}
