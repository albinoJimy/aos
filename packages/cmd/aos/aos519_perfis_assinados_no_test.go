package main

// AOS-519 — OS PERFIS DE ROTA ASSINADOS, medidos pelo nó COMPOSTO
// (`AOS_MODEL_ROUTE_PROFILES_DIR` e `AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR`).
//
// O molde é o do AOS-505: o cliente de modelo sai de [parseModelFromEnv], como no arranque, e fala
// com um provider em httptest que emite os cabeçalhos que o proxy de produção emite sobre a rota.
// A matriz completa das causas de recusa está no pacote `policy/routeentry` do gateway; aqui
// prova-se que cada conferência chega ao ARRANQUE DO NÓ como um aborto, que sem a pasta o nó é o
// de antes, e que um perfil assinado serve de facto um run.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	"github.com/aos-ref/platform/audit"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos519Novo é o modelo esperado do perfil assinado destes testes: um que a tabela em código não
// conhece, com `/` e com `.`.
const aos519Novo = "openai/modelo-novo-4.5"

// aos519DigestsDaTabela são os digests dos quatro perfis da tabela em código, medidos na base
// deste ticket (são os do AOS-513). Sem a pasta, têm de ser estes.
var aos519DigestsDaTabela = map[string]string{
	"gpt-4o-mini":     "sha256:0cbc100e54038f013a6c84867ff08510095f47dd17b2dd07f68b5dc74f090d0f",
	"gpt-4o":          "sha256:efdd13fbdba0b96a8bc83cad7e2fb3f30f7e3ce5553a4eb0f736d7ab69efba36",
	"kimi-for-coding": "sha256:8ed9035604f0339c00e51557c4e0be712126b5f7d381853483acfdd67112cd64",
	"k3":              "sha256:3047b14cc414c48103182b051c0be23b6430c0ad2d53c0b4bb9eafa822958095",
}

// aos519Pasta cria uma pasta de perfis com um registo VÁLIDO para o perfil dado, e põe as duas
// variáveis no ambiente. Devolve a pasta, a chave, e os bytes do registo e do relatório.
func aos519Pasta(t *testing.T, perfil string) (dir string, key rt.Key, entry, report []byte) {
	t.Helper()
	dir, key = t.TempDir(), rt.NewKey(t)
	entry, report = rt.Good(t, dir, "modelo-novo", perfil, rt.Board, rt.Region, key)
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", dir)
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", key.Anchor())
	return dir, key, entry, report
}

