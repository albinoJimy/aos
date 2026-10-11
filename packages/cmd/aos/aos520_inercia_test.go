package main

// AOS-520 (PR 1) — A INÉRCIA DO CANDIDATO, PRESA POR DIGEST CONTRA O BINÁRIO DA BASE.
//
// O critério: com `AOS_MODEL_CANDIDATE` ausente ou `off` — haja ou não um registo de candidato na
// pasta — e com `on` e a fatia assinada a 0, o pedido ao provider, o manifesto de cada turno, o
// registo de retoma, as linhas do arranque que já existiam e o `/metrics` são byte a byte os do
// binário da base sobre as mesmas entradas.
//
// COMO SE MEDIU. Este ficheiro só usa símbolos que existem no commit da base (2e27bf66, o PR 0 do
// AOS-520): foi copiado para lá, corrido, e os digests que imprimiu são as constantes de
// [aos520InerciaDaBase]. No binário da base a variável `AOS_MODEL_CANDIDATE` não é lida — os três
// cenários são, lá, o mesmo nó com outra pasta. Aqui têm de dar os MESMOS digests.
//
// O QUE NÃO ENTRA NO DIGEST, e porquê: do `/metrics`, só as famílias do modelo (`aos_model_*`) e a
// lista dos nomes de TODAS as famílias — os valores das outras dependem do relógio e do processo;
// do arranque, as linhas dos perfis assinados (a linha nova «candidato não servido» é um acerto do
// dono, e é conferida em [TestAOS520_Banner_TitularECandidato]).

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
	"github.com/aos-ref/substrate/eventstore"
)

// aos520AliasCand é o alias do candidato destes testes: um nome que a allowlist embebida já
// autoriza e que NÃO é o do titular (`gpt-4o`).
const aos520AliasCand = "gpt-4o-mini"

// aos520PerfilCand é o perfil assinado do candidato: o do AOS-519, para o outro alias.
var aos520PerfilCand = strings.Replace(rt.ProfileJSON, `"requested":"gpt-4o"`, `"requested":"`+aos520AliasCand+`"`, 1)

// aos520Registo é um registo de entrada assinado a pôr na pasta.
type aos520Registo struct {
	rotulo, perfil string
	fatia          int
}

// aos520PastaCom cria uma pasta de perfis assinados com os registos dados, cada um com a sua
// fatia, e põe as duas variáveis no ambiente. Sem registos, a pasta fica vazia.
func aos520PastaCom(t *testing.T, regs ...aos520Registo) {
	t.Helper()
	dir, key := t.TempDir(), rt.NewKey(t)
	for _, r := range regs {
		report, reportDigest := rt.SealReport(t, rt.ReportDoc(rt.ProfileDigest(t, r.perfil)))
		doc := rt.EntryDoc(t, r.perfil, rt.Board, rt.Region, reportDigest)
		doc["fatia_inicial_percentagem"] = r.fatia
		entry := rt.Marshal(t, doc)
		rt.Write(t, dir, r.rotulo, entry, routeentry.Sign(entry, key.Priv), report)
	}
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", dir)
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", key.Anchor())
}

// aos520Interruptor põe `AOS_MODEL_CANDIDATE` no valor dado; vazio ⇒ a variável não existe.
func aos520Interruptor(t *testing.T, valor string) {
	t.Helper()
	if valor == "" {
		aos504SemVariavel(t, "AOS_MODEL_CANDIDATE")
		return
	}
	t.Setenv("AOS_MODEL_CANDIDATE", valor)
}

// aos520ComporComPasta levanta uma incarnação do nó sobre o substrato dado, com o interruptor do
// candidato e a pasta de perfis no estado em que o ambiente os tiver ([aos520PastaCom] e
// [aos520Interruptor] chamam-se antes). É o [aos520Compor] sem a parte da pasta.
func aos520ComporComPasta(t *testing.T, sub *aos520Substrato) *aos520No {
	t.Helper()
	pinBreakerEnv(t, "0", "0", "0", "0")
	aos504SemVariavel(t, "AOS_MODEL_ROUTE_GOVERNANCE")
	t.Setenv("AOS_MODEL_ROUTE_API_HOST", "")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
	n := aos486ComporCom(t, "native", func(cfg *Config) {
		cfg.EventStore, cfg.EventStorePath = sub.store, ""
		if sub.escuta != nil {
			cfg.EventStore = sub.escuta
		}
		cfg.DSARVault = sub.vault
		cfg.Approvers = sub.approvers
	})
	n.upstream.pede = "arquivo"
	n.upstream.responde = aos490RespostaDoProvider
	log := &bytes.Buffer{}
	n.svc.logMu.Lock()
	n.svc.logw = log
	n.svc.logMu.Unlock()
	return &aos520No{aos486No: n, log: log}
}

