package identity

// aos427_tecto_de_ttl_test.go — O TECTO DE TTL (AOS-427, decisão 4).
//
// Antes deste ticket, `ClassPolicy.TTL` era aceite tal-qual: zero, negativo, um dia, um ano.
// Nenhum teste da árvore cobria nenhum desses casos — o que quer dizer que a ausência de tecto
// não era uma escolha testada, era um buraco que ninguém tinha olhado.

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"
)

// classesComTTL monta uma política mínima com o TTL dado.
func classesComTTL(ttl time.Duration) map[string]ClassPolicy {
	return map[string]ClassPolicy{"researcher": {TTL: ttl, Scope: []string{"cap:doc.read"}}}
}

func chaveDeTeste(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	return priv
}

// TestAOS427TTLAcimaDoTectoERecusado é o núcleo da decisão (4).
func TestAOS427TTLAcimaDoTectoERecusado(t *testing.T) {
	priv := chaveDeTeste(t)

	for _, c := range []struct {
		nome string
		ttl  time.Duration
	}{
		{"um minuto acima do tecto", TTLMaximo + time.Minute},
		{"um turno de trabalho", 8 * time.Hour},
		{"um dia", 24 * time.Hour},
		{"um ano", 365 * 24 * time.Hour},
	} {
		t.Run(c.nome, func(t *testing.T) {
			_, err := NewIssuer("iss:teste", priv, classesComTTL(c.ttl))
			if !errors.Is(err, ErrTTLForaDeGama) {
				t.Fatalf("TTL de %v tinha de ser recusado com ErrTTLForaDeGama, veio %v\n"+
					"sem o tecto, uma cunhagem AUTOMATICA emite isto sem ninguem ver", c.ttl, err)
			}
			// A MENSAGEM TEM DE SER ACCIONAVEL: quem a lê tem de saber qual é o tecto e qual
			// foi o valor pedido, senão vai procurar o limite no código.
			if !strings.Contains(err.Error(), TTLMaximo.String()) {
				t.Errorf("a recusa nao nomeia o tecto (%v): %v", TTLMaximo, err)
			}
			if !strings.Contains(err.Error(), "researcher") {
				t.Errorf("a recusa nao nomeia a CLASSE que o pediu: %v", err)
			}
		})
	}
}

// TestAOS427TTLInutilizavelERecusado — zero e negativo nascem expirados.
//
// Nunca foram recusados e são sempre defeito. Um token com `exp == iat` é aceite pelo emissor,
// assinado, entregue, e morre na primeira verificação — com um `ErrTokenExpired` que manda quem
// diagnostica procurar um problema de relógio.
func TestAOS427TTLInutilizavelERecusado(t *testing.T) {
	priv := chaveDeTeste(t)
	for _, ttl := range []time.Duration{0, -time.Second, -time.Hour} {
		if _, err := NewIssuer("iss:teste", priv, classesComTTL(ttl)); !errors.Is(err, ErrTTLForaDeGama) {
			t.Errorf("TTL de %v tinha de ser recusado, veio %v — um token que nasce expirado "+
				"e sempre defeito, e falha longe de quem o configurou", ttl, err)
		}
	}
}

// TestAOS427OsValoresLEGITIMOSDaArvoreContinuamAPassar é o controlo que impede o tecto de ser
// apertado até partir produção.
//
// Estes quatro valores não são inventados: são os que os chamadores reais usam. Se alguém baixar
// o `TTLMaximo` abaixo de qualquer um deles, este teste diz exactamente qual parte e onde vive.
func TestAOS427OsValoresLEGITIMOSDaArvoreContinuamAPassar(t *testing.T) {
	priv := chaveDeTeste(t)
	for _, c := range []struct {
		ttl  time.Duration
		onde string
	}{
		{15 * time.Minute, "cmd/aos/main.go — a classe `researcher` do no"},
		{30 * time.Minute, "cmd/aos-orq/planner_wiring.go — `tokenTTL`"},
		{15 * time.Minute, "cmd/aos-issuer — o default do flag --ttl"},
		{45 * time.Minute, "deploy/server/get-id-token.ps1 — a RECEITA DE PRODUCAO"},
	} {
		if _, err := NewIssuer("iss:teste", priv, classesComTTL(c.ttl)); err != nil {
			t.Errorf("o TTL de %v usado por %s passou a ser RECUSADO: %v\n"+
				"o tecto nao pode partir o que ja existe — ou se baixa o chamador primeiro, "+
				"ou nao se baixa o tecto", c.ttl, c.onde, err)
		}
	}
}

