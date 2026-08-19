package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/aos-ref/integration"
	"github.com/aos-ref/kernel/agent-runtime/control"
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
