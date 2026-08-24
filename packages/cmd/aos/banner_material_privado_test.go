package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// O BANNER SO NOMEIA MATERIAL QUE O PROCESSO DETEM.
//
// Achado A da verificacao de funcionamento de 2026-08-23. `credentialBrokerPostureBanner`
// era a UNICA das dezasseis funcoes de banner do binario SEM parametro de estado: uma
// constante que afirmava, no presente do indicativo, que o processo detinha tres chaves
// privadas — e que essa lista era a superficie "COMPLETA" de material privado.
//
// Em producao o processo detem ZERO das tres. E a primeira, a chave de assinatura do issuer,
// e ESTRUTURALMENTE impossivel no modo em que producao corre: `bootstrap.go` recusa o
// arranque com ErrConflictingIssuerKey se ela estiver definida em modo endurecido. A linha
// descrevia um estado que, se existisse, teria impedido o no de arrancar — quatro linhas
// depois de outra linha do MESMO banner dizer "NENHUMA chave de assinatura entra no runtime".
func TestBanner_NaoNomeiaMaterialQueNaoDetem(t *testing.T) {
	casos := []struct {
		nome        string
		m           materialPrivadoDoNo
		querContem  []string
		naoQuerTerm []string
	}{
		{
			nome: "endurecido sem chaves (a POSTURA DE PRODUCAO)",
			m:    materialPrivadoDoNo{Endurecido: true},
			querContem: []string{
				"NENHUM material privado carregado",
				"ESTRUTURALMENTE IMPOSSIVEL",
				"ErrConflictingIssuerKey",
			},
			// O NOME DA VARIAVEL NAO PODE APARECER COMO POSSE. Era esta a mentira.
			naoQuerTerm: []string{"AOS_ISSUER_KEY_PATH (chave ed25519 de ASSINATURA"},
		},
		{
			nome: "referencia COM a chave do issuer (onde a frase antiga era verdadeira)",
			m:    materialPrivadoDoNo{IssuerKey: true},
			querContem: []string{
				"material privado CARREGADO",
				"AOS_ISSUER_KEY_PATH",
				"ESTA no processo",
			},
			naoQuerTerm: []string{"NENHUM material privado", "ESTRUTURALMENTE IMPOSSIVEL"},
		},
		{
			nome: "referencia SEM chave nenhuma",
			m:    materialPrivadoDoNo{},
			querContem: []string{
				"NENHUM material privado carregado",
				"NAO esta definida",
			},
			naoQuerTerm: []string{"ESTRUTURALMENTE IMPOSSIVEL"},
		},
		{
			nome: "endurecido com mTLS do colector composto",
			m:    materialPrivadoDoNo{Endurecido: true, OTLPClientKey: true, OTLPBearer: true},
			querContem: []string{
				"material privado CARREGADO",
				"AOS_OTLP_CLIENT_KEY_PATH",
				"Bearer do colector CARREGADO",
			},
			naoQuerTerm: []string{"NENHUM material privado", "AOS_ISSUER_KEY_PATH (chave ed25519"},
		},
	}

	for _, tc := range casos {
		t.Run(tc.nome, func(t *testing.T) {
			texto := strings.Join(credentialBrokerPostureBanner(tc.m), "\n")
			for _, q := range tc.querContem {
				if !strings.Contains(texto, q) {
					t.Fatalf("o banner nao diz %q\n---\n%s", q, texto)
				}
			}
			for _, n := range tc.naoQuerTerm {
				if strings.Contains(texto, n) {
					t.Fatalf("o banner AFIRMA %q num estado em que e falso\n---\n%s", n, texto)
				}
			}
		})
	}
}

