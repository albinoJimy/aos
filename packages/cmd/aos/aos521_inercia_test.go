package main

// AOS-521 — A INÉRCIA DO DISJUNTOR, PRESA POR DIGEST CONTRA O BINÁRIO DA BASE.
//
// O critério: com `AOS_MODEL_BREAKER` ausente ou `off`, o nó — com um candidato A SERVIR — é byte a
// byte o do AOS-520 sobre as mesmas entradas: os pedidos ao provider, o manifesto de cada turno, o
// registo de retoma, a sequência de tipos de evento de cada run e do livro do canary, o `/metrics`, e
// o número de leituras do Event Store que a hospedagem de cada run faz. Sem candidato a servir vale
// a inércia do PR 1 (`aos520_inercia_test.go`), que continua verde sem alteração.
//
// COMO SE MEDIU. Este ficheiro só usa símbolos que existem no commit da base (e85108dc, o PR 2 do
// AOS-520): foi corrido lá com `AOS520_MEDIR_A_BASE=1`, num checkout destacado, e o que imprimiu são
// as constantes de [aos521InerciaDaBase]. Na base a variável `AOS_MODEL_BREAKER` não é lida: os dois
// cenários são, lá, o mesmo nó.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
)

// aos521Inercia é o que se mede: um digest por artefacto, e as leituras em claro.
type aos521Inercia struct {
	pedidos, manifestos, registos, tipos, livro, metricas, familias string
	// leituras é o número de leituras do Event Store que a HOSPEDAGEM de cada run fez — o do
	// candidato e o do titular — ao stream do run, ao das aprovações e ao livro do canary.
	leituras string
}

// aos521MedirInercia corre, num nó com `on` e a fatia a 10%, o run "0" (do candidato) e o run "a"
// (do titular), e mede os artefactos do critério.
func aos521MedirInercia(t *testing.T) aos521Inercia {
	t.Helper()
	aos520PastaCom(t, aos520cCand(10))
	aos520Interruptor(t, "on")
	sub := aos520NovoSubstrato(t)
	conta := &aos520StoreQueConta{Store: sub.store}
	n := aos520ComporComPasta(t, sub, func(cfg *Config) { cfg.EventStore = conta })

	var out aos521Inercia
	var pedidos, manifestos, tipos, registos, leituras []string
	for _, runID := range []string{"0", "a"} {
		antes := [3]int{conta.lidas(runID), conta.lidas(aos520StreamDasAprovacoes), conta.lidas(aos520StreamDoLivro)}
		m := n.correr(t, runID, nil)
		leituras = append(leituras, fmt.Sprintf("%s: run=%d aprovacoes=%d livro=%d", runID,
			conta.lidas(runID)-antes[0], conta.lidas(aos520StreamDasAprovacoes)-antes[1], conta.lidas(aos520StreamDoLivro)-antes[2]))
		for _, p := range m.pedidos {
			pedidos = append(pedidos, string(p.cru))
		}
		for _, ev := range m.eventos {
			tipos = append(tipos, ev.Type)
			if ev.Type != "turn.recorded" {
				continue
			}
			var p struct {
				Manifest json.RawMessage `json:"manifest"`
			}
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatal(err)
			}
			manifestos = append(manifestos, string(p.Manifest))
		}
		rec, ok, err := n.node.ResumeRecords.Get(t.Context(), runID)
		if err != nil || !ok {
			t.Fatalf("registo de retoma de %s: ok=%v err=%v", runID, ok, err)
		}
		cru, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		registos = append(registos, string(cru))
	}
	if len(manifestos) != 4 || len(pedidos) != 4 {
		t.Fatalf("dois runs de dois turnos: %d manifestos, %d pedidos", len(manifestos), len(pedidos))
	}
	out.pedidos, out.tipos, out.leituras = aos520Sha(pedidos...), aos520Sha(tipos...), strings.Join(leituras, "; ")

	// O digest do registo de entrada e o do perfil mudam com a chave de cada execução do teste: no
	// manifesto e no registo de retoma trocam-se por um marcador antes do digest.
	neutro := func(s string) string {
		for i, d := range []string{n.node.canary.candidato.entryDigest, n.node.canary.candidato.profileDigest} {
			s = strings.ReplaceAll(s, d, fmt.Sprintf("<digest-%d>", i))
		}
		return s
	}
	for i := range manifestos {
		manifestos[i] = neutro(manifestos[i])
	}
	for i := range registos {
		registos[i] = neutro(registos[i])
	}
	out.manifestos, out.registos = aos520Sha(manifestos...), aos520Sha(registos...)

	// O LIVRO: a sequência dos tipos de evento. (Os payloads levam o digest do registo.)
	evs, err := sub.store.Read(t.Context(), aos520StreamDoLivro, 1)
	if err != nil {
		t.Fatalf("ler o livro do canary: %v", err)
	}
	var doLivro []string
	for _, ev := range evs {
		doLivro = append(doLivro, ev.Type)
	}
	out.livro = strings.Join(doLivro, ",")

	r := getReq(n.h, "/metrics", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", r.Code)
	}
	var linhas []string
	familias := map[string]bool{}
	for _, linha := range strings.Split(r.Body.String(), "\n") {
		if strings.HasPrefix(linha, "# TYPE ") {
			if campos := strings.Fields(linha); len(campos) >= 3 {
				familias[campos[2]] = true
			}
		}
		for _, prefixo := range []string{"aos_model_", "aos_runs_finished", "aos_runs_completed_without_tool_call_total", "aos_runs_hosted_total", "aos_tool_calls"} {
			if strings.Contains(linha, prefixo) {
				linhas = append(linhas, linha)
				break
			}
		}
	}
	var nomes []string
	for f := range familias {
		nomes = append(nomes, f)
	}
	sort.Strings(nomes)
	if len(nomes) < 20 || len(linhas) < 100 {
		t.Fatalf("o /metrics nao foi lido como se esperava: %d familias, %d linhas", len(nomes), len(linhas))
	}
	out.metricas, out.familias = aos520Sha(linhas...), aos520Sha(nomes...)
	return out
}

