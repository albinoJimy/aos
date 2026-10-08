package bancoensaio

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentruntime "github.com/aos-ref/kernel/agent-runtime"
	modelgateway "github.com/aos-ref/platform/model-gateway"
	"github.com/aos-ref/platform/model-gateway/port"
)

// AOS-512 — OS BRAÇOS DA EXPERIÊNCIA DOS SEPARADORES.

// protocoloPublicado devolve o texto de protocolo de uma versão PUBLICADA, pedido à projecção
// de produção (uma vista sem system e sem tail dá só a mensagem `system`, com o protocolo).
func protocoloPublicado(t *testing.T, versao string) string {
	t.Helper()
	msgs, err := modelgateway.ProjectNativeVersion(versao, agentruntime.PromptView{AssemblyVersion: agentruntime.AssemblyVersion140})
	if err != nil || len(msgs) != 1 || msgs[0].Role != port.RoleSystem {
		t.Fatalf("a projeccao %s nao deu so a mensagem system: %v", versao, err)
	}
	return msgs[0].Content
}

type pedidoCapturado struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

// pedidosPorBraco corre UMA amostra da experiência dos separadores e devolve, por braço, o
// pedido que chegou ao provider falso — o que foi de facto para o fio.
func pedidosPorBraco(t *testing.T) (map[Braco]pedidoCapturado, *ambienteFalso) {
	t.Helper()
	a := novoAmbienteFalso(t, []Comportamento{ComportamentoCumpre}, nil)
	plano := PlanoDosSeparadores(7)
	plano.Amostras = 1
	r := a.correr(t, plano, nil)
	corpos := a.falso.Corpos()
	if len(corpos) != 4 || len(r.Observacoes) != 4 {
		t.Fatalf("uma amostra dos separadores sao 4 pedidos e 4 runs; vieram %d e %d", len(corpos), len(r.Observacoes))
	}
	out := map[Braco]pedidoCapturado{}
	for i, o := range r.Observacoes {
		var p pedidoCapturado
		if err := json.Unmarshal(corpos[i], &p); err != nil {
			t.Fatal(err)
		}
		out[Braco(o.Braco)] = p
	}
	if len(out) != 4 {
		t.Fatalf("faltam bracos: %d", len(out))
	}
	return out, a
}

func mensagem(t *testing.T, p pedidoCapturado, papel string) string {
	t.Helper()
	for _, m := range p.Messages {
		if m.Role == papel {
			return m.Content
		}
	}
	t.Fatalf("pedido sem mensagem %s", papel)
	return ""
}

// Os braços A e D são as versões PUBLICADAS, byte a byte: o banco não lhes toca.
func TestAOS512_Bracos_AeDSaoAsVersoesPublicadas(t *testing.T) {
	pedidos, a := pedidosPorBraco(t)
	sufixo := "=== SYSTEM ===\n" + a.bateria.System
	if got, quer := mensagem(t, pedidos[BracoA], "system"), protocoloPublicado(t, modelgateway.NativeProjectionVersion120)+sufixo; got != quer {
		t.Errorf("o system do braco A nao e o da projeccao 1.2.0 publicada")
	}
	if got, quer := mensagem(t, pedidos[BracoD], "system"), protocoloPublicado(t, modelgateway.NativeProjectionVersion)+sufixo; got != quer {
		t.Errorf("o system do braco D nao e o da projeccao 1.0.0 publicada")
	}
	utilizadorA := mensagem(t, pedidos[BracoA], "user")
	if !strings.HasPrefix(utilizadorA, "<objective>\n") || !strings.HasSuffix(utilizadorA, "</objective>\n") {
		t.Errorf("braco A: a semente tinha de ser o segmento objective com a linha de fim da 1.2.0")
	}
	utilizadorD := mensagem(t, pedidos[BracoD], "user")
	if !strings.HasPrefix(utilizadorD, "<objective>\n") || strings.Contains(utilizadorD, "</objective>") {
		t.Errorf("braco D: a 1.0.0 nao tem linha de fim")
	}
	msgs := []port.Message{{Role: port.RoleSystem, Content: "x"}, {Role: port.RoleUser, Content: "<objective>\ny\n</objective>\n"}}
	for _, b := range []Braco{BracoA, BracoD} {
		out, err := TransformarMensagens(b, msgs)
		if err != nil || &out[0] != &msgs[0] {
			t.Errorf("braco %s: as mensagens tinham de sair tal e qual (o mesmo slice)", b)
		}
	}
}

