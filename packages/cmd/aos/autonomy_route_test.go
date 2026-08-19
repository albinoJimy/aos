package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aos-ref/control-plane/governance/autonomy"
	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
	audit "github.com/aos-ref/platform/audit"
)

// TestPayloadDeAutonomiaAmarraNivelEMotivo é o controlo central desta rota.
//
// A assinatura do emissor cobre o payload canónico. Se o NÍVEL não estivesse lá dentro, uma
// assinatura legítima de "L1" seria reapresentável como "L5" — o operador assinaria conceder
// pouca autonomia e alguém aplicaria muita, com o selo a nomeá-lo a ele.
//
// E o MOTIVO também entra. Sem isso, a justificação seria um campo que se troca depois de
// assinado: o registo ficaria com a assinatura de uma decisão e o texto de outra.
func TestPayloadDeAutonomiaAmarraNivelEMotivo(t *testing.T) {
	base := integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "leitura de rotina")

	variantes := map[string][]byte{
		"outro nivel":   integration.CanonicalAutonomyPayload("agt-1", "fs", "L5", "leitura de rotina"),
		"outro motivo":  integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "outra coisa"),
		"outro agente":  integration.CanonicalAutonomyPayload("agt-2", "fs", "L1", "leitura de rotina"),
		"outro dominio": integration.CanonicalAutonomyPayload("agt-1", "http", "L1", "leitura de rotina"),
	}
	for nome, v := range variantes {
		if string(v) == string(base) {
			t.Errorf("%s: payload IGUAL ao base — a assinatura serviria para os dois", nome)
		}
	}

	// Deslizamento de fronteira: sem length-prefix, ("agt", "1fs") e ("agt1", "fs") colidiriam,
	// e uma assinatura de um par valeria para outro.
	if string(integration.CanonicalAutonomyPayload("agt", "1fs", "L1", "x")) ==
		string(integration.CanonicalAutonomyPayload("agt1", "fs", "L1", "x")) {
		t.Error("colisao por deslizamento de fronteira entre agent e domain")
	}
}

// TestEmissorAssinadoEAceiteEOsOutrosNao percorre os controlos que separam "a rota existe" de "a
// rota autentica": emissor registado passa, desconhecido não, e o MESMO nonce não passa duas
// vezes.
//
// O último é o que impede capturar um pedido legítimo e reaplicá-lo — que numa API que muda o
// nível de supervisão seria uma escalada de privilégio silenciosa.
func TestEmissorAssinadoEAceiteEOsOutrosNao(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	outraPub, outraPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = outraPub

	auth, err := integration.NewEd25519Authenticator(memNonceStore{vistos: map[string]bool{}}, 5*time.Minute)
	if err != nil {
		t.Fatalf("autenticador: %v", err)
	}
	auth.Register("human:op", pub)

	payload := integration.CanonicalAutonomyPayload("agt-1", "fs", "L4", "porque sim")
	agora := time.Now().UTC()

	// (1) emissor REGISTADO, assinatura sobre este payload ⇒ aceite.
	em, err := integration.SignEmitter("human:op", priv, integration.AutonomyScope, control.SignalAutonomy, payload, agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em); err != nil {
		t.Fatalf("emissor registado devia passar: %v", err)
	}

	// (2) CONTROLO — o MESMO emitter outra vez. O nonce é de uso único.
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em); err == nil {
		t.Fatal("o MESMO nonce passou duas vezes — um pedido capturado seria reaplicavel")
	}

	// (3) CONTROLO — assinatura de uma chave NÃO registada.
	em2, err := integration.SignEmitter("human:intruso", outraPriv, integration.AutonomyScope, control.SignalAutonomy, payload, agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em2); err == nil {
		t.Fatal("um emissor NAO registado foi aceite")
	}

	// (4) CONTROLO — assinatura válida, mas para OUTRO nível. É o cenário que o payload existe
	// para fechar: a mesma pessoa, a mesma chave, um pedido diferente do que assinou.
	em3, err := integration.SignEmitter("human:op", priv, integration.AutonomyScope, control.SignalAutonomy,
		integration.CanonicalAutonomyPayload("agt-1", "fs", "L1", "porque sim"), agora)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Authenticate(t.Context(), integration.AutonomyScope, control.SignalAutonomy, payload, em3); err == nil {
		t.Fatal("uma assinatura de L1 foi aceite para uma mudanca para L4")
	}
}

