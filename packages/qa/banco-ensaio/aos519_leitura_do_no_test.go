package bancoensaio

// AOS-519 — O QUE O NÓ LÊ DE UM RELATÓRIO É O QUE O BANCO ESCREVE.
//
// O nó confere o relatório de qualificação que um registo de entrada assinado cita, e não importa
// o banco: lê-o pela leitura fechada do pacote `routeentry`, com listas FECHADAS de nomes de
// membros e os valores do vocabulário escritos lá. Estes testes prendem as duas escritas uma à
// outra — um campo novo no relatório, ou um valor que mude de nome, avermelha aqui, e não num nó
// que deixa de arrancar em produção.

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/aos-ref/platform/model-gateway/policy/routeentry"
)

// nomesDosMembros devolve os nomes JSON dos campos de uma struct, ordenados.
func nomesDosMembros(v any) []string {
	t := reflect.TypeOf(v)
	var nomes []string
	for i := 0; i < t.NumField(); i++ {
		nome, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if nome != "" && nome != "-" {
			nomes = append(nomes, nome)
		}
	}
	sort.Strings(nomes)
	return nomes
}

func ordenadas(l []string) []string {
	sort.Strings(l)
	return l
}

// As listas fechadas do nó são EXACTAMENTE os membros que o banco escreve, nos três objectos por
// onde a leitura passa; e os valores que o nó exige são os do banco.
func TestAOS519_Banco_AsListasFechadasDoNoSaoAsDoRelatorio(t *testing.T) {
	for nome, par := range map[string][2][]string{
		"topo":             {nomesDosMembros(Relatorio{}), ordenadas(routeentry.ReportTopFields())},
		"qualificacao":     {nomesDosMembros(QualificacaoDoModelo{}), ordenadas(routeentry.QualificationFields())},
		"rota qualificada": {nomesDosMembros(RotaQualificada{}), ordenadas(routeentry.QualifiedRouteFields())},
	} {
		if !reflect.DeepEqual(par[0], par[1]) {
			t.Errorf("%s: o banco escreve %v e o no admite %v", nome, par[0], par[1])
		}
	}
	if routeentry.ReportVersion != VersaoDoRelatorio || routeentry.ModeReal != ModoReal ||
		routeentry.ExperimentQualification != string(ExperienciaQualificacao) || routeentry.VerdictQualified != VeredictoQualificado ||
		routeentry.ReportDigestField != CampoDoDigestDoRelatorio {
		t.Error("um valor que o no exige do relatorio ja nao e o que o banco escreve")
	}
	if routeentry.ProductionProxyImage != ImagemDoProxy || routeentry.ProductionProxyImageDigest() != digestDaImagem(ImagemDoProxy) {
		t.Error("a imagem do proxy que o no confere nao e a que o banco declara")
	}
}

// Um relatório VERDADEIRO do banco — o de uma qualificação no modo falso, escrito pela linha de
// comandos — passa pela leitura fechada do nó, e o nó lê dele o que o banco lá pôs: `qualificado`,
// mas num ensaio, que NÃO vale para produção. Um registo que o citasse seria recusado.
func TestAOS519_Banco_ONoLeUmRelatorioVerdadeiroENaoOAceitaDeUmEnsaio(t *testing.T) {
	e, r := aos518Qualificar(t, aos519PerfilDeProducao, "--roteiro", "cumpre", "--silencioso")
	achados, _ := filepath.Glob(filepath.Join(e.saida, "*.json"))
	if len(achados) != 1 {
		t.Fatalf("relatorios: %v", achados)
	}
	cru, err := os.ReadFile(achados[0])
	if err != nil {
		t.Fatal(err)
	}
	v, err := routeentry.ReadVerdict(cru)
	if err != nil {
		t.Fatalf("o no nao le um relatorio verdadeiro do banco: %v", err)
	}
	q := aos518Veredicto(t, r)
	if v.Digest != r.DigestDoRelatorio || v.Mode != ModoFalso || v.Verdict != VeredictoQualificado || v.ValidForProduction ||
		v.ProfileDigest != q.Rota.DigestDoPerfil || v.ProxyImageDigest != "" {
		t.Fatalf("o no leu %+v; o relatorio diz modo=%s veredicto=%s vale=%v perfil=%s", v, r.Modo, q.Veredicto, q.ValeParaProducao, q.Rota.DigestDoPerfil)
	}
	// O registo que citasse este relatório: o nó confere-o e recusa-o, porque é de um ensaio.
	registo := routeentry.Entry{ReportDigest: v.Digest, ProfileDigest: v.ProfileDigest, ProxyImageDigest: routeentry.ProductionProxyImageDigest()}
	if causa := routeentry.CauseOf(routeentry.CheckReport(registo, cru)); causa != routeentry.CauseVerdictNotForProduction {
		t.Fatalf("um veredicto do modo falso tinha de ser recusado com %s; veio %q", routeentry.CauseVerdictNotForProduction, causa)
	}
}
