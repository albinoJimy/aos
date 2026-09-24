package identity

// aos433_revogacao_indisponivel_test.go — «não consegui perguntar» deixa de ser «foi revogado».
//
// O verificador embrulhava as duas condições na MESMA sentinela, com a causa em `%v`. Numa
// indisponibilidade do registo de revogação, todas as verificações de todos os titulares eram
// recusadas e o log dizia «revogada» para todos — o operador iria revogar e reemitir identidades
// num incidente que se resolvia reiniciando um serviço.

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

// registoAvariado é um [Revocations] que nunca consegue responder.
type registoAvariado struct{ causa error }

func (r registoAvariado) IsRevoked(context.Context, string) (bool, error) { return false, r.causa }

// registoQueRevoga responde, e diz que sim.
type registoQueRevoga struct{}

func (registoQueRevoga) IsRevoked(context.Context, string) (bool, error) { return true, nil }

// emissorEVerificadorDeTeste devolve um par pronto, com o trust anchor ligado.
func emissorEVerificadorDeTeste(t *testing.T, opts ...VerifierOption) (*Issuer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("gerar chave: %v", err)
	}
	iss, err := NewIssuer("iss:aos433", priv, map[string]ClassPolicy{
		"researcher": {TTL: 15 * time.Minute, Scope: []string{"cap:doc.read"}},
	})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	tok, err := iss.Issue(context.Background(), IssueRequest{
		UserID:        "alice",
		AgentID:       "nhi:agente-433",
		AgentClass:    "researcher",
		UserAuthority: []string{"cap:doc.read"},
	})
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return iss, tok.Compact
}

// TestAOS433RegistoIndisponivelNaoSeConfundeComRevogacao é o núcleo.
func TestAOS433RegistoIndisponivelNaoSeConfundeComRevogacao(t *testing.T) {
	iss, compact := emissorEVerificadorDeTeste(t)
	avaria := errors.New("dial tcp: connection refused")

	v := NewVerifier(
		WithTrustedIssuer(iss.IssuerID(), iss.PublicKey()),
		WithRevocations(registoAvariado{causa: avaria}),
	)

	_, err := v.Verify(context.Background(), compact)

	// (1) FAIL-CLOSED: continua a recusar. É o que não pode mudar.
	if err == nil {
		t.Fatal("com o registo de revogacao em baixo, a verificacao tinha de RECUSAR — " +
			"a postura fail-closed nao e negociavel e este ticket nao a relaxa")
	}
	// (2) E NÃO SE CHAMA «REVOGADO».
	if errors.Is(err, ErrTokenRevoked) {
		t.Errorf("uma AVARIA do registo foi reportada como revogacao: %v\n"+
			"numa indisponibilidade, o operador le «revogada» para todos os titulares e vai "+
			"revogar e reemitir identidades por causa de um servico em baixo", err)
	}
	if !errors.Is(err, ErrRevocationUnavailable) {
		t.Errorf("erro = %v, quer ErrRevocationUnavailable", err)
	}
	// (3) A CAUSA É RECUPERÁVEL. Era o que o `%v` destruía.
	if !errors.Is(err, avaria) {
		t.Errorf("a causa subjacente nao viaja no erro (%v) — foi achatada para texto, e quem "+
			"diagnostica nao consegue distinguir uma recusa de rede de uma de permissoes", err)
	}
}

// TestAOS433RevogacaoGENUINAContinuaAeSerRevogacao é o controlo de não-vacuidade.
//
// Sem ele, mudar a sentinela de sítio passaria no teste acima e partiria a revogação a sério —
// que é a coisa que o mecanismo existe para fazer.
func TestAOS433RevogacaoGENUINAContinuaAeSerRevogacao(t *testing.T) {
	iss, compact := emissorEVerificadorDeTeste(t)

	v := NewVerifier(
		WithTrustedIssuer(iss.IssuerID(), iss.PublicKey()),
		WithRevocations(registoQueRevoga{}),
	)

	_, err := v.Verify(context.Background(), compact)
	if !errors.Is(err, ErrTokenRevoked) {
		t.Fatalf("um token REVOGADO tinha de dar ErrTokenRevoked, veio %v", err)
	}
	if errors.Is(err, ErrRevocationUnavailable) {
		t.Error("uma revogacao genuina foi reportada como indisponibilidade — a distincao " +
			"inverteu-se, e agora e o oposto que engana o operador")
	}
}
