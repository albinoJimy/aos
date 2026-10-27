package main

// AOS-548 — O ARMAZÉM DE ARTEFACTOS DO RUN (ADR-043 §2.3): a tool `artifact_write`, a cifra por
// titular, os tectos, a proveniência e as rotas de leitura soberana.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	referencemonitor "github.com/aos-ref/kernel/reference-monitor"
	"github.com/aos-ref/platform/audit"
	"github.com/aos-ref/substrate/eventstore"
	"github.com/aos-ref/substrate/sandbox"
)

const aos548SVGBom = `<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg" width="100" height="50"><defs><linearGradient id="g"/></defs><rect x="0" y="0" width="60" height="40" fill="url(#g)"/><text x="5" y="20">SALA</text><use href="#g"/></svg>`

func TestAOS548_SVG_RecusaScriptELigacoesExternas(t *testing.T) {
	t.Parallel()
	if !svgSeguro([]byte(aos548SVGBom)) {
		t.Fatal("o SVG bom foi recusado")
	}
	maus := map[string]string{
		"script":            `<svg><script>alert(1)</script></svg>`,
		"script maiusculas": `<svg><SCRIPT>alert(1)</SCRIPT></svg>`,
		"onload":            `<svg onload="alert(1)"></svg>`,
		"onclick no filho":  `<svg><rect onClick="x()"/></svg>`,
		"foreignObject":     `<svg><foreignObject><div/></foreignObject></svg>`,
		"href externo":      `<svg><a href="https://atacante.example/x"><text>x</text></a></svg>`,
		"xlink externo":     `<svg xmlns:xlink="http://www.w3.org/1999/xlink"><image xlink:href="https://atacante.example/p.png"/></svg>`,
		"javascript":        `<svg><a href="javascript:alert(1)"/></svg>`,
		"url externo":       `<svg><rect fill="url(https://atacante.example/x)"/></svg>`,
		"url externo aspas": `<svg><rect style="fill:url( 'https://atacante.example/x')"/></svg>`,
		"import no style":   `<svg><style>@import url(https://atacante.example/c.css);</style></svg>`,
		"doctype":           `<!DOCTYPE svg [<!ENTITY x "y">]><svg>&x;</svg>`,
		"raiz nao svg":      `<html><body/></html>`,
		"pi estranha":       `<?xml-stylesheet href="https://atacante.example/s.css"?><svg/>`,
		"mal formado":       `<svg><rect></svg>`,
		"vazio":             ``,
		"iframe":            `<svg><iframe src="x"/></svg>`,
	}
	for nome, svg := range maus {
		if svgSeguro([]byte(svg)) {
			t.Errorf("%s: aceite", nome)
		}
	}
}

func TestAOS548_ConteudoPorTipo(t *testing.T) {
	t.Parallel()
	casos := []struct {
		tipo, conteudo string
		ok             bool
	}{
		{"application/json", `{"comodos":["SALA","WC"]}`, true},
		{"application/json", `{"comodos":`, false},
		{"text/markdown", "# Cômodos\n- SALA", true},
		{"text/csv", "nome,area\nSALA,20", true},
		{"text/plain", "olá", true},
		{"text/plain", "nul\x00aqui", false},
		{"text/plain", "\xff\xfe", false},
		{"image/svg+xml", aos548SVGBom, true},
		{"image/svg+xml", `<svg onload="x()"/>`, false},
	}
	for _, c := range casos {
		if got := validarConteudo(c.tipo, []byte(c.conteudo)) == ""; got != c.ok {
			t.Errorf("%s %q: ok=%v, quer %v", c.tipo, c.conteudo, got, c.ok)
		}
	}
}