// TestNivelDeWireEFailClosed — "L9", "l4 " ou vazio não podem resolver para o valor-zero, que é
// L0. Resolver silenciosamente daria um 200 a quem pediu outra coisa: o operador acreditaria ter
// mudado a postura e teria aplicado a mais restritiva.
func TestNivelDeWireEFailClosed(t *testing.T) {
	for _, mau := range []string{"", "L9", "9", "alto", "LX", "L-1"} {
		if _, ok := parseAutonomyLevelWire(mau); ok {
			t.Errorf("level %q foi aceite", mau)
		}
	}
	for entrada, quero := range map[string]string{
		"L0": "L0", "l4": "L4", " L5 ": "L5", "L3": "L3",
	} {
		got, ok := parseAutonomyLevelWire(entrada)
		if !ok || got.String() != quero {
			t.Errorf("level %q -> (%v,%v), quero %s", entrada, got, ok, quero)
		}
	}
}

// memNonceStore e um armazem de nonces EM MEMORIA para os testes. Consome de verdade: o par
// (scope, nonce) so passa a primeira vez. Um duplo que aceitasse sempre tornaria o controlo de
// replay verde sem exercitar nada.
type memNonceStore struct{ vistos map[string]bool }

func (m memNonceStore) ConsumeNonce(_ context.Context, scope string, nonce []byte) (bool, error) {
	k := fmt.Sprintf("%s|%x", scope, nonce)
	if m.vistos[k] {
		return false, nil
	}
	m.vistos[k] = true
	return true, nil
}

// TestPisoDeAmbienteEFailClosed — a fronteira de ambiente do piso.
//
// VAZIO ⇒ L0, exactamente como sem a variável: um nó que não a defina não muda de comportamento.
// Um valor FORA do vocabulário ABORTA em vez de cair no valor-zero — que é L0 e passaria por
// "aceite" enquanto ignorava em silêncio o que o operador escreveu. Um typo que produz a postura
// mais restritiva é o pior tipo de typo: nada parece errado, logo ninguém o procura.
func TestPisoDeAmbienteEFailClosed(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_DEFAULT", "")
	if lvl, err := parseAutonomyDefault(); err != nil || lvl.String() != "L0" {
		t.Fatalf("vazio -> (%v,%v), quero (L0,nil)", lvl, err)
	}
	t.Setenv("AOS_AUTONOMY_DEFAULT", "L3")
	if lvl, err := parseAutonomyDefault(); err != nil || lvl.String() != "L3" {
		t.Fatalf("L3 -> (%v,%v), quero (L3,nil)", lvl, err)
	}
	for _, mau := range []string{"L9", "alto", "3", "LX"} {
		t.Setenv("AOS_AUTONOMY_DEFAULT", mau)
		if _, err := parseAutonomyDefault(); !errors.Is(err, ErrBadAutonomyDefault) {
			t.Errorf("AOS_AUTONOMY_DEFAULT=%q devia abortar, veio %v", mau, err)
		}
	}
}

