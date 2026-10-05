package planapproval

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// AOS-501 — A VERSÃO DO CONTRATO DO CARTÃO SOBE QUANDO O CARTÃO LEVA ORIGEM.
//
// O AOS-500 alargou a gramática de uma saída no cartão (o quarto segmento, `:tool=<nome>`) e
// deixou o carimbo em 1.1.0, porque nenhum plano com origem corria. Com a entrega por referência
// passam a existir cartões com origem, e a regra de version.go vale: sem a subida, dois binários
// carimbavam a mesma versão e discordavam sobre o que o cartão mostra.
//
// O carimbo é o do contrato que o cartão USA: 1.2.0 com origem, 1.1.0 sem — pelo que nenhum
// cartão de hoje muda de bytes (isso está preso em aos500_origem_da_saida_test.go).

func TestAOS501_OCartaoComOrigemCarimba120(t *testing.T) {
	v120 := PlanCardSchemaVersion{Major: 1, Minor: 2, Patch: 0}
	v110 := PlanCardSchemaVersion{Major: 1, Minor: 1, Patch: 0}
	if CurrentVersion != v120 {
		t.Fatalf("a versao corrente do contrato do cartao e %s; o AOS-501 sobe-a a 1.2.0", CurrentVersion)
	}
	if k := Classify(v110, CurrentVersion); k != ChangeMinor || !CurrentVersion.Compatible(v110) {
		t.Fatalf("1.1.0 -> %s tem de ser um MINOR compativel; e %s", CurrentVersion, k)
	}
	com, err := BuildPlanCard(planoDeLeitura("doc_read"))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	if com.SchemaVersion != v120 {
		t.Fatalf("um cartao com origem declarada carimba %s; tem de carimbar 1.2.0", com.SchemaVersion)
	}
	sem, err := BuildPlanCard(planoDeLeitura(""))
	if err != nil {
		t.Fatalf("BuildPlanCard: %v", err)
	}
	if sem.SchemaVersion != v110 {
		t.Fatalf("um cartao sem origem carimba %s; tem de continuar a carimbar 1.1.0", sem.SchemaVersion)
	}
	// O carimbo do span é o do cartão do mesmo plano.
	if got := versionFor(planDeclaresOutputSource(planoDeLeitura("doc_read"))); got != com.SchemaVersion {
		t.Fatalf("o span carimbaria %s e o cartao carimba %s", got, com.SchemaVersion)
	}
	if got := versionFor(planDeclaresOutputSource(planoDeLeitura(""))); got != sem.SchemaVersion {
		t.Fatalf("o span carimbaria %s e o cartao carimba %s", got, sem.SchemaVersion)
	}
}

// TestAOS501_CartaoComOrigemEmVersaoAnteriorERecusado: o carimbo tem de identificar o contrato
// apresentado. Um cartão que mostra a origem e diz 1.1.0 — o que um binário entre o AOS-500 e o
// AOS-501 escreveria — é recusado; o mesmo cartão com 1.2.0 passa, e um sem origem passa com
// qualquer das duas.
func TestAOS501_CartaoComOrigemEmVersaoAnteriorERecusado(t *testing.T) {
	wire := string(wireDoCartao(t, planoDeLeitura("doc_read")))
	if !strings.Contains(wire, `{"schema_version":"1.2.0","run_id"`) {
		t.Fatalf("pre-condicao: o wire do cartao com origem carimba 1.2.0 no topo:\n%s", wire)
	}
	antigo := strings.Replace(wire, `{"schema_version":"1.2.0","run_id"`, `{"schema_version":"1.1.0","run_id"`, 1)
	var c PlanCard
	if err := json.Unmarshal([]byte(antigo), &c); err != nil {
		t.Fatalf("o wire com o carimbo trocado tem de desserializar (a recusa e do Validate): %v", err)
	}
	if err := c.Validate(); !errors.Is(err, ErrOutputSourceBelowVersion) {
		t.Fatalf("um cartao com origem carimbado 1.1.0 tinha de ser recusado com ErrOutputSourceBelowVersion; veio %v", err)
	}
	var novo PlanCard
	if err := json.Unmarshal([]byte(wire), &novo); err != nil || novo.Validate() != nil {
		t.Fatalf("o cartao com origem carimbado 1.2.0 valida: %v / %v", err, novo.Validate())
	}
	// Um cartão SEM origem carimbado 1.2.0 (um leitor futuro que carimbe sempre a corrente) passa:
	// a regra só exige o carimbo a quem mostra a origem.
	semWire := strings.Replace(string(wireDoCartao(t, planoDeLeitura(""))), `{"schema_version":"1.1.0","run_id"`, `{"schema_version":"1.2.0","run_id"`, 1)
	var sem PlanCard
	if err := json.Unmarshal([]byte(semWire), &sem); err != nil || sem.Validate() != nil {
		t.Fatalf("um cartao sem origem carimbado 1.2.0 valida: %v / %v", err, sem.Validate())
	}
}