// aos548Armazem abre um armazém numa pasta nova com o cifrador por titular de referência.
func aos548Armazem(t *testing.T, maximo int64) (*armazemDeArtefactos, *audit.InMemoryKeyVault, string) {
	t.Helper()
	vault := audit.NewInMemoryKeyVault(nil)
	pasta := t.TempDir()
	a, err := abrirArmazemDeArtefactos(pasta, maximo, newContentSealer(vault, audit.NewInMemorySubjectPartitionIndex()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.fechar)
	return a, vault, pasta
}

func TestAOS548_Armazem_CifraTectosEApagamento(t *testing.T) {
	ctx := context.Background()
	if a, err := abrirArmazemDeArtefactos(t.TempDir(), 1<<20, nil); !errors.Is(err, ErrArtifactSemCifra) || a != nil {
		t.Fatalf("sem cifrador: %v", err)
	}
	if a, err := abrirArmazemDeArtefactos("", 1<<20, nil); err != nil || a != nil {
		t.Fatalf("desligado: %v", err)
	}
	a, vault, pasta := aos548Armazem(t, 1<<30)
	claro := []byte(`{"comodos":["SALA-SEGREDO-548"]}`)
	d, causa := a.guardar(ctx, "titular-1", "run-1", claro)
	if causa != "" || d != "sha256:"+digestHex(claro) {
		t.Fatalf("guardar: %q %q", d, causa)
	}
	// Nunca em claro no disco.
	_ = filepath.Walk(pasta, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte("SALA-SEGREDO-548")) {
				t.Errorf("o artefacto esta em claro em %s", p)
			}
		}
		return nil
	})
	got, err := a.abrir(ctx, "titular-1", "run-1", d)
	if err != nil || !bytes.Equal(got, claro) {
		t.Fatalf("abrir: %v", err)
	}
	// Outro run não o vê; um digest que não existe também não.
	if _, err := a.abrir(ctx, "titular-1", "run-2", d); err == nil {
		t.Fatal("outro run abriu o artefacto")
	}
	if _, err := a.abrir(ctx, "titular-1", "run-1", "sha256:"+strings.Repeat("0", 64)); err == nil {
		t.Fatal("abriu um digest inexistente")
	}
	// O mesmo conteúdo no mesmo run: idempotente.
	if d2, c2 := a.guardar(ctx, "titular-1", "run-1", claro); d2 != d || c2 != "" {
		t.Fatalf("repetido: %q %q", d2, c2)
	}
	// Tecto de artefactos por run.
	for i := 1; i < artifactMaxPorRun; i++ {
		if _, c := a.guardar(ctx, "titular-1", "run-1", []byte(strings.Repeat("x", i))); c != "" {
			t.Fatalf("guardar %d: %s", i, c)
		}
	}
	if _, c := a.guardar(ctx, "titular-1", "run-1", []byte("mais um")); c != artCausaRun {
		t.Fatalf("o 17.º artefacto do run passou: %q", c)
	}
	// Apagado (KEK destruída): ilegível.
	vault.Delete("titular-1")
	if _, err := a.abrir(ctx, "titular-1", "run-1", d); !errors.Is(err, audit.ErrDecrypt) {
		t.Fatalf("depois do apagamento: %v", err)
	}
	// Tecto do volume.
	pequeno, _, _ := aos548Armazem(t, 10)
	if _, c := pequeno.guardar(ctx, "t", "r", []byte("conteudo maior que dez bytes")); c != artCausaVolume {
		t.Fatalf("o volume cheio deixou gravar: %q", c)
	}
}

func TestAOS548_Reescrita_RunPassoETitularDaCall(t *testing.T) {
	t.Parallel()
	rw := newArtifactRewriter()
	outra := referencemonitor.Call{ToolID: "doc_read", Input: []byte(`{"doc_id":"x"}`)}
	if got, err := rw(outra, &bilheteDaCall{}); err != nil || string(got.Input) != `{"doc_id":"x"}` {
		t.Fatalf("outra tool mexida: %v", err)
	}
	call := referencemonitor.Call{ToolID: artifactWriteToolName, RunID: "run-1", StepID: "step-1-tool-1",
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "titular-9"},
		Input:     []byte(`{"name":"comodos.json","media_type":"application/json","content":"{}"}`)}
	got, err := rw(call, &bilheteDaCall{})
	if err != nil {
		t.Fatal(err)
	}
	var in entradaDoArtefacto
	if err := json.Unmarshal(got.Input, &in); err != nil || in.Schema != artifactInputSchema || in.RunID != "run-1" || in.StepID != "step-1-tool-1" || in.Subject != "titular-9" || in.Name != "comodos.json" {
		t.Fatalf("entrada reescrita: %+v %v", in, err)
	}
	// O modelo não escolhe o run, o passo nem o titular.
	for _, mau := range []string{
		`{"name":"a.json","media_type":"application/json","content":"{}","run_id":"outro"}`,
		`{"name":"a.json","media_type":"application/json","content":"{}","subject":"outro"}`,
		`{"name":"a.json","media_type":"application/json","content":"{}"} {}`,
		`nao e json`,
	} {
		c := call
		c.Input = []byte(mau)
		if _, err := rw(c, &bilheteDaCall{}); err == nil {
			t.Errorf("aceite: %s", mau)
		}
	}
	sem := call
	sem.RunID = ""
	if _, err := rw(sem, &bilheteDaCall{}); err == nil {
		t.Error("aceite sem run")
	}
}

