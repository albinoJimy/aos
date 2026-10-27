package main

// AOS-546 — MEDIA NO TURNO (ADR-043 §2.2): o interruptor, a composição, a tool `media_view` e o
// resolvedor do adaptador.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

const aos546SpecBoa = `{"name":"media_view","description":"ve uma imagem","parameters":{"type":"object","properties":{"doc_id":{"type":"string"}},"required":["doc_id"]},` +
	`"capability":"cap:fs.read","resource_type":"file","resource_value":"doc://{doc_id}","resource_region":"eu-west","egress":"none","mutation":"none","reversibility":"reversible"}`

func aos546PNG() []byte {
	return append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, []byte("IHDR-aos546")...)
}

// aos546Ligacao compõe a ligação com a spec dada e uma pasta semeada com sala.png e notas.txt.
func aos546Ligacao(t *testing.T, spec string) (*ligacaoMedia, string, error) {
	t.Helper()
	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "sala.png"), aos546PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pasta, "notas.txt"), []byte("texto"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AOS_OCR_SEED_DIR", pasta)
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+spec+"]"))
	l, err := comporMediaNoTurno(true, true)
	if l != nil {
		t.Cleanup(l.fechar)
	}
	return l, pasta, err
}

// aos546Entrada é o input da `media_view` DEPOIS da reescrita do efeito (AOS-563): o que o
// executor recebe numa tool call mediada. O run e o passo são os de um passo qualquer — os testes
// que precisam de um run REAL (o do espaço de handles) constroem o seu.
func aos546Entrada(doc string) []byte {
	raw, err := json.Marshal(entradaOCR{Schema: mediaViewInputSchema, RunID: "r1", StepID: "s1", DocID: doc})
	if err != nil {
		panic(err)
	}
	return raw
}

func TestAOS546_Env_Vocabulario(t *testing.T) {
	for valor, quer := range map[string]bool{"": false, "off": false, "on": true} {
		t.Setenv("AOS_MEDIA_NO_TURNO", valor)
		got, err := parseMediaNoTurnoFromEnv()
		if err != nil || got != quer {
			t.Fatalf("%q: %v, %v", valor, got, err)
		}
	}
	for _, mau := range []string{"ON", "1", "true", "sim"} {
		t.Setenv("AOS_MEDIA_NO_TURNO", mau)
		if _, err := parseMediaNoTurnoFromEnv(); !errors.Is(err, ErrBadMediaNoTurno) {
			t.Fatalf("%q: %v", mau, err)
		}
	}
}

func TestAOS546_Compor_ExigeTudo(t *testing.T) {
	if l, err := comporMediaNoTurno(false, false); l != nil || err != nil {
		t.Fatalf("off: %v, %v", l, err)
	}
	if _, err := comporMediaNoTurno(true, false); !errors.Is(err, ErrMediaNoTurnoSemPedirAntesDeLer) {
		t.Fatalf("sem pedir antes de ler: %v", err)
	}
	maus := map[string]string{
		"outra tool":     strings.Replace(aos546SpecBoa, `"media_view"`, `"outra"`, 1),
		"egress externo": strings.Replace(aos546SpecBoa, `"egress":"none"`, `"egress":"external"`, 1),
		"mutadora":       strings.Replace(aos546SpecBoa, `"mutation":"none"`, `"mutation":"mutates"`, 1),
		"outra cap":      strings.Replace(aos546SpecBoa, `"cap:fs.read"`, `"cap:fs.stat"`, 1),
		"outro recurso":  strings.Replace(aos546SpecBoa, `"doc://{doc_id}"`, `"http://{doc_id}"`, 1),
		"com sandbox":    strings.Replace(aos546SpecBoa, `"egress":"none"`, `"egress":"none","sandbox":{"command":"read","path_arg":"doc_id"}`, 1),
		"com modelo":     strings.Replace(aos546SpecBoa, `"egress":"none"`, `"egress":"none","modelo":{"alias":"ocr-visao"}`, 1),
	}
	// Uma forma que a validação geral do registo já recusa (o bloco `modelo` noutra tool) falha com
	// o erro dela; as restantes chegam à conferência da media.
	for nome, spec := range maus {
		l, _, err := aos546Ligacao(t, spec)
		if l != nil || err == nil || (nome != "com modelo" && !errors.Is(err, ErrMediaNoTurnoSemTool)) {
			t.Fatalf("%s: %v", nome, err)
		}
	}
	t.Setenv("AOS_MODEL_TOOLS", writeTools(t, "["+aos546SpecBoa+"]"))
	t.Setenv("AOS_OCR_SEED_DIR", "")
	if _, err := comporMediaNoTurno(true, true); !errors.Is(err, ErrMediaNoTurnoSemPasta) {
		t.Fatalf("sem pasta: %v", err)
	}
	l, pasta, err := aos546Ligacao(t, aos546SpecBoa)
	if err != nil || l == nil || len(l.opcaoDoAdaptador()) != 1 {
		t.Fatalf("composicao boa: %v", err)
	}
	banner := strings.Join(mediaNoTurnoBanner(l), "\n")
	if !strings.Contains(banner, agentruntime.AssemblyVersion170) || !strings.Contains(banner, strconv.Quote(pasta)) || !strings.Contains(banner, "imagem") {
		t.Fatalf("banner: %s", banner)
	}
	if mediaNoTurnoBanner(nil) != nil || (*ligacaoMedia)(nil).opcaoDoAdaptador() != nil {
		t.Fatal("desligada tem banner ou opcao")
	}
}

