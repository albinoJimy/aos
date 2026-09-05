package vault

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/substrate/redaction"
)

// AOS-337 — O CLIENTE VAULT DO BROKER NÃO DEIXA CREDENCIAIS SAÍREM NOS ERROS.
//
// Achado na revisão adversarial do AOS-333, que fechou a fuga no helper de validação do nó e no
// banner e parou aí. Havia QUATRO ramos a ecoar o endereço — o ticket contava três e falhou o
// `NewRequest` do `Fetch`, que tem o mesmo defeito do de `Ready`:
//
//   - `NewRequest` devolve um `*url.Error` com o endereço COMO FOI ESCRITO, sem redacção
//     nenhuma: a senha ia inteira, e com ela o PATH DO SEGREDO, que diz a quem lê o log qual a
//     credencial em causa;
//   - `Do` devolve um `*url.Error` em que o `net/http` redige a senha e deixa o UTILIZADOR, que
//     num endereço de Vault é quem identifica o principal.
//
// DUAS CAMADAS, E A ORDEM IMPORTA. O controlo PRIMÁRIO é [NewKVv2]: um endereço com credenciais
// não constrói cliente nenhum, o que fecha os quatro ramos de uma vez no ponto de entrada. A
// redacção nos caminhos de erro é DEFESA EM PROFUNDIDADE — cobre um estado que o construtor já
// não deixa acontecer, e continua a valer para o path do segredo, que não é user-info e sairia
// por ali na mesma.

const aos337Senha = "s3nh4-do-broker-que-nao-pode-vazar"

// exigeSemCredenciais é o critério comum: nem a senha, nem o utilizador.
func exigeSemCredenciais(t *testing.T, msg string) {
	t.Helper()
	if strings.Contains(msg, aos337Senha) {
		t.Errorf("a mensagem contem a SENHA do Vault: %s", msg)
	}
	if strings.Contains(msg, "admin") {
		t.Errorf("a mensagem contem o utilizador, que num endereco de Vault identifica o principal: %s", msg)
	}
}

// ---------------------------------------------------------------------------------------------
// CAMADA 1 — o construtor recusa. É a forma FORTE da garantia: torna impossível construir o
// objecto sobre o qual os testes de fuga fariam sentido.
// ---------------------------------------------------------------------------------------------

func TestAOS337_ConstrutorRecusaCredenciaisNoEndereco(t *testing.T) {
	t.Parallel()
	casos := []string{
		"https://admin:" + aos337Senha + "@vault.interno:8200",
		"https://admin@vault.interno:8200",
		"http://admin:" + aos337Senha + "@127.0.0.1:8200",
	}
	for _, addr := range casos {
		c, err := NewKVv2(KVv2Config{Addr: addr, Token: "tok"})
		if err == nil {
			t.Fatalf("NewKVv2(%q) devia recusar credenciais embutidas", addr)
		}
		if c != nil {
			t.Error("um construtor que recusa nao pode devolver cliente")
		}
		if !errors.Is(err, ErrKVConfig) {
			t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
		}
		exigeSemCredenciais(t, err.Error())
	}
}