func TestAOS548_Executar_GravaRegistaEDevolveAReferencia(t *testing.T) {
	ctx := context.Background()
	a, _, _ := aos548Armazem(t, 1<<30)
	entrada := func(nome, tipo, conteudo string) []byte {
		b, _ := json.Marshal(entradaDoArtefacto{Schema: artifactInputSchema, RunID: "run-1", StepID: "s1", Subject: "tit", Name: nome, MediaType: tipo, Content: conteudo})
		return b
	}
	if _, err := a.executar(ctx, entrada("a.json", "application/json", "{}")); err == nil {
		t.Fatal("gravou sem registo de proveniencia")
	}
	var registados []artefactoCriado
	a.registar = func(_ context.Context, runID string, p artefactoCriado) error {
		if runID != "run-1" {
			t.Errorf("run %q", runID)
		}
		registados = append(registados, p)
		return nil
	}
	out, err := a.executar(ctx, entrada("comodos.json", "application/json", `{"comodos":5}`))
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	if err := json.Unmarshal(out, &ref); err != nil || ref["schema"] != ArtifactRefSchema || ref["name"] != "comodos.json" || strings.Contains(string(out), "comodos\":5") {
		t.Fatalf("referencia: %s", out)
	}
	if len(registados) != 1 || registados[0].SealedSubject != "tit" || registados[0].StepID != "s1" || registados[0].Digest != ref["digest"] || registados[0].Tool != artifactWriteToolName {
		t.Fatalf("proveniencia: %+v", registados)
	}
	for nome, in := range map[string][]byte{
		"tipo html":      entrada("a.html", "text/html", "<p/>"),
		"nome com /":     entrada("../a.json", "application/json", "{}"),
		"nome vazio":     entrada("", "application/json", "{}"),
		"json invalido":  entrada("a.json", "application/json", "{"),
		"svg com script": entrada("p.svg", "image/svg+xml", `<svg><script/></svg>`),
		"vazio":          entrada("a.txt", "text/plain", ""),
		"grande":         entrada("a.txt", "text/plain", strings.Repeat("x", artifactMaxBytes+1)),
		"schema":         []byte(`{"schema":"outro","run_id":"r","step_id":"s","subject":"t","name":"a.txt","media_type":"text/plain","content":"x"}`),
	} {
		if _, err := a.executar(ctx, in); err == nil {
			t.Errorf("%s: aceite", nome)
		}
	}
	if a.gravados.Load() != 1 {
		t.Fatalf("gravados = %d", a.gravados.Load())
	}
}

func TestAOS548_Env(t *testing.T) {
	t.Setenv("AOS_ARTIFACT_DIR", "")
	t.Setenv("AOS_ARTIFACT_MAX_BYTES", "")
	if p, m, err := artifactConfigFromEnv(); p != "" || m != artifactMaxBytesOmissao || err != nil {
		t.Fatalf("omissao: %q %d %v", p, m, err)
	}
	for _, mau := range []string{"0", "-1", "dez", "1.5"} {
		t.Setenv("AOS_ARTIFACT_MAX_BYTES", mau)
		if _, _, err := artifactConfigFromEnv(); !errors.Is(err, ErrBadArtifactMaxBytes) {
			t.Fatalf("%q: %v", mau, err)
		}
	}
	if artifactBanner(nil) != nil {
		t.Fatal("banner sem armazem")
	}
}

