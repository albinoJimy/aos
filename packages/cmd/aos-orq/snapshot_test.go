package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aos-ref/control-plane/orchestrator/plan"
	"github.com/aos-ref/kernel/reference-monitor/risk"
)

// snapshot_test.go — O CARREGADOR DO SNAPSHOT PINADO É FAIL-CLOSED.
//
// # Porque este ficheiro existe
//
// O snapshot é a fonte do ORÁCULO DE EFEITO (DEF-273): é dele que sai a decisão de
// que tools um verificador pode reter. Um carregador permissivo aqui não produz um
// erro — produz uma capability silenciosamente mal classificada e, com ela, um clamp
// errado. E o erro é assimétrico: classificar «de efeito» uma tool inócua deixa o
// verificador sem autoridade (inerte, e ninguém percebe porquê); classificar
// «read-only» uma tool de egress dá autoridade de efeito a quem, por desenho, não a
// pode ter.
//
// Os comentários de `snapshot.go` afirmam três propriedades. Este ficheiro exerce-as,
// em vez de as deixar como afirmação.

func escreverTmp(t *testing.T, conteudo string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "snap.json")
	if err := os.WriteFile(p, []byte(conteudo), 0o600); err != nil {
		t.Fatalf("escrita: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// TESTE — o caminho feliz: os eixos por NOME chegam aos enums certos.
//
// Não-vacuidade de tudo o resto: se a tradução não funcionasse, os testes de recusa
// abaixo passariam por acidente.
// ---------------------------------------------------------------------------

func TestSnapshot_EixosPorNomeTraduzem(t *testing.T) {
	p := escreverTmp(t, `{
  "hash": "sha256:s",
  "tools": [
    {"name":"fs.read","version":"1.0.0","digest":"sha256:a","admissible":true,
     "sensitivity":"public","egress":"none","reversibility":"reversible"},
    {"name":"db.delete","version":"1.0.0","digest":"sha256:b","admissible":true,
     "sensitivity":"sensitive","egress":"internal","reversibility":"irreversible"}
  ]
}`)
	snap, err := carregarSnapshot(p)
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	if snap.Hash != "sha256:s" || len(snap.Tools) != 2 {
		t.Fatalf("snapshot = hash %q com %d tools, quer sha256:s com 2", snap.Hash, len(snap.Tools))
	}
	a, b := snap.Tools[0], snap.Tools[1]
	if a.Sensitivity != risk.SensitivityPublic || a.Egress != risk.EgressNone || a.Reversibility != risk.Reversible {
		t.Fatalf("eixos de fs.read = (%v,%v,%v), quer (public,none,reversible)", a.Sensitivity, a.Egress, a.Reversibility)
	}
	if b.Sensitivity != risk.SensitivitySensitive || b.Egress != risk.EgressInternal || b.Reversibility != risk.Irreversible {
		t.Fatalf("eixos de db.delete = (%v,%v,%v), quer (sensitive,internal,irreversible)", b.Sensitivity, b.Egress, b.Reversibility)
	}

	// A CONSEQUÊNCIA: o oráculo derivado classifica-as como o `IsEffectTool` manda.
	// É isto que liga este carregador ao clamp do verificador.
	oraculo := snap.EffectOracle()
	if oraculo(toolRef("fs.read", "1.0.0", "sha256:a")) {
		t.Error("fs.read (local, reversível) classificada como DE EFEITO — o verificador perderia autoridade legítima")
	}
	if !oraculo(toolRef("db.delete", "1.0.0", "sha256:b")) {
		t.Error("db.delete (irreversível) classificada como SEM efeito — um verificador reteria autoridade de efeito")
	}
}

// ---------------------------------------------------------------------------
// TESTE — um eixo com NOME DESCONHECIDO é ERRO, não um default.
//
// É a propriedade que `snapshot.go` argumenta e a razão de os eixos entrarem por nome
// e não pelos inteiros do enum: o valor-zero dos três eixos é DESCONHECIDO, que é
// fail-closed — logo «sensível/externo/irreversível». Aceitar um nome inválido como
// zero daria uma capability tratada como perigosa sem que ninguém soubesse porquê.
// ---------------------------------------------------------------------------

func TestSnapshot_EixoDesconhecidoERecusado(t *testing.T) {
	casos := map[string]string{
		"sensitivity":   `"sensitivity":"publico"`,
		"egress":        `"egress":"nenhum"`,
		"reversibility": `"reversibility":"reversivel"`,
	}
	for eixo, campo := range casos {
		t.Run(eixo, func(t *testing.T) {
			p := escreverTmp(t, `{"hash":"h","tools":[{"name":"t","version":"1","digest":"d","admissible":true,`+campo+`}]}`)
			_, err := carregarSnapshot(p)
			if err == nil {
				t.Fatalf("eixo %s com nome inválido foi ACEITE — resolveria para o valor fail-closed em silêncio", eixo)
			}
			if !strings.Contains(err.Error(), eixo) {
				t.Fatalf("o erro não nomeia o eixo em falta (%s): %v", eixo, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TESTE — um campo JSON desconhecido é ERRO.
//
// Um eixo escrito com o nome errado (`egres`, `reversibility_`) seria IGNORADO pelo
// decoder permissivo e a capability resolveria fail-closed — «perigosa» — sem sinal
// nenhum. É a mesma classe de falha do teste anterior, por outra porta.
// ---------------------------------------------------------------------------

func TestSnapshot_CampoDesconhecidoERecusado(t *testing.T) {
	p := escreverTmp(t, `{"hash":"h","tools":[{"name":"t","version":"1","digest":"d","admissible":true,"egres":"none"}]}`)
	_, err := carregarSnapshot(p)
	if err == nil {
		t.Fatal("campo JSON desconhecido (`egres`) foi ACEITE — o eixo real ficaria por preencher e a capability resolveria como perigosa, em silêncio")
	}
	if !strings.Contains(err.Error(), "egres") {
		t.Fatalf("o erro não nomeia o campo desconhecido: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TESTE — a AUSÊNCIA de um eixo resolve para o valor fail-closed, deliberadamente.
//
// É o único default admitido, e a direcção é a segura: uma capability por classificar
// conta como perigosa. O teste existe para que a diferença entre «ausente» (default
// declarado) e «inválido» (erro) seja uma propriedade e não uma impressão.
// ---------------------------------------------------------------------------

func TestSnapshot_EixoAusenteEFailClosed(t *testing.T) {
	p := escreverTmp(t, `{"hash":"h","tools":[{"name":"t","version":"1","digest":"d","admissible":true}]}`)
	snap, err := carregarSnapshot(p)
	if err != nil {
		t.Fatalf("carregarSnapshot: %v", err)
	}
	c := snap.Tools[0]
	if c.Sensitivity != risk.SensitivityUnknown || c.Egress != risk.EgressUnknown || c.Reversibility != risk.ReversibilityUnknown {
		t.Fatalf("eixos ausentes = (%v,%v,%v), quer os três UNKNOWN (fail-closed)", c.Sensitivity, c.Egress, c.Reversibility)
	}
	// E a consequência: uma capability por classificar é tratada como DE EFEITO.
	if !snap.EffectOracle()(toolRef("t", "1", "d")) {
		t.Error("uma capability SEM eixos declarados foi classificada como SEM efeito — o fail-closed inverteu-se")
	}
}

// ---------------------------------------------------------------------------
// TESTE — um snapshot SEM capabilities é recusado.
//
// Um snapshot em que nada resolve faz o oráculo devolver «efeito» para tudo: é o
// `DefaultEffectOracle` por outro nome, que é precisamente o que esta via existe para
// eliminar. Recusar é a única resposta que não reintroduz a dívida.
// ---------------------------------------------------------------------------

func TestSnapshot_VazioERecusado(t *testing.T) {
	for nome, conteudo := range map[string]string{
		"tools vazio":   `{"hash":"h","tools":[]}`,
		"tools ausente": `{"hash":"h"}`,
	} {
		t.Run(nome, func(t *testing.T) {
			if _, err := carregarSnapshot(escreverTmp(t, conteudo)); err == nil {
				t.Fatal("snapshot sem capabilities foi ACEITE — o oráculo devolveria «efeito» para tudo, que é o default que ele substitui")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TESTE — ficheiro inexistente e JSON malformado são erros, não snapshots vazios.
// ---------------------------------------------------------------------------

func TestSnapshot_FicheiroInvalido(t *testing.T) {
	if _, err := carregarSnapshot(filepath.Join(t.TempDir(), "nao-existe.json")); err == nil {
		t.Fatal("ficheiro inexistente não deu erro")
	}
	if _, err := carregarSnapshot(escreverTmp(t, `{"hash":`)); err == nil {
		t.Fatal("JSON malformado não deu erro")
	}
}

// toolRef constrói a referência pinada que o oráculo recebe. Local ao teste para não
// arrastar o import do `plan` para o resto do ficheiro.
func toolRef(name, version, digest string) plan.ToolRef {
	return plan.ToolRef{Name: name, Version: version, Digest: digest}
}