// TestAOS337_ConstrutorAceitaOsEnderecosLegitimos é o CONTROLO do construtor. Sem ele, uma
// recusa universal passaria o teste acima — e recusar tudo abortaria o arranque de todos os
// deployments correctos, que é um defeito pior do que o que se fecha.
func TestAOS337_ConstrutorAceitaOsEnderecosLegitimos(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{
		"https://vault.interno:8200",
		"http://127.0.0.1:8200",
		"https://vault.interno:8200/",
		"http://localhost:8200",
	} {
		if _, err := NewKVv2(KVv2Config{Addr: addr, Token: "tok"}); err != nil {
			t.Errorf("endereco legitimo recusado: %q -> %v", addr, err)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// CAMADA 2 — defesa em profundidade nos caminhos de erro.
//
// Constrói-se a struct DIRECTAMENTE, sem passar pelo construtor. É deliberado, e é a única forma
// de exercer estes ramos agora que o construtor recusa: um controlo que só existe no construtor
// deixa de existir no dia em que alguém compuser um KVv2 por outra via — e é exactamente essa a
// história deste ticket. O AOS-333 pôs o critério no parser do ambiente, e a fuga sobreviveu num
// caminho que não passava por lá.
// ---------------------------------------------------------------------------------------------

func clienteCru(addr string) *KVv2 {
	return &KVv2{
		addr:  strings.TrimRight(addr, "/"),
		mount: defaultKVMount,
		field: defaultKVField,
		token: "tok",
		hc:    &http.Client{Timeout: 2 * time.Second},
	}
}

var chaveDeTeste = Key{Provider: "p", Region: "eu", Capability: "cap:http.get"}

// TestAOS337_ParseFalhadoNaoEcoaOEnderecoCru cobre o ramo PIOR: o `*url.Error` do `NewRequest`
// trazia o endereço INTEIRO, sem a redacção que o `http.Client` aplica aos SEUS erros.
func TestAOS337_ParseFalhadoNaoEcoaOEnderecoCru(t *testing.T) {
	t.Parallel()
	// Porta com espaço: o que o `url.Parse` recusa (invalid port).
	c := clienteCru("https://admin:" + aos337Senha + "@vault.interno:82 00")
	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		_, err := c.Fetch(ctx, chaveDeTeste)
		if err == nil {
			t.Fatal("um endereco que nao produz pedido devia dar erro")
		}
		if !errors.Is(err, ErrKVFetch) {
			t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
		}
		exigeSemCredenciais(t, err.Error())
		// O PATH DO SEGREDO também não pode sair: diz QUAL a credencial em causa, mesmo sem
		// revelar o valor. Era o que saía junto com a senha neste ramo.
		if strings.Contains(err.Error(), "cap_http.get") || strings.Contains(err.Error(), "/data/") {
			t.Errorf("a mensagem revela o path do segredo: %v", err)
		}
	})

	t.Run("Ready", func(t *testing.T) {
		err := c.Ready(ctx)
		if err == nil {
			t.Fatal("um endereco que nao produz pedido devia dar erro")
		}
		// Este ramo devolvia err CRU, sem sentinela nenhuma.
		if !errors.Is(err, ErrKVFetch) {
			t.Errorf("o erro tem de ser ATRIBUIVEL: %v", err)
		}
		exigeSemCredenciais(t, err.Error())
	})
}

// TestAOS337_ErroDeTransporteNaoEcoaOUtilizador cobre o ramo em que o endereço É analisável: o
// pedido sai, o transporte falha, e o `*url.Error` do `net/http` traz `admin:***@`.
//
// O CONTROLO de «continua a nomear o host» vive aqui e não no teste acima, e é deliberado: no
// ramo de parse falhado o endereço, por definição, não se analisa, pelo que não há forma
// publicável para dar e a mensagem é a constante `(inválida)`. Exigir host e porta lá seria
// exigir o impossível — mas aqui é exigível, e sem isto um redactor que apagasse tudo passaria
// os testes de ausência por vacuidade.
func TestAOS337_ErroDeTransporteNaoEcoaOUtilizador(t *testing.T) {
	t.Parallel()
	c := clienteCru("http://admin:" + aos337Senha + "@127.0.0.1:1")
	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		_, err := c.Fetch(ctx, chaveDeTeste)
		if err == nil {
			t.Fatal("um vault inalcancavel devia dar erro")
		}
		exigeSemCredenciais(t, err.Error())
		if !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("a mensagem devia continuar a nomear o host e a porta: %v", err)
		}
	})

	t.Run("Ready", func(t *testing.T) {
		err := c.Ready(ctx)
		if err == nil {
			t.Fatal("um vault inalcancavel devia dar erro")
		}
		exigeSemCredenciais(t, err.Error())
		if !strings.Contains(err.Error(), "127.0.0.1:1") {
			t.Errorf("a mensagem devia continuar a nomear o host e a porta: %v", err)
		}
	})
}