// AS ROTAS, no nó composto com o gate soberano: a lista e o conteúdo de um artefacto do run, só
// pelo evento do próprio run, como anexo; e as recusas.
func TestAOS548_No_RotasDosArtefactos(t *testing.T) {
	n := aos533Compor(t, aos533Opcoes{artefactos: t.TempDir()}, aos533Guiao)
	runID := "run-aos548-rotas"
	n.correr(t, agentruntime.Goal{RunID: runID})
	ctx := context.Background()
	a := n.node.artefactos
	if a == nil {
		t.Fatal("o armazem nao foi composto")
	}
	svg := []byte(aos548SVGBom)
	d, causa := a.guardar(ctx, acnAgent, runID, svg)
	if causa != "" {
		t.Fatal(causa)
	}
	if err := registoDeArtefacto(n.node.EventStore)(ctx, runID, artefactoCriado{Digest: d, MediaType: "image/svg+xml", Bytes: len(svg), Name: "planta.svg", Tool: artifactWriteToolName, StepID: "step-000001-tool-1", SealedSubject: acnAgent}); err != nil {
		t.Fatal(err)
	}
	// Um ficheiro do run SEM evento: não se serve.
	semEvento, _ := a.guardar(ctx, acnAgent, runID, []byte("orfao"))

	rec := getReq(n.h, "/runs/"+runID+"/artifacts", govHeaders())
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), d) || !strings.Contains(rec.Body.String(), "planta.svg") || strings.Contains(rec.Body.String(), semEvento) {
		t.Fatalf("lista: %d %s", rec.Code, rec.Body.String())
	}
	rec = getReq(n.h, "/runs/"+runID+"/artifacts/"+d, govHeaders())
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), svg) {
		t.Fatalf("conteudo: %d %s", rec.Code, rec.Body.String())
	}
	for k, quer := range map[string]string{
		"Content-Type":            "image/svg+xml",
		"Content-Disposition":     `attachment; filename="planta.svg"`,
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": "sandbox; default-src 'none'",
		"Cache-Control":           "no-store",
	} {
		if got := rec.Header().Get(k); got != quer {
			t.Errorf("%s = %q, quer %q", k, got, quer)
		}
	}
	for nome, alvo := range map[string]string{
		"sem evento":         "/runs/" + runID + "/artifacts/" + semEvento,
		"digest inexistente": "/runs/" + runID + "/artifacts/sha256:" + strings.Repeat("a", 64),
		"outro run":          "/runs/run-que-nao-existe/artifacts/" + d,
	} {
		if rec := getReq(n.h, alvo, govHeaders()); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d", nome, rec.Code)
		}
	}
	// Sem a credencial soberana do leitor: o mesmo 404 uniforme (ou 401), nunca o conteúdo.
	if rec := getReq(n.h, "/runs/"+runID+"/artifacts/"+d, nil); rec.Code == http.StatusOK {
		t.Fatalf("sem leitor: %d", rec.Code)
	}

	// Num nó sem armazém: 501.
	sem := aos533Compor(t, aos533Opcoes{}, aos533Guiao)
	sem.correr(t, agentruntime.Goal{RunID: "run-aos548-sem"})
	if rec := getReq(sem.h, "/runs/run-aos548-sem/artifacts", govHeaders()); rec.Code != http.StatusNotImplemented {
		t.Fatalf("sem armazem: %d", rec.Code)
	}
}

// MUTAÇÃO M4: um ficheiro trocado no disco pelo de outro artefacto do mesmo titular decifra-se — e
// tem de ser recusado pelo digest.
func TestAOS548_FicheiroTrocadoNoDiscoERecusado(t *testing.T) {
	ctx := context.Background()
	a, _, pasta := aos548Armazem(t, 1<<30)
	dA, _ := a.guardar(ctx, "tit", "run-1", []byte("artefacto A"))
	dB, _ := a.guardar(ctx, "tit", "run-1", []byte("artefacto B"))
	dir := filepath.Join(pasta, pastaDoRun("run-1"))
	b, err := os.ReadFile(filepath.Join(dir, strings.TrimPrefix(dB, "sha256:")))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, strings.TrimPrefix(dA, "sha256:")), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := a.abrir(ctx, "tit", "run-1", dA); err == nil {
		t.Fatalf("o ficheiro trocado foi servido como A: %q", got)
	}
}