// O braço B muda UMA coisa: os separadores deixam de ter sinais de menor e de maior.
func TestAOS512_BracoB_SemSinaisDeMenorNemDeMaior(t *testing.T) {
	pedidos, _ := pedidosPorBraco(t)
	a, b := pedidos[BracoA], pedidos[BracoB]
	systemB := mensagem(t, b, "system")
	if strings.ContainsAny(systemB, "<>") {
		t.Errorf("o system do braco B ainda tem sinais de menor ou de maior")
	}
	if !strings.Contains(systemB, `"[[kind label=value ...]]"`) || !strings.Contains(systemB, `"[[/kind]]"`) || !strings.Contains(systemB, `"[[objective]]"`) {
		t.Errorf("o system do braco B nao nomeia os separadores novos")
	}
	// Desfazer as trocas dá o texto do braço A: nada mais mudou.
	desfeito := systemB
	for _, tr := range trocasDoBracoB {
		desfeito = strings.ReplaceAll(desfeito, tr.para, tr.de)
	}
	if desfeito != mensagem(t, a, "system") {
		t.Errorf("o system do braco B difere do do braco A em mais do que as frases dos separadores")
	}
	utilizadorA, utilizadorB := mensagem(t, a, "user"), mensagem(t, b, "user")
	quer := strings.Replace(strings.Replace(utilizadorA, "<objective>\n", "[[objective]]\n", 1), "</objective>\n", "[[/objective]]\n", 1)
	if utilizadorB != quer {
		t.Errorf("a semente do braco B nao e a do braco A com os separadores trocados:\n%q", utilizadorB)
	}
	if strings.ContainsAny(utilizadorB, "<>") {
		t.Errorf("a semente do braco B ainda tem sinais de menor ou de maior")
	}
}

// O braço C muda UMA coisa: não há linhas de fim, nem as frases que falam delas.
func TestAOS512_BracoC_SemLinhasDeFim(t *testing.T) {
	pedidos, _ := pedidosPorBraco(t)
	a, c := pedidos[BracoA], pedidos[BracoC]
	systemA, systemC := mensagem(t, a, "system"), mensagem(t, c, "system")
	if strings.Contains(systemC, "end line") || strings.Contains(systemC, "</kind>") {
		t.Errorf("o system do braco C ainda fala da linha de fim")
	}
	if !strings.Contains(systemC, `"<kind label=value ...>"`) || !strings.Contains(systemC, `"<objective>"`) {
		t.Errorf("o braco C tinha de manter os separadores de cabecalho da 1.2.0")
	}
	if n := strings.Count(systemA, "end line"); n != 5 {
		t.Fatalf("o protocolo 1.2.0 fala da linha de fim %d vezes; o braco C foi escrito para 5 — rever as trocas", n)
	}
	utilizadorA, utilizadorC := mensagem(t, a, "user"), mensagem(t, c, "user")
	if utilizadorC != strings.Replace(utilizadorA, "</objective>\n", "", 1) || strings.Contains(utilizadorC, "</") {
		t.Errorf("a semente do braco C nao e a do braco A sem a linha de fim:\n%q", utilizadorC)
	}
}

// A reescrita é linha a linha, sobre o que o runtime escreveu; o corpo não muda.
func TestAOS512_Bracos_ReescritaDosSegmentos(t *testing.T) {
	original := "<plan_input taint=untrusted from=ler>\nlinha um\n\\<escapada pelo kernel\n[parece separador\n</plan_input>\n<objective>\nfaz isto\n</objective>\n"
	b, err := reescreverSegmentos(BracoB, original)
	if err != nil {
		t.Fatal(err)
	}
	if quer := "[[plan_input taint=untrusted from=ler]]\nlinha um\n\\<escapada pelo kernel\n\\[parece separador\n[[/plan_input]]\n[[objective]]\nfaz isto\n[[/objective]]\n"; b != quer {
		t.Errorf("braco B:\n%q\nquer\n%q", b, quer)
	}
	c, err := reescreverSegmentos(BracoC, original)
	if err != nil {
		t.Fatal(err)
	}
	if quer := "<plan_input taint=untrusted from=ler>\nlinha um\n\\<escapada pelo kernel\n[parece separador\n<objective>\nfaz isto\n"; c != quer {
		t.Errorf("braco C:\n%q\nquer\n%q", c, quer)
	}
	if _, err := reescreverSegmentos(BracoB, "<objective\nsem fecho\n"); !errors.Is(err, ErrVariante) {
		t.Errorf("cabecalho malformado: err = %v, quer ErrVariante", err)
	}
}