func aos520Sha(partes ...string) string {
	h := sha256.New()
	for _, p := range partes {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// aos520Inercia é o que se mede de um nó e de um run: um digest por artefacto.
type aos520Inercia struct {
	pedidos, manifestos, registo, tipos, metricasDoModelo, familias, arranque string
}

// aos520MedirInercia corre um run por `POST /runs` no nó dado e mede os artefactos do critério.
func aos520MedirInercia(t *testing.T, n *aos520No, runID string) aos520Inercia {
	t.Helper()
	ctx := context.Background()
	m := n.correr(t, runID, nil)
	var out aos520Inercia

	var pedidos []string
	for _, p := range m.pedidos {
		pedidos = append(pedidos, string(p.cru))
	}
	out.pedidos = aos520Sha(pedidos...)

	var manifestos, tipos []string
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
	if len(manifestos) != 2 {
		t.Fatalf("turnos gravados = %d; o run destes testes tem 2", len(manifestos))
	}
	out.manifestos, out.tipos = aos520Sha(manifestos...), aos520Sha(tipos...)

	rec, ok, err := n.node.ResumeRecords.Get(ctx, runID)
	if err != nil || !ok {
		t.Fatalf("registo de retoma: ok=%v err=%v", ok, err)
	}
	cru, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	out.registo = aos520Sha(string(cru))

	r := getReq(n.h, "/metrics", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("GET /metrics: %d", r.Code)
	}
	var doModelo []string
	familias := map[string]bool{}
	for _, linha := range strings.Split(r.Body.String(), "\n") {
		if strings.HasPrefix(linha, "# TYPE ") {
			if campos := strings.Fields(linha); len(campos) >= 3 {
				familias[campos[2]] = true
			}
		}
		if strings.Contains(linha, "aos_model_") {
			doModelo = append(doModelo, linha)
		}
	}
	var nomes []string
	for f := range familias {
		nomes = append(nomes, f)
	}
	sort.Strings(nomes)
	if len(nomes) < 20 || len(doModelo) == 0 {
		t.Fatalf("o /metrics nao foi lido como se esperava: %d familias, %d linhas do modelo", len(nomes), len(doModelo))
	}
	out.metricasDoModelo, out.familias = aos520Sha(doModelo...), aos520Sha(nomes...)

	out.arranque = aos520Sha(modelRouteProfilesBanner(n.node.perfisDaRota)...)

	// O livro do canary não existe: nenhum dos três cenários serve um candidato.
	if evs, err := n.node.EventStore.Read(ctx, "aos-internal/model-canary", 1); len(evs) != 0 || !errors.Is(err, eventstore.ErrStreamNotFound) {
		t.Fatalf("sem candidato a servir o livro do canary nao existe: %d eventos, err=%v", len(evs), err)
	}
	return out
}

// aos520InerciaDaBase são os digests medidos no commit da base (2e27bf66) com ESTE ficheiro.
var aos520InerciaDaBase = map[string]aos520Inercia{
	"off com registo de candidato":     {pedidos: "60b4f3a9b7f553979f7581adfd05f7227580163736a0d5d8d1bf99887dc1ed32", manifestos: "0baf6885e1bc1c6f631770fc86989b290148b587720accf414af060af4b302f0", registo: "c2305b9a7e63a6eb3908c8c467a51864829ff61a7bb96ea4b2d6528b94a1826e", tipos: "99f6fdfda8807fc05782d380f028209f0ac839343385afe7e943f7ff37595738", metricasDoModelo: "b94457fad7ceb495e085aad9844120534b125b83fabdff4e0103ae0da9adb5a8", familias: "0765632673de6aaa3a32d9fbbf27a302e1241bd6cdddc3cd3f01675140c5e6b8", arranque: "ab88114fac5070ce23415899a019c4d6098173ebc747a260a6319348b3b03d84"},
	"ausente com registo de candidato": {pedidos: "60b4f3a9b7f553979f7581adfd05f7227580163736a0d5d8d1bf99887dc1ed32", manifestos: "0baf6885e1bc1c6f631770fc86989b290148b587720accf414af060af4b302f0", registo: "c2305b9a7e63a6eb3908c8c467a51864829ff61a7bb96ea4b2d6528b94a1826e", tipos: "99f6fdfda8807fc05782d380f028209f0ac839343385afe7e943f7ff37595738", metricasDoModelo: "b94457fad7ceb495e085aad9844120534b125b83fabdff4e0103ae0da9adb5a8", familias: "0765632673de6aaa3a32d9fbbf27a302e1241bd6cdddc3cd3f01675140c5e6b8", arranque: "ab88114fac5070ce23415899a019c4d6098173ebc747a260a6319348b3b03d84"},
	"on com a fatia a zero":            {pedidos: "60b4f3a9b7f553979f7581adfd05f7227580163736a0d5d8d1bf99887dc1ed32", manifestos: "0baf6885e1bc1c6f631770fc86989b290148b587720accf414af060af4b302f0", registo: "c2305b9a7e63a6eb3908c8c467a51864829ff61a7bb96ea4b2d6528b94a1826e", tipos: "99f6fdfda8807fc05782d380f028209f0ac839343385afe7e943f7ff37595738", metricasDoModelo: "b94457fad7ceb495e085aad9844120534b125b83fabdff4e0103ae0da9adb5a8", familias: "0765632673de6aaa3a32d9fbbf27a302e1241bd6cdddc3cd3f01675140c5e6b8", arranque: "520b845e288bf7c50bf74e25c82a954392f1ece350b1d9a9ff1bd15aba75b955"},
	"on sem registo de candidato":      {pedidos: "60b4f3a9b7f553979f7581adfd05f7227580163736a0d5d8d1bf99887dc1ed32", manifestos: "f89110ea3e0186ccba048b2e0e23b667d990c03b2db3e8252e17e4deabfe2633", registo: "e9ea1daca16718c3f1d5c33240d17fd1f8d50b2f789d08821b016e640db8306a", tipos: "99f6fdfda8807fc05782d380f028209f0ac839343385afe7e943f7ff37595738", metricasDoModelo: "b94457fad7ceb495e085aad9844120534b125b83fabdff4e0103ae0da9adb5a8", familias: "0765632673de6aaa3a32d9fbbf27a302e1241bd6cdddc3cd3f01675140c5e6b8", arranque: "45248163431695cc103ea7ce58accf1d391d838dae784d5347f982e3af378116"},
}

// aos520CenariosDaInercia são os três cenários do critério. No binário da base o interruptor não é
// lido: lá só difere a pasta.
var aos520CenariosDaInercia = []struct {
	nome, interruptor string
	regs              []aos520Registo
}{
	{"off com registo de candidato", "off", []aos520Registo{{"candidato", aos520PerfilCand, 10}}},
	{"ausente com registo de candidato", "", []aos520Registo{{"candidato", aos520PerfilCand, 10}}},
	{"on com a fatia a zero", "on", []aos520Registo{{"candidato", aos520PerfilCand, 0}}},
	{"on sem registo de candidato", "on", nil},
}

func TestAOS520_Inercia_SemCandidatoAServirEOBinarioDaBase(t *testing.T) {
	medir := os.Getenv("AOS520_MEDIR_A_BASE") != ""
	for _, c := range aos520CenariosDaInercia {
		t.Run(c.nome, func(t *testing.T) {
			aos520PastaCom(t, c.regs...)
			aos520Interruptor(t, c.interruptor)
			n := aos520ComporComPasta(t, aos520NovoSubstrato(t))
			got := aos520MedirInercia(t, n, "run-520-inercia-do-candidato")
			if medir {
				t.Logf("BASE %q: %#v", c.nome, got)
				return
			}
			quer, tem := aos520InerciaDaBase[c.nome]
			if !tem {
				t.Fatalf("sem digests da base para o cenario %q", c.nome)
			}
			if got != quer {
				t.Fatalf("o no sem candidato a servir NAO e o da base:\n veio:  %#v\n quero: %#v", got, quer)
			}
		})
	}
}