// ACHADO 2 DA REVISÃO: os contornos do filtro de SVG.
func TestAOS548_SVG_ContornosDaRevisao(t *testing.T) {
	t.Parallel()
	for nome, svg := range map[string]string{
		"set href javascript com tab": `<svg xmlns="http://www.w3.org/2000/svg"><g><set attributeName="href" to="java&#9;script:alert(1)"/></g></svg>`,
		"set em image":                `<svg><image><set attributeName="href" to="https://evil/p.png"/></image></svg>`,
		"animate values data":         `<svg><rect><animate attributeName="href" values="data:text/html,x"/></rect></svg>`,
		"escape css no style":         `<svg><rect style="fill:u\72l(https://evil)"/></svg>`,
		"style elemento":              `<svg><style>@\69mport 'https://evil'</style></svg>`,
		"image-set":                   `<svg><rect style="fill:image-set('x' 1x)"/></svg>`,
		"xml:base":                    `<svg xml:base="https://evil/"><use href="#a"/></svg>`,
		"xhtml prefixado":             `<svg xmlns:h="http://www.w3.org/1999/xhtml"><h:img srcset="https://evil/x.png"/></svg>`,
		"meta refresh":                `<svg xmlns:h="http://www.w3.org/1999/xhtml"><h:meta http-equiv="refresh" content="0;URL=https://evil"/></svg>`,
		"namespace estranho":          `<svg xmlns:e="https://evil/ns"><rect/></svg>`,
		"href data":                   `<svg><use href="data:image/svg+xml,x"/></svg>`,
		"a com href":                  `<svg><a href="#x"><text>x</text></a></svg>`,
	} {
		if svgSeguro([]byte(svg)) {
			t.Errorf("%s: aceite", nome)
		}
	}
	if !svgSeguro([]byte(`<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink"><defs><linearGradient id="g"/></defs><use xlink:href="#g"/><rect fill="url(#g)"/></svg>`)) {
		t.Fatal("um SVG com o namespace XLink e referencias internas foi recusado")
	}
}

// ACHADO 1 DA REVISÃO: a reescrita que o nó COMPÕE (não a de teste) leva os argumentos do modelo à
// entrada com o run, o passo e o titular da Call.
func TestAOS548_ReescritaComposta(t *testing.T) {
	t.Parallel()
	call := referencemonitor.Call{ToolID: artifactWriteToolName, RunID: "run-c", StepID: "step-1",
		Principal: referencemonitor.Principal{NHIID: "nhi:a", Subject: "tit-c"},
		Input:     []byte(`{"name":"a.json","media_type":"application/json","content":"{}"}`)}
	// Com um binding de sandbox, como em produção (a doc_read): o despacho composto tem de levar a
	// artifact_write à reescrita dela, e não à da sandbox.
	bindings := map[string]sandbox.SandboxBinding{"doc_read": {Command: "read", PathArg: "doc_id"}}
	got, err := reescritaDoEfeito(bindings, nil, nil, nil, true, nil)(call)
	if err != nil {
		t.Fatal(err)
	}
	var in entradaDoArtefacto
	if err := json.Unmarshal(got.Input, &in); err != nil || in.RunID != "run-c" || in.Subject != "tit-c" || in.Schema != artifactInputSchema {
		t.Fatalf("composta: %s %v", got.Input, err)
	}
	// O modelo não consegue mandar a entrada já reescrita: a reescrita recusa campos a mais, e a tool
	// recusa uma entrada que não seja a da reescrita.
	forjada := call
	forjada.Input = []byte(`{"schema":"aos-artifact-write/v1","run_id":"run-alheio","step_id":"x","subject":"outro","name":"a.json","media_type":"application/json","content":"{}"}`)
	if _, err := reescritaDoEfeito(bindings, nil, nil, nil, true, nil)(forjada); err == nil {
		t.Fatal("a reescrita aceitou a entrada forjada pelo modelo")
	}
	a, _, _ := aos548Armazem(t, 1<<30)
	a.registar = func(context.Context, string, artefactoCriado) error { return nil }
	if _, err := a.executar(context.Background(), []byte(`{"schema":"aos-artifact-write/v1","run_id":"r","step_id":"s","subject":"t","name":"a.json","media_type":"application/json","content":"{}","extra":1}`)); err == nil {
		t.Fatal("a tool aceitou uma entrada com campos a mais")
	}
}

