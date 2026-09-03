package main

import (
	"context"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	audit "github.com/aos-ref/platform/audit"
)

// Achados de revisão sobre a precedência de AOS-307 — R-03 (adversarial) e S-03 (segurança).
//
// A regra «o último selo de operador prevalece sobre o ambiente» era incondicional na DIRECÇÃO e
// só olhava para os pares DECLARADOS. Isso produzia dois buracos:
//
//   - **sem de-escalada por configuração**: a resposta natural a um incidente — retirar o operador
//     de AOS_AUTONOMY_SETTERS, repor um nível baixo em AOS_AUTONOMY_LEVELS, reiniciar — NÃO
//     baixava o nível; e com a lista de setters vazia `POST /autonomy` recusa tudo, pelo que não
//     sobrava caminho nenhum senão editar o ficheiro do WORM;
//   - **par silencioso**: um par elevado por operador e AUSENTE do ambiente nunca era visitado
//     pelo ciclo de `specs` — ficava em vigor sem entrar na contagem do banner nem em nenhuma
//     lista de divergência, contra o CA que exige que a divergência «nunca seja silenciosa».

// selarComoOperador aplica e sela uma alteração como se viesse de POST /autonomy.
func selarComoOperador(t *testing.T, w *autonomyWiring, agente, dominio string, nivel autonomy.Level, actor string) {
	t.Helper()
	if _, err := w.registry.SetLevel(context.Background(), agente, dominio, nivel, "decisao humana", actor); err != nil {
		t.Fatalf("selar como operador: %v", err)
	}
}

// TestAOS307_OAmbienteGanhaQuandoMudaEPerdeQuandoNao — os dois lados da precedência.
//
// O critério NÃO é a direcção (uma primeira tentativa fê-lo assim e destruía o ticket: o uso
// normal é o ambiente declarar um piso e o operador subir acima dele, pelo que «o mais baixo
// ganha» apagava toda a promoção a cada reinício). O critério é se o FICHEIRO MUDOU desde o que
// aplicou da última vez.
func TestAOS307_OAmbienteGanhaQuandoMudaEPerdeQuandoNao(t *testing.T) {
	worm := audit.NewMemStore()

	// Arranque 1: ambiente L1; operador eleva para L3.
	w1 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	if err := w1.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	selarComoOperador(t, w1, "agt-1", "fs", autonomy.L3, "op:jimy")

	// Arranque 2: ficheiro INALTERADO (ainda L1). A decisão assinada PREVALECE — é AOS-307.
	w2 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}, autonomy.L0)
	if err := w2.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor("agt-1", "fs"); got != autonomy.L3 {
		t.Fatalf("com o ficheiro inalterado a decisao do operador tem de prevalecer: veio %s, quero L3", got)
	}
	if len(w2.preservedOverEnv) != 1 || !strings.Contains(w2.preservedOverEnv[0], "inalterado") {
		t.Errorf("a preservacao tem de dizer que o ficheiro nao mudou: %v", w2.preservedOverEnv)
	}

	// Arranque 3: o responsável EDITA o ficheiro para L0 (resposta a incidente). GANHA.
	w3 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L0}}, autonomy.L0)
	if err := w3.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w3.registry.LevelFor("agt-1", "fs"); got != autonomy.L0 {
		t.Fatalf("o ficheiro editado devia BAIXAR de L3 para L0, veio %s — sem isto nao ha resposta a incidente sem chaves", got)
	}
	if len(w3.ambienteEditado) != 1 || !strings.Contains(w3.ambienteEditado[0], "op:jimy") {
		t.Errorf("a mudanca tem de ser declarada e nomear quem tinha decidido: %v", w3.ambienteEditado)
	}
	// E fica ela própria selada, com actor config:node.
	head, _ := worm.Head(context.Background(), autonomy.DefaultAutonomyPartition)
	recs, _ := worm.Read(context.Background(), autonomy.DefaultAutonomyPartition, head, head)
	if len(recs) != 1 || recs[0].Obligations[0].Params["new_level"] != "L0" ||
		recs[0].Obligations[0].Params["actor"] != autonomyProvisionActor {
		t.Errorf("a mudanca do ambiente nao ficou selada como config:node: %+v", recs)
	}
	linha := strings.Join(autonomyPostureBanner(w3), "\n")
	if !strings.Contains(linha, "MUDOU desde o ultimo provisionamento") {
		t.Errorf("o banner nao declara que o ambiente ganhou por ter mudado:\n%s", linha)
	}

	// Arranque 4: o ficheiro editado também ganha a SUBIR — a autoridade é a do acto deliberado,
	// não a da direcção.
	selarComoOperador(t, w3, "agt-1", "fs", autonomy.L1, "op:maria")
	w4 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L2}}, autonomy.L0)
	if err := w4.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w4.registry.LevelFor("agt-1", "fs"); got != autonomy.L2 {
		t.Fatalf("o ficheiro editado para L2 devia ganhar; veio %s", got)
	}
}

