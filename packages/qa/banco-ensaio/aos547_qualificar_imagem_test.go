package bancoensaio

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/allowlist"
	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
	rt "github.com/aos-ref/platform/model-gateway/policy/routeentry/routeentrytest"
)

// AOS-547 — o `qualificar` de um perfil do MODELO DO RUN com `imagem` corre, na mesma execução, a
// qualificação e a medida de imagem (a experiência `ocr` com o perfil do titular), e só com as duas
// positivas deixa o pacote: o registo cita os dois relatórios, e o nó carrega-o.

const aos547Perfil = `{"requested":"gpt-4o","expected_model":"openai/` + modeloKimiDeTeste + `","wire_class":"openai-chat-completions","capabilities":["tools","imagem"]}`

// aos547Lancador serve o primeiro lançamento (a qualificação) com o provider falso das tools e o
// segundo (a medida de imagem) com o modelo de visão falso dado.
type aos547Lancador struct {
	qual  *lancadorDaQualificacao
	visao *lancadorDeTeste
	n     int
}

func (l *aos547Lancador) Lancar(ctx context.Context, p PedidoDeProxy) (*ProxyVivo, error) {
	l.n++
	if l.n == 1 {
		return l.qual.Lancar(ctx, p)
	}
	return l.visao.Lancar(ctx, p)
}

func aos547Posto(t *testing.T, visao http.Handler) (*aos522Posto, *aos547Lancador) {
	t.Helper()
	p := aos522NovoPosto(t, ComportamentoCumpre)
	l := &aos547Lancador{qual: p.lanc, visao: &lancadorDeTeste{t: t, devolver: visao}}
	p.amb.Lancador = l
	return p, l
}

func TestAOS547_Qualificar_ComImagemPreparaOsDoisRelatoriosQueONoCarrega(t *testing.T) {
	p, l := aos547Posto(t, NovaVisaoFalsa(aos529Bateria(t)))
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos547Perfil)...)
	if e.codigo != SaidaOK || l.n != 2 {
		t.Fatalf("codigo %d, %d lancamentos\n%s\n%s", e.codigo, l.n, e.stdout, e.stderr)
	}
	pacote := filepath.Join(saida, PastaDoPacote)
	if got := aos522Ficheiros(t, pacote); !reflect.DeepEqual(got, []string{"gpt-4o.registo.json", "gpt-4o.relatorio-de-media.json", "gpt-4o.relatorio.json"}) {
		t.Fatalf("pacote = %v", got)
	}
	registo, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o.registo.json"))
	media, _ := os.ReadFile(filepath.Join(pacote, "gpt-4o.relatorio-de-media.json"))
	dm, err := routeentry.VerifyReportDigest(media)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(registo, &doc); err != nil || doc["digest_do_relatorio_de_media"] != dm {
		t.Fatalf("o registo nao cita a medida que esta no pacote: %v %v", doc, err)
	}
	if !strings.Contains(e.stdout, "--media-report") {
		t.Fatalf("a linha de assinar nao leva a medida:\n%s", e.stdout)
	}
	key := rt.NewKey(t)
	if err := os.WriteFile(filepath.Join(pacote, "gpt-4o"+routeentry.SuffixSignature), []byte(routeentry.Sign(registo, key.Priv)), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := allowlist.LoadPolicy()
	if err != nil {
		t.Fatal(err)
	}
	entradas, err := routeentry.LoadDir(routeentry.Config{Dir: pacote, TrustAnchor: key.Anchor(), Board: rt.Board, Region: rt.Region, Allowlist: pol})
	if err != nil || len(entradas) != 1 || entradas[0].ForTool() || entradas[0].MediaReportDigest != dm {
		t.Fatalf("o no nao carrega o pacote com a medida: %v %+v", err, entradas)
	}
	if c := executar(t, Ambiente{}, "conferir", "--pacote", pacote, "--ancora", key.Anchor(), "--board", rt.Board, "--regiao", rt.Region); c.codigo != SaidaOK {
		t.Fatalf("conferir: %d\n%s", c.codigo, c.stderr)
	}
}