// aos519AmbienteDoGateway põe no ambiente o mínimo para [parseModelFromEnv] compor um gateway (o
// de [aos486ComporCom]), sem nó. O endpoint não é contactado.
func aos519AmbienteDoGateway(t *testing.T) {
	t.Helper()
	t.Setenv("AOS_MODEL_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("AOS_MODEL_NAME", aos486Modelo)
	for _, v := range []string{"AOS_MODEL_REGION", "AOS_MODEL_BOARD", "AOS_MODEL_API_KEY_PATH", "AOS_MODEL_ALLOWLIST_BUNDLE_DIR",
		"AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", "AOS_MODEL_PRICING_PATH", "AOS_MODEL_AUDIT_PATH", "AOS_MODEL_EGRESS_TIMEOUT", "AOS_MODEL_TOOLS",
		"AOS_MODEL_ROUTE_GOVERNANCE", "AOS_MODEL_ROUTE_API_HOST", "AOS_MODEL_PROVIDER_STATE", "AOS_MODEL_RESPONSE_SHAPE"} {
		t.Setenv(v, "")
	}
	t.Setenv("AOS_MODEL_PROJECTION", "native")
	aos504SemVariavel(t, "AOS_MODEL_PROJECTION_VERSION")
}

// INERTE SEM A PASTA. Com `AOS_MODEL_ROUTE_PROFILES_DIR` ausente ou vazia — e a âncora definida
// com LIXO, para provar que nem é lida —: nada é carregado, o cliente de modelo não ganha
// invólucro, os digests dos quatro perfis da tabela são os da base, os pedidos ao provider são os
// goldens do AOS-490, os `turn.recorded` e o `/metrics` são os da base do AOS-505, e o arranque
// não declara linha nenhuma. (Os goldens dos AOS-490/505/513/514/516 correm sem a pasta e ficam
// verdes sem alteração: é a outra metade desta prova.)
func TestAOS519_No_SemPasta_EOBinarioDeAntes(t *testing.T) {
	for nome, definir := range map[string]bool{"variavel ausente": false, "variavel vazia": true} {
		t.Run(nome, func(t *testing.T) {
			if definir {
				t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", "  ")
			} else {
				aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_DIR")
			}
			t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", "isto nao e uma chave")
			if p, err := loadSignedRouteProfilesFromEnv(); p != nil || err != nil {
				t.Fatalf("sem pasta nada e lido: %v %v", p, err)
			}
			for nomePedido, quer := range aos519DigestsDaTabela {
				perfil, tem := (*perfisAssinados)(nil).perfis().For(nomePedido)
				if !tem || perfil.Digest() != quer {
					t.Errorf("perfil %q: digest %s; o da base e %s", nomePedido, perfil.Digest(), quer)
				}
			}
			// O cliente que sai da fronteira do ambiente não tem o invólucro dos perfis.
			aos519AmbienteDoGateway(t)
			cliente, _, err := parseModelFromEnv(false)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := cliente.(clienteComPerfis); ok || perfisDoCliente(cliente) != nil {
				t.Fatal("sem pasta o cliente de modelo nao tem o involucro dos perfis")
			}
			cabecalhos := func(int) map[string]string { return aos505NoCabecalhos(aos519Novo, aos505NoAPIBaseB) }
			n := aos505NoCompor(t, "", false, "", cabecalhos)
			if n.node.perfisDaRota != nil {
				t.Fatal("sem pasta o no nao tem perfis assinados")
			}
			if linhas := modelRouteProfilesBanner(n.node.perfisDaRota); linhas != nil {
				t.Fatalf("sem pasta o arranque nao declara nada: %v", linhas)
			}
			m := n.correr(t, "run-490-nativo", nil)
			if len(m.pedidos) != 2 {
				t.Fatalf("queria 2 pedidos, vieram %d", len(m.pedidos))
			}
			for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
				if got := string(m.pedidos[i].cru); got != quer {
					t.Errorf("pedido %d nao e o golden do AOS-490:\n veio:  %s\n quero: %s", i+1, got, quer)
				}
			}
			var turnos []string
			for _, ev := range m.eventos {
				if ev.Type == agentruntime.EventTypeTurnRecorded {
					turnos = append(turnos, string(ev.Payload))
				}
			}
			if !reflect.DeepEqual(turnos, aos505BaseTurnos) {
				t.Errorf("os turn.recorded nao sao os da base:\n veio:  %v\n quero: %v", turnos, aos505BaseTurnos)
			}
			if got := aos505NoFamilias(aos505NoMetrics(t, n)); !reflect.DeepEqual(got, aos505BaseFamiliasDoMetrics) {
				t.Errorf("as familias do /metrics mudaram:\n veio:  %v\n quero: %v", got, aos505BaseFamiliasDoMetrics)
			}
		})
	}
}

// A PASTA VAZIA: o nó arranca, diz «zero perfis assinados» e comporta-se como sem pasta — os
// mesmos pedidos, os mesmos turnos, as mesmas famílias. Mas a âncora é exigida mesmo assim.
func TestAOS519_No_PastaVazia_ZeroPerfisAssinados(t *testing.T) {
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", t.TempDir())
	t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", rt.NewKey(t).Anchor())
	n := aos505NoCompor(t, "", false, "", func(int) map[string]string { return aos505NoCabecalhos(aos519Novo, aos505NoAPIBaseB) })
	if n.node.perfisDaRota == nil || len(n.node.perfisDaRota.entradas) != 0 || n.node.perfisDaRota.perfis() != nil {
		t.Fatalf("com a pasta vazia o no sabe que ha pasta, tem zero registos e usa a tabela em codigo: %+v", n.node.perfisDaRota)
	}
	linhas := modelRouteProfilesBanner(n.node.perfisDaRota)
	if len(linhas) != 1 || !strings.Contains(linhas[0], "zero perfis assinados") {
		t.Fatalf("o arranque tinha de dizer `zero perfis assinados`: %v", linhas)
	}
	m := n.correr(t, "run-490-nativo", nil)
	for i, quer := range []string{aos490Pedido1, aos490Pedido2} {
		if got := string(m.pedidos[i].cru); got != quer {
			t.Errorf("pedido %d nao e o golden do AOS-490", i+1)
		}
	}
	var turnos []string
	for _, ev := range m.eventos {
		if ev.Type == agentruntime.EventTypeTurnRecorded {
			turnos = append(turnos, string(ev.Payload))
		}
	}
	if !reflect.DeepEqual(turnos, aos505BaseTurnos) {
		t.Errorf("os turn.recorded nao sao os da base")
	}
	if got := aos505NoFamilias(aos505NoMetrics(t, n)); !reflect.DeepEqual(got, aos505BaseFamiliasDoMetrics) {
		t.Errorf("as familias do /metrics mudaram: %v", got)
	}
}

