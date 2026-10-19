package main

// AOS-536 (parte 1) — UM REGISTO DE USO `tool` DE VÍDEO NO ARRANQUE DO NÓ.
//
// Nada muda no arranque além do que o `routeentry` decide: um registo de uso `tool` com um perfil de
// vídeo (capacidade `video`, a citar um relatório `video` qualificado) carrega e, como o de imagem,
// nunca é candidato — `naoTitulares` já o exclui. A linha do arranque diz a experiência citada; e a
// `ocr_read` não se liga a um registo de vídeo (aos530_ocr_read_no_test.go).

import (
	"errors"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// aos536PerfilVideoTool é o perfil de um registo de uso `tool` de vídeo, para o alias que a
// allowlist externa dos testes do AOS-530 autoriza ([aos530AliasTool]).
var aos536PerfilVideoTool = strings.Replace(rt.VideoToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos530AliasTool+`"`, 1)

// O CASO DO CRITÉRIO: `on`, um candidato real a 100% e um registo de uso `tool` de vídeo. O nó
// arranca, o candidato é o único não-titular, e o registo de vídeo não tem adaptador de run mas
// está no conjunto de perfis do gateway.
func TestAOS536_No_RegistoToolDeVideoCarregaENaoECandidato(t *testing.T) {
	for _, interruptor := range []string{"on", "off"} {
		t.Run(interruptor, func(t *testing.T) {
			aos519AmbienteDoGateway(t)
			aos530ComAllowlistExterna(t)
			aos530PastaCom(t, []aos520Registo{aos520cCand(100)}, aos530Tool{rotulo: "video", perfil: aos536PerfilVideoTool, video: true})
			aos520Interruptor(t, interruptor)
			cliente, _, err := parseModelFromEnv(false)
			if err != nil {
				t.Fatalf("candidato e registo tool de video: %v", err)
			}
			assinados := perfisDoCliente(cliente)
			if assinados == nil || len(assinados.entradas) != 2 {
				t.Fatalf("a pasta tinha de carregar os dois registos: %+v", assinados)
			}
			var video routeentry.Entry
			for _, e := range assinados.entradas {
				if e.ForTool() {
					video = e
				}
			}
			if video.Media != modelgateway.CapabilityVideo || video.Profile.Requested != aos530AliasTool {
				t.Fatalf("o registo de video carregou como %+v", video)
			}
			nt := assinados.naoTitulares(aos486Modelo)
			if len(nt) != 1 || nt[0].Profile.Requested != aos520AliasCand {
				t.Fatalf("naoTitulares = %+v; quer so o candidato", nt)
			}
			if c, serve := assinados.candidatoAServir(aos486Modelo); serve != (interruptor == "on") || (serve && c.alias != aos520AliasCand) {
				t.Fatalf("o candidato a servir: %+v %v", c, serve)
			}
			if d := assinados.despachante; d != nil {
				if _, tem := d.porAlias[aos530AliasTool]; tem {
					t.Fatal("o registo tool de video ganhou um adaptador de run")
				}
			}
			if p, tem := assinados.perfis().For(aos530AliasTool); !tem || p.Digest() != rt.ProfileDigest(t, aos536PerfilVideoTool) {
				t.Fatal("o perfil do registo tool de video tinha de estar no conjunto de perfis do gateway")
			}
			no := &Node{modelID: aos486Modelo, modeloAutoritativo: true, perfisDaRota: assinados}
			if no.aliasServido(aos530AliasTool) {
				t.Fatal("o alias de um registo tool de video e servido a runs")
			}
			linhas := modelRouteProfilesBanner(assinados)
			if len(linhas) != 2 || !strings.Contains(linhas[1], "USO TOOL") || !strings.Contains(linhas[1], "experiencia video") || strings.Contains(linhas[1], "experiencia ocr") {
				t.Fatalf("o arranque tinha de declarar o registo tool de video a parte, com a experiencia video: %q", linhas)
			}
		})
	}
}

// A LINHA DE UM REGISTO TOOL DE IMAGEM é, byte a byte, a do AOS-530: diz a experiência `ocr`.
func TestAOS536_No_LinhaDoRegistoDeImagemNaoMuda(t *testing.T) {
	aos519AmbienteDoGateway(t)
	aos530ComAllowlistExterna(t)
	aos530PastaCom(t, nil, aos530Tool{rotulo: "ocr", perfil: aos530PerfilTool})
	cliente, _, err := parseModelFromEnv(false)
	if err != nil {
		t.Fatal(err)
	}
	linhas := modelRouteProfilesBanner(perfisDoCliente(cliente))
	if len(linhas) != 1 || !strings.Contains(linhas[0], "(experiencia ocr, veredicto qualificado, do modo real,") || strings.Contains(linhas[0], "video") {
		t.Fatalf("a linha do registo de imagem mudou: %q", linhas)
	}
}

// AS RECUSAS DE ARRANQUE de um registo de vídeo: a citar um relatório `ocr`, com um perfil de vídeo
// e imagem, e para o alias do titular.
func TestAOS536_No_RegistoToolDeVideoQueRecusaOArranque(t *testing.T) {
	casos := []struct {
		nome  string
		tool  aos530Tool
		causa string
		erro  error
	}{
		{nome: "registo de video a citar um relatorio ocr", causa: routeentry.CauseReportOfOtherExperiment,
			tool: aos530Tool{perfil: aos536PerfilVideoTool}},
		{nome: "registo de imagem a citar um relatorio video", causa: routeentry.CauseReportOfOtherExperiment,
			tool: aos530Tool{perfil: aos530PerfilTool, video: true}},
		{nome: "perfil de video e imagem", causa: routeentry.CauseToolProfileCapabilities,
			tool: aos530Tool{perfil: strings.Replace(aos536PerfilVideoTool, `["video"]`, `["imagem","video"]`, 1), video: true}},
		{nome: "alias do titular", erro: ErrModelToolEntryForTitular,
			tool: aos530Tool{perfil: strings.Replace(rt.VideoToolProfileJSON, `"requested":"gpt-4o-mini"`, `"requested":"`+aos486Modelo+`"`, 1), video: true}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			aos519AmbienteDoGateway(t)
			aos530ComAllowlistExterna(t)
			c.tool.rotulo = "video"
			aos530PastaCom(t, nil, c.tool)
			_, _, err := parseModelFromEnv(false)
			if c.erro != nil {
				if !errors.Is(err, c.erro) || !strings.Contains(err.Error(), `"video"`) {
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
