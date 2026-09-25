package main

// aos441_catalogo_de_tools_test.go — AOS-441, a metade do NÓ.
//
// O `aos-orq` passa a recusar um snapshot cujas tools o nó não tenha. Para isso o nó tem de dizer
// que tools tem — e dizê-lo com os MESMOS nomes que a lista-branca compara e os MESMOS digests que
// o registo assinado sela. Estes testes fixam as duas coisas e a forma do fio, que é o contrato com
// o cliente em packages/cmd/aos-orq/node_client.go.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// aos441ManifestoDeProducao é o manifesto de tools que o nó de produção oferece ao modelo. É ELE,
// e não uma cópia, para que um teste aqui fale dos nomes reais — `doc_read`, e não o `fs.read` que
// o snapshot de produção teve durante semanas.
const aos441ManifestoDeProducao = "../../../deploy/server/model-tools/tools.json"

func aos441Ambiente(t *testing.T, manifesto string) {
	t.Helper()
	t.Setenv("AOS_MODEL_ENDPOINT", "http://modelo.invalido")
	t.Setenv("AOS_MODEL_TOOLS", manifesto)
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "")
}

// O catálogo serve os nomes do manifesto e os eixos normalizados pela semântica do nó.
func TestAOS441CatalogoServeOsNomesDoManifesto(t *testing.T) {
	aos441Ambiente(t, aos441ManifestoDeProducao)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	porNome := map[string]entradaDoCatalogo{}
	for _, e := range cat {
		porNome[e.Name] = e
	}
	doc, ok := porNome["doc_read"]
	if !ok {
		t.Fatalf("o manifesto de produção nomeia `doc_read` e o catálogo não o tem: %+v", cat)
	}
	if doc.Egress != "none" || doc.Reversibility != "reversible" || doc.Version != "1.0.0" {
		t.Errorf("doc_read: %+v — esperado egress none, reversible, 1.0.0", doc)
	}
	post, ok := porNome["web_post"]
	if !ok {
		t.Fatalf("o manifesto de produção nomeia `web_post` e o catálogo não o tem: %+v", cat)
	}
	// `web_post` NÃO declara reversibility: pela semântica do nó isso é IRREVERSÍVEL, e o catálogo
	// tem de o dizer assim — servir o vazio deixaria o consumidor adivinhar.
	if post.Egress != "external" || post.Reversibility != "irreversible" {
		t.Errorf("web_post: %+v — esperado egress external, irreversible", post)
	}
	if _, fantasma := porNome["fs.read"]; fantasma {
		t.Error("o catálogo tem `fs.read`, que o manifesto do nó não nomeia")
	}
	if !sort.SliceIsSorted(cat, func(i, j int) bool { return cat[i].Name < cat[j].Name }) {
		t.Errorf("o catálogo tem de sair ordenado por nome (determinismo do fio): %+v", cat)
	}
}

// O DIGEST SERVIDO É O DO REGISTO ASSINADO. É o que o torna comparável: um digest calculado de
// outra forma seria só mais um rótulo, como o `sha256:aaa` que o snapshot de produção tinha.
func TestAOS441DigestEOMesmoDoRegistoAssinado(t *testing.T) {
	aos441Ambiente(t, aos441ManifestoDeProducao)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	t.Setenv("AOS_MODEL_TOOLS_REGISTER", "1")
	spec, err := parseSignedToolRegistryFromEnv()
	if err != nil || spec == nil {
		t.Fatalf("parseSignedToolRegistryFromEnv: spec=%v err=%v", spec, err)
	}
	entradas, err := spec.Catalog.ActiveEntries(context.Background())
	if err != nil {
		t.Fatalf("ActiveEntries: %v", err)
	}
	doRegisto := map[string][2]string{}
	for _, e := range entradas {
		doRegisto[e.ID] = [2]string{e.Version.String(), e.Digest}
	}
	if len(doRegisto) != len(cat) {
		t.Fatalf("o registo tem %d tools e o catálogo %d", len(doRegisto), len(cat))
	}
	for _, e := range cat {
		r, ok := doRegisto[e.Name]
		if !ok {
			t.Errorf("%s está no catálogo e não no registo assinado", e.Name)
			continue
		}
		if r[0] != e.Version || r[1] != e.Digest {
			t.Errorf("%s: o catálogo diz %s@%s, o registo assinado %s@%s", e.Name, e.Version, e.Digest, r[0], r[1])
		}
		if len(e.Digest) != len("sha256:")+64 {
			t.Errorf("%s: digest %q não é um sha256 real", e.Name, e.Digest)
		}
	}
}