// CONTROLO ANTI-VACUIDADE: OS ESTADOS TEM DE PRODUZIR TEXTOS DIFERENTES.
//
// Sem esta asercao, uma funcao que voltasse a ser CONSTANTE — que era o defeito — passaria
// todos os casos acima cujo `querContem` calhasse estar no texto fixo. E o parametro de
// estado seria decorativo, que e uma forma pior do mesmo problema.
func TestBanner_EstadosDiferentesProduzemTextosDiferentes(t *testing.T) {
	estados := map[string]materialPrivadoDoNo{
		"endurecido vazio": {Endurecido: true},
		"referencia vazio": {},
		"com issuer":       {IssuerKey: true},
		"com otlp":         {Endurecido: true, OTLPClientKey: true},
	}
	vistos := map[string]string{}
	for nome, m := range estados {
		texto := strings.Join(credentialBrokerPostureBanner(m), "\n")
		if antes, repetido := vistos[texto]; repetido {
			t.Fatalf("os estados %q e %q produzem o MESMO texto — o parametro de estado nao esta a ser lido", antes, nome)
		}
		vistos[texto] = nome
	}
}

// A LINHA DO BANNER E A GUARDA DO ARRANQUE NAO PODEM DIVERGIR.
//
// E esta a asercao que impede o achado de voltar. O defeito nasceu de duas afirmacoes sobre
// o MESMO facto viverem em sitios diferentes e uma delas nao ter parametro. Aqui amarra-se o
// texto ao predicado: se o banner diz "estruturalmente impossivel", entao o modo tem de ser
// o endurecido — que e exactamente o modo em que bootstrap.go devolve ErrConflictingIssuerKey.
func TestBanner_ImpossibilidadeSoNoModoQueARecusa(t *testing.T) {
	for _, m := range []materialPrivadoDoNo{
		{Endurecido: true}, {Endurecido: true, IssuerKey: true},
		{}, {IssuerKey: true}, {OTLPClientKey: true},
	} {
		texto := strings.Join(credentialBrokerPostureBanner(m), "\n")
		diz := strings.Contains(texto, "ESTRUTURALMENTE IMPOSSIVEL")
		if diz != m.Endurecido {
			t.Fatalf("banner diz impossivel=%v mas Endurecido=%v — a linha e a guarda de ErrConflictingIssuerKey divergiram\n---\n%s", diz, m.Endurecido, texto)
		}
	}
}

// A CABLAGEM: O ARRANQUE REAL PASSA O ESTADO REAL.
//
// Sem este teste a correccao ficava a meio, e SOUBE-SE POR MUTACAO: pôr o bootstrap a passar
// `Endurecido:false, IssuerKey:false` fixos NAO fazia cair nada. O teste de arranque real que
// ja existia so verifica a linha ESTRUTURAL do broker, que nao depende do estado, e o helper
// dele levanta o no em modo de REFERENCIA — onde `Endurecido:false` produz exactamente o
// mesmo texto que o valor certo.
//
// Distinguir exige arrancar ENDURECIDO, que e a postura de producao. E a decima quinta vez
// que o padrao de cablagem aparece neste repositorio.
func TestBanner_ArranqueEndurecidoDeclaraAImpossibilidade(t *testing.T) {
	banner := runEndurecido(t)

	if !strings.Contains(banner, "ESTRUTURALMENTE IMPOSSIVEL") {
		t.Fatalf("o banner do arranque ENDURECIDO nao declara que a chave do issuer e impossivel neste modo — o bootstrap nao esta a passar o estado real\n---\n%s", recorte(banner))
	}
	if !strings.Contains(banner, "NENHUM material privado carregado") {
		t.Fatalf("o no endurecido nao carrega material privado nenhum e o banner devia diz-lo\n---\n%s", recorte(banner))
	}
	// A MENTIRA ORIGINAL, explicitamente: o nome da chave do issuer NAO pode aparecer como
	// posse num modo que a proibe.
	if strings.Contains(banner, "AOS_ISSUER_KEY_PATH (chave ed25519 de ASSINATURA") {
		t.Fatalf("o banner AFIRMA deter a chave do issuer num modo em que te-la ABORTA o arranque\n---\n%s", recorte(banner))
	}
	// CONTROLO: a outra linha do MESMO banner, quatro acima, ja dizia a verdade. As duas tem
	// agora de concordar — foi a contradicao entre elas que revelou o achado.
	if !strings.Contains(banner, "NENHUMA chave de assinatura entra no runtime do no") {
		t.Fatalf("CONTROLO: o arranque nao e o endurecido que este teste precisa\n---\n%s", recorte(banner))
	}
}