// Sem a medida de imagem positiva não há pacote, mesmo com a qualificação positiva.
func TestAOS547_Qualificar_SemAMedidaNaoHaPacote(t *testing.T) {
	visao := NovaVisaoFalsa(aos529Bateria(t))
	visao.Cortar = true
	p, l := aos547Posto(t, visao)
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos547Perfil)...)
	if e.codigo == SaidaOK || l.n != 2 {
		t.Fatalf("codigo %d, %d lancamentos\n%s", e.codigo, l.n, e.stderr)
	}
	if _, err := os.Stat(filepath.Join(saida, PastaDoPacote)); !os.IsNotExist(err) {
		t.Fatalf("ficou pacote sem a medida: %v", err)
	}
	if !strings.Contains(e.stderr, "medida de imagem") {
		t.Fatalf("o motivo nao diz a medida:\n%s", e.stderr)
	}
}

// O modo proxy não mede imagem: recusa antes de enviar.
func TestAOS547_Qualificar_ProxyComImagemRecusado(t *testing.T) {
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, Ambiente{}, "qualificar", "proxy", "--binario-do-falso", "x", "--perfil", aos516Ficheiro(t, aos547Perfil), "--fatia", "10",
		"--board", rt.Board, "--regiao", rt.Region, "--saida", saida)
	if e.codigo != SaidaUso || !strings.Contains(e.stderr, "imagem") {
		t.Fatalf("codigo %d\n%s", e.codigo, e.stderr)
	}
}

// `ocr --titular` aceita o perfil do titular (tools e imagem); sem --titular, recusa-o como sempre.
func TestAOS547_OCR_TitularAceitaOPerfilComTools(t *testing.T) {
	perfil := aos516Ficheiro(t, aos547Perfil)
	if e := executar(t, Ambiente{}, "ocr", "--motor", "falso", "--perfil", perfil, "--saida", t.TempDir()); e.codigo != SaidaUso {
		t.Fatalf("sem --titular: %d\n%s", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{}, "ocr", "--motor", "falso", "--perfil", perfil, "--titular", "--saida", t.TempDir()); e.codigo != SaidaOK {
		t.Fatalf("com --titular: %d\n%s", e.codigo, e.stderr)
	}
	semImagem := aos516Ficheiro(t, strings.Replace(aos547Perfil, `,"imagem"`, ``, 1))
	if e := executar(t, Ambiente{}, "ocr", "--motor", "falso", "--perfil", semImagem, "--titular", "--saida", t.TempDir()); e.codigo != SaidaUso {
		t.Fatalf("titular sem imagem: %d\n%s", e.codigo, e.stderr)
	}
	if e := executar(t, Ambiente{}, "ocr", "--motor", "falso", "--titular", "--saida", t.TempDir()); e.codigo != SaidaUso {
		t.Fatalf("--titular sem --perfil: %d\n%s", e.codigo, e.stderr)
	}
}

// BARREIRA 2 DA MEDIDA (achado 3 da revisão): o relatório da medida no disco não é o que a corrida
// dela diz ter escrito — não fica pacote.
func TestAOS547_Qualificar_MedidaTrocadaNoDiscoNaoDeixaPacote(t *testing.T) {
	p, _ := aos547Posto(t, NovaVisaoFalsa(aos529Bateria(t)))
	// O relatório no disco deixa de ser o que a corrida da medida diz ter escrito.
	p.amb.entreAMedidaEAPreparacao = func(m *medidaDeMedia) { m.digest = "sha256:" + strings.Repeat("0", 64) }
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos547Perfil)...)
	if e.codigo == SaidaOK || !strings.Contains(e.stderr+e.stdout, "relatorio_trocado") {
		t.Fatalf("codigo %d\n%s\n%s", e.codigo, e.stdout, e.stderr)
	}
	if _, err := os.Stat(filepath.Join(saida, PastaDoPacote)); !os.IsNotExist(err) {
		t.Fatal("ficou pacote com a medida trocada")
	}
}