// TestConfigAceitaClasseERecusaAmbiguidade — a fronteira de configuração da cascata.
func TestConfigAceitaClasseERecusaAmbiguidade(t *testing.T) {
	t.Setenv("AOS_AUTONOMY_LEVELS", "agt-1:fs=L4,class:agent-worker:http=L3")
	specs, err := parseAutonomyLevels()
	if err != nil {
		t.Fatalf("config valida recusada: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("li %d entradas, quero 2", len(specs))
	}
	if specs[0].agent != "agt-1" || specs[0].domain != "fs" {
		t.Errorf("instancia mal lida: %+v", specs[0])
	}
	// A entrada de classe fica com o PREFIXO no alvo — é o que a distingue de uma instância no
	// registo, e o que faz a regra passar pelo mesmo selo e histórico das outras.
	if specs[1].agent != autonomy.ClassPrefix+"agent-worker" || specs[1].domain != "http" {
		t.Errorf("classe mal lida: %+v", specs[1])
	}

	// CONTROLO — um agente NÃO pode invadir o namespace das classes. Sem esta recusa,
	// `class:x:fs` seria ambíguo entre "a classe x" e "o agente literalmente chamado class:x", e
	// a ambiguidade decidir-se-ia por ordem de código em vez de por intenção.
	t.Setenv("AOS_AUTONOMY_LEVELS", "class:x:fs=L4")
	if s, err := parseAutonomyLevels(); err != nil {
		t.Fatalf("class:x:fs devia ser lido como CLASSE x: %v", err)
	} else if s[0].agent != autonomy.ClassPrefix+"x" {
		t.Errorf("class:x:fs foi lido como %q", s[0].agent)
	}

	// E as recusas de sempre continuam.
	for _, mau := range []string{"agt-1=L4", "agt-1:fs", "agt-1:fs=L9", ":fs=L4", "agt-1:=L4"} {
		t.Setenv("AOS_AUTONOMY_LEVELS", mau)
		if _, err := parseAutonomyLevels(); err == nil {
			t.Errorf("config %q foi aceite", mau)
		}
	}
}

// TestBannerNaoAfirmaOPresente fecha duas mentiras que ESTE trabalho introduziu no banner.
//
// A primeira: enquanto o piso era sempre L0, dizer "par nao registado ⇒ L0" era verdade. Com
// AOS_AUTONOMY_DEFAULT deixou de ser, e anunciar L0 quando alguem declarou L3 diz ao operador que
// o sistema e MAIS supervisionado do que e — o sentido errado para uma imprecisao de seguranca.
//
// A segunda: a contagem e a do ARRANQUE. Com POST /autonomy os niveis mudam em runtime, e uma
// linha que afirme o presente descrevendo o passado e pior do que nao afirmar nada. O banner tem
// de dizer que e uma fotografia, e apontar onde esta o filme.
func TestBannerNaoAfirmaOPresente(t *testing.T) {
	w := buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "fs", level: autonomy.L4},
	}, autonomy.L3)
	if err := w.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatalf("provision: %v", err)
	}
	linha := strings.Join(autonomyPostureBanner(w), "\n")

	// O piso ANUNCIADO tem de ser o piso REAL.
	if !strings.Contains(linha, "L3") {
		t.Errorf("o banner nao anuncia o piso declarado L3:\n%s", linha)
	}
	if strings.Contains(linha, "⇒ L0") {
		t.Errorf("o banner continua a afirmar L0 com um piso L3 declarado:\n%s", linha)
	}
	// E tem de declarar que e uma fotografia do arranque, com o GET como fonte de verdade.
	for _, exigido := range []string{"NO ARRANQUE", "GET /autonomy", "DECLARADO em AOS_AUTONOMY_DEFAULT"} {
		if !strings.Contains(linha, exigido) {
			t.Errorf("o banner nao contem %q:\n%s", exigido, linha)
		}
	}

	// CONTROLO — sem piso declarado, o banner tem de dizer L0 E que e por OMISSAO. Sao posturas
	// identicas com responsaveis diferentes, e confundi-las apaga a informacao que o piso
	// declaravel introduziu.
	w2 := buildAutonomyOracle([]autonomyLevelSpec{
		{agent: "agt-1", domain: "fs", level: autonomy.L4},
	}, autonomy.L0)
	if err := w2.provision(context.Background(), audit.NewMemStore()); err != nil {
		t.Fatal(err)
	}
	l2 := strings.Join(autonomyPostureBanner(w2), "\n")
	if !strings.Contains(l2, "por omissao") {
		t.Errorf("sem piso declarado o banner devia dizer que L0 e por OMISSAO:\n%s", l2)
	}
}