// recorte devolve so as linhas do broker/identidade, para a falha ser legivel.
func recorte(banner string) string {
	var out []string
	for _, l := range strings.Split(banner, "\n") {
		if strings.Contains(l, "credential broker") || strings.Contains(l, "chave de assinatura") || strings.Contains(l, "IDENTIDADE") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// runEndurecido arranca o no em identidade ENDURECIDA (trust-anchor-only) e devolve o banner.
// Segue o molde de [runWithoutTouchingBoardRegions] — limpa a superficie inteira e repoe o
// minimo — acrescentando a UNICA coisa que muda o modo: a pubkey do issuer.
func runEndurecido(t *testing.T) string {
	t.Helper()
	for name := range envVarsReadBySources(t, envSourceRoots) {
		t.Setenv(name, "")
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gerar chave de teste: %v", err)
	}
	t.Setenv("AOS_ISSUER_ID", "iss:aos-endurecido")
	t.Setenv("AOS_ISSUER_PUBKEY", hex.EncodeToString(pub))
	// A chave PRIVADA fica por definir de proposito: com ela, o arranque ABORTA
	// (ErrConflictingIssuerKey). E precisamente esse facto que o banner passa a declarar.
	t.Setenv("AOS_ISSUER_KEY_PATH", "")

	var sb strings.Builder
	if err := run(&sb); err != nil {
		t.Fatalf("o arranque endurecido devia correr, veio: %v", err)
	}
	return sb.String()
}

// A CABLAGEM, O OUTRO LADO: QUANDO A CHAVE ESTA MESMO LA, O BANNER DIZ QUE ESTA.
//
// Tambem esta soube-se por mutacao: com so o teste endurecido, pôr `IssuerKey:false` fixo no
// bootstrap NAO fazia cair nada — num no endurecido a chave esta ausente de qualquer forma.
// Distinguir exige o modo de REFERENCIA com a chave carregada, que e o unico estado em que a
// frase antiga ("o processo detem") era verdadeira.
func TestBanner_ArranqueDeReferenciaComChaveDeclaraAPosse(t *testing.T) {
	for name := range envVarsReadBySources(t, envSourceRoots) {
		t.Setenv(name, "")
	}
	t.Setenv("AOS_ISSUER_ID", "iss:aos-referencia")
	t.Setenv("AOS_HUMANS", "operator")
	// Modo de REFERENCIA (sem pubkey) COM a chave de assinatura por ficheiro: a autoridade e
	// co-localizada e a chave privada ESTA no processo. `LoadOrCreateIssuerKey` cria-a.
	t.Setenv("AOS_ISSUER_KEY_PATH", filepath.Join(t.TempDir(), "issuer.key"))

	var sb strings.Builder
	if err := run(&sb); err != nil {
		t.Fatalf("o arranque de referencia devia correr, veio: %v", err)
	}
	banner := sb.String()

	if !strings.Contains(banner, "material privado CARREGADO") {
		t.Fatalf("a chave do issuer ESTA no processo e o banner nao o declara — o bootstrap nao esta a passar o estado real\n---\n%s", recorte(banner))
	}
	if !strings.Contains(banner, "AOS_ISSUER_KEY_PATH") {
		t.Fatalf("o banner nao nomeia a chave que o processo DETEM — e o que ha a rodar e a proteger\n---\n%s", recorte(banner))
	}
	if strings.Contains(banner, "NENHUM material privado carregado") {
		t.Fatalf("o banner diz NENHUM com a chave carregada\n---\n%s", recorte(banner))
	}
	// CONTROLO: neste modo a impossibilidade estrutural NAO se aplica — dize-la aqui seria a
	// mentira simetrica da que o achado A fechou.
	if strings.Contains(banner, "ESTRUTURALMENTE IMPOSSIVEL") {
		t.Fatalf("CONTROLO: o banner declara impossivel uma chave que ESTA carregada\n---\n%s", recorte(banner))
	}
}