// Sem modelo o nó não oferece tools nenhumas, e o catálogo diz exactamente isso.
func TestAOS441SemModeloOCatalogoEVazio(t *testing.T) {
	aos441Ambiente(t, aos441ManifestoDeProducao)
	t.Setenv("AOS_MODEL_ENDPOINT", "")
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v", err)
	}
	if cat == nil || len(cat) != 0 {
		t.Fatalf("sem AOS_MODEL_ENDPOINT o catálogo tinha de ser vazio (e não nil), veio %#v", cat)
	}
}

// Um eixo de risco ilegível ABORTA — não se serve um eixo adivinhado.
func TestAOS441EgressIlegivelAborta(t *testing.T) {
	dir := t.TempDir()
	m := filepath.Join(dir, "tools.json")
	if err := os.WriteFile(m, []byte(`[{"name":"x","capability":"cap:x","resource_type":"file","resource_value":"doc://x","egress":"lá-fora"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	aos441Ambiente(t, m)
	_, err := catalogoDeToolsDoAmbiente()
	if !errors.Is(err, ErrBadModelTools) {
		t.Fatalf("egress ilegível tinha de abortar com ErrBadModelTools, veio %v", err)
	}
}

// Um egress que o manifesto NÃO declara sai `unknown` — fail-closed, na convenção do `aos-orq` —
// e não o `none` que o contrato usa para o digest. O digest, esse, não muda: continua a ser o do
// registo, que é o que o torna comparável.
func TestAOS441EgressAusenteSaiUnknown(t *testing.T) {
	dir := t.TempDir()
	m := filepath.Join(dir, "tools.json")
	sem := `[{"name":"x","capability":"cap:x","resource_type":"file","resource_value":"doc://x"}]`
	com := `[{"name":"x","capability":"cap:x","resource_type":"file","resource_value":"doc://x","egress":"none"}]`
	if err := os.WriteFile(m, []byte(sem), 0o600); err != nil {
		t.Fatal(err)
	}
	aos441Ambiente(t, m)
	cat, err := catalogoDeToolsDoAmbiente()
	if err != nil || len(cat) != 1 {
		t.Fatalf("catalogoDeToolsDoAmbiente: %v %+v", err, cat)
	}
	if cat[0].Egress != "unknown" {
		t.Fatalf("egress não declarado tinha de sair `unknown`, saiu %q", cat[0].Egress)
	}
	if err := os.WriteFile(m, []byte(com), 0o600); err != nil {
		t.Fatal(err)
	}
	catNone, err := catalogoDeToolsDoAmbiente()
	if err != nil || len(catNone) != 1 {
		t.Fatalf("catalogoDeToolsDoAmbiente (none): %v %+v", err, catNone)
	}
	if catNone[0].Egress != "none" {
		t.Fatalf("egress declarado `none` tinha de sair `none`, saiu %q", catNone[0].Egress)
	}
	if cat[0].Digest != catNone[0].Digest {
		t.Fatalf("o digest do contrato não depende de o `none` ser explícito: %s vs %s", cat[0].Digest, catNone[0].Digest)
	}
}

// O SERVIDOR QUE O ENTRYPOINT CONSTRÓI serve o catálogo. Sem isto, apagar o `WithToolCatalog` de
// [serveAPI] deixava a suite verde — todos os outros testes montam o handler à mão (o vácuo que o
// TestAOS277ServeAPIEnforcaOsLimitesLidos fechou para os limites de ingresso).
func TestAOS441ServeAPIServeOCatalogo(t *testing.T) {
	clearIngressEnv(t)
	aos441Ambiente(t, aos441ManifestoDeProducao)
	node, _ := newAPINode(t, &countingModel{}, false)
	defer func() { _ = node.Close() }()

	addr := portaLivreLoopback(t)
	ctx, cancel := context.WithCancel(context.Background())
	fim := make(chan error, 1)
	go func() { fim <- serveAPI(ctx, io.Discard, node, addr) }()
	defer func() {
		cancel()
		select {
		case err := <-fim:
			if err != nil {
				t.Errorf("serveAPI devia encerrar graciosamente, veio %v", err)
			}
		case <-time.After(15 * time.Second):
			t.Error("serveAPI nao encerrou apos cancelamento do ctx")
		}
	}()
	esperarPorta(t, addr)

	resp, err := (&http.Client{Timeout: 5 * time.Second}).Get("http://" + addr + "/tools")
	if err != nil {
		t.Fatalf("GET /tools: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var corpo respostaDoCatalogo
	if err := json.NewDecoder(resp.Body).Decode(&corpo); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /tools: %d %v", resp.StatusCode, err)
	}
	nomes := make([]string, 0, len(corpo.Tools))
	for _, e := range corpo.Tools {
		nomes = append(nomes, e.Name)
	}
	if !reflect.DeepEqual(nomes, []string{"doc_read", "web_post"}) {
		t.Fatalf("o servidor de serveAPI tinha de servir o catálogo do manifesto de produção, serviu %v", nomes)
	}
}

// A FORMA DO FIO. É o contrato com o `aos-orq`: um campo renomeado aqui deixa o cliente sem o
// ler, e o cliente recusa — mas o teste tem de avermelhar deste lado primeiro.
func TestAOS441GetToolsServeOCatalogoComAFormaDoFio(t *testing.T) {
	node := newTestNode(t, &countingModel{})
	cat := []entradaDoCatalogo{{Name: "doc_read", Version: "1.0.0", Digest: "sha256:d", Egress: "none", Reversibility: "reversible"}}
	_, h := newAPI(t, node, WithToolCatalog(cat))

	rec := getReq(h, "/tools", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tools sem gate soberano devia servir pelo read-path legado, veio %d (%s)", rec.Code, rec.Body.String())
	}
	var bruto map[string][]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bruto); err != nil {
		t.Fatalf("corpo ilegível: %v (%s)", err, rec.Body.String())
	}
	quer := map[string][]map[string]any{"tools": {{
		"name": "doc_read", "version": "1.0.0", "digest": "sha256:d", "egress": "none", "reversibility": "reversible",
	}}}
	if !reflect.DeepEqual(bruto, quer) {
		t.Fatalf("forma do fio:\n veio %v\n quer %v", bruto, quer)
	}

	// Sem catálogo composto, `tools` continua PRESENTE e vazio.
	_, h2 := newAPI(t, node)
	rec = getReq(h2, "/tools", nil)
	if rec.Code != http.StatusOK || rec.Body.String() == "" {
		t.Fatalf("GET /tools sem catálogo: %d %s", rec.Code, rec.Body.String())
	}
	var vazio map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &vazio); err != nil || string(vazio["tools"]) != "[]" {
		t.Fatalf("sem catálogo, `tools` tinha de vir [] — veio %s", rec.Body.String())
	}
}

// Com o gate soberano composto, a rota exige a credencial das rotas irmãs do `aos-orq`.
func TestAOS441GetToolsComGateSoberanoExigeCredencial(t *testing.T) {
	node := newTwoRegionGovNode(t, &countingModel{})
	_, h := newAPI(t, node, WithToolCatalog([]entradaDoCatalogo{{Name: "doc_read"}}))

	if rec := getReq(h, "/tools", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("sem credencial, com o gate composto, GET /tools tinha de dar 403, veio %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := getReq(h, "/tools", euReaderHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("com a credencial do leitor, GET /tools tinha de dar 200, veio %d (%s)", rec.Code, rec.Body.String())
	}
}
