package main

// AOS-467 — A GERAÇÃO QUE PASSA O TECTO FECHA SEM PLANEAR.
//
// O nó decide o tecto de gerações e entrega a geração que o passa marcada (`generations_exhausted`);
// o `consume` fecha-a como terminal com a saída 12, SEM `serve` — o molde da saída 11 do AOS-439.
// Estes testes correm o binário real contra o nó falso do AOS-442, com um espião no desfecho.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAOS467OCodigo12NaoCorreServeEFecha(t *testing.T) {
	bin := construir(t)
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	espiao := &aos443Espiao{}
	frente := espiao.servidor(t, strings.TrimPrefix(a.env[0], "AOS_ORQ_NODE_URL="))
	a.env[0] = "AOS_ORQ_NODE_URL=" + frente.URL

	const run = "plan-aos467"
	// Um documento de uma geração anterior: o pedido acabou, e a cópia em claro tem de sair.
	doc := a.documentoDe(t, run)
	if err := os.MkdirAll(filepath.Dir(doc), 0o700); err != nil {
		t.Fatal(err)
	}
	escrever(t, doc, planoFixtureDuasFolhasComSnapshotAOS408)

	no.oferecer(pedidoReclamado{RunID: run, Objective: "", Geracao: 6, GeracoesEsgotadas: true})
	r := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408)

	exigirDesfechos(t, no.vistos(), pedidoDeDesfechoVisto{RunID: run, Geracao: 6, Classe: "terminal", Codigo: exitGeracoesEsgotadas})
	if strings.Contains(r.stdout, "origem do plano:") || strings.Contains(r.stdout, "posse:") {
		t.Fatalf("a geracao esgotada correu um serve:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "aviso: run="+run+" geracao=6 classe=terminal codigo=12") {
		t.Fatalf("o fecho tem de avisar o operador (AOS-445):\n%s", r.stdout)
	}
	if _, err := os.Stat(doc); !os.IsNotExist(err) {
		t.Fatalf("o documento do plano fechado ficou no disco (%v)", err)
	}
	espiao.mu.Lock()
	defer espiao.mu.Unlock()
	if len(espiao.desfechos) != 1 {
		t.Fatalf("esperava 1 desfecho, vieram %d", len(espiao.desfechos))
	}
	c, _ := espiao.desfechos[0]["consumo"].(map[string]any)
	if c["tokens"] != float64(0) || c["tokens_medidos"] != true || c["custo_medido"] != true {
		t.Fatalf("o fecho nao chama o modelo: consumo zero medido, veio %v", c)
	}
	if espiao.desfechos[0]["chamou_modelo"] != false {
		t.Fatalf("o fecho nao chama o modelo: chamou_modelo=%v", espiao.desfechos[0]["chamou_modelo"])
	}
	if !strings.HasSuffix(espiao.desfechos[0]["detalhe"].(string), "erro=geracoes_esgotadas") {
		t.Fatalf("o detalhe tem de dizer porque fechou: %v", espiao.desfechos[0]["detalhe"])
	}
}

// TestAOS467SemAMarcaPlaneiaComoAntes — o CONTROLO: a mesma geração sem a marca corre o `serve`.
func TestAOS467SemAMarcaPlaneiaComoAntes(t *testing.T) {
	bin := construir(t)
	no := &aos442No{}
	a := novoAmbiente442(t, bin, no)
	no.oferecer(pedidoReclamado{RunID: "plan-aos467-ctl", Objective: "o", Geracao: 6})
	r := a.consumir(t, planoFixtureDuasFolhasComSnapshotAOS408, "--plan-timeout", "30s")
	if !strings.Contains(r.stdout, "origem do plano:") {
		t.Fatalf("sem a marca o pedido planeia:\n%s", r.stdout)
	}
	if v := no.vistos(); len(v) != 1 || v[0].Codigo == exitGeracoesEsgotadas {
		t.Fatalf("sem a marca nao ha saida 12: %+v", v)
	}
}

// TestAOS467AMarcaVemComONomeQueONoEscreve — o nome escrito à MÃO, como no
// TestAOS466ODesfechoLevaOConsumoComOsNomesQueONoLe: o nó falso dos outros testes serializa o MESMO
// struct, e uma tag mudada nas duas pontas passava. O nó escreve `generations_exhausted`
// (respostaDeReclamo em packages/cmd/aos/plan_claim.go).
func TestAOS467AMarcaVemComONomeQueONoEscreve(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /plans/claim", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"run_id":"plan-467","objective":"","generation":6,"generations_exhausted":true}`))
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"access_token":"tok"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	p, houve, err := aos413ClienteDoAmbiente(t, srv.URL).ReclamarPedido(context.Background())
	if err != nil || !houve {
		t.Fatalf("reclamar: %v %v", houve, err)
	}
	if !p.GeracoesEsgotadas || p.Geracao != 6 {
		t.Fatalf("a marca do no nao chegou ao consume: %+v", p)
	}
}