// TestAOS307_ParForaDoAmbienteNaoFicaSilencioso — o cenário literal da auditoria: promover uma
// CLASSE que nunca esteve em AOS_AUTONOMY_LEVELS.
func TestAOS307_ParForaDoAmbienteNaoFicaSilencioso(t *testing.T) {
	worm := audit.NewMemStore()
	specs := []autonomyLevelSpec{{agent: "agt-1", domain: "fs", level: autonomy.L1}}

	w1 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w1.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	// Um par que o ambiente NUNCA declarou.
	selarComoOperador(t, w1, autonomy.ClassPrefix+"agent-break-glass", "fs", autonomy.L5, "op:jimy")

	w2 := buildAutonomyOracle(specs, autonomy.L0)
	if err := w2.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	if got := w2.registry.LevelFor(autonomy.ClassPrefix+"agent-break-glass", "fs"); got != autonomy.L5 {
		t.Fatalf("o par fora do ambiente devia continuar em vigor (L5), veio %s", got)
	}
	if len(w2.foraDoAmbiente) != 1 || !strings.Contains(w2.foraDoAmbiente[0], "agent-break-glass") {
		t.Fatalf("o par fora do ambiente TEM de ser nomeado; veio %v", w2.foraDoAmbiente)
	}
	linha := strings.Join(autonomyPostureBanner(w2), "\n")
	for _, exigido := range []string{"AUSENTE(S) de AOS_AUTONOMY_LEVELS", "agent-break-glass", "op:jimy"} {
		if !strings.Contains(linha, exigido) {
			t.Errorf("o banner nao contem %q — a divergencia fica silenciosa:\n%s", exigido, linha)
		}
	}
	// E conta para o que vigora: sem isto a contagem de provisionamento subestima a postura real.
	if _, ok := w2.sealedPairs[autonomy.ClassPrefix+"agent-break-glass:fs"]; !ok {
		t.Error("o par fora do ambiente tem de contar para o que vigora")
	}

	// CONTROLO — um par de PROVISIONAMENTO antigo que saiu do ambiente NÃO é listado: cede, e
	// nada vigora por decisão humana.
	w3 := buildAutonomyOracle([]autonomyLevelSpec{{agent: "outro", domain: "http", level: autonomy.L1}}, autonomy.L0)
	if err := w3.provision(context.Background(), worm); err != nil {
		t.Fatal(err)
	}
	for _, l := range w3.foraDoAmbiente {
		if strings.Contains(l, "agt-1:fs") {
			t.Errorf("um par de provisionamento que saiu do ambiente nao devia ser listado: %v", w3.foraDoAmbiente)
		}
	}
}

// TestAOS305_BannerQualificaOProvisionamento — o banner não pode afirmar sem reservas que L4/L5
// exigem duas assinaturas, porque o provisionamento por ambiente aplica-os sem nenhuma.
func TestAOS305_BannerQualificaOProvisionamento(t *testing.T) {
	linha := strings.Join(autonomySettersBanner(map[string]bool{"op:a": true, "op:b": true}), "\n")
	if !strings.Contains(linha, "POR POST /autonomy") {
		t.Errorf("o banner nao qualifica a cerimonia como sendo da ROTA:\n%s", linha)
	}
	if !strings.Contains(linha, "SEM assinatura") || !strings.Contains(linha, "AOS_AUTONOMY_LEVELS") {
		t.Errorf("o banner nao declara que o provisionamento por ambiente aplica L4/L5 sem assinatura:\n%s", linha)
	}
	// E o ramo vazio declara a de-escalada como via que sobra.
	vazio := strings.Join(autonomySettersBanner(nil), "\n")
	if !strings.Contains(vazio, "BAIXAR") {
		t.Errorf("sem setters, o banner tem de dizer que o ambiente ainda consegue BAIXAR:\n%s", vazio)
	}
}