// MUTAÇÕES MB, ME e MG: concorrência, dedup e o tecto do volume com o armazém a funcionar.
func TestAOS548_ConcorrenciaDedupEVolume(t *testing.T) {
	ctx := context.Background()
	a, _, _ := aos548Armazem(t, 1<<30)
	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, c := a.guardar(ctx, "tit", "run-c", []byte(fmt.Sprintf("conteudo %d", i))); c == "" {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != artifactMaxPorRun {
		t.Fatalf("gravaram %d em concorrencia; o tecto e %d", ok.Load(), artifactMaxPorRun)
	}
	b, _, _ := aos548Armazem(t, 1<<30)
	antes := b.usado.Load()
	d1, _ := b.guardar(ctx, "tit", "run-d", []byte("igual"))
	depois := b.usado.Load()
	d2, _ := b.guardar(ctx, "tit", "run-d", []byte("igual"))
	if d1 != d2 || depois == antes || b.usado.Load() != depois {
		t.Fatalf("dedup: %s %s usado %d->%d->%d", d1, d2, antes, depois, b.usado.Load())
	}
	// O tecto do volume em funcionamento: cabe o primeiro, não cabe o segundo.
	c, _, _ := aos548Armazem(t, 1<<30)
	um, _ := c.guardar(ctx, "tit", "run-v", []byte(strings.Repeat("a", 1000)))
	c.maximo = c.usado.Load() + 100
	if _, causa := c.guardar(ctx, "tit", "run-v", []byte(strings.Repeat("b", 1000))); causa != artCausaVolume || um == "" {
		t.Fatalf("o tecto do volume nao actuou: %q", causa)
	}
}

// MUTAÇÃO MF: o artefacto entra na retenção e na DSAR pelo titular do evento.
func TestAOS548_RetencaoPeloTitularDoEvento(t *testing.T) {
	t.Parallel()
	raw, _ := json.Marshal(artefactoCriado{Digest: "sha256:x", SealedSubject: "tit-r"})
	if got := subjectOf(eventstore.Event{Type: EventTypeArtifactCreated, Payload: raw}); got != "tit-r" {
		t.Fatalf("subjectOf = %q", got)
	}
}

// MUTAÇÕES MA, MC e MD na rota: o produtor do evento, o tipo e o nome revalidados, e o 410.
func TestAOS548_No_RotaRevalidaEApagado(t *testing.T) {
	vault := audit.NewInMemoryKeyVault(nil)
	n := aos533Compor(t, aos533Opcoes{artefactos: t.TempDir(), vault: vault}, aos533Guiao)
	runID := "run-aos548-reval"
	n.correr(t, agentruntime.Goal{RunID: runID})
	ctx := context.Background()
	a := n.node.artefactos
	conteudo := []byte("texto do artefacto")
	d, _ := a.guardar(ctx, acnAgent, runID, conteudo)
	// Um evento de OUTRO produtor com o mesmo digest não conta.
	raw, _ := json.Marshal(artefactoCriado{Digest: d, MediaType: "text/plain", Bytes: len(conteudo), Name: "x.txt", Tool: artifactWriteToolName, StepID: "s0", SealedSubject: acnAgent})
	if _, err := n.node.EventStore.Append(ctx, runID, eventstore.EventInput{Type: EventTypeArtifactCreated, Payload: raw, RunID: runID, StepID: "forjado-s0", Producer: eventstore.Producer{NHIID: "nhi:outro"}}); err != nil {
		t.Fatal(err)
	}
	if rec := getReq(n.h, "/runs/"+runID+"/artifacts/"+d, govHeaders()); rec.Code != http.StatusNotFound {
		t.Fatalf("evento de outro produtor serviu o artefacto: %d", rec.Code)
	}
	// O do nó, com um tipo fora do vocabulário e um nome mau: octet-stream e o nome de recurso.
	if err := registoDeArtefacto(n.node.EventStore)(ctx, runID, artefactoCriado{Digest: d, MediaType: "text/html", Bytes: len(conteudo), Name: "../x\".html", Tool: artifactWriteToolName, StepID: "s1", SealedSubject: acnAgent}); err != nil {
		t.Fatal(err)
	}
	rec := getReq(n.h, "/runs/"+runID+"/artifacts/"+d, govHeaders())
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/octet-stream" || rec.Header().Get("Content-Disposition") != `attachment; filename="artefacto"` {
		t.Fatalf("revalidacao: %d %v", rec.Code, rec.Header())
	}
	// Apagado (a KEK do titular destruída): 410.
	vault.Delete(acnAgent)
	if rec := getReq(n.h, "/runs/"+runID+"/artifacts/"+d, govHeaders()); rec.Code != http.StatusGone {
		t.Fatalf("apagado: %d %s", rec.Code, rec.Body.String())
	}
}