// aos521InerciaDaBase é o que este ficheiro mediu no commit da base (e85108dc).
var aos521InerciaDaBase = aos521Inercia{
	pedidos:    "e73b2e0973d2c2b67919bc14833e15af7159a0910d41c4ed006dc96c31719988",
	manifestos: "3a049a98499e368819a82cf3ae3d21faf5e600d614fc87eec59dfb384fa80ec7",
	registos:   "343a2af6aa943701dc03706b3224b0816a33c8db58f77f6da4fab9669fce9b4b",
	// AOS-481: o `tool.call.outcome` passa a chegar ao stream do run; sem ele o digest é o da base
	// (225eff7b…), medido tirando-o da lista.
	tipos:    "4120c3cc3a0d5920a0255597a539ea312cb89e727e770eacf922f761b31851ce",
	livro:    "model.canary.opened,model.canary.plan_assigned",
	metricas: "9701c6288da62eb86521cc545420364f0aac433c4e6a1c8cd8e26bf4548faa00",
	familias: "fd78c028d9ed24c6f6f6c828f563529d2c10aad0005b0bb353e878e73884948d",
	// Seis leituras do stream do run, uma do das aprovações e UMA do livro, por hospedagem — a do
	// candidato e a do titular. O disjuntor desligado não pode acrescentar nenhuma.
	leituras: "0: run=6 aprovacoes=1 livro=1; a: run=6 aprovacoes=1 livro=1",
}

func TestAOS521_Inercia_DisjuntorDesligadoEOBinarioDaBase(t *testing.T) {
	medir := os.Getenv("AOS520_MEDIR_A_BASE") != ""
	for _, valor := range []string{"", "off"} {
		nome := "ausente"
		if valor != "" {
			nome = valor
		}
		t.Run(nome, func(t *testing.T) {
			if valor == "" {
				aos504SemVariavel(t, "AOS_MODEL_BREAKER")
			} else {
				t.Setenv("AOS_MODEL_BREAKER", valor)
			}
			got := aos521MedirInercia(t)
			if medir {
				t.Logf("BASE: %#v", got)
				return
			}
			if got != aos521InerciaDaBase {
				t.Fatalf("com o disjuntor desligado o no NAO e o da base:\n veio:  %#v\n quero: %#v", got, aos521InerciaDaBase)
			}
		})
	}
}