// BARREIRA 4 DA MEDIDA: o relatório da medida alterado no pacote depois de escrito — o pacote sai.
func TestAOS547_Qualificar_MedidaAlteradaNoPacoteRetiraOPacote(t *testing.T) {
	p, _ := aos547Posto(t, NovaVisaoFalsa(aos529Bateria(t)))
	p.amb.depoisDeEscreverOPacote = func(pacote string) {
		f := filepath.Join(pacote, "gpt-4o"+routeentry.SuffixMediaReport)
		cru, _ := os.ReadFile(f)
		_ = os.WriteFile(f, append(cru, ' '), 0o600)
	}
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos547Perfil)...)
	if e.codigo == SaidaOK || !strings.Contains(e.stderr+e.stdout, "pacote_alterado") {
		t.Fatalf("codigo %d\n%s\n%s", e.codigo, e.stdout, e.stderr)
	}
	if _, err := os.Stat(filepath.Join(saida, PastaDoPacote)); !os.IsNotExist(err) {
		t.Fatal("ficou pacote com a medida alterada")
	}
}

// A MEDIDA USA A ROTA DA QUALIFICAÇÃO (achado 8): o fornecedor, o modelo, as chaves, os preços e o
// contador passam, com o nome da opção da `ocr`.
func TestAOS547_ArgumentosDaMedidaSaoOsDaRota(t *testing.T) {
	o := opcoesDoQualificar{perfil: "p.json", saida: "S"}
	args := strings.Join(argumentosDaMedidaDeImagem(o, map[string]string{"chaves": "C", "fornecedor": "openrouter", "modelo": "M", "precos": "P", "reconstruir-contador": "true", "pausa": "1s"}), " ")
	for _, quer := range []string{"--motor " + NomeMotorVisao, "--perfil p.json", "--titular", "--saida S", "--chaves=C", "--visao-fornecedor=openrouter", "--visao-modelo=M", "--precos=P", "--reconstruir-contador"} {
		if !strings.Contains(args, quer) {
			t.Errorf("falta %q em %s", quer, args)
		}
	}
	if strings.Contains(args, "pausa") {
		t.Errorf("passou uma opcao que a ocr nao tem: %s", args)
	}
}

// PRÉ-CONFERÊNCIA (achado 2): com uma qualificação do perfil já feita hoje, o `qualificar` com
// imagem recusa ANTES de enviar — a medida seria a terceira do dia.
func TestAOS547_Qualificar_SemDuasVagasRecusaAntesDeEnviar(t *testing.T) {
	p, l := aos547Posto(t, NovaVisaoFalsa(aos529Bateria(t)))
	// Uma medida isolada hoje, pelo mesmo ficheiro de chaves (o mesmo registo do dia).
	isolada := &lancadorDeTeste{t: t, devolver: NovaVisaoFalsa(aos529Bateria(t))}
	if e := executar(t, Ambiente{Lancador: isolada, Relogio: p.amb.Relogio}, "ocr", "--motor", "visao", "--chaves", p.chaves, "--visao-fornecedor", "kimi",
		"--perfil", aos516Ficheiro(t, aos547Perfil), "--titular", "--saida", t.TempDir()); e.codigo != SaidaOK {
		t.Fatalf("medida isolada: %d\n%s", e.codigo, e.stderr)
	}
	saida := filepath.Join(t.TempDir(), "saida")
	e := executar(t, p.amb, p.real(saida, aos547Perfil)...)
	if e.codigo != SaidaRecusada || l.n != 0 || !strings.Contains(e.stderr, "duas vagas") {
		t.Fatalf("codigo %d, %d lancamentos\n%s", e.codigo, l.n, e.stderr)
	}
}

// --destino-fora-da-lista com imagem recusa-se nos argumentos.
func TestAOS547_Qualificar_DestinoForaDaListaComImagem(t *testing.T) {
	p, l := aos547Posto(t, NovaVisaoFalsa(aos529Bateria(t)))
	e := executar(t, p.amb, p.real(filepath.Join(t.TempDir(), "s"), aos547Perfil, "--destino-fora-da-lista", "exemplo.invalid")...)
	if e.codigo != SaidaUso || l.n != 0 {
		t.Fatalf("codigo %d, %d lancamentos\n%s", e.codigo, l.n, e.stderr)
	}
}
