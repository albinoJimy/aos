package semver

import (
	"strings"
	"testing"

	"github.com/aos-ref/platform/registry/domain"
)

// AOS-335 — UMA MUDANÇA DE MANIFESTO NÃO É UM BUMP PATCH.
//
// `ClassifyContract` era cega ao `ManifestDigest`. O contrato de um `mcp_server` é
// `{Egress, ManifestDigest}`: com o digest ignorado, sobrava o egress, e dois servidores com
// superfícies completamente diferentes classificavam como `ChangeNone` — `1.0.0 → 1.0.1` passava
// no `ValidateBump`.
//
// O QUE ISTO NÃO É: a omissão não deixava a classificação INDEFINIDA. DEGRADAVA-A para
// «compatível», que é uma afirmação positiva e falsa. É a diferença entre não saber e dizer mal.

func contratoMCP(digest string) domain.Contract {
	return domain.Contract{Egress: domain.EgressInternal, ManifestDigest: digest}
}

func TestAOS335_ManifestoDiferenteNaoEPatch(t *testing.T) {
	t.Parallel()
	velho := contratoMCP("sha256:manifesto-a")
	novo := contratoMCP("sha256:manifesto-b")

	got, reasons := ClassifyContract(velho, novo, false)
	if got != domain.ChangeMajor {
		t.Fatalf("classificacao = %v, quer ChangeMajor — uma troca de manifesto e uma troca de IDENTIDADE do servidor", got)
	}
	if !contemRazao(reasons, "manifest_digest_changed") {
		t.Errorf("razoes = %v, quer conter manifest_digest_changed", reasons)
	}

	// O QUE DECIDE, ponta a ponta: o bump PATCH deixa de ser aceite.
	pedido := func(to domain.Version) ChangeRequest {
		return ChangeRequest{
			From: domain.Version{Major: 1}, To: to,
			OldContract: velho, NewContract: novo,
		}
	}
	if _, err := ValidateBump(pedido(domain.Version{Major: 1, Patch: 1})); err == nil {
		t.Error("1.0.0 -> 1.0.1 com manifesto diferente tinha de ser recusado")
	}
	// E o MAJOR passa a ser o bump que serve.
	if _, err := ValidateBump(pedido(domain.Version{Major: 2})); err != nil {
		t.Errorf("1.0.0 -> 2.0.0 com manifesto diferente tem de ser aceite: %v", err)
	}
}

// TestAOS335_ManifestoIgualNaoMoveNada é o CONTROLO, e é o que prova que a mudança é ADITIVA.
//
// Sem ele, uma implementação que devolvesse MAJOR sempre passaria o teste acima — e exigiria bump
// MAJOR a todo o catálogo, incluindo as tools e skills que não têm manifesto nenhum.
func TestAOS335_ManifestoIgualNaoMoveNada(t *testing.T) {
	t.Parallel()
	casos := []struct {
		nome        string
		velho, novo domain.Contract
	}{
		{"mcp_server com o mesmo manifesto", contratoMCP("sha256:manifesto-a"), contratoMCP("sha256:manifesto-a")},
		// Os artefactos sem manifesto têm o campo vazio DOS DOIS LADOS — é a maioria do
		// catálogo, e nenhum deles se pode ter movido.
		{"tool sem manifesto", domain.Contract{Egress: domain.EgressNone}, domain.Contract{Egress: domain.EgressNone}},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			got, reasons := ClassifyContract(c.velho, c.novo, false)
			if got != domain.ChangeNone {
				t.Errorf("classificacao = %v, quer ChangeNone; razoes=%v", got, reasons)
			}
			if len(reasons) != 0 {
				t.Errorf("razoes = %v, quer vazio", reasons)
			}
		})
	}
}

