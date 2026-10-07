package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/substrate/eventstore"
)

// AOS-505 — O NÓ COMPOSTO A PEDIR O NOME REAL DO MODELO.
//
// A troca do nome pedido pelo nome real é um passo de produção: a allowlist é re-assinada pelo
// operador, com a chave custodiada. O que se prova aqui é o que NÃO depende dessa assinatura — que
// o binário aguenta o nome, com os caracteres que ele tiver (`-`, `/`, `.`), em tudo o que um run
// compõe a partir do modelo. O caminho é o do operador: um bundle de allowlist EXTERNO, aqui
// assinado com uma chave de teste, montado por `AOS_MODEL_ALLOWLIST_BUNDLE_DIR`.

// aos505BundleDeTeste escreve um bundle de allowlist assinado com uma chave de TESTE que autoriza
// os modelos dados para `board-eu`, e devolve o directório e o trust anchor.
func aos505BundleDeTeste(t *testing.T, modelos []string) (dir, anchor string) {
	t.Helper()
	doc := `{"version":"aos505-no/v1","default":"deny","rules":[{"id":"r1","board":"board-eu","models":["` +
		strings.Join(modelos, `","`) + `"],"regions":["eu","eu-west"]}]}`
	seed := sha256.Sum256([]byte("aos-505-no-chave-de-teste"))
	priv := ed25519.NewKeyFromSeed(seed[:])
	digest, err := allowlist.Digest([]byte(doc))
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundlePolicyFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(digest)))
	if err := os.WriteFile(filepath.Join(dir, allowlist.BundleSigFile), []byte(sig), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey))
}

func TestAOS505_No_NomeRealDoModelo(t *testing.T) {
	for _, c := range []struct {
		nome, modo, declarado string
		check                 string
	}{
		// Os dois nomes reais de produção, com perfil: a rota é comparada.
		{"kimi-for-coding", "observe", "openai/kimi-for-coding", "igual"},
		{"k3", "enforce", "openai/k3", "igual"},
		// Nomes com `/` e com `.`: sem perfil, pelo que a governação fica desligada — o que se
		// mede é que o run corre e grava com o nome tal como é.
		{"openai/kimi-for-coding", "off", "", ""},
		{"moonshot/kimi-k2.5-preview", "off", "", ""},
	} {
		t.Run(c.nome, func(t *testing.T) {
			dir, anchor := aos505BundleDeTeste(t, []string{c.nome})
			runID := "run-505-nome-real"
			t.Setenv("AOS_MODEL_ROUTE_GOVERNANCE", c.modo)
			// `enforce` exige o host esperado do endpoint (E4); os outros modos correm sem ele.
			host := ""
			if c.modo == "enforce" {
				host = aos505NoHostA
			}
			t.Setenv("AOS_MODEL_ROUTE_API_HOST", host)
			aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
			t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", anchor)
			real := aos486ComporCom(t, "native", func(cfg *Config) {
				// O harness compôs o cliente antes de este ambiente existir: compõe-se outra vez,
				// pelo mesmo caminho do arranque, já com o nome real e o bundle externo.
				t.Setenv("AOS_MODEL_NAME", c.nome)
				t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", dir)
				modelo, binder, err := parseModelFromEnv(false)
				if err != nil {
					t.Fatalf("parseModelFromEnv com o nome real %q: %v", c.nome, err)
				}
				cfg.Model, cfg.ModelIdentityBinder, cfg.ModelID = modelo, binder, c.nome
			})
			real.upstream.pede = "arquivo"
			real.upstream.responde = aos490RespostaDoProvider
			real.upstream.cabecalhos = func(int) map[string]string { return aos505NoCabecalhos(c.declarado, aos505NoAPIBaseA) }
			m := real.correr(t, runID, nil)

			if len(m.pedidos) != 2 {
				t.Fatalf("queria 2 pedidos, vieram %d", len(m.pedidos))
			}
			for i, p := range m.pedidos {
				var corpo struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(p.cru, &corpo); err != nil || corpo.Model != c.nome {
					t.Fatalf("pedido %d: o provider recebeu o modelo %q (err=%v); quero %q, byte a byte", i+1, corpo.Model, err, c.nome)
				}
			}
			for i, ev := range aos505NoTurnos(m.eventos) {
				var p struct {
					Manifest agentruntime.Manifest `json:"manifest"`
					Check    string                `json:"route_check"`
				}
				if err := json.Unmarshal(ev, &p); err != nil {
					t.Fatal(err)
				}
				if p.Manifest.Model.ModelID != c.nome {
					t.Errorf("turno %d: model_id = %q, quero %q", i+1, p.Manifest.Model.ModelID, c.nome)
				}
				if p.Check != c.check {
					t.Errorf("turno %d: route_check = %q, quero %q", i+1, p.Check, c.check)
				}
				if c.check != "" && p.Manifest.Model.ServedModelID != c.declarado {
					t.Errorf("turno %d: served_model_id = %q, quero %q", i+1, p.Manifest.Model.ServedModelID, c.declarado)
				}
			}
			// Tudo o que o run gravou está em streams de nome válido, e o run conclui.
			if err := eventstore.ValidarStreamID(runID); err != nil {
				t.Fatal(err)
			}
			if got := aos504TiposDeEvento(m); len(got) != len(aos505BaseTiposDeEvento) {
				t.Errorf("o run com o nome real gravou %d eventos, o de referencia grava %d: %v", len(got), len(aos505BaseTiposDeEvento), got)
			}
			rep := aos505NoReplayCom(t, real, runID, m, c.nome)
			if len(rep.Steps) != 2 {
				t.Fatalf("replay: %d passos", len(rep.Steps))
			}
		})
	}
}