func TestAOS546_MediaView_DevolveAReferenciaEOResolvedorConfere(t *testing.T) {
	l, pasta, err := aos546Ligacao(t, aos546SpecBoa)
	if err != nil {
		t.Fatal(err)
	}
	out, err := l.executar(context.Background(), aos546Entrada("sala.png"))
	if err != nil {
		t.Fatalf("executar: %v", err)
	}
	var ref agentruntime.MediaRef
	if err := json.Unmarshal(out, &ref); err != nil {
		t.Fatal(err)
	}
	quer := port.MediaPart{MediaType: port.ImageMediaPNG, Data: aos546PNG()}
	if ref.Schema != agentruntime.MediaRefSchema || ref.Doc != "sala.png" || ref.SHA256 != quer.Digest() || ref.MediaType != port.ImageMediaPNG || ref.Bytes != int64(len(quer.Data)) {
		t.Fatalf("referencia = %+v", ref)
	}
	if strings.Contains(string(out), "IHDR") {
		t.Fatal("a referencia leva os bytes")
	}
	// O kernel reconhece-a: num turno da 1.7.0 ela vira um segmento media.
	seq, _ := agentruntime.NewTailSequence(agentruntime.AssemblyVersion170)
	segs, _ := seq.Turn("s1", "", []agentruntime.CapturedToolResult{{Invocation: agentruntime.ToolInvocation{ToolID: mediaViewToolName}, Result: agentruntime.Untrusted(out)}})
	seg, ok := agentruntime.MediaOfSegment(segs[len(segs)-1])
	if !ok || seg.SHA256 != ref.SHA256 {
		t.Fatalf("o kernel nao reconheceu a referencia da tool: %+v", segs)
	}
	parte, err := l.resolver(context.Background(), "", seg)
	if err != nil || parte.Digest() != ref.SHA256 {
		t.Fatalf("resolver: %v", err)
	}
	// O ficheiro desaparece: o resolvedor falha (o adaptador recusa o turno).
	if err := os.Remove(filepath.Join(pasta, "sala.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.resolver(context.Background(), "", seg); err == nil {
		t.Fatal("resolveu um ficheiro que ja nao existe")
	}
	if l.lidas.Load() != 1 || l.resolvidas.Load() != 1 || l.falhadas.Load() != 1 {
		t.Fatalf("contadores: lidas %d resolvidas %d falhadas %d", l.lidas.Load(), l.resolvidas.Load(), l.falhadas.Load())
	}
}

func TestAOS546_MediaView_Recusas(t *testing.T) {
	l, _, err := aos546Ligacao(t, aos546SpecBoa)
	if err != nil {
		t.Fatal(err)
	}
	// AOS-563: a entrada e a da REESCRITA DO EFEITO ([entradaOCR] com [mediaViewInputSchema]).
	// Os casos sao os mesmos do AOS-546 mais os tres que a forma nova traz: sem run, sem passo e
	// com outro schema — nenhum deles vem de uma tool call mediada.
	for nome, in := range map[string]string{
		"nao e imagem":   string(aos546Entrada("notas.txt")),
		"inexistente":    string(aos546Entrada("nada.png")),
		"fora da raiz":   string(aos546Entrada("../sala.png")),
		"campo a mais":   `{"schema":"` + mediaViewInputSchema + `","run_id":"r1","step_id":"s1","doc_id":"sala.png","url":"http://x"}`,
		"sem doc":        `{"schema":"` + mediaViewInputSchema + `","run_id":"r1","step_id":"s1"}`,
		"sem run":        `{"schema":"` + mediaViewInputSchema + `","step_id":"s1","doc_id":"sala.png"}`,
		"sem passo":      `{"schema":"` + mediaViewInputSchema + `","run_id":"r1","doc_id":"sala.png"}`,
		"outro schema":   `{"schema":"aos-ocr-entrada/v1","run_id":"r1","step_id":"s1","doc_id":"sala.png"}`,
		"args crus":      `{"doc_id":"sala.png"}`,
		"nao e JSON":     `sala.png`,
		"dois objectos":  string(aos546Entrada("sala.png")) + `{}`,
		"doc com espaco": string(aos546Entrada("foto 1.png")),
	} {
		if out, err := l.executar(context.Background(), []byte(in)); err == nil || out != nil {
			t.Fatalf("%s: aceite (%s)", nome, out)
		}
	}
	if l.recusadas.Load() != 12 || l.lidas.Load() != 0 {
		t.Fatalf("contadores: recusadas %d lidas %d", l.recusadas.Load(), l.lidas.Load())
	}
}

func TestAOS546_LayoutDoRunNovoComMedia(t *testing.T) {
	com := agentruntime.Goal{Inputs: []agentruntime.PlanInput{{From: "n0"}}}
	for _, g := range []agentruntime.Goal{{}, com} {
		if v := layoutDoRunNovo(false, true, true, g); v != agentruntime.AssemblyVersion170 {
			t.Fatalf("com media: %s", v)
		}
	}
	r := novoRunsPorLayout(false, true, true)
	if !strings.Contains(strings.Join(r.publicadas(), ","), agentruntime.AssemblyVersion170) {
		t.Fatal("a serie da 1.7.0 nao e publicada com a media ligada")
	}
	if strings.Contains(strings.Join(novoRunsPorLayout(false, true, false).publicadas(), ","), agentruntime.AssemblyVersion170) {
		t.Fatal("a serie da 1.7.0 e publicada com a media desligada")
	}
}

// ACHADO 3 DA REVISÃO: ligada, a media_view tem executor (código do nó) e sai da linha das tools
// oferecidas sem executor; desligada, continua lá.
func TestAOS546_MediaViewSaiDasOrfasSoLigada(t *testing.T) {
	orfas := []string{"web_post", mediaViewToolName}
	if got := semMediaView(orfas, true); len(got) != 1 || got[0] != "web_post" {
		t.Fatalf("ligada: %v", got)
	}
	if got := semMediaView(orfas, false); len(got) != 2 {
		t.Fatalf("desligada: %v", got)
	}
}

// ACHADO 6 DA REVISÃO — A COMPOSIÇÃO DO NÓ, PONTA A PONTA: com a media ligada, o modelo pede a
// media_view, o RM permite-a e o executor do nó lê a imagem; no turno seguinte o adaptador resolve-a
// pela pasta e o gateway REAL recusa o pedido, porque o perfil da rota do titular não declara
// `imagem`. O run falha com essa causa e o provider só viu o primeiro pedido: nenhuma imagem saiu.
func TestAOS546_No_MediaViewPeloRMEAGuardaDaRota(t *testing.T) {
	ordem, extra, opcoes := aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra
	t.Cleanup(func() { aos486OrdemDoFicheiro, aos486CamposExtraDaSpec, aos486OpcoesExtra = ordem, extra, opcoes })
	pasta := t.TempDir()
	if err := os.WriteFile(filepath.Join(pasta, "sala.png"), aos546PNG(), 0o600); err != nil {
		t.Fatal(err)
	}
	raiz, err := os.OpenRoot(pasta)
	if err != nil {
		t.Fatal(err)
	}
	lig := &ligacaoMedia{root: raiz, raiz: raizOS{raiz}, pasta: pasta}
	t.Cleanup(lig.fechar)
	aos486OrdemDoFicheiro = []string{mediaViewToolName}
	aos486CamposExtraDaSpec = `,"egress":"none","reversibility":"reversible","mutation":"none"`
	aos486OpcoesExtra = lig.opcaoDoAdaptador()
	n := aos486Compor(t, func(cfg *Config) {
		cfg.PlanInputDepoisDoPrimeiroTurno = true
		cfg.MediaNoTurno = lig
	})
	n.upstream.mu.Lock()
	n.upstream.pede = mediaViewToolName
	n.upstream.responde = func(pedeTool bool, tool string) []byte {
		if !pedeTool {
			return []byte(`{"id":"cmpl-2","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"feito"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
		}
		return []byte(`{"id":"cmpl-1","object":"chat.completion","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"` + tool + `","arguments":"{\"doc_id\":\"sala.png\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
	}
	n.upstream.mu.Unlock()
	rec := postJSON(n.h, "POST", "/runs", map[string]any{
		"run_id": "run-aos546-no", "objective": "Ve a foto sala.png e descreve os comodos",
		"principal_nhi": durAgent, "credential": n.tok, "tools": []string{mediaViewToolName}, "max_turns": 3,
	})
	if rec.Code != 201 {
		t.Fatalf("POST /runs: %d %s", rec.Code, rec.Body.String())
	}
	wctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	oc, ok, werr := n.svc.Wait(wctx, "run-aos546-no")
	if werr != nil || !ok {
		t.Fatalf("Wait: ok=%v err=%v", ok, werr)
	}
	if oc.Err == nil || !errors.Is(oc.Err, modelgateway.ErrRouteWithoutImage) {
		t.Fatalf("o run devia falhar pela guarda da rota (sem imagem), veio %v", oc.Err)
	}
	if got := len(n.upstream.pedidos()); got != 1 {
		t.Fatalf("o provider recebeu %d pedidos; so o primeiro podia sair", got)
	}
	if lig.lidas.Load() != 1 || lig.resolvidas.Load() < 1 {
		t.Fatalf("a media_view leu %d e o adaptador resolveu %d", lig.lidas.Load(), lig.resolvidas.Load())
	}
	if m := aos505NoMetrics(t, n); !strings.Contains(m, `aos_runs_hosted_total{assembly_version="1.7.0"} 1`) {
		t.Fatalf("o run devia ter corrido em 1.7.0: %s", amostrasDe(m, "aos_runs_hosted_total"))
	}
}