// TestAOS335_GanharManifestoTambemEMajor cobre a transição que o AOS-334 torna possível: uma
// entrada histórica sem digest e a sua sucessora com digest.
//
// É o caso de MIGRAÇÃO, e é MAJOR pela mesma razão: até aqui não se sabia o que estava por trás
// do contrato; a partir daqui sabe-se. Tratar isso como compatível seria afirmar que o
// desconhecido e o conhecido são a mesma coisa.
func TestAOS335_GanharManifestoTambemEMajor(t *testing.T) {
	t.Parallel()
	got, reasons := ClassifyContract(contratoMCP(""), contratoMCP("sha256:manifesto-a"), false)
	if got != domain.ChangeMajor {
		t.Fatalf("classificacao = %v, quer ChangeMajor", got)
	}
	if !contemRazao(reasons, "manifest_digest_changed") {
		t.Errorf("razoes = %v, quer conter manifest_digest_changed", reasons)
	}
}

// TestAOS335_AInvarianteDasRazoesSobrevive: `ChangeNone` ⟺ razões vazias. É uma invariante que já
// existia (`classify_test.go`), e uma razão nova é precisamente o tipo de acrescento que a parte.
func TestAOS335_AInvarianteDasRazoesSobrevive(t *testing.T) {
	t.Parallel()
	casos := []struct{ velho, novo domain.Contract }{
		{contratoMCP("a"), contratoMCP("a")},
		{contratoMCP("a"), contratoMCP("b")},
		{contratoMCP(""), contratoMCP("")},
		{contratoMCP(""), contratoMCP("b")},
		{contratoMCP("b"), contratoMCP("")},
	}
	for _, c := range casos {
		k, reasons := ClassifyContract(c.velho, c.novo, false)
		if (k == domain.ChangeNone) != (len(reasons) == 0) {
			t.Errorf("invariante partida para (%q -> %q): kind=%v razoes=%v", c.velho.ManifestDigest, c.novo.ManifestDigest, k, reasons)
		}
	}
}

func contemRazao(reasons []string, quer string) bool {
	for _, r := range reasons {
		if strings.TrimSpace(r) == quer {
			return true
		}
	}
	return false
}

// TestAOS335_ToolComAncoraMudadaTambemEMajor cobre o caso que NÃO TINHA TESTE NENHUM em pacote
// nenhum, e que é a maior parte do custo real desta regra.
//
// O `mcp.Host` grava a MESMA âncora em cada entrada `kind=tool` que o servidor expõe. Mover o
// endpoint de um servidor com N tools exige, portanto, bump MAJOR nas N — e a primeira redacção
// do impacto na `tecnica/05` afirmava o contrário («só os mcp_server se movem»), omitindo
// justamente o custo que devia declarar.
//
// `ClassifyContract` é agnóstica ao kind, e isto fixa que assim seja: a regra é sobre o CAMPO,
// não sobre o kind. Uma implementação que a condicionasse a `mcp_server` deixaria as tools
// derivadas a promover como PATCH sobre um servidor trocado.
func TestAOS335_ToolComAncoraMudadaTambemEMajor(t *testing.T) {
	t.Parallel()
	comAncora := func(a string) domain.Contract {
		return domain.Contract{
			InputSchema:    []byte(`{"type":"object"}`),
			Egress:         domain.EgressInternal,
			ManifestDigest: a,
		}
	}
	got, reasons := ClassifyContract(comAncora("sha256:ancora-a"), comAncora("sha256:ancora-b"), false)
	if got != domain.ChangeMajor {
		t.Fatalf("classificacao = %v, quer ChangeMajor — uma tool cuja ancora mudou vem de um servidor trocado", got)
	}
	if !contemRazao(reasons, "manifest_digest_changed") {
		t.Errorf("razoes = %v, quer conter manifest_digest_changed", reasons)
	}

	// CONTROLO: uma tool GENÉRICA, sem âncora dos dois lados, não se move — é a maioria do
	// catálogo e nenhuma delas pode ter passado a exigir MAJOR.
	generica := domain.Contract{InputSchema: []byte(`{"type":"object"}`), Egress: domain.EgressNone}
	if k, r := ClassifyContract(generica, generica, false); k != domain.ChangeNone || len(r) != 0 {
		t.Errorf("tool generica moveu-se: kind=%v razoes=%v", k, r)
	}
}
