package bancoensaio

// AOS-519 — O BANCO QUALIFICA O PERFIL COM O NOME PEDIDO DE PRODUÇÃO.
//
// O digest de um perfil de rota inclui o nome pedido (`requested`). Enquanto o banco exigia
// `rota-de-ensaio`, o `digest_do_perfil` de um relatório de qualificação era o de um perfil que o
// nó de produção nunca carrega: o veredicto e o perfil assinado não se podiam ligar. Desde o
// AOS-519 o nome pedido do perfil candidato é o alias que o nó de produção pede, e o banco usa-o
// em todo o lado onde usava o seu: no pedido ao modelo, na allowlist do nó de ensaio e na rota
// efémera do proxy.

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	modelgateway "github.com/aos-ref/platform/model-gateway"
)

// aos519PerfilDeProducao é um perfil com um nome pedido de produção — um alias que a allowlist
// assinada em vigor autoriza — e um modelo esperado com `/` e com `.`.
const aos519PerfilDeProducao = `{"requested":"gpt-4o","expected_model":"openai/modelo-falso-do-banco","wire_class":"openai-chat-completions","capabilities":["tools"]}`

// O relatório de uma qualificação feita com o nome de produção cita o MESMO digest que
// [modelgateway.RouteProfile.Digest] dá ao perfil que o nó de produção lê do mesmo documento — e
// o nome viajou de facto no pedido.
func TestAOS519_Banco_ORelatorioCitaODigestDoPerfilDeProducao(t *testing.T) {
	falso := NovoProviderFalso([]Comportamento{ComportamentoCumpre})
	falso.DeclararModeloServido("openai/modelo-falso-do-banco")
	falso.GuardarCorpos()
	amb := aos518Ambiente(t, aos519PerfilDeProducao, falso, nil, TectoDePedidosDaQualificacao)
	r := amb.correr(t, context.Background())
	q := aos518Veredicto(t, r)

	// O que o nó de produção faz ao mesmo documento: a leitura fechada do gateway.
	doNo, err := modelgateway.ParseRouteProfile([]byte(aos519PerfilDeProducao))
	if err != nil {
		t.Fatal(err)
	}
	if doNo.Requested != "gpt-4o" {
		t.Fatalf("nome pedido = %q", doNo.Requested)
	}
	if q.Rota.DigestDoPerfil != doNo.Digest() || r.Digests.Perfil != doNo.Digest() {
		t.Fatalf("o relatorio cita %q (rota_qualificada) e %q (digests); o perfil que o no carrega tem %q",
			q.Rota.DigestDoPerfil, r.Digests.Perfil, doNo.Digest())
	}
	// Controlo: o mesmo perfil com o nome de sempre do banco tem OUTRO digest — é por isso que o
	// nome tinha de mudar, e não o digest.
	deSempre, err := modelgateway.ParseRouteProfile([]byte(strings.Replace(aos519PerfilDeProducao, `"gpt-4o"`, `"`+AliasDaRota+`"`, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if deSempre.Digest() == doNo.Digest() {
		t.Fatal("o digest do perfil nao depende do nome pedido: o teste nao distingue nada")
	}
	if q.Veredicto != VeredictoQualificado {
		t.Fatalf("veredicto %s %v; a corrida com o nome de producao tinha de correr como a de sempre", q.Veredicto, q.Razoes)
	}
	// O nome viajou: todos os pedidos levam o alias de produção, e nenhum o de sempre.
	corpos := falso.Corpos()
	if len(corpos) == 0 {
		t.Fatal("nenhum pedido chegou ao provider falso")
	}
	for i, corpo := range corpos {
		if !bytes.Contains(corpo, []byte(`"model":"gpt-4o"`)) || bytes.Contains(corpo, []byte(AliasDaRota)) {
			t.Fatalf("pedido %d nao pede o alias de producao: %.200s", i, corpo)
		}
	}
}

// O nome pedido é validado: sem ponto (entra num nome de stream), sem `/` nem `:` (entra numa
// linha da configuração do proxy), e com tecto.
func TestAOS519_Banco_ONomePedidoEValidado(t *testing.T) {
	for _, bom := range []string{"gpt-4o", "gpt-4o-mini", "k3", "kimi-for-coding", AliasDaRota, "A_b-9"} {
		if !NomeDaRotaAceite(bom) {
			t.Errorf("%q tinha de servir de alias", bom)
		}
		doc := strings.Replace(aos519PerfilDeProducao, `"gpt-4o"`, `"`+bom+`"`, 1)
		if _, err := LerPerfilCandidato([]byte(doc)); err != nil {
			t.Errorf("%q: %v", bom, err)
		}
	}
	for _, mau := range []string{"gpt-4.1", "anthropic/claude", "a:b", "a b", strings.Repeat("a", 65)} {
		if NomeDaRotaAceite(mau) {
			t.Errorf("%q nao pode servir de alias", mau)
		}
		doc := strings.Replace(aos519PerfilDeProducao, `"gpt-4o"`, `"`+mau+`"`, 1)
		if _, err := LerPerfilCandidato([]byte(doc)); err == nil {
			t.Errorf("%q: um perfil com este nome pedido tinha de ser recusado", mau)
		}
	}
	if NomeDaRotaAceite("") {
		t.Error("o nome vazio nao pode servir de alias")
	}
	// O alias chega à configuração da rota efémera do proxy, e um alias mau não chega.
	cfg, err := configDoProxyCom("gpt-4o", "openai", "modelo-x", true)
	if err != nil || !strings.Contains(cfg, "model_name: \"gpt-4o\"\n") || strings.Contains(cfg, AliasDaRota) {
		t.Fatalf("configuracao do proxy com o alias de producao: %v\n%s", err, cfg)
	}
	if _, err := configDoProxyCom("gpt-4.1", "openai", "modelo-x", true); !errors.Is(err, ErrProxy) {
		t.Fatalf("um alias com ponto tinha de ser recusado na configuracao do proxy; veio %v", err)
	}
	// Os nomes que o YAML não lê como texto sem aspas: vão entre aspas, e só assim são o nome.
	for _, traicoeiro := range []string{"null", "true", "false", "123", "-", "0x10", "1e3", "yes", "no"} {
		cfg, err := configDoProxyCom(traicoeiro, "openai", "modelo-x", true)
		if err != nil || !strings.Contains(cfg, "model_name: \""+traicoeiro+"\"\n") || strings.Contains(cfg, "model_name: "+traicoeiro+"\n") {
			t.Errorf("alias %q: tinha de ir entre aspas na configuracao do proxy (%v)\n%s", traicoeiro, err, cfg)
		}
	}
	// Sem alias dado, a configuração é a de sempre.
	deSempre, _ := configDoProxy("openai", "modelo-x", true)
	comOmissao, _ := configDoProxyCom(AliasDaRota, "openai", "modelo-x", true)
	if deSempre != comOmissao || !strings.Contains(deSempre, "model_name: \""+AliasDaRota+"\"\n") {
		t.Fatal("a configuracao por omissao mudou")
	}
}