// CADA CONFERÊNCIA ABORTA O ARRANQUE DO NÓ, com a sua causa. O caso parte de uma pasta válida e
// estraga uma coisa; [parseModelFromEnv] — a fronteira que lê o ambiente no arranque — tem de
// devolver [ErrBadModelRouteProfiles] com a causa exacta, sem cliente e sem binder.
func TestAOS519_No_CadaConferenciaAbortaOArranque(t *testing.T) {
	reescrever := func(t *testing.T, dir, sufixo string, conteudo []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "modelo-novo"+sufixo), conteudo, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// refazer escreve um registo novo (com outro relatório ou outros campos) assinado pela chave certa.
	refazer := func(t *testing.T, dir string, key rt.Key, perfil string, relatorio, registo func(map[string]any)) {
		t.Helper()
		rdoc := rt.ReportDoc(rt.ProfileDigest(t, perfil))
		if relatorio != nil {
			relatorio(rdoc)
		}
		report, digest := rt.SealReport(t, rdoc)
		edoc := rt.EntryDoc(t, perfil, rt.Board, rt.Region, digest)
		if registo != nil {
			registo(edoc)
		}
		entry := rt.Marshal(t, edoc)
		rt.Write(t, dir, "modelo-novo", entry, routeentry.Sign(entry, key.Priv), report)
	}
	casos := []struct {
		nome, causa string
		estragar    func(t *testing.T, dir string, key rt.Key, entry []byte)
	}{
		{"pasta sem ancora", routeentry.CauseNoTrustAnchor, func(t *testing.T, _ string, _ rt.Key, _ []byte) {
			t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", "")
		}},
		{"ancora que nao e uma chave", routeentry.CauseBadTrustAnchor, func(t *testing.T, _ string, _ rt.Key, _ []byte) {
			t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", "lixo")
		}},
		{"pasta que nao existe", routeentry.CauseDirUnreadable, func(t *testing.T, dir string, _ rt.Key, _ []byte) {
			t.Setenv("AOS_MODEL_ROUTE_PROFILES_DIR", filepath.Join(dir, "nao-existe"))
		}},
		{"ficheiro a mais", routeentry.CauseUnknownFile, func(t *testing.T, dir string, _ rt.Key, _ []byte) {
			if err := os.WriteFile(filepath.Join(dir, "notas.txt"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{"assinatura em falta", routeentry.CauseSignatureMissing, func(t *testing.T, dir string, _ rt.Key, _ []byte) {
			if err := os.Remove(filepath.Join(dir, "modelo-novo"+routeentry.SuffixSignature)); err != nil {
				t.Fatal(err)
			}
		}},
		{"assinatura de outra chave", routeentry.CauseSignatureInvalid, func(t *testing.T, dir string, _ rt.Key, entry []byte) {
			reescrever(t, dir, routeentry.SuffixSignature, []byte(routeentry.Sign(entry, rt.NewKey(t).Priv)))
		}},
		{"perfil alterado depois de assinado", routeentry.CauseSignatureInvalid, func(t *testing.T, dir string, _ rt.Key, entry []byte) {
			reescrever(t, dir, routeentry.SuffixEntry, []byte(strings.Replace(string(entry), "modelo-novo-4.5", "outro-modelo-9", 1)))
		}},
		{"campo desconhecido no registo", routeentry.CauseEntryUnreadable, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, nil, func(d map[string]any) { d["sem_allowlist"] = true })
		}},
		{"versao de formato desconhecida", routeentry.CauseUnknownFormat, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, nil, func(d map[string]any) { d["formato"] = "outro/9" })
		}},
		{"fatia fora de 0 a 100", routeentry.CauseShareOutOfRange, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, nil, func(d map[string]any) { d["fatia_inicial_percentagem"] = 250 })
		}},
		{"digest do perfil que nao e o do perfil", routeentry.CauseProfileDigestMismatch, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, nil, func(d map[string]any) { d["digest_do_perfil"] = "sha256:" + strings.Repeat("1", 64) })
		}},
		{"perfil que devolve estado", routeentry.CauseProfileReturnsState, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"projection_version":"1.3.0","devolver":"opcional"`, 1), nil, nil)
		}},
		{"registo de outro ambiente", routeentry.CauseOtherEnvironment, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, nil, func(d map[string]any) { d["regiao"] = "eu-west" })
		}},
		{"relatorio em falta", routeentry.CauseReportMissing, func(t *testing.T, dir string, _ rt.Key, _ []byte) {
			if err := os.Remove(filepath.Join(dir, "modelo-novo"+routeentry.SuffixReport)); err != nil {
				t.Fatal(err)
			}
		}},
		{"relatorio que nao e o citado", routeentry.CauseReportNotCited, func(t *testing.T, dir string, _ rt.Key, _ []byte) {
			doc := rt.ReportDoc(rt.ProfileDigest(t, rt.ProfileJSON))
			doc["data_utc"] = "2026-10-12T00:00:00Z"
			outro, _ := rt.SealReport(t, doc)
			reescrever(t, dir, routeentry.SuffixReport, outro)
		}},
		{"veredicto recusado", routeentry.CauseNotQualified, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, func(d map[string]any) { rt.Qualification(d)["veredicto"] = "recusado" }, nil)
		}},
		{"veredicto inconclusivo", routeentry.CauseNotQualified, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, func(d map[string]any) { rt.Qualification(d)["veredicto"] = "inconclusivo" }, nil)
		}},
		{"veredicto do modo falso", routeentry.CauseVerdictNotForProduction, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, func(d map[string]any) {
				d["modo"] = "falso"
				rt.Qualification(d)["vale_para_producao"] = false
			}, nil)
		}},
		{"veredicto de outro perfil", routeentry.CauseVerdictOfOtherProfile, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, func(d map[string]any) {
				rt.QualifiedRoute(d)["digest_do_perfil"] = "sha256:" + strings.Repeat("2", 64)
			}, nil)
		}},
		{"veredicto de outra imagem do proxy", routeentry.CauseVerdictOfOtherProxyImage, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, rt.ProfileJSON, func(d map[string]any) {
				rt.QualifiedRoute(d)["digest_da_imagem_do_proxy"] = "sha256:" + strings.Repeat("3", 64)
			}, nil)
		}},
		{"alias que a allowlist nao autoriza", routeentry.CauseNameNotAllowlisted, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			refazer(t, dir, key, strings.Replace(rt.ProfileJSON, `"gpt-4o"`, `"modelo-sem-autorizacao"`, 1), nil, nil)
		}},
		{"dois registos para o mesmo nome", routeentry.CauseDuplicateName, func(t *testing.T, dir string, key rt.Key, _ []byte) {
			rt.Good(t, dir, "outro-registo", rt.ProfileJSON, rt.Board, rt.Region, key)
		}},
		// As duas âncoras não são intermutáveis: a chave da allowlist embebida não serve de âncora dos perfis.
		{"ancora dos perfis igual a da allowlist", routeentry.CauseSharedTrustAnchor, func(t *testing.T, _ string, _ rt.Key, _ []byte) {
			t.Setenv("AOS_MODEL_ROUTE_PROFILES_TRUST_ANCHOR", allowlist.EmbeddedTrustAnchor())
		}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos519AmbienteDoGateway(t)
			dir, key, entry, _ := aos519Pasta(t, rt.ProfileJSON)
			// Controlo: a pasta, tal como está, deixa o nó compor o gateway.
			if cliente, binder, err := parseModelFromEnv(false); err != nil || cliente == nil || binder == nil {
				t.Fatalf("o controlo (pasta valida) tinha de compor: %v", err)
			}
			c.estragar(t, dir, key, entry)
			cliente, binder, err := parseModelFromEnv(false)
			if !errors.Is(err, ErrBadModelRouteProfiles) || cliente != nil || binder != nil {
				t.Fatalf("o arranque tinha de abortar com ErrBadModelRouteProfiles, sem cliente nem binder; veio cliente=%v err=%v", cliente != nil, err)
			}
			if got := routeentry.CauseOf(err); got != c.causa {
				t.Fatalf("causa = %q; quer %q (%v)", got, c.causa, err)
			}
			// A mensagem leva a causa em claro, e nada do conteúdo do registo.
			if !strings.Contains(err.Error(), "causa="+c.causa) || strings.Contains(err.Error(), "modelo-novo-4.5") || strings.Contains(err.Error(), key.Anchor()) {
				t.Fatalf("mensagem do aborto: %v", err)
			}
		})
	}
}

// UM PERFIL ASSINADO SERVE UM RUN. Com o perfil assinado do alias que o nó pede e a governação da
// rota em `observe`: o run completa; o manifesto de CADA turno traz o digest do perfil ASSINADO, e
// não o da tabela; a comparação dá `igual` contra o modelo novo; o rótulo do modelo servido é o
// modelo novo (com `/` e `.`); e o arranque diz que alias foi substituído, de que modelo para que
// modelo, com os dois digests. O controlo — os mesmos cabeçalhos, sem a pasta — dá `diferente`.
func TestAOS519_No_PerfilAssinadoServeORun(t *testing.T) {
	cabecalhos := func(int) map[string]string { return aos505NoCabecalhos(aos519Novo, aos505NoAPIBaseB) }
	assinado, _ := modelgateway.ParseRouteProfile([]byte(rt.ProfileJSON))
	daTabela := aos519DigestsDaTabela[aos486Modelo]
	if assinado.Digest() == daTabela {
		t.Fatal("o perfil assinado tem o digest do da tabela: o teste nao distingue nada")
	}

	t.Run("controlo sem a pasta", func(t *testing.T) {
		aos504SemVariavel(t, "AOS_MODEL_ROUTE_PROFILES_DIR")
		n := aos505NoCompor(t, "observe", true, "", cabecalhos)
		m := n.correr(t, "run-519-controlo", nil)
		for i, r := range aos505NoRotas(t, m.eventos) {
			if r.check != "diferente" || r.digest != daTabela {
				t.Fatalf("turno %d sem a pasta: check=%s digest=%s; quer diferente e o digest da tabela", i+1, r.check, r.digest)
			}
		}
	})

	_, _, entry, report := aos519Pasta(t, rt.ProfileJSON)
	n := aos505NoCompor(t, "observe", true, "", cabecalhos)
	m := n.correr(t, "run-519-assinado", nil)
	rotas := aos505NoRotas(t, m.eventos)
	if len(rotas) != 2 {
		t.Fatalf("turnos = %d", len(rotas))
	}
	for i, r := range rotas {
		if r.digest != assinado.Digest() || r.check != "igual" || r.servido != aos519Novo {
			t.Fatalf("turno %d: digest=%s check=%q servido=%s; quer o digest do perfil ASSINADO (%s), igual e %s", i+1, r.digest, r.check, r.servido, assinado.Digest(), aos519Novo)
		}
	}
	// As métricas: o modelo novo é um valor do conjunto fechado, e conta os dois turnos.
	metrics := aos505NoMetrics(t, n)
	if v, tem := aos505NoAmostra(metrics, "igual", aos519Novo); !tem || v != "2" {
		t.Fatalf("aos_model_route_checks_total{igual,%s} = %q (existe=%v); quer 2", aos519Novo, v, tem)
	}
	if _, tem := aos505NoAmostra(metrics, "igual", "outro"); !tem {
		t.Fatal("o conjunto do rotulo continua a ter `outro`")
	}
	if got := len(n.node.rotaDoModelo.resultados) * len(n.node.rotaDoModelo.servidos); got > maxSeriesDaRotaDoModelo {
		t.Fatalf("series da rota = %d, acima do tecto %d", got, maxSeriesDaRotaDoModelo)
	}

	// O arranque: a linha do perfil carregado, com tudo o que identifica a substituição.
	linhas := modelRouteProfilesBanner(n.node.perfisDaRota)
	if len(linhas) != 1 {
		t.Fatalf("linhas do arranque = %d", len(linhas))
	}
	digestDoRelatorio, _ := routeentry.ReportDigest(report)
	for _, quer := range []string{
		"SUBSTITUI", `alias "gpt-4o"`, `registo "modelo-novo"`, `"openai/k3"`, daTabela, `"` + aos519Novo + `"`, assinado.Digest(),
		routeentry.EntryDigest(entry), digestDoRelatorio, "10%", "SEM EFEITO",
	} {
		if !strings.Contains(linhas[0], quer) {
			t.Errorf("a linha do arranque nao diz %q:\n%s", quer, linhas[0])
		}
	}
	// E a linha da governação da rota declara o perfil que de facto se compara — o assinado.
	rota := modelRouteBannerFromEnvCom(true, aos486Modelo, n.node.perfisDaRota.perfis())
	if len(rota) != 1 || !strings.Contains(rota[0], assinado.Digest()) || !strings.Contains(rota[0], aos519Novo) || strings.Contains(rota[0], daTabela) {
		t.Fatalf("a linha da governacao da rota tinha de declarar o perfil assinado: %v", rota)
	}
	// Nenhuma linha leva conteúdo do registo além de identificadores e digests.
	if strings.Contains(linhas[0], "wire_class") || strings.Contains(linhas[0], "capabilities") {
		t.Errorf("a linha do arranque repete o perfil: %s", linhas[0])
	}
}

// SÓ NO ARRANQUE. Com o nó a correr, apagar o registo, trocar-lhe o perfil ou encher a pasta de
// lixo não tem efeito: o run seguinte é servido com o MESMO perfil assinado, e o arranque não é
// relido da pasta. Só ao recriar o nó é que a pasta volta a contar — e aí, estando estragada, o
// nó não arranca.
func TestAOS519_No_MudarAPastaComONoACorrerNaoTemEfeito(t *testing.T) {
	cabecalhos := func(int) map[string]string { return aos505NoCabecalhos(aos519Novo, aos505NoAPIBaseB) }
	dir, key, _, _ := aos519Pasta(t, rt.ProfileJSON)
	assinado, _ := modelgateway.ParseRouteProfile([]byte(rt.ProfileJSON))
	n := aos505NoCompor(t, "observe", true, "", cabecalhos)
	antes := modelRouteProfilesBanner(n.node.perfisDaRota)

	// Troca-se o registo por outro, VÁLIDO, para outro modelo; acrescenta-se lixo; e apaga-se o relatório.
	rt.Good(t, dir, "modelo-novo", strings.Replace(rt.ProfileJSON, "modelo-novo-4.5", "modelo-trocado", 1), rt.Board, rt.Region, key)
	if err := os.WriteFile(filepath.Join(dir, "lixo.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "modelo-novo"+routeentry.SuffixReport)); err != nil {
		t.Fatal(err)
	}

	m := n.correr(t, "run-519-depois-da-troca", nil)
	for i, r := range aos505NoRotas(t, m.eventos) {
		if r.digest != assinado.Digest() || r.check != "igual" {
			t.Fatalf("turno %d depois de mexer na pasta: digest=%s check=%q; o perfil em vigor e o do ARRANQUE (%s)", i+1, r.digest, r.check, assinado.Digest())
		}
	}
	if depois := modelRouteProfilesBanner(n.node.perfisDaRota); !reflect.DeepEqual(antes, depois) {
		t.Fatalf("o que o no declara mudou sem ser recriado:\n antes:  %v\n depois: %v", antes, depois)
	}
	// Recriar o nó com a pasta neste estado: não arranca.
	if _, _, err := parseModelFromEnv(false); !errors.Is(err, ErrBadModelRouteProfiles) {
		t.Fatalf("recriado sobre a pasta estragada, o no tinha de recusar arrancar; veio %v", err)
	}
}

// O SELO «PERFIL CARREGADO» é entregue ao gateway pelo nó: um por perfil, com os três digests; e
// sem perfis assinados o audit de governação não ganha nada.
func TestAOS519_No_OCarregamentoFicaSelado(t *testing.T) {
	const particao = "modelgw-gov:route-profiles"
	ctx := context.Background()
	compor := func(t *testing.T, assinados *perfisAssinados) *audit.MemStore {
		t.Helper()
		store := audit.NewMemStore()
		if _, err := newGatewayModelClientComPerfis(&lateBoundModelVerifier{}, "http://127.0.0.1:9", aos486Modelo, "", rt.Region, rt.Board, nil, nil, store, nil,
			false, nil, 0, modelgateway.RouteGovernance{}, "", "", assinados); err != nil {
			t.Fatal(err)
		}
		return store
	}
	if head, _ := compor(t, nil).Head(ctx, particao); head != 0 {
		t.Fatalf("sem perfis assinados ficaram %d selos", head)
	}
	if head, _ := compor(t, &perfisAssinados{}).Head(ctx, particao); head != 0 {
		t.Fatalf("com a pasta vazia ficaram %d selos", head)
	}

	_, _, entry, report := aos519Pasta(t, rt.ProfileJSON)
	t.Setenv("AOS_MODEL_REGION", "")
	t.Setenv("AOS_MODEL_BOARD", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_BUNDLE_DIR", "")
	t.Setenv("AOS_MODEL_ALLOWLIST_TRUST_ANCHOR", "")
	assinados, err := loadSignedRouteProfilesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	store := compor(t, assinados)
	head, _ := store.Head(ctx, particao)
	if head != 1 {
		t.Fatalf("selos = %d; quer um por perfil carregado", head)
	}
	selo, _, _ := store.At(ctx, particao, 1)
	digestDoRelatorio, _ := routeentry.ReportDigest(report)
	quer := map[string]string{
		"route_profile": "loaded", "requested": "gpt-4o", "expected_model": aos519Novo,
		"profile_digest": rt.ProfileDigest(t, rt.ProfileJSON), "entry_digest": routeentry.EntryDigest(entry), "report_digest": digestDoRelatorio,
	}
	if len(selo.Obligations) != 1 || !reflect.DeepEqual(selo.Obligations[0].Params, quer) {
		t.Fatalf("selo = %+v; quer %v", selo.Obligations, quer)
	}
}

// O CONJUNTO DO RÓTULO DO MODELO SERVIDO CONTINUA FECHADO, e o número de séries tem um máximo
// escrito: com o máximo de registos, cada um com um modelo esperado diferente, não o passa.
func TestAOS519_No_AsSeriesDaRotaTemTecto(t *testing.T) {
	if maxSeriesDaRotaDoModelo != 42 {
		t.Fatalf("o tecto de series mudou para %d: reveja o runbook", maxSeriesDaRotaDoModelo)
	}
	var candidatos []modelgateway.RouteProfile
	for i := 0; i < routeentry.MaxEntries; i++ {
		letra := string(rune('a' + i))
		candidatos = append(candidatos, modelgateway.RouteProfile{
			Requested: "alias-" + letra, ExpectedModel: "openai/modelo-" + letra, WireClass: modelgateway.WireOpenAIChat, Capabilities: []string{modelgateway.CapabilityTools},
		})
	}
	set, err := modelgateway.NewRouteProfileSet(candidatos...)
	if err != nil {
		t.Fatal(err)
	}
	c := novosContadoresDaRotaCom(set)
	if got := len(c.resultados) * len(c.servidos); got > maxSeriesDaRotaDoModelo || got != 3*(2+routeentry.MaxEntries+2) {
		t.Fatalf("series = %d; tecto %d", got, maxSeriesDaRotaDoModelo)
	}
	// Um nome que nenhum perfil espera continua a cair em `outro`: o texto do proxy não chega a um rótulo.
	c.observar(modelgateway.RouteObservation{Check: "diferente", Served: "um-nome-qualquer-do-proxy"})
	if c.lido("diferente", modelgateway.ServedLabelOther) != 1 || c.lido("diferente", "um-nome-qualquer-do-proxy") != 0 {
		t.Fatal("um nome fora do conjunto tinha de contar em `outro`")
	}
	// Sem perfis assinados, o conjunto é o de sempre.
	if got := novosContadoresDaRotaCom(nil).servidos; !reflect.DeepEqual(got, novosContadoresDaRota().servidos) || len(got) != 4 {
		t.Fatalf("conjunto sem perfis assinados = %v", got)
	}
}

// A VERSÃO DA PROJECÇÃO de um run novo é a do perfil ASSINADO do alias, e não a da tabela.
func TestAOS519_No_AProjeccaoDoRunEADoPerfilAssinado(t *testing.T) {
	comVersao := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"projection_version":"1.2.0"`, 1)
	aos519AmbienteDoGateway(t)
	aos519Pasta(t, comVersao)
	assinados, err := loadSignedRouteProfilesFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	goal := agentruntime.Goal{Model: agentruntime.ModelConfig{ModelID: aos486Modelo}}
	if got := fixarProjeccaoCom(goal, assinados.perfis()).ProjectionVersion; got != "1.2.0" {
		t.Fatalf("versao da projeccao com o perfil assinado = %q; quer 1.2.0", got)
	}
	if got := fixarProjeccao(goal).ProjectionVersion; got != "" {
		t.Fatalf("a tabela em codigo nao declara versao; veio %q", got)
	}
	// Um run retomado, que já traz a sua, não é tocado — nem pelo perfil assinado.
	goal.ProjectionVersion = "1.1.0"
	if got := fixarProjeccaoCom(goal, assinados.perfis()).ProjectionVersion; got != "1.1.0" {
		t.Fatalf("um run com versao fixada mudou para %q", got)
	}
}