// TestAOS427OTectoValeParaASignerToo: a via de custódia externa é a que o AOS-427 vai usar, e
// seria a pior de todas para ter o tecto em falta.
//
// As duas vias são o mesmo caminho (`NewIssuer` delega em `NewIssuerWithSigner`), e este teste
// existe para que continuem a ser: se alguém as separar, o tecto tem de continuar nas duas.
func TestAOS427OTectoValeParaASignerToo(t *testing.T) {
	priv := chaveDeTeste(t)
	if _, err := NewIssuerWithSigner("iss:teste", priv, classesComTTL(24*time.Hour)); !errors.Is(err, ErrTTLForaDeGama) {
		t.Fatalf("a via crypto.Signer tinha de recusar o mesmo TTL que a via da chave crua, veio %v\n"+
			"e e ESTA a via que a cunhagem automatica vai usar (Vault Transit)", err)
	}
}

// TestAOS427UmaClasseMaEstragaOEmissorINTEIRO.
//
// Um mapa com quatro classes boas e uma má não produz um emissor «com quatro classes». Produz
// erro. Aceitar as boas e calar a má deixaria o operador com um emissor que funciona até alguém
// pedir a classe que falta — e aí falharia com `ErrUnknownClass`, que aponta para o sítio errado.
func TestAOS427UmaClasseMaEstragaOEmissorINTEIRO(t *testing.T) {
	priv := chaveDeTeste(t)
	classes := map[string]ClassPolicy{
		"boa-1": {TTL: 15 * time.Minute, Scope: []string{"cap:a"}},
		"boa-2": {TTL: 30 * time.Minute, Scope: []string{"cap:b"}},
		"ma":    {TTL: 48 * time.Hour, Scope: []string{"cap:c"}},
	}
	iss, err := NewIssuer("iss:teste", priv, classes)
	if !errors.Is(err, ErrTTLForaDeGama) {
		t.Fatalf("uma classe fora da gama tinha de recusar o emissor inteiro, veio %v", err)
	}
	if iss != nil {
		t.Error("com erro, o emissor tem de vir nil — um emissor meio-configurado e pior que nenhum")
	}
}

// TestAOS427OTectoNaoEeMaiorQueUmaHora é um guard sobre a PRÓPRIA constante.
//
// O valor foi escolhido por medição: fica acima dos 45m da receita de produção e abaixo de
// qualquer coisa que se pareça com um turno. Se alguém o subir, que o faça a olhar para isto —
// porque a defesa que este ticket acrescenta é exactamente o tamanho deste número.
func TestAOS427OTectoNaoEeMaiorQueUmaHora(t *testing.T) {
	if TTLMaximo > time.Hour {
		t.Errorf("TTLMaximo subiu para %v.\n"+
			"O ADR-006 invariante 2 pede «TTL curto», e o atrito humano que limitava o raio de "+
			"accao de uma credencial desapareceu com a cunhagem automatica (AOS-427). Se este "+
			"valor tem mesmo de subir, a decisao e de seguranca e pertence a um ADR.", TTLMaximo)
	}
	if TTLMaximo < 45*time.Minute {
		t.Errorf("TTLMaximo desceu para %v, abaixo dos 45m da receita de producao — "+
			"parte `deploy/server/get-id-token.ps1`", TTLMaximo)
	}
}