// TestAOS337_ACausaSobreviveARedaccao é o segundo CONTROLO, e a versão anterior dele era um
// `t.Logf` — o revisor apagou a causa por completo e a suite passou. Um controlo que não falha
// não é um controlo.
//
// A asserção NÃO pode ser sobre o texto do sistema operativo (connection refused / recusou), que
// varia por SO e por locale — foi essa a razão pela qual era um log. A forma portável é comparar
// com o erro CRU: o redigido tem de conter tudo o que o cru trazia depois do endereço.
func TestAOS337_ACausaSobreviveARedaccao(t *testing.T) {
	t.Parallel()
	const alvo = "http://admin:" + aos337Senha + "@127.0.0.1:1/v1/sys/seal-status"

	hc := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest(http.MethodGet, alvo, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, errCru := hc.Do(req)
	if errCru == nil {
		t.Fatal("premissa do teste partida: a porta 1 devia recusar a ligacao")
	}
	var ue *url.Error
	if !errors.As(errCru, &ue) {
		t.Fatalf("premissa do teste partida: o erro cru devia ser *url.Error, e %T", errCru)
	}

	redigido := redaction.TransportError(alvo, errCru)
	exigeSemCredenciais(t, redigido.Error())
	if causa := ue.Err.Error(); !strings.Contains(redigido.Error(), causa) {
		t.Errorf("a CAUSA perdeu-se na redaccao — o diagnostico deixaria de distinguir DNS de recusa de ligacao de TLS.\n  redigido: %v\n  causa que faltava: %s", redigido, causa)
	}
	if !strings.Contains(redigido.Error(), ue.Op) {
		t.Errorf("o Op perdeu-se na redaccao: %v", redigido)
	}
}

// TestAOS337_UrlErrorAninhadoNaoEscapa fecha o achado F6 da revisão: `errors.As` apanha o
// `*url.Error` de FORA, e reimprimir o Err tal-qual deixava sair inteiro um `*url.Error`
// INTERIOR — que é o que um transporte injectado produz ao falhar contra um proxy.
//
// A via é o seam público `KVv2Config.HTTPClient`; hoje o nó passa nil, pelo que não é alcançável
// em produção. É por isso que é teste e não incidente — e é por isso que existe.
func TestAOS337_UrlErrorAninhadoNaoEscapa(t *testing.T) {
	t.Parallel()
	const senhaDoProxy = "senha-do-proxy-de-terceiros"
	interior := &url.Error{
		Op:  "parse",
		URL: "http://proxy-admin:" + senhaDoProxy + "@proxy.interno:3128",
		Err: errors.New("invalid port"),
	}
	exterior := &url.Error{
		Op:  "Get",
		URL: "http://admin:***@vault.interno:8200/v1/secret/data/prod",
		Err: interior,
	}

	got := redaction.TransportError("http://vault.interno:8200", exterior).Error()
	if strings.Contains(got, senhaDoProxy) {
		t.Errorf("a senha do PROXY saiu inteira depois da redaccao: %s", got)
	}
	if strings.Contains(got, "proxy-admin") {
		t.Errorf("o utilizador do proxy sobreviveu a redaccao: %s", got)
	}
	// CONTROLO: descer nos aninhados nao pode apagar a informacao de CONTRA QUEM se falhou.
	if !strings.Contains(got, "proxy.interno:3128") {
		t.Errorf("a redaccao apagou o endereco do proxy — perde-se que a falha foi contra ELE e nao contra o Vault: %s", got)
	}
	if !strings.Contains(got, "invalid port") {
		t.Errorf("a causa interior perdeu-se: %s", got)
	}
}

// TestAOS337_MountInvalidoNaoAcusaOEndereco fecha o achado F5. `c.mount` vem de
// `AOS_BROKER_VAULT_KV_MOUNT` e NÃO era escapado, ao contrário dos segmentos do path. Um mount
// com um escape inválido fazia o `NewRequest` falhar — e a mensagem nova acusaria um endereço
// PERFEITO, mandando o operador depurar a variável errada.
func TestAOS337_MountInvalidoNaoAcusaOEndereco(t *testing.T) {
	t.Parallel()
	c, err := NewKVv2(KVv2Config{Addr: "https://vault.interno:8200", Token: "tok", Mount: "sec%zzret"})
	if err != nil {
		t.Fatalf("NewKVv2: %v", err)
	}
	// Com o mount escapado, o pedido CONSTRÓI-SE — o mount passa a ser um segmento literal, e
	// o que falha é a ligação, não o parse. É a falha certa a acusar a coisa certa.
	_, ferr := c.Fetch(context.Background(), chaveDeTeste)
	if ferr == nil {
		t.Fatal("um vault inexistente devia dar erro")
	}
	if strings.Contains(ferr.Error(), "nao produz um pedido HTTP valido") {
		t.Errorf("um mount mal escrito continua a ser acusado ao ENDERECO, que esta perfeito: %v", ferr)
	}
}

// TestAOS337_RedactURL fixa o que o redactor partilhado pode devolver. Vive aqui, e não só em
// `substrate/redaction`, porque é ESTE pacote que perde se ele alargar.
func TestAOS337_RedactURL(t *testing.T) {
	t.Parallel()
	casos := []struct{ nome, entrada, quer string }{
		{"credenciais deitadas fora", "https://admin:" + aos337Senha + "@vault.interno:8200", "https://vault.interno:8200"},
		{"caminho deitado fora", "https://vault.interno:8200/v1/secret/data/prod", "https://vault.interno:8200"},
		{"query deitada fora", "https://vault.interno:8200?token=" + aos337Senha, "https://vault.interno:8200"},
		{"forma limpa fica igual", "https://vault.interno:8200", "https://vault.interno:8200"},
		{"loopback fica igual", "http://127.0.0.1:8200", "http://127.0.0.1:8200"},
		{"sem esquema continua re-analisavel", "//admin:" + aos337Senha + "@vault.interno:8200", "//vault.interno:8200"},
		{"host verdadeiro e nao o que parece", "https://vault:8200@evil.example:9000", "https://evil.example:9000"},
		{"invalida nao devolve o valor", "://" + aos337Senha, "(inválida)"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			t.Parallel()
			got := redaction.URL(c.entrada)
			if got != c.quer {
				t.Errorf("redaction.URL(%q) = %q, quer %q", c.entrada, got, c.quer)
			}
			if strings.Contains(got, aos337Senha) {
				t.Errorf("a forma redigida contem o segredo: %q", got)
			}
		})
	}
}

// TestAOS337_ErroQueNaoEDeURLPassaTalQual: não se inventa redacção sobre uma forma que não se
// conhece.
func TestAOS337_ErroQueNaoEDeURLPassaTalQual(t *testing.T) {
	t.Parallel()
	outro := errors.New("erro que nao e de URL")
	if got := redaction.TransportError("https://admin:pw@vault:8200", outro); !errors.Is(got, outro) {
		t.Errorf("um erro que nao e *url.Error tem de passar tal-qual: %v", got)
	}
}
