package pdp

import (
	"context"
	"testing"

	"github.com/aos-ref/control-plane/governance/autonomy"
	rm "github.com/aos-ref/kernel/reference-monitor"
)

// ---------------------------------------------------------------------------
// UMA ESCALADA TEM DE DEIXAR O «PORQUÊ» ESTRUTURADO NO TRILHO.
//
// Observado em produção a 2026-08-19, ao ler os selos da cerimónia four-eyes lado a lado:
//
//	allow     -> Obligations:[{"Type":"autonomy","Params":{"level":"L4","oversight":"run",...}}]
//	escalate  -> Obligations: null   +  Reason:"autonomia L0 x gray -> suggest (gate humano)"
//
// A mesma informação existia nos dois casos — num, legível por máquina; no outro, só em TEXTO
// LIVRE. Um auditor que percorra obrigações vê as autorizações e NÃO vê as escaladas.
//
// A perda era em DOIS sítios, e é por isso que estes testes verificam a cadeia inteira e não uma
// função:
//
//  1. `applyAutonomy` anexa a obrigação ANTES de rebaixar o efeito — portanto a decisão SAI de lá
//     com ela. O adaptador RM↔PDP é que a deitava fora no ramo `Escalate`;
//  2. e mesmo propagada, `Monitor.fail` construía o registo de mediação SEM obrigações.
//
// Corrigir só um dos dois deixaria a cadeia a perder na mesma. É a razão de a verificação ser
// ponta-a-ponta.
// ---------------------------------------------------------------------------

// oraculoFixo devolve sempre o mesmo nível — o suficiente para forçar a escalada.
type oraculoFixo struct{ nivel autonomy.Level }

func (o oraculoFixo) LevelFor(string, string) autonomy.Level { return o.nivel }

// TestEscaladaLevaAObrigacaoDeAutonomia percorre PDP → adaptador e exige que a obrigação
// sobreviva ao rebaixamento para `Escalate`.
func TestEscaladaLevaAObrigacaoDeAutonomia(t *testing.T) {
	dec := decisaoDeAutonomia(t, autonomy.L0) // L0 ⇒ tudo escala

	if dec.Decision != rm.HookEscalate {
		t.Fatalf("preparacao: queria HookEscalate, veio %v (%s)", dec.Decision, dec.Reason)
	}
	ob := obrigacaoDeAutonomia(dec.Obligations)
	if ob == nil {
		t.Fatalf("a ESCALADA nao levou a obrigacao `autonomy` — o selo sai com Obligations:null e o "+
			"porque fica so em texto livre: %q", dec.Reason)
	}
	// E leva a INFORMAÇÃO, não um invólucro vazio: sem estes campos a obrigação existiria e não
	// diria nada, que é uma maneira mais discreta de ter o mesmo defeito.
	for _, chave := range []string{"level", "domain", "oversight", "risk_class"} {
		if ob.Params[chave] == "" {
			t.Errorf("a obrigacao da escalada nao traz %q: %+v", chave, ob.Params)
		}
	}
	if ob.Params["level"] != "L0" {
		t.Errorf("nivel na obrigacao = %q, quero L0", ob.Params["level"])
	}
}

// TestAutorizacaoContinuaALevarAObrigacao é o CONTROLO do teste acima.
//
// Sem ele, uma "correcção" que partisse o ramo de permit passaria despercebida — e esse ramo é o
// único que já funcionava.
func TestAutorizacaoContinuaALevarAObrigacao(t *testing.T) {
	dec := decisaoDeAutonomia(t, autonomy.L5) // L5 ⇒ corre

	if dec.Decision != rm.HookAllow {
		t.Fatalf("com L5 queria HookAllow, veio %v (%s)", dec.Decision, dec.Reason)
	}
	if obrigacaoDeAutonomia(dec.Obligations) == nil {
		t.Error("o ramo de PERMIT deixou de levar a obrigacao — a correccao partiu o que ja funcionava")
	}
}

// TestNegacaoNaoLevaObrigacoes fixa a assimetria que ficou DELIBERADA, para não ser lida como
// esquecimento pelo próximo leitor.
//
// `applyAutonomy` só corre sobre uma base permit, pelo que numa negação não existe obrigação de
// autonomia. Registar as da BASE (redacção, ttl) sobre uma acção que NÃO aconteceu sugeriria que
// algo lhes foi aplicado — e o trilho passaria a afirmar um facto falso sobre um efeito que nunca
// existiu.
func TestNegacaoNaoLevaObrigacoes(t *testing.T) {
	res := paraRM(Decision{
		Effect: Deny,
		Reason: "negado pela politica",
		Obligations: []Obligation{
			{Type: "redact", Params: map[string]string{"campo": "email"}},
		},
	})
	if res.Decision != rm.HookDeny {
		t.Fatalf("queria HookDeny, veio %v", res.Decision)
	}
	if len(res.Obligations) != 0 {
		t.Errorf("a NEGACAO levou %d obrigacao(oes) — o registo sugeriria que uma redaccao foi "+
			"aplicada a um efeito que nunca aconteceu: %+v", len(res.Obligations), res.Obligations)
	}
}

// decisaoDeAutonomia corre o overlay de autonomia sobre um permit de base e devolve o resultado
// JÁ CONVERTIDO para o modelo do RM — que é a fronteira onde a obrigação se perdia.
func decisaoDeAutonomia(t *testing.T, nivel autonomy.Level) rm.HookResult {
	t.Helper()
	p := &PDP{autonomyOracle: oraculoFixo{nivel: nivel}}
	in := Input{
		Capability: "cap:fs.read",
		Resource:   Resource{Type: "file", Value: "doc://notes", Region: "eu"},
	}
	in.Principal.ID = "agt-1"
	in.Context.RiskClass = "gray"
	base := Decision{Effect: Permit, Reason: "permitido pela politica", PolicyVersion: "1.0.0"}
	return paraRM(p.applyAutonomy(context.Background(), in, base))
}

func obrigacaoDeAutonomia(obs []rm.Obligation) *rm.Obligation {
	for i := range obs {
		if obs[i].Type == rm.ObligationAutonomy {
			return &obs[i]
		}
	}
	return nil
}