// DE PONTA A PONTA: um run NOVO num nó cujo perfil assinado declara a versão 1.2.0 — com o
// interruptor do nó na de omissão — é projectado na 1.2.0 em todos os turnos, e o seu registo de
// retoma fica com essa versão: retomado, continua nela. É o nó composto a ligar o conjunto de
// perfis ao adaptador (a projecção de cada turno) e ao Goal (o que fica fixado no run).
func TestAOS519_No_ORunNovoFicaFixadoNaVersaoDoPerfilAssinado(t *testing.T) {
	comVersao := strings.Replace(rt.ProfileJSON, `"capabilities":["tools"]`, `"capabilities":["tools"],"projection_version":"1.2.0"`, 1)
	aos519Pasta(t, comVersao)
	n := aos505NoCompor(t, "", false, "", func(int) map[string]string { return aos505NoCabecalhos(aos519Novo, aos505NoAPIBaseB) })
	if defaultModelProjectionVersion == "1.2.0" {
		t.Fatal("a versao de omissao do no e a do perfil: o teste nao distingue nada")
	}
	const runID = "run-519-projeccao"
	m := n.correr(t, runID, nil)
	manifestos, _ := aos490Manifestos(t, m.eventos)
	if len(manifestos) != 2 {
		t.Fatalf("turnos = %d", len(manifestos))
	}
	for i, man := range manifestos {
		if man.ProjectionVersion != "1.2.0" {
			t.Fatalf("turno %d projectado na %q; o perfil assinado declara 1.2.0", i+1, man.ProjectionVersion)
		}
	}
	// O que a hospedagem fixa no Goal de um run novo deste nó — e que vai para o registo de retoma.
	goal := n.node.fixarProjeccaoDoRun(agentruntime.Goal{Model: agentruntime.ModelConfig{ModelID: aos486Modelo}})
	if goal.ProjectionVersion != "1.2.0" {
		t.Fatalf("o no fixa o run novo na versao %q; quer 1.2.0 (a do perfil assinado)", goal.ProjectionVersion)
	}
	rec, err := resumeRecordFromGoal(agentruntime.Goal{RunID: runID, Objective: "o", CompletionMode: agentruntime.CompletionOff,
		Model: agentruntime.ModelConfig{ModelID: aos486Modelo}, ProjectionVersion: goal.ProjectionVersion})
	if err != nil {
		t.Fatal(err)
	}
	if got := rec.GoalWith("credencial").ProjectionVersion; got != "1.2.0" {
		t.Fatalf("o registo de retoma traz a versao %q; quer 1.2.0", got)
	}
}