// FAIL-CLOSED: se o texto do protocolo da 1.2.0 mudar, a variante não se constrói e o pedido
// não sai — em vez de se medir um texto que já não é «a 1.2.0 com uma diferença».
func TestAOS512_Bracos_ProtocoloMudadoNaoSeTransforma(t *testing.T) {
	protocolo := protocoloPublicado(t, modelgateway.NativeProjectionVersion120)
	// Uma frase que cada braço troca, alterada num só byte.
	mudados := map[Braco]string{
		BracoB: strings.Replace(protocolo, `"<objective>"`, `"<objective >"`, 1),
		BracoC: strings.Replace(protocolo, `then an end line "</kind>"`, `then a closing line "</kind>"`, 1),
	}
	for _, b := range []Braco{BracoB, BracoC} {
		mudado := mudados[b]
		if mudado == protocolo {
			t.Fatalf("a frase que o teste altera para o braco %s ja nao existe no protocolo 1.2.0", b)
		}
		if _, err := TransformarMensagens(b, []port.Message{{Role: port.RoleSystem, Content: protocolo}}); err != nil {
			t.Errorf("braco %s sobre o protocolo publicado: %v", b, err)
		}
		if _, err := TransformarMensagens(b, []port.Message{{Role: port.RoleSystem, Content: mudado}}); !errors.Is(err, ErrVariante) {
			t.Errorf("braco %s sobre um protocolo mudado: err = %v, quer ErrVariante", b, err)
		}
		if _, err := TransformarMensagens(b, []port.Message{{Role: port.RoleUser, Content: "<objective>\nx\n</objective>\n"}}); !errors.Is(err, ErrVariante) {
			t.Errorf("braco %s sem mensagem system: err = %v, quer ErrVariante", b, err)
		}
	}
	if _, err := TransformarMensagens(Braco("E"), nil); !errors.Is(err, ErrVariante) {
		t.Errorf("braco desconhecido: err = %v, quer ErrVariante", err)
	}
}

// raizDosPacotes devolve a pasta `packages/` do repositório.
func raizDosPacotes(t *testing.T) string {
	t.Helper()
	raiz, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(raiz, "cmd", "aos", "go.mod")); err != nil {
		t.Fatalf("nao encontrei packages/cmd/aos a partir de %s: %v", raiz, err)
	}
	return raiz
}

// AS VARIANTES EXISTEM SÓ NO BANCO. O nó não as aceita como versão de projecção, nenhum
// ficheiro fora do banco tem o seu texto, e nenhum binário do nó depende deste módulo.
func TestAOS512_ONoNaoContemOBanco(t *testing.T) {
	// (1) O vocabulário fechado do nó recusa os braços, com qualquer grafia.
	for _, v := range []string{"A", "B", "C", "D", "1.2.0-B", "1.2.0-C", "1.2.0+sem-fim", "1.2.0b", "b", "separadores"} {
		if _, err := modelgateway.ParseNativeProjectionVersion(v); !errors.Is(err, modelgateway.ErrBadProjectionVersion) {
			t.Errorf("ParseNativeProjectionVersion(%q) nao recusou: %v", v, err)
		}
	}
	// (2) As únicas versões que um braço pede ao adaptador do nó são publicadas.
	for _, b := range Bracos() {
		if _, err := modelgateway.ParseNativeProjectionVersion(VersaoPublicadaDoBraco(b)); err != nil {
			t.Errorf("braco %s parte de uma versao nao publicada: %v", b, err)
		}
	}
	// (3) Nenhum ficheiro Go fora do banco tem o texto das variantes nem importa o banco ou a
	// porta pública dos providers falsos; nenhum go.mod fora do banco depende do banco.
	raiz := raizDosPacotes(t)
	marcas := []string{`[[kind label=value ...]]`, `[[/kind]]`, `[[objective]]`, `A header line starts at the very first character of a line`}
	lidos := 0
	err := filepath.WalkDir(raiz, func(caminho string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := filepath.ToSlash(strings.TrimPrefix(caminho, raiz))
		if d.IsDir() {
			if rel == "/qa/banco-ensaio" {
				return filepath.SkipDir
			}
			return nil
		}
		nome := d.Name()
		if !strings.HasSuffix(nome, ".go") && nome != "go.mod" {
			return nil
		}
		cru, rerr := os.ReadFile(caminho)
		if rerr != nil {
			return rerr
		}
		lidos++
		texto := string(cru)
		if strings.Contains(texto, "aos-ref/qa/banco-ensaio") {
			t.Errorf("%s depende do banco de ensaio", rel)
		}
		if strings.Contains(texto, `"github.com/aos-ref/platform/model-gateway/wiretest"`) {
			t.Errorf("%s importa a porta publica dos providers falsos — so o banco o pode fazer", rel)
		}
		for _, m := range marcas {
			if strings.Contains(texto, m) {
				t.Errorf("%s contem texto de uma variante do banco (%q)", rel, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if lidos < 500 {
		t.Fatalf("so foram lidos %d ficheiros: o teste nao esta a varrer packages/", lidos)
	}
	// (4) A imagem de produção não constrói nem copia o banco.
	docker, err := os.ReadFile(filepath.Join(raiz, "..", "deploy", "node", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(docker), "banco-ensaio") || strings.Contains(string(docker), "aos-ensaio") || strings.Contains(string(docker), "packages/qa") {
		t.Error("o Dockerfile de producao refere o banco de ensaio")
	}
}
